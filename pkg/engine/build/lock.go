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
	Written     bool // bob.lock changed on disk
	Reformatted bool // only the formatting changed ("note: bob.lock was reformatted")
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
		stale, err := staleSections(ctx, g, lf, f, ecos)
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
			return nil, err
		}
		if lf.Sources == nil {
			lf.Sources = map[string]lock.Source{}
		}
		lf.Sources[n.Label] = e
	}
	if len(stale) > 0 {
		return nil, errors.Join(stale...)
	}
	if r.o.Lock {
		if _, err := lockEcosystems(ctx, f, g, lf, nil, false); err != nil {
			return nil, err
		}
	}
	after, err := lf.Encode()
	if err != nil {
		return nil, err
	}
	if !bytes.Equal(before, after) {
		if err := lf.Write(lockPath(r.o.Root)); err != nil {
			return nil, err
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

// lockEcosystems re-resolves the sections of the ecosystems used by g. Sections that bob.lock
// cannot hold (an ecosystem without a lock section key) are left to their ecosystem.
func lockEcosystems(ctx context.Context, f fetch.Fetcher, g *ir.Graph, lf *lock.File, update []string, all bool) ([]LockChange, error) {
	ecos, err := ecosystemsOf(g)
	if err != nil {
		return nil, err
	}
	var changes []LockChange
	for _, e := range ecos {
		name := e.Name()
		if !knownSection(name) {
			continue
		}
		upd := update
		if all {
			upd = []string{}
		}
		sec, err := e.Lock(ctx, engine.LockRequest{Graph: g, Prev: lf.Sections[name], Update: upd, Fetch: f})
		if err != nil {
			return nil, err
		}
		if !bytes.Equal(lf.Sections[name], sec) {
			op := "update"
			if lf.Sections[name] == nil {
				op = "add"
			}
			changes = append(changes, LockChange{Ecosystem: name, Name: name, Op: op})
			if lf.Sections == nil {
				lf.Sections = map[string]json.RawMessage{}
			}
			lf.Sections[name] = sec
		}
	}
	return changes, nil
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
	g, _, err := evaluate(ctx, st, o)
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
	f := fetch.New(st, fetch.Policy{})
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
		pin, err := lock.PinNixpkgs(ctx, f, decl, prev, lo.All || updates["nixpkgs"], nil)
		if err != nil {
			return nil, err
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
			return nil, err
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

	ecoChanges, err := lockEcosystems(ctx, f, g, lf, lo.Update, lo.All)
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
	res := &LockResult{Changes: changes}
	if !bytes.Equal(data, raw) {
		if _, err := lock.WriteBytes(lockPath(o.Root), data); err != nil {
			return nil, err
		}
		res.Written = true
		res.Reformatted = len(changes) == 0 && rerr == nil && !lo.Rebuild
	}
	return res, nil
}

func short(rev string) string {
	if len(rev) > 12 {
		return rev[:12]
	}
	return rev
}
