package build

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/megakuul/bob/pkg/bob/diag"
	"github.com/megakuul/bob/pkg/engine"
	"github.com/megakuul/bob/pkg/engine/drv"
	"github.com/megakuul/bob/pkg/engine/fetch"
	"github.com/megakuul/bob/pkg/engine/ir"
	"github.com/megakuul/bob/pkg/engine/lock"
	"github.com/megakuul/bob/pkg/engine/nixidx"
	"github.com/megakuul/bob/pkg/engine/store"
)

// LockOptions are the flags of bob lock.
type LockOptions struct {
	Update  []string // --update [names…]: "nixpkgs", an ecosystem, a fetch label; empty with UpdateAll
	All     bool     // --update without names: move every pin
	Nixpkgs string   // --nixpkgs <rev>
	Rebuild bool     // --rebuild: ignore the old file except nixpkgs.rev (R-FW-048)
}

// LockChange is one entry of `bob lock --json` result.data.changes.
type LockChange struct {
	Ecosystem string `json:"ecosystem"`
	Name      string `json:"name"`
	From      string `json:"from"`
	To        string `json:"to"`
	Op        string `json:"op"` // add | update | remove
}

// LockResult is the outcome of Lock.
type LockResult struct {
	Changes     []LockChange
	Written     bool     // bob.lock changed on disk
	Reformatted bool     // only the formatting changed ("note: bob.lock was reformatted")
	Notes       []string // the ecosystems' notes ("note: <text>"), e.g. npm install scripts not run
}

func lockPath(root string) string { return filepath.Join(root, "bob.lock") }

// readLock reads bob.lock; a missing file is an empty lock.
func readLock(root string) (*lock.File, error) {
	f, err := lock.Read(lockPath(root))
	if errors.Is(err, fs.ErrNotExist) {
		return &lock.File{LockVersion: lock.Version}, nil
	}
	return f, err
}

// nixpkgsDecl is RepoOpts.Nixpkgs of the graph (default channel when unset).
func nixpkgsDecl(g *ir.Graph) string {
	if g.Repo.Nixpkgs != "" {
		return g.Repo.Nixpkgs
	}
	return lock.DefaultChannel
}

// usesNixpkgs reports whether any node needs a nixpkgs package.
func usesNixpkgs(g *ir.Graph) bool {
	for _, n := range g.Nodes {
		if n.Nix != nil {
			return true
		}
	}
	return false
}

// hasEcoNodes reports whether any node belongs to an ecosystem (they take their toolchains
// from nixpkgs).
func hasEcoNodes(g *ir.Graph) bool {
	for _, n := range g.Nodes {
		if !isCoreKind(n.Kind) {
			return true
		}
	}
	return false
}

// nixpkgsStale reports whether pin no longer matches the declaration decl.
func nixpkgsStale(pin lock.NixpkgsPin, decl string) (bool, error) {
	spec, err := lock.ParseNixpkgs(decl)
	if err != nil {
		return false, err
	}
	if pin.Channel != spec.Channel || spec.Rev != "" && spec.Rev != pin.Rev {
		return true, nil
	}
	in, err := lock.Inputs(decl)
	return pin.Inputs != "" && pin.Inputs != in, err
}

// fetchEntry is the lock entry a bob.fetch node declares (without its hashes).
func fetchEntry(n *ir.Node) (fetchSpec, lock.Source, error) {
	var s fetchSpec
	if err := json.Unmarshal(n.Spec, &s); err != nil {
		return s, lock.Source{}, err
	}
	e := lock.Source{URL: s.URL, Kind: string(fetch.File), Strip: s.Strip, Name: s.Name}
	if s.Archive {
		e.Kind = string(fetch.Archive)
	}
	if e.Name == "" {
		base := "source"
		if u, err := url.Parse(s.URL); err == nil && path.Base(u.Path) != "/" && path.Base(u.Path) != "." {
			base = path.Base(u.Path)
		}
		e.Name = drv.Name(base)
	}
	return s, e, nil
}

func sameDecl(a, b lock.Source) bool {
	return a.URL == b.URL && a.Kind == b.Kind && a.Strip == b.Strip && a.Name == b.Name
}

// resolveSource downloads a declared fetch input and returns its pinned entry.
func resolveSource(ctx context.Context, f fetch.Fetcher, label string, s fetchSpec, e lock.Source) (lock.Source, error) {
	hash, narHash, err := f.Resolve(ctx, fetch.Request{URLs: append([]string{e.URL}, s.Mirrors...),
		Kind: fetch.Kind(e.Kind), Name: e.Name, Strip: e.Strip, Label: label})
	if err != nil {
		return e, err
	}
	e.Hash = hash
	if e.Kind == string(fetch.Archive) {
		e.NarHash = narHash
	}
	return e, nil
}

// lockStage is the lock check of bob build/test (R-FW-005): missing pins (nixpkgs, fetch
// inputs) are added on demand; pins that no longer match their declaration and stale
// ecosystem sections (engine.LockFreshness) fail with BOB-E-1012 unless --lock, which
// re-resolves them and the ecosystem sections.
func (r *runner) lockStage(ctx context.Context, st *store.Store, f fetch.Fetcher, g *ir.Graph) (*lock.File, error) {
	lf, err := readLock(r.o.Root)
	if err != nil {
		return nil, err
	}
	before, err := lf.Encode()
	if err != nil {
		return nil, err
	}
	if !r.o.Lock {
		// Stale ecosystem sections fail before anything is downloaded (R-FW-062).
		ecos, err := ecosystemsOf(g)
		if err != nil {
			return nil, err
		}
		stale, err := staleSections(ctx, r.o.Root, g, lf, f, ecos, lazyIndex(st, f, g, lf))
		if err != nil {
			return nil, err
		}
		if len(stale) > 0 {
			errs := make([]error, len(stale))
			for i, d := range stale {
				errs[i] = d
			}
			return nil, errors.Join(errs...)
		}
	}
	var stale []error
	if decl := nixpkgsDecl(g); usesNixpkgs(g) || lf.Nixpkgs.Rev != "" || r.transientPin.Rev != "" {
		old := lf.Nixpkgs.Rev != ""
		isStale, err := nixpkgsStale(lf.Nixpkgs, decl)
		if err != nil {
			return nil, err
		}
		switch {
		case old && isStale && !r.o.Lock:
			stale = append(stale, diag.New(1012, "what", fmt.Sprintf("nixpkgs is declared as %s but pinned to %s@%s", decl, lf.Nixpkgs.Channel, lf.Nixpkgs.Rev)))
		case !old || isStale:
			if r.o.Offline {
				return nil, diag.New(1012, "what", "nixpkgs is not pinned and --offline forbids pinning it")
			}
			prev := lf.Nixpkgs
			if !old && r.transientPin.Rev != "" {
				prev = r.transientPin // already downloaded for the evaluator's Go
			}
			if lf.Nixpkgs, err = lock.PinNixpkgs(ctx, f, decl, prev, false, nil); err != nil {
				return nil, err
			}
		}
	}
	for _, n := range g.Nodes {
		if n.Kind != "bob.fetch" {
			continue
		}
		s, want, err := fetchEntry(n)
		if err != nil {
			return nil, internalf(n.Label, "fetch spec: %v", err)
		}
		have, ok := lf.Sources[n.Label]
		switch {
		case ok && sameDecl(have, want):
			continue
		case ok && !r.o.Lock:
			stale = append(stale, diag.New(1012, "what", n.Label+" changed its download"))
			continue
		case r.o.Offline:
			return nil, diag.New(1009, "label", n.Label)
		}
		e, err := resolveSource(ctx, f, n.Label, s, want)
		if err != nil {
			return nil, offlineError(n.Label, err)
		}
		if lf.Sources == nil {
			lf.Sources = map[string]lock.Source{}
		}
		lf.Sources[n.Label] = e
	}
	if len(stale) > 0 {
		return nil, errors.Join(stale...)
	}
	// --lock re-resolves every section; otherwise sections bob.lock does not have yet are
	// locked on demand like missing pins (stale ones failed above).
	if r.o.Offline && !r.o.Lock {
		if missing := missingSections(g, lf); len(missing) > 0 {
			return nil, diag.New(1012, "what", missing[0]+" is not locked and --offline forbids locking it")
		}
	} else if _, err := lockEcosystems(ctx, f, g, lf, r.o.Root, nil, false, !r.o.Lock, r.o.Offline, nil, lazyIndex(st, f, g, lf)); err != nil {
		return nil, err
	}
	after, err := lf.Encode()
	if err != nil {
		return nil, err
	}
	raw, rerr := os.ReadFile(lockPath(r.o.Root))
	if !bytes.Equal(before, after) || rerr == nil && !bytes.Equal(raw, after) {
		if err := lf.Write(lockPath(r.o.Root)); err != nil {
			return nil, err
		}
		if rerr == nil && bytes.Equal(before, after) {
			// Same content, other bytes: formatted by hand (R-FW-004 (b)).
			r.emit(Event{Event: "note", Note: &Note{Code: "lock-reformatted", Text: "bob.lock was reformatted"}})
		}
	}
	return lf, nil
}

// ecosystemsOf returns the registered ecosystems that own nodes of g, sorted by name.
func ecosystemsOf(g *ir.Graph) ([]engine.Ecosystem, error) {
	all, err := engine.Ecosystems()
	if err != nil {
		return nil, err
	}
	used := map[string]bool{}
	for _, n := range g.Nodes {
		if !isCoreKind(n.Kind) {
			name, _, _ := strings.Cut(n.Kind, ".")
			used[name] = true
		}
	}
	var out []engine.Ecosystem
	for _, e := range all {
		if used[e.Name()] {
			out = append(out, e)
		}
	}
	return out, nil
}

// lazyIndex opens the nixpkgs index of lf's pin on first use (nil when bob.lock has no pin or
// the graph needs no nixpkgs), so that a lock stage with nothing to resolve opens nothing.
func lazyIndex(st *store.Store, f fetch.Fetcher, g *ir.Graph, lf *lock.File) func(context.Context) (*nixidx.Index, error) {
	var (
		idx    *nixidx.Index
		err    error
		opened bool
	)
	return func(ctx context.Context) (*nixidx.Index, error) {
		if !opened {
			opened = true
			idx, err = nativeIndex(ctx, nil, st, f, g, lf)
		}
		return idx, err
	}
}

// lockEcosystems re-resolves the sections of the ecosystems used by g (onlyMissing: only those
// bob.lock has no section for). Sections that bob.lock cannot hold (an ecosystem without a
// lock section key) are left to their ecosystem. index opens the nixpkgs index the resolvers
// read pinned toolchain versions from (LockRequest.Nixpkgs: npm's engines check, Rust's
// rust-version fallback); nil passes none. offline (--offline) keeps every section that is
// present, fresh (engine.LockFreshness) and not named by --update, and refuses to resolve any
// other: the resolvers download registry metadata and sources (BOB-E-3002, A456).
func lockEcosystems(ctx context.Context, f fetch.Fetcher, g *ir.Graph, lf *lock.File, root string, update []string, all, onlyMissing, offline bool, note func(string),
	index func(context.Context) (*nixidx.Index, error)) ([]LockChange, error) {
	ecos, err := ecosystemsOf(g)
	if err != nil {
		return nil, err
	}
	var changes []LockChange
	for _, e := range ecos {
		name := e.Name()
		if !knownSection(name) || onlyMissing && lf.Sections[name] != nil {
			continue
		}
		upd := update
		if all {
			upd = []string{}
		}
		if offline {
			if err := offlineSection(ctx, e, g, lf, f, root, len(update) > 0 || all); err != nil {
				return nil, err
			}
			continue
		}
		var idx *nixidx.Index
		if index != nil {
			if idx, err = index(ctx); err != nil {
				return nil, err
			}
		}
		sec, err := e.Lock(ctx, engine.LockRequest{Graph: g, Prev: lf.Sections[name], Update: upd, Fetch: f, Nixpkgs: idx, Root: root, Note: note})
		if err != nil {
			return nil, err
		}
		if !sameSection(name, lf.Sections[name], sec) {
			op := "update"
			if lf.Sections[name] == nil {
				op = "add"
			}
			switch name {
			case lock.SectionGo:
				changes = append(changes, goChanges(lf.Sections[name], sec)...)
			case lock.SectionNpm:
				changes = append(changes, npmChanges(lf.Sections[name], sec)...)
			default:
				changes = append(changes, LockChange{Ecosystem: name, Name: name, Op: op})
			}
			if lf.Sections == nil {
				lf.Sections = map[string]json.RawMessage{}
			}
			lf.Sections[name] = sec
		}
	}
	return changes, nil
}

// offlineSection checks that bob lock --offline can keep e's section as it is: present, not
// to be moved (update) and fresh. Anything else needs the network.
func offlineSection(ctx context.Context, e engine.Ecosystem, g *ir.Graph, lf *lock.File, f fetch.Fetcher, root string, update bool) error {
	name := e.Name()
	refuse := func(why string) error {
		return diag.New(3002, "label", "bob.lock section "+name, "reason", why+", which needs the network (--offline)", "attempts", "0")
	}
	prev := lf.Sections[name]
	switch {
	case prev == nil:
		return refuse("it is not locked yet")
	case update:
		return refuse("bob lock --update resolves it again")
	}
	lfr, ok := e.(engine.LockFreshness)
	if !ok {
		return nil
	}
	what, _, err := lfr.Stale(ctx, engine.LockRequest{Graph: g, Prev: prev, Fetch: f, Root: root})
	if err != nil {
		return err
	}
	if what != "" {
		return refuse(what)
	}
	return nil
}

// missingSections are the lock sections of g's ecosystems that bob.lock does not have.
func missingSections(g *ir.Graph, lf *lock.File) []string {
	ecos, err := ecosystemsOf(g)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range ecos {
		if knownSection(e.Name()) && lf.Sections[e.Name()] == nil {
			out = append(out, e.Name())
		}
	}
	return out
}

func knownSection(name string) bool {
	switch name {
	case lock.SectionGo, lock.SectionRust, lock.SectionNpm, lock.SectionOpam, lock.SectionCC:
		return true
	}
	return false
}

// Lock runs bob lock (R-CLI-010): evaluate, pin nixpkgs, pin every bob.fetch input, re-resolve
// the ecosystem sections, remove entries no declaration uses, and write bob.lock only when its
// bytes change.
func Lock(ctx context.Context, o Options, lo LockOptions) (*LockResult, error) {
	st, err := openStore(ctx, o.Store, o.Home, o.Caches)
	if err != nil {
		return nil, err
	}
	g, _, err := (&runner{o: o, salvageLock: lo.Rebuild}).evaluate(ctx, st)
	if err != nil {
		return nil, err
	}
	raw, rerr := os.ReadFile(lockPath(o.Root))
	var lf *lock.File
	switch {
	case lo.Rebuild:
		lf = &lock.File{LockVersion: lock.Version}
		if rev := lock.SalvageNixpkgsRev(raw); rev != "" {
			lf.Nixpkgs.Rev = rev
		}
	case errors.Is(rerr, fs.ErrNotExist):
		lf = &lock.File{LockVersion: lock.Version}
	case rerr != nil:
		return nil, rerr
	default:
		if lf, err = lock.Parse(raw); err != nil {
			return nil, err
		}
	}
	old := *lf
	var f fetch.Fetcher = fetch.New(st, fetch.Policy{})
	var channelRev func(context.Context, string) (string, error) // nil: lock.ChannelRev
	if o.Offline { // --offline: what the store holds and the pins bob.lock has, nothing else (A456)
		f = offlineFetcher{st: st}
		channelRev = func(context.Context, string) (string, error) {
			return "", fmt.Errorf("the channel's current revision: %w", errOffline)
		}
	}
	updates := map[string]bool{}
	for _, u := range lo.Update {
		updates[u] = true
	}
	var changes []LockChange

	decl := nixpkgsDecl(g)
	if lo.Nixpkgs != "" {
		spec, err := lock.ParseNixpkgs(decl)
		if err != nil {
			return nil, err
		}
		decl = spec.Channel + "@" + lo.Nixpkgs
	}
	ecos, err := ecosystemsOf(g)
	if err != nil {
		return nil, err
	}
	{ // bob lock always pins nixpkgs: ecosystems take their toolchains from it.
		prev := lf.Nixpkgs
		if lo.Rebuild && prev.Rev != "" {
			if spec, err := lock.ParseNixpkgs(decl); err == nil {
				prev.Channel = spec.Channel // keep the salvaged revision
			}
		}
		pin, err := lock.PinNixpkgs(ctx, f, decl, prev, lo.All || updates["nixpkgs"], channelRev)
		if err != nil {
			return nil, offlineError("nixpkgs", err)
		}
		if pin.Rev != old.Nixpkgs.Rev {
			op := "update"
			if old.Nixpkgs.Rev == "" {
				op = "add"
			}
			changes = append(changes, LockChange{Ecosystem: "nixpkgs", Name: "nixpkgs", From: short(old.Nixpkgs.Rev), To: short(pin.Rev), Op: op})
		}
		lf.Nixpkgs = pin
	}

	used := map[string]bool{}
	sources := map[string]lock.Source{}
	for _, n := range g.Nodes {
		if n.Kind != "bob.fetch" {
			continue
		}
		used[n.Label] = true
		s, want, err := fetchEntry(n)
		if err != nil {
			return nil, internalf(n.Label, "fetch spec: %v", err)
		}
		have, ok := lf.Sources[n.Label]
		if ok && sameDecl(have, want) && !lo.All && !updates[n.Label] {
			sources[n.Label] = have
			continue
		}
		e, err := resolveSource(ctx, f, n.Label, s, want)
		if err != nil {
			return nil, offlineError(n.Label, err)
		}
		sources[n.Label] = e
		switch {
		case !ok:
			changes = append(changes, LockChange{Ecosystem: "sources", Name: n.Label, To: e.URL, Op: "add"})
		case have != e:
			changes = append(changes, LockChange{Ecosystem: "sources", Name: n.Label, From: have.URL, To: e.URL, Op: "update"})
		}
	}
	for label, e := range lf.Sources {
		if !used[label] {
			changes = append(changes, LockChange{Ecosystem: "sources", Name: label, From: e.URL, Op: "remove"})
		}
	}
	lf.Sources = sources
	if len(lf.Sources) == 0 {
		lf.Sources = nil
	}

	var notes []string
	ecoChanges, err := lockEcosystems(ctx, f, g, lf, o.Root, lo.Update, lo.All, false, o.Offline, func(n string) { notes = append(notes, n) },
		lazyIndex(st, f, g, lf))
	if err != nil {
		return nil, err
	}
	changes = append(changes, ecoChanges...)
	inUse := map[string]bool{}
	for _, e := range ecos {
		inUse[e.Name()] = true
	}
	for name := range lf.Sections {
		if knownSection(name) && !inUse[name] {
			changes = append(changes, LockChange{Ecosystem: name, Name: name, Op: "remove"})
			delete(lf.Sections, name)
		}
	}
	sort.SliceStable(changes, func(i, j int) bool { return changes[i].Ecosystem < changes[j].Ecosystem })

	data, err := lf.Encode()
	if err != nil {
		return nil, err
	}
	res := &LockResult{Changes: changes, Notes: notes}
	if !bytes.Equal(data, raw) {
		if _, err := lock.WriteBytes(lockPath(o.Root), data); err != nil {
			return nil, err
		}
		res.Written = true
		res.Reformatted = len(changes) == 0 && rerr == nil && !lo.Rebuild
	}
	if err := writeRootGoMod(o.Root, g); err != nil {
		return nil, err
	}
	return res, nil
}

// offlineError is err as BOB-E-3002 when it is the offline fetcher's refusal (--offline).
func offlineError(what string, err error) error {
	if errors.Is(err, errOffline) {
		return diag.New(3002, "label", what, "reason", err.Error()+" (--offline)", "attempts", "0")
	}
	return err
}

func short(rev string) string {
	if len(rev) > 12 {
		return rev[:12]
	}
	return rev
}

// goChanges lists the module versions that differ between two go sections, one change per
// module path (SPEC §02.5: "~ go golang.org/x/net v0.30.0 → v0.31.0").
func goChanges(prev, next json.RawMessage) []LockChange {
	versions := func(raw json.RawMessage) map[string]string {
		out := map[string]string{}
		if len(raw) == 0 {
			return out
		}
		v, err := lock.DecodeSection(lock.SectionGo, raw)
		if err != nil {
			return out
		}
		for _, bl := range v.(*lock.GoSection).Workspaces {
			for _, mv := range bl.Modules {
				if p, ver, ok := strings.Cut(mv, "@"); ok && (out[p] == "" || out[p] < ver) {
					out[p] = ver
				}
			}
		}
		return out
	}
	a, b := versions(prev), versions(next)
	var out []LockChange
	for p, v := range b {
		switch w, ok := a[p]; {
		case !ok:
			out = append(out, LockChange{Ecosystem: lock.SectionGo, Name: p, To: v, Op: "add"})
		case w != v:
			out = append(out, LockChange{Ecosystem: lock.SectionGo, Name: p, From: w, To: v, Op: "update"})
		}
	}
	for p, v := range a {
		if _, ok := b[p]; !ok {
			out = append(out, LockChange{Ecosystem: lock.SectionGo, Name: p, From: v, Op: "remove"})
		}
	}
	if len(out) == 0 { // only hashes or other details changed
		out = append(out, LockChange{Ecosystem: lock.SectionGo, Name: lock.SectionGo, Op: "update"})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// npmChanges lists the package versions that differ between two npm sections, one change per
// package name ("~ npm react 19.1.0 → 19.2.0"; several versions of a name are joined by ",").
func npmChanges(prev, next json.RawMessage) []LockChange {
	versions := func(raw json.RawMessage) map[string]string {
		out := map[string]string{}
		if len(raw) == 0 {
			return out
		}
		v, err := lock.DecodeSection(lock.SectionNpm, raw)
		if err != nil {
			return out
		}
		sets := map[string]map[string]bool{}
		for _, p := range v.(*lock.NpmSection).Packages {
			if sets[p.Name] == nil {
				sets[p.Name] = map[string]bool{}
			}
			sets[p.Name][p.Version] = true
		}
		for name, vs := range sets {
			out[name] = strings.Join(sortedKeys(vs), ",")
		}
		return out
	}
	a, b := versions(prev), versions(next)
	var out []LockChange
	for n, v := range b {
		switch w, ok := a[n]; {
		case !ok:
			out = append(out, LockChange{Ecosystem: lock.SectionNpm, Name: n, To: v, Op: "add"})
		case w != v:
			out = append(out, LockChange{Ecosystem: lock.SectionNpm, Name: n, From: w, To: v, Op: "update"})
		}
	}
	for n, v := range a {
		if _, ok := b[n]; !ok {
			out = append(out, LockChange{Ecosystem: lock.SectionNpm, Name: n, From: v, Op: "remove"})
		}
	}
	if len(out) == 0 { // only edges, importers or hashes changed
		out = append(out, LockChange{Ecosystem: lock.SectionNpm, Name: lock.SectionNpm, Op: "update"})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// sameSection compares two encodings of a lock section by content: bob.lock holds sections
// indented, ecosystems return them compact.
func sameSection(name string, a, b json.RawMessage) bool {
	if bytes.Equal(a, b) {
		return true
	}
	if len(a) == 0 || len(b) == 0 {
		return false
	}
	va, err1 := lock.DecodeSection(name, a)
	vb, err2 := lock.DecodeSection(name, b)
	if err1 != nil || err2 != nil {
		return false
	}
	ea, err1 := lock.EncodeSection(name, va)
	eb, err2 := lock.EncodeSection(name, vb)
	return err1 == nil && err2 == nil && bytes.Equal(ea, eb)
}
