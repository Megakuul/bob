package gen

import (
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

	"github.com/megakuul/bob/pkg/bob/diag"
	"github.com/megakuul/bob/pkg/engine"
	"github.com/megakuul/bob/pkg/engine/ir"
	"github.com/megakuul/bob/pkg/engine/lock"
	"github.com/megakuul/bob/pkg/engine/nar"
	"github.com/megakuul/bob/pkg/golang/internal/gospec"
	"github.com/megakuul/bob/pkg/golang/resolve"
)

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
	for _, d := range decls {
		w, err := workspaceOf(nodes[d.Path], req.Root)
		if err != nil {
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
		return resolve.Workspace{}, internal(n, "module spec: %v", err)
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
// not reflect (R-FW-062): a module without a build list, another Go version, or a declared
// requirement its build list does not select at the declared version or higher (bob lock
// --update may have moved it up). Only modules of the graph are compared, so a build of
// part of the repository sees the same answer as one of all of it.
func (Ecosystem) Stale(_ context.Context, req engine.LockRequest) (string, ir.Pos, error) {
	decls, nodes, err := declarations(req.Graph)
	if err != nil {
		return "", ir.Pos{}, err
	}
	v, err := lock.DecodeSection(lock.SectionGo, req.Prev)
	if err != nil {
		return "the go section cannot be read: " + err.Error(), ir.Pos{}, nil
	}
	sec := v.(*lock.GoSection)
	for _, d := range decls {
		at := nodes[d.Path].DefinedAt
		bl, ok := sec.Workspaces[d.Path]
		switch {
		case !ok:
			return fmt.Sprintf("Go module %s is not locked", d.Path), at, nil
		case bl.Go != goDirective(d.Go):
			return fmt.Sprintf("Go module %s declares Go %s but is locked for %s", d.Path, d.Go, bl.Go), at, nil
		}
		sel := selected(bl)
		for _, r := range d.Require {
			if resolve.Compare(sel[r.Path], r.Version) < 0 {
				return fmt.Sprintf("Go module %s requires %s %s, which is not locked", d.Path, r.Path, r.Version), at, nil
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
