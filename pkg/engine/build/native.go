package build

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/megakuul/bob/pkg/bob/diag"
	"github.com/megakuul/bob/pkg/engine"
	"github.com/megakuul/bob/pkg/engine/drv"
	"github.com/megakuul/bob/pkg/engine/fetch"
	"github.com/megakuul/bob/pkg/engine/ir"
	"github.com/megakuul/bob/pkg/engine/lock"
	"github.com/megakuul/bob/pkg/engine/nixidx"
	"github.com/megakuul/bob/pkg/engine/store"
	gogen "github.com/megakuul/bob/pkg/golang/gen"
)

// Generated native tooling files (SPEC §03.0.2, R-FW-060, R-FW-061).

const (
	nativeStatePath = ".bob/state/native.json"
	backupDir       = ".bob/gen/backup"
	nativeMode      = 0o444
	maxExcerpt      = 12 // diag.Error.Excerpt limit
)

// nativeState is .bob/state/native.json: what bob wrote last (R-FW-060).
type nativeState struct {
	Version int                    `json:"version"`
	Files   map[string]nativeEntry `json:"files"`
}

type nativeEntry struct {
	SHA256 string `json:"sha256"`
	Owner  string `json:"owner"`
}

// Check reports, without changing any file and without network, the findings of bob check
// (R-CLI-009): bob.lock entries that are missing, extra or stale (BOB-E-1012), generated
// native files edited by hand (BOB-E-1011), and .bob/ or not-committed generated files that
// git does not ignore (BOB-E-1013). Evaluation failures and other errors are returned as the
// error (diagnostics as *diag.Error or ir.Errors). Only the evaluator's Go toolchain may be
// downloaded (nixpkgs' go, when no go is on PATH and the store lacks it).
func Check(ctx context.Context, o Options) ([]*diag.Error, error) {
	if o.Root == "" {
		return nil, errors.New("build: Options.Root is empty")
	}
	st, err := openStore(ctx, o.Store, o.Home, o.Caches)
	if err != nil {
		return nil, err
	}
	o.Patterns, o.Platform, o.Variants = nil, "", nil // the whole repository (as Gen)
	g, _, err := evaluate(ctx, st, o)
	if err != nil {
		return nil, err
	}
	lf, err := readLock(o.Root)
	if err != nil {
		return nil, err
	}
	f := offlineFetcher{st: st}
	findings, err := lockFindings(ctx, g, lf, f)
	if err != nil {
		return nil, err
	}
	idx, err := nativeIndex(ctx, st, f, g, lf)
	if err != nil {
		return nil, err
	}
	files, err := nativeFiles(ctx, g, lf, idx)
	if err == nil {
		files, err = withRootGoMod(files, g, o.Root)
	}
	if err != nil {
		if errs, ok := diagErrors(err); ok {
			return append(findings, errs...), nil
		}
		return nil, err
	}
	drift, err := compareNative(o.Root, files, readNativeState(o.Root))
	if err != nil {
		return nil, err
	}
	for _, d := range drift {
		findings = append(findings, d.finding())
	}
	ignore := []string{BobDir}
	for _, f := range files {
		if !f.Committed {
			ignore = append(ignore, f.Path)
		}
	}
	return append(findings, IgnoreFindings(ctx, o.Root, ignore)...), nil
}

// Gen writes the generated native files of the graph (bob gen, bob check --fix): missing files
// and files bob wrote from older declarations are (re)written, unchanged ones keep their bytes
// (mode 0444 restored), and a file edited by hand fails with BOB-E-1011 unless force, which
// first copies the edited file to .bob/gen/backup/<UTC time>/ (R-FW-061). Nothing is written
// when a file was edited and force is false. Files bob wrote earlier that no declaration
// produces any more are removed when unchanged. It returns the
// repo-relative paths of the generated files that are not committed (bob's .git/info/exclude
// block, BOB-E-1013).
func Gen(ctx context.Context, o Options, force bool) ([]string, error) {
	if o.Root == "" {
		return nil, errors.New("build: Options.Root is empty")
	}
	st, err := openStore(ctx, o.Store, o.Home, o.Caches)
	if err != nil {
		return nil, err
	}
	// Native files describe whole modules, so they always come from the whole repository's
	// graph in its host configuration, whatever patterns name (those select codegen roots).
	o.Patterns, o.Platform, o.Variants = nil, "", nil
	g, _, err := evaluate(ctx, st, o)
	if err != nil {
		return nil, err
	}
	lf, err := readLock(o.Root)
	if err != nil {
		return nil, err
	}
	var f fetch.Fetcher = fetch.New(st, fetch.Policy{})
	if o.Offline {
		f = offlineFetcher{st: st}
	}
	idx, err := nativeIndex(ctx, st, f, g, lf)
	if err != nil {
		return nil, err
	}
	return generate(ctx, o.Root, g, lf, idx, force, time.Now())
}

// generate is Gen after evaluation (also the drift check of bob build/test, with force false).
// g is the whole repository's graph.
func generate(ctx context.Context, root string, g *ir.Graph, lf *lock.File, idx *nixidx.Index, force bool, now time.Time) ([]string, error) {
	files, err := nativeFiles(ctx, g, lf, idx)
	if err == nil {
		files, err = withRootGoMod(files, g, root)
	}
	if err != nil {
		return nil, err
	}
	prev := readNativeState(root)
	drift, err := compareNative(root, files, prev)
	if err != nil {
		return nil, err
	}
	if len(drift) > 0 && !force {
		errs := make([]error, len(drift))
		for i, d := range drift {
			errs[i] = d.finding()
		}
		return nil, errors.Join(errs...)
	}
	if err := backup(root, drift, now); err != nil {
		return nil, err
	}
	next := nativeState{Version: 1, Files: map[string]nativeEntry{}}
	var excluded []string
	for _, gf := range files {
		if err := writeNative(root, gf); err != nil {
			return nil, err
		}
		sum := sha256.Sum256(gf.Content)
		next.Files[gf.Path] = nativeEntry{SHA256: hex.EncodeToString(sum[:]), Owner: gf.Owner}
		if !gf.Committed {
			excluded = append(excluded, gf.Path)
		}
	}
	for p, e := range prev.Files {
		if _, ok := next.Files[p]; !ok {
			removeOrphan(root, p, e)
		}
	}
	if err := writeNativeState(root, next); err != nil {
		return nil, err
	}
	sort.Strings(excluded)
	return excluded, nil
}

// nativeScope reports whether a run's graph is the one native files are generated from: the
// whole repository in the host configuration. Runs of a part of the repository (or for another
// platform or variant) see only part of each module and leave native files alone.
func nativeScope(o Options) bool {
	whole := len(o.Patterns) == 0 || len(o.Patterns) == 1 && o.Patterns[0] == "//..."
	return whole && o.Platform == "" && len(o.Variants) == 0
}

// nativeFiles asks every ecosystem of g for its generated files, sorted by path. Paths must be
// local, clean and unique, and never inside .git or bob's own state.
func nativeFiles(ctx context.Context, g *ir.Graph, lf *lock.File, idx *nixidx.Index) ([]engine.GeneratedFile, error) {
	ecos, err := ecosystemsOf(g)
	if err != nil {
		return nil, err
	}
	var out []engine.GeneratedFile
	owner := map[string]string{} // path → ecosystem
	for _, e := range ecos {
		name := e.Name()
		var nodes []*ir.Node
		for _, n := range g.Nodes {
			if strings.HasPrefix(n.Kind, name+".") {
				nodes = append(nodes, n)
			}
		}
		files, err := e.Native(ctx, engine.PlanRequest{Graph: g, Nodes: nodes, Lock: lf.Sections[name], Nixpkgs: idx})
		if err != nil {
			return nil, err
		}
		for _, f := range files {
			if !nativePathOK(f.Path) {
				return nil, internalf(f.Owner, "ecosystem %s generates the file %q outside the repository's working files", name, f.Path)
			}
			if other, dup := owner[f.Path]; dup {
				return nil, internalf(f.Owner, "ecosystems %s and %s both generate %s", other, name, f.Path)
			}
			owner[f.Path] = name
			out = append(out, f)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, nil
}

// withRootGoMod adds the root go.mod of the bob.go module (generated by the Go side of bob,
// golang/gen.RootGoMod, whatever ecosystems the graph uses) unless a Go module of the
// repository already generates that file.
func withRootGoMod(files []engine.GeneratedFile, g *ir.Graph, root string) ([]engine.GeneratedFile, error) {
	for _, f := range files {
		if f.Path == "go.mod" {
			return files, nil
		}
	}
	f, ok, err := gogen.RootGoMod(g, root)
	if err != nil || !ok {
		return files, err
	}
	files = append(files, f)
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	return files, nil
}

func nativePathOK(p string) bool {
	if p == "" || p != path.Clean(p) || path.IsAbs(p) || strings.ContainsAny(p, "\\\x00\n") {
		return false
	}
	first, _, _ := strings.Cut(p, "/")
	return first != ".." && first != ".git" && !strings.HasPrefix(p, ".bob/state/") && !strings.HasPrefix(p, ".bob/gen/")
}

// drifted is one generated file whose working-tree copy differs from what bob would write.
type drifted struct {
	f      engine.GeneratedFile
	onDisk []byte
	other  string // not a regular file: what it is (BOB-E-1016)
}

// finding is the drift's diagnostic: BOB-E-1011 with the ecosystem's explanation as excerpt,
// or BOB-E-1016 for something that is not a file at the generated path.
func (d drifted) finding() *diag.Error {
	if d.other != "" {
		return diag.New(1016, "path", d.f.Path, "label", d.f.Owner, "reason", "it is "+d.other)
	}
	e := diag.New(1011, "file", d.f.Path)
	if d.f.Explain != nil {
		for _, l := range d.f.Explain(d.onDisk) {
			if len(e.Excerpt) == maxExcerpt {
				break
			}
			e.Excerpt = append(e.Excerpt, strings.TrimSpace(l.Op+" "+l.Text))
		}
	}
	return e
}

// compareNative returns the generated files edited by hand: the working-tree copy differs from
// what bob would write and from what bob wrote last (prev; with no record: unless adoptable).
// A missing file is not drift, nor is a copy bob wrote from older declarations (it is
// rewritten), nor a mode change.
func compareNative(root string, files []engine.GeneratedFile, prev nativeState) ([]drifted, error) {
	var out []drifted
	for _, f := range files {
		p := filepath.Join(root, filepath.FromSlash(f.Path))
		fi, err := os.Lstat(p)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			continue
		case err != nil:
			return nil, diag.New(7002, "reason", f.Path+" cannot be read")
		case fi.Mode()&fs.ModeSymlink != 0:
			out = append(out, drifted{f: f, other: "a symbolic link"})
			continue
		case fi.IsDir():
			out = append(out, drifted{f: f, other: "a directory"})
			continue
		case !fi.Mode().IsRegular():
			out = append(out, drifted{f: f, other: "not a regular file"})
			continue
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return nil, diag.New(7002, "reason", f.Path+" cannot be read")
		}
		if bytes.Equal(b, f.Content) {
			continue
		}
		sum := sha256.Sum256(b)
		e, recorded := prev.Files[f.Path]
		switch {
		case recorded && e.SHA256 == hex.EncodeToString(sum[:]):
			continue // bob wrote it from older declarations
		case !recorded && adoptable(f, b):
			continue // bob's file without a record (a fresh clone of a committed go.mod)
		}
		out = append(out, drifted{f: f, onDisk: b})
	}
	return out, nil
}

// adoptable reports whether b, found where f belongs but not recorded in native.json, is a
// copy bob wrote (another clone, an older bob): the ecosystem's Explain finds no difference in
// bob.go terms, or, without Explain, it starts with the same first line (bob's header) as f.
func adoptable(f engine.GeneratedFile, b []byte) bool {
	if f.Explain != nil {
		return len(f.Explain(b)) == 0
	}
	first := func(x []byte) []byte {
		if i := bytes.IndexByte(x, '\n'); i >= 0 {
			return x[:i]
		}
		return x
	}
	h := first(f.Content)
	return len(bytes.TrimSpace(h)) > 0 && bytes.Equal(first(b), h)
}

// backup moves the drifted files to .bob/gen/backup/<UTC time>/<path> (bob gen --force).
func backup(root string, drift []drifted, now time.Time) error {
	if len(drift) == 0 {
		return nil
	}
	dir := filepath.Join(root, filepath.FromSlash(backupDir), now.UTC().Format("20060102T150405Z"))
	for _, d := range drift {
		if d.other != "" {
			// Not a file bob may move: refused even with --force.
			return d.finding()
		}
		dst := filepath.Join(dir, filepath.FromSlash(d.f.Path))
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return diag.New(7002, "reason", backupDir+" cannot be written")
		}
		if err := os.WriteFile(dst, d.onDisk, 0o644); err != nil {
			return diag.New(7002, "reason", backupDir+" cannot be written")
		}
	}
	return nil
}

// writeNative writes f with mode 0444 (temp file + rename) unless the working tree already has
// its bytes; then only the mode is restored.
func writeNative(root string, f engine.GeneratedFile) error {
	p := filepath.Join(root, filepath.FromSlash(f.Path))
	if b, err := os.ReadFile(p); err == nil && bytes.Equal(b, f.Content) {
		if fi, err := os.Lstat(p); err == nil && fi.Mode().Perm() != nativeMode {
			if err := os.Chmod(p, nativeMode); err != nil {
				return diag.New(7002, "reason", f.Path+" cannot be written")
			}
		}
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return diag.New(7002, "reason", f.Path+" cannot be written")
	}
	tmp := p + ".bob-tmp"
	_ = os.Remove(tmp)
	if err := os.WriteFile(tmp, f.Content, 0o600); err != nil {
		return diag.New(7002, "reason", f.Path+" cannot be written")
	}
	if err := os.Chmod(tmp, nativeMode); err != nil {
		_ = os.Remove(tmp)
		return diag.New(7002, "reason", f.Path+" cannot be written")
	}
	if err := os.Rename(tmp, p); err != nil {
		_ = os.Remove(tmp)
		return diag.New(7002, "reason", f.Path+" cannot be written")
	}
	return nil
}

// removeOrphan deletes a file bob wrote earlier that nothing generates any more, if it still
// has the bytes bob wrote (an edited copy is the user's now and stays).
func removeOrphan(root, rel string, e nativeEntry) {
	if !nativePathOK(rel) {
		return
	}
	p := filepath.Join(root, filepath.FromSlash(rel))
	fi, err := os.Lstat(p)
	if err != nil || !fi.Mode().IsRegular() {
		return
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return
	}
	if sum := sha256.Sum256(b); hex.EncodeToString(sum[:]) == e.SHA256 {
		_ = os.Remove(p)
	}
}

// readNativeState reads .bob/state/native.json; a missing or unreadable file is an empty state
// (it only decides which orphans to remove).
func readNativeState(root string) nativeState {
	var s nativeState
	b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(nativeStatePath)))
	if err != nil || json.Unmarshal(b, &s) != nil || s.Version != 1 {
		return nativeState{Version: 1, Files: map[string]nativeEntry{}}
	}
	if s.Files == nil {
		s.Files = map[string]nativeEntry{}
	}
	return s
}

// writeNativeState writes next when the file does not hold it yet (temp file + rename).
func writeNativeState(root string, next nativeState) error {
	b, err := json.MarshalIndent(next, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	p := filepath.Join(root, filepath.FromSlash(nativeStatePath))
	if old, err := os.ReadFile(p); err == nil && bytes.Equal(old, b) {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return diag.New(7002, "reason", nativeStatePath+" cannot be written")
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return diag.New(7002, "reason", nativeStatePath+" cannot be written")
	}
	if err := os.Rename(tmp, p); err != nil {
		return diag.New(7002, "reason", nativeStatePath+" cannot be written")
	}
	return nil
}

// nativeIndex opens the nixpkgs index the ecosystems' Native calls see: only when bob.lock has
// a pin and the graph has nixpkgs or ecosystem nodes, and (with the offline fetcher) only when
// the nixpkgs source is already in the store.
func nativeIndex(ctx context.Context, st *store.Store, f fetch.Fetcher, g *ir.Graph, lf *lock.File) (*nixidx.Index, error) {
	if lf.Nixpkgs.Rev == "" || !usesNixpkgs(g) && !hasEcoNodes(g) {
		return nil, nil
	}
	src, err := f.Ensure(ctx, lock.NixpkgsRequest(lf.Nixpkgs))
	if errors.Is(err, errOffline) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return nixidx.Open(ctx, st, nixidx.Config{Source: src, Rev: lf.Nixpkgs.Rev, System: store.HostSystem()})
}

// lockFindings is the lock half of bob check: every bob.lock entry that is missing, extra or
// stale, as BOB-E-1012, without network.
func lockFindings(ctx context.Context, g *ir.Graph, lf *lock.File, f fetch.Fetcher) ([]*diag.Error, error) {
	var out []*diag.Error
	stale := func(what string) { out = append(out, diag.New(1012, "what", what)) }

	decl := nixpkgsDecl(g)
	switch {
	case lf.Nixpkgs.Rev == "" && usesNixpkgs(g): // as bob build pins it (D-M4d-5)
		stale("nixpkgs is not pinned")
	case lf.Nixpkgs.Rev != "":
		isStale, err := nixpkgsStale(lf.Nixpkgs, decl)
		if err != nil {
			return nil, err
		}
		if isStale {
			stale(fmt.Sprintf("nixpkgs is declared as %s but pinned to %s@%s", decl, lf.Nixpkgs.Channel, lf.Nixpkgs.Rev))
		}
	}

	used := map[string]bool{}
	for _, n := range g.Nodes {
		if n.Kind != "bob.fetch" {
			continue
		}
		used[n.Label] = true
		_, want, err := fetchEntry(n)
		if err != nil {
			return nil, internalf(n.Label, "fetch spec: %v", err)
		}
		have, ok := lf.Sources[n.Label]
		switch {
		case !ok:
			stale(n.Label + " is not pinned")
		case !sameDecl(have, want):
			stale(n.Label + " changed its download")
		}
	}
	for _, label := range sortedKeys(lf.Sources) {
		if !used[label] {
			stale(label + " is pinned but no longer declared")
		}
	}

	ecos, err := ecosystemsOf(g)
	if err != nil {
		return nil, err
	}
	inUse := map[string]bool{}
	for _, e := range ecos {
		inUse[e.Name()] = true
	}
	for _, name := range missingSections(g, lf) {
		stale(name + " is not locked")
	}
	for _, name := range sortedKeys(lf.Sections) {
		if knownSection(name) && !inUse[name] {
			stale("the " + name + " section is no longer used")
		}
	}
	ecoStale, err := staleSections(ctx, g, lf, f, ecos)
	if err != nil {
		return nil, err
	}
	return append(out, ecoStale...), nil
}

// staleSections asks every ecosystem that records its declarations (engine.LockFreshness)
// whether its section still matches them (R-FW-062).
func staleSections(ctx context.Context, g *ir.Graph, lf *lock.File, f fetch.Fetcher, ecos []engine.Ecosystem) ([]*diag.Error, error) {
	var out []*diag.Error
	for _, e := range ecos {
		lfr, ok := e.(engine.LockFreshness)
		if !ok || lf.Sections[e.Name()] == nil {
			continue // a missing section is locked on demand (bob build) or reported as missing
		}
		what, at, err := lfr.Stale(ctx, engine.LockRequest{Graph: g, Prev: lf.Sections[e.Name()], Fetch: f})
		if err != nil {
			return nil, err
		}
		if what == "" {
			continue
		}
		d := diag.New(1012, "what", what)
		if at.File != "" {
			d.DefinedAt = diag.Pos{File: at.File, Line: at.Line}
		}
		out = append(out, d)
	}
	return out, nil
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// errOffline is returned by offlineFetcher for anything that would need the network.
var errOffline = errors.New("not in the store, and this command does not download")

// offlineFetcher is the fetcher of bob check: it only finds paths already valid in the store.
type offlineFetcher struct{ st *store.Store }

func (o offlineFetcher) Ensure(ctx context.Context, r fetch.Request) (string, error) {
	sp, err := pinnedPath(r)
	if err != nil {
		return "", err
	}
	missing, err := o.st.Invalid(ctx, []string{sp})
	if err != nil {
		return "", err
	}
	if len(missing) > 0 {
		return "", fmt.Errorf("%s: %w", r.Label, errOffline)
	}
	return sp, nil
}

func (offlineFetcher) Resolve(_ context.Context, r fetch.Request) (string, string, error) {
	return "", "", fmt.Errorf("%s: %w", r.Label, errOffline)
}

// pinnedPath is the store path of a request with a pinned hash (fetch's path identity).
func pinnedPath(r fetch.Request) (string, error) {
	sri, recursive := r.Hash, false
	if r.Kind == fetch.Archive {
		sri, recursive = r.NarHash, true
	}
	algo, b64, ok := strings.Cut(sri, "-")
	d, err := base64.StdEncoding.DecodeString(b64)
	if !ok || err != nil {
		return "", fmt.Errorf("%s: %w", r.Label, errOffline)
	}
	if recursive && algo != "sha256" {
		return "", fmt.Errorf("%s: narHash %q is not sha256", r.Label, sri)
	}
	return drv.FixedOutputPath(algo, hex.EncodeToString(d), recursive, r.Name)
}
