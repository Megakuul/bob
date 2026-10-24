package gen

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/megakuul/bob/pkg/bob/diag"
	"github.com/megakuul/bob/pkg/engine"
	"github.com/megakuul/bob/pkg/engine/ir"
	"github.com/megakuul/bob/pkg/engine/lock"
	"github.com/megakuul/bob/pkg/engine/nar"
	"github.com/megakuul/bob/pkg/golang/internal/gospec"
	"github.com/megakuul/bob/pkg/golang/resolve"
)

// goSectionMemo remembers the last go section decoded read-only (R-FW-011 memoisation): one
// bob command decodes the same bytes in the lock stage, in every Go plan and in every code
// generator that needs the Go build list. Only successful decodes are kept; callers must not
// modify the returned section.
var goSectionMemo struct {
	mu  sync.Mutex
	raw []byte
	sec *lock.GoSection
}

// readGoSection decodes and validates a go section (lock.DecodeSection) for reading.
func readGoSection(raw []byte) (*lock.GoSection, error) {
	goSectionMemo.mu.Lock()
	if goSectionMemo.sec != nil && bytes.Equal(goSectionMemo.raw, raw) {
		sec := goSectionMemo.sec
		goSectionMemo.mu.Unlock()
		return sec, nil
	}
	goSectionMemo.mu.Unlock()
	v, err := lock.DecodeSection(lock.SectionGo, raw)
	if err != nil {
		return nil, err
	}
	sec := v.(*lock.GoSection)
	goSectionMemo.mu.Lock()
	goSectionMemo.raw, goSectionMemo.sec = bytes.Clone(raw), sec
	goSectionMemo.mu.Unlock()
	return sec, nil
}

// proxyEnv overrides the Go module proxy bob reads (a mirror, an internal proxy, or a
// file:// directory in tests). bob.lock records it when it is an http(s) URL; a file://
// directory is a local stand-in for the default proxy.
const proxyEnv = "BOB_GOPROXY"

// readProxy is the proxy bob reads modules from: BOB_GOPROXY, else the one bob.lock names,
// else the default.
func readProxy(locked string) string {
	if p := strings.TrimSpace(os.Getenv(proxyEnv)); p != "" {
		return p
	}
	if locked != "" {
		return locked
	}
	return resolve.DefaultProxy
}

// recordedProxy is the proxy written into bob.lock for the one read from.
func recordedProxy(used string) string {
	if strings.HasPrefix(used, "http://") || strings.HasPrefix(used, "https://") {
		return strings.TrimSuffix(used, "/")
	}
	return resolve.DefaultProxy
}

// ProxyDir is bob's local GOPROXY directory (§03.2.5): $XDG_CACHE_HOME/bob/goproxy, else
// $HOME/.cache/bob/goproxy; "" when neither is set. Resolution keeps every go.mod and zip it
// reads there, and bob env points GOPROXY at it.
func ProxyDir() string {
	if d := os.Getenv("XDG_CACHE_HOME"); filepath.IsAbs(d) {
		return filepath.Join(d, "bob", "goproxy")
	}
	if h := os.Getenv("HOME"); filepath.IsAbs(h) {
		return filepath.Join(h, ".cache", "bob", "goproxy")
	}
	return ""
}

func newProxy(raw string) (*resolve.Proxy, error) {
	p, err := resolve.NewProxy(raw)
	if err != nil {
		return nil, err
	}
	if d := ProxyDir(); d != "" {
		p.WithCache(d)
	}
	return p, nil
}

// Lock resolves the go section (§03.2.4, R-GO-002b): one build list per workspace module,
// computed as the go command computes it from the go.mod bob writes, and an entry per module
// version of the graphs with its go.mod hash, and for modules that provide packages its zip
// hash and unpacked tree hash.
func (Ecosystem) Lock(ctx context.Context, req engine.LockRequest) (json.RawMessage, error) {
	decls, nodes, err := declarations(req.Graph)
	if err != nil {
		return nil, err
	}
	inputs, err := lock.Inputs(decls)
	if err != nil {
		return nil, err
	}
	var prev *lock.GoSection
	if len(req.Prev) > 0 {
		if v, err := lock.DecodeSection(lock.SectionGo, req.Prev); err == nil {
			prev = v.(*lock.GoSection)
		}
	}
	sec := &lock.GoSection{Inputs: inputs, Workspaces: map[string]lock.GoBuildList{}, Modules: []lock.GoModule{}}
	rq := resolve.Request{Update: req.Update, Fresh: req.Update != nil && len(req.Update) == 0, TmpDir: os.TempDir()}
	locked := ""
	if prev != nil {
		locked = prev.Proxy
	}
	used := readProxy(locked)
	sec.Proxy = recordedProxy(used)
	if rq.Proxy, err = newProxy(used); err != nil {
		return nil, diag.New(1015, "ecosystem", "Go", "root", "this repository", "reason", err.Error())
	}
	rt := runtimes{ctx: ctx, req: req}
	for _, d := range decls {
		w, err := workspaceOf(nodes[d.Path], req.Root)
		if err != nil {
			return nil, err
		}
		if err := rt.resolve(nodes[d.Path], &w); err != nil {
			return nil, err
		}
		if prev != nil {
			for _, mv := range prev.Workspaces[d.Path].Modules {
				if p, v, ok := strings.Cut(mv, "@"); ok {
					w.Prev = append(w.Prev, resolve.Module{Path: p, Version: v})
				}
			}
		}
		rq.Workspaces = append(rq.Workspaces, w)
	}
	res, err := resolve.Resolve(ctx, rq)
	if err != nil {
		return nil, lockError(err, nodes)
	}
	defer res.Close()
	for _, d := range decls {
		bl := lock.GoBuildList{Go: goDirective(d.Go), Modules: []string{}}
		for _, m := range res.BuildLists[d.Path] {
			bl.Modules = append(bl.Modules, m.String())
		}
		sort.Strings(bl.Modules)
		sec.Workspaces[d.Path] = bl
	}
	prevEntry := map[string]lock.GoModule{}
	if prev != nil {
		for _, m := range prev.Modules {
			prevEntry[m.Path+"@"+m.Version] = m
		}
	}
	for _, mi := range res.Modules {
		e := lock.GoModule{Path: mi.Path, Version: mi.Version, ModH1: mi.ModH1, Go: mi.Go, Packages: mi.Packages}
		if mi.Packages {
			if e.H1, err = mi.Zip.H1(); err != nil {
				return nil, fmt.Errorf("%s: %w", mi.Module, err)
			}
			if p := prevEntry[mi.String()]; p.H1 == e.H1 && p.Tree != "" {
				e.Tree = p.Tree // the same content: the tree hash needs no new unpacking
			} else if e.Tree, err = treeHash(mi.Zip); err != nil {
				return nil, fmt.Errorf("%s: %w", mi.Module, err)
			}
		}
		sec.Modules = append(sec.Modules, e)
	}
	return lock.EncodeSection(lock.SectionGo, sec)
}

// workspaceOf is the resolution input of one go.module node.
func workspaceOf(n *ir.Node, root string) (resolve.Workspace, error) {
	var s gospec.Module
	if err := json.Unmarshal(n.Spec, &s); err != nil {
		return resolve.Workspace{}, engine.Internal(n, "module spec: %v", err)
	}
	dir := labelDir(n.Label)
	w := resolve.Workspace{Path: s.Path, Go: s.Go, BobGo: path.Join(dir, "bob.go")}
	if root != "" {
		w.Dir = filepath.Join(root, filepath.FromSlash(dir))
	}
	for _, r := range s.Require {
		w.Require = append(w.Require, resolve.Module{Path: r.Path, Version: r.Version})
	}
	for _, g := range s.Generated {
		w.Generated = append(w.Generated, s.Path+"/"+g.Dir)
		for _, r := range g.Require {
			w.GenRequire = append(w.GenRequire, resolve.Module{Path: r.Path, Version: r.Version})
		}
	}
	return w, nil
}

// lockError maps resolution errors to bob's codes at the declaring bob.go.
func lockError(err error, nodes map[string]*ir.Node) error {
	var nf *resolve.NotFoundError
	var te *resolve.ToolchainError
	switch {
	case errors.As(err, &te):
		e := diag.New(1008, "variant", "requirement", "requirement", te.Module.String(), "need", te.NeedsGo,
			"module", te.Workspace, "go", te.WorkspaceGo, "file", te.BobGo)
		if n := nodes[te.Workspace]; n != nil {
			e.DefinedAt = diag.Pos{File: n.DefinedAt.File, Line: n.DefinedAt.Line}
		}
		return e
	case errors.As(err, &nf):
		root := nf.Workspace
		if root == "" {
			root = "this repository"
		}
		e := diag.New(1015, "ecosystem", "Go module", "root", root, "reason", nf.Reason, "label", labelOf(nodes[nf.Workspace]))
		if n := nodes[nf.Workspace]; n != nil {
			e.DefinedAt = diag.Pos{File: n.DefinedAt.File, Line: n.DefinedAt.Line}
		}
		return e
	}
	return err
}

func labelOf(n *ir.Node) string {
	if n == nil {
		return ""
	}
	return n.Label
}

// treeHash is the SRI NAR hash of the module tree bob's fetcher unpacks from the zip
// (fetch.Archive with the "<path>@<version>/" prefix stripped): the lock's `tree`, the
// path identity of the module source (§03.0.4).
func treeHash(z *resolve.ModuleZip) (string, error) {
	dir, err := os.MkdirTemp("", "bob-gotree-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(dir)
	root := filepath.Join(dir, "tree")
	if err := z.Extract(root); err != nil {
		return "", err
	}
	sum, _, err := nar.Hash(root, nil)
	if err != nil {
		return "", err
	}
	return "sha256-" + base64.StdEncoding.EncodeToString(sum[:]), nil
}

// Stale reports the first workspace module of the graph whose declarations the section does
// not reflect (R-FW-062): a module without a build list, another Go version, a declared
// requirement its build list does not select at the declared version or higher (bob lock
// --update may have moved it up), or, when the graph holds exactly the section's modules and
// their `inputs` hash differs, a requirement declared below its selected version. Partial graphs
// compare only their own modules, so a build of part of the repository sees no stale section
// the whole repository would not. The message follows SPEC §02.5 ("<label> needs go <module>
// <version> (not pinned)"), the position is the golang.Require call of the changed requirement
// when the bob.go can be read (req.Root), else the module's definition.
func (Ecosystem) Stale(ctx context.Context, req engine.LockRequest) (string, ir.Pos, error) {
	decls, nodes, err := declarations(req.Graph)
	if err != nil {
		return "", ir.Pos{}, err
	}
	sec, err := readGoSection(req.Prev)
	if err != nil {
		return "the go section cannot be read: " + err.Error(), ir.Pos{}, nil
	}
	rt := runtimes{ctx: ctx, req: req}
	for _, d := range decls {
		// A runtime pin that contradicts its generator is the R-GEN-006 error, not a stale lock.
		w := resolve.Workspace{BobGo: path.Join(labelDir(nodes[d.Path].Label), "bob.go")}
		var mismatch *diag.Error
		if err := rt.resolve(nodes[d.Path], &w); errors.As(err, &mismatch) && mismatch.Code == 1021 {
			return "", ir.Pos{}, mismatch
		}
		if req.Nixpkgs == nil {
			// Without the nixpkgs index the generator versions are unknown: an explicit
			// requirement of a generator's runtime is reported stale, so the caller asks again
			// with the index (build.staleSections), which decides.
			if r, ok := pinnedRuntime(nodes[d.Path]); ok {
				n := nodes[d.Path]
				return fmt.Sprintf("%s needs go %s, a code generator's runtime", n.Label, r), requirePos(req.Root, n, r), nil
			}
		}
	}
	for _, d := range decls {
		n := nodes[d.Path]
		bl, ok := sec.Workspaces[d.Path]
		switch {
		case !ok:
			return fmt.Sprintf("%s needs go module %s (not pinned)", n.Label, d.Path), n.DefinedAt, nil
		case bl.Go != goDirective(d.Go):
			return fmt.Sprintf("%s needs go %s (locked for go %s)", n.Label, d.Go, bl.Go), n.DefinedAt, nil
		}
		sel := selected(bl)
		for _, r := range d.Require {
			if strings.HasPrefix(r.Version, generatorPrefix) {
				continue // a generator's runtime: its version comes from the nixpkgs pin
			}
			if resolve.Compare(sel[r.Path], r.Version) < 0 {
				return fmt.Sprintf("%s needs go %s %s (not pinned)", n.Label, r.Path, r.Version), requirePos(req.Root, n, r.Path), nil
			}
		}
	}
	if len(decls) == 0 || len(decls) != len(sec.Workspaces) || sec.Inputs == "" {
		return "", ir.Pos{}, nil // a partial graph (or an old section): only its modules count
	}
	inputs, err := lock.Inputs(decls)
	if err != nil {
		return "", ir.Pos{}, err
	}
	if inputs == sec.Inputs {
		return "", ir.Pos{}, nil
	}
	// Every module is locked and every requirement selected at its version or higher, yet the
	// declarations changed. A requirement declared below its selected version may have been
	// lowered (the lock does not keep the declared versions, so one that MVS or --update raised
	// looks the same and costs one bob lock): the build would not use the declared version, so
	// the section is stale. A removed requirement or codegen recipe leaves the section usable;
	// bob lock drops the entries no declaration needs (R-FW-005).
	for _, d := range decls {
		n := nodes[d.Path]
		sel := selected(sec.Workspaces[d.Path])
		for _, r := range d.Require {
			if !strings.HasPrefix(r.Version, generatorPrefix) && resolve.Compare(sel[r.Path], r.Version) > 0 {
				return fmt.Sprintf("%s needs go %s %s (pinned %s)", n.Label, r.Path, r.Version, sel[r.Path]), requirePos(req.Root, n, r.Path), nil
			}
		}
	}
	return "", ir.Pos{}, nil
}

// selected maps the module paths of a build list to their versions.
func selected(bl lock.GoBuildList) map[string]string {
	out := map[string]string{}
	for _, mv := range bl.Modules {
		if p, v, ok := strings.Cut(mv, "@"); ok {
			out[p] = v
		}
	}
	return out
}

// generatorPrefix marks a runtime requirement whose version is the generator's in the nixpkgs
// pin (golang.GeneratorRuntime): "nixpkgs:<attr>" (at least) or "nixpkgs:=<attr>" (exact).
const generatorPrefix = "nixpkgs:"

// runtimes resolves the generator runtime requirements of workspace modules (§03.6.4).
type runtimes struct {
	ctx context.Context
	req engine.LockRequest
}

// resolve replaces the generator versions of w's runtime requirements by the pinned
// generators' versions and checks the module's explicit requirements against them (R-GEN-006):
// a contradicting golang.Require is BOB-E-1021 before anything is built.
func (rt runtimes) resolve(n *ir.Node, w *resolve.Workspace) error {
	var s gospec.Module
	if err := json.Unmarshal(n.Spec, &s); err != nil {
		return engine.Internal(n, "module spec: %v", err)
	}
	declared := map[string]string{}
	for _, r := range s.Require {
		declared[r.Path] = r.Version
	}
	byID := map[string]*ir.Node{}
	if rt.req.Graph != nil {
		for _, x := range rt.req.Graph.Nodes {
			byID[x.ID] = x
		}
	}
	var keep []resolve.Module
	for i := range w.Require {
		if !strings.HasPrefix(w.Require[i].Version, generatorPrefix) {
			keep = append(keep, w.Require[i])
		}
	}
	w.Require = keep
	for _, g := range s.Generated {
		label, at := n.Label, n.DefinedAt
		if g.Dep >= 0 && g.Dep < len(n.Deps) && byID[n.Deps[g.Dep]] != nil {
			label, at = byID[n.Deps[g.Dep]].Label, byID[n.Deps[g.Dep]].DefinedAt
		}
		for _, r := range g.Require {
			spec, ok := strings.CutPrefix(r.Version, generatorPrefix)
			if !ok {
				continue
			}
			attr, exact := strings.CutPrefix(spec, "=")
			version, err := rt.version(attr)
			if err != nil {
				return engine.Internal(n, "%s: %v", label, err)
			}
			need := "v" + version
			if have, ok := declared[r.Path]; ok {
				bad := have != need
				if !exact {
					bad = resolve.Compare(have, need) < 0
				}
				if bad {
					return engine.RuntimeMismatch(label, attr, version, r.Path, version, w.BobGo, have, at)
				}
			}
			for j := range w.GenRequire {
				if w.GenRequire[j].Path == r.Path && w.GenRequire[j].Version == r.Version {
					w.GenRequire[j].Version = need
				}
			}
		}
	}
	return nil
}

// version is the version of nixpkgs attribute attr in the pin.
func (rt runtimes) version(attr string) (string, error) {
	if rt.req.Nixpkgs == nil {
		return "", fmt.Errorf("the version of generator %s needs the nixpkgs index", attr)
	}
	es, err := rt.req.Nixpkgs.Lookup(rt.ctx, []string{attr})
	if err != nil {
		return "", err
	}
	e := es[attr]
	if e.Missing || e.Version == "" {
		return "", fmt.Errorf("nixpkgs has no generator %s", attr)
	}
	return e.Version, nil
}

// pinnedRuntime returns a module path that n's module both requires explicitly and needs as a
// generator runtime (golang.GeneratorRuntime).
func pinnedRuntime(n *ir.Node) (string, bool) {
	var s gospec.Module
	if err := json.Unmarshal(n.Spec, &s); err != nil {
		return "", false
	}
	declared := map[string]bool{}
	for _, r := range s.Require {
		declared[r.Path] = true
	}
	for _, g := range s.Generated {
		for _, r := range g.Require {
			if strings.HasPrefix(r.Version, generatorPrefix) && declared[r.Path] {
				return r.Path, true
			}
		}
	}
	return "", false
}
