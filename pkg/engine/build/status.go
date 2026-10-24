package build

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"sort"
	"strings"

	"github.com/megakuul/bob/pkg/bob/diag"
	"github.com/megakuul/bob/pkg/engine"
	"github.com/megakuul/bob/pkg/engine/eval"
	"github.com/megakuul/bob/pkg/engine/ir"
	"github.com/megakuul/bob/pkg/engine/lock"
	"github.com/megakuul/bob/pkg/engine/materialise"
	"github.com/megakuul/bob/pkg/engine/srcs"
	"github.com/megakuul/bob/pkg/engine/store"
)

// bob status (SPEC §02.1, §03.6.6) and the codegen watch of bob dev. Neither plans nor builds:
// whether an owned directory is fresh is decided by an inputs digest that the materialise step
// records in the manifest (A280). It covers what the output is made from as far as the graph
// tells without planning: for codegen the declarations and source files of the generator's
// closure and the nixpkgs pin, for other working-tree outputs (an npm importer's node_modules)
// the package's own declaration and bob.lock.

// StatusResult is what bob status reports.
type StatusResult struct {
	Roots []materialise.RootStatus // sorted by directory, then the not yet generated ones
	Lock  []*diag.Error            // stale, missing or extra bob.lock entries (BOB-E-1012)
}

// Status reports, per owned directory, fresh | stale | missing | edited | orphan, and the
// freshness of bob.lock, without network and without writing to the working tree (bob status). Only the
// evaluator's Go toolchain may be fetched, as for bob check.
func Status(ctx context.Context, o Options) (*StatusResult, error) {
	st, g, lf, err := statusGraph(ctx, o)
	if err != nil {
		return nil, err
	}
	roots, err := materialiseStatus(ctx, o.Root, g, lf, statusSrcPath(o.Root, nil))
	if err != nil {
		return nil, err
	}
	findings, err := lockFindings(ctx, o.Root, g, lf, offlineFetcher{st: st})
	if err != nil {
		return nil, err
	}
	return &StatusResult{Roots: roots, Lock: findings}, nil
}

// CodegenState is what bob dev's codegen watch needs (StaleCodegen).
type CodegenState struct {
	// Stale are the codegen packages whose owned directory is stale or missing, with the inputs
	// digest of each (the digest tells a new change from one already tried).
	Stale map[string]string
	// Watch are the files the digests read (codegen sources, bob.lock), repo-relative: a change
	// of one of them is worth another StaleCodegen.
	Watch []string
}

// StaleCodegen evaluates the repository and reports which codegen packages bob dev regenerates.
func StaleCodegen(ctx context.Context, o Options) (*CodegenState, error) {
	_, g, lf, err := statusGraph(ctx, o)
	if err != nil {
		return nil, err
	}
	watch := map[string]bool{"bob.lock": true}
	srcPath := statusSrcPath(o.Root, func(files []string) {
		for _, f := range files {
			watch[f] = true
		}
	})
	roots, err := materialiseStatus(ctx, o.Root, g, lf, srcPath)
	if err != nil {
		return nil, err
	}
	kinds := labelKinds(g)
	digests := newDigester(o.Root, g, lf, srcPath)
	out := &CodegenState{Stale: map[string]string{}}
	for _, r := range roots {
		if !kinds.codegen[r.Label] || r.State != materialise.StateStale && r.State != materialise.StateMissing {
			continue
		}
		d, err := digests.of(r.Label)
		if err != nil {
			return nil, err
		}
		out.Stale[r.Label] = d
	}
	for f := range watch {
		out.Watch = append(out.Watch, f)
	}
	sort.Strings(out.Watch)
	return out, nil
}

// statusGraph evaluates the whole repository in its host configuration (as bob gen does) and
// reads bob.lock.
func statusGraph(ctx context.Context, o Options) (*store.Store, *ir.Graph, *lock.File, error) {
	st, err := openStore(ctx, o.Store, o.Home, o.Caches)
	if err != nil {
		return nil, nil, nil, err
	}
	o.Patterns, o.Platform, o.Variants = nil, "", nil
	g, _, err := evaluate(ctx, st, o)
	if err != nil {
		return nil, nil, nil, err
	}
	lf, err := readLock(o.Root)
	if err != nil {
		return nil, nil, nil, err
	}
	return st, g, lf, nil
}

// materialiseStatus is materialise.Status for the graph: every codegen package and every
// package a manifest root names is defined, with its current inputs digest.
func materialiseStatus(ctx context.Context, root string, g *ir.Graph, lf *lock.File, srcPath func(*ir.Node) (string, error)) ([]materialise.RootStatus, error) {
	kinds := labelKinds(g)
	digests := newDigester(root, g, lf, srcPath)
	defined := map[string]string{}
	want := []string{}
	for l := range kinds.codegen {
		want = append(want, l)
	}
	for _, l := range materialise.ManifestLabels(root) {
		if kinds.all[l] {
			want = append(want, l)
		}
	}
	for _, l := range want {
		d, err := digests.of(l)
		if err != nil {
			return nil, err
		}
		defined[l] = d
	}
	return materialise.Status(ctx, root, defined)
}

type kindsOfLabels struct {
	all, codegen map[string]bool
}

func labelKinds(g *ir.Graph) kindsOfLabels {
	k := kindsOfLabels{all: map[string]bool{}, codegen: map[string]bool{}}
	for _, n := range g.Nodes {
		k.all[n.Label] = true
		if engine.IsCodegenKind(n.Kind) {
			k.codegen[n.Label] = true
		}
	}
	return k
}

// statusSrcPath computes a node's source store path as the sources stage imports it
// (importSources), without touching the store; seen (if not nil) receives the files.
func statusSrcPath(root string, seen func(files []string)) func(*ir.Node) (string, error) {
	return func(n *ir.Node) (string, error) {
		if len(n.Srcs) == 0 {
			return "", nil
		}
		files, err := srcs.ResolveAll(root, n.Srcs, n.DefinedAt.String(), nil)
		if err != nil {
			return "", err
		}
		if seen != nil {
			seen(files)
		}
		return srcs.Path(storeName(&ir.Node{Label: n.Label})+"-src", srcs.Tree{Root: root, Files: files})
	}
}

// digester computes inputs digests of labels of one graph, sharing source paths.
type digester struct {
	g       *ir.Graph
	byID    map[string]*ir.Node
	pin     string // the nixpkgs pin (codegen tools come from it)
	lockSum string // sha256 of bob.lock's bytes
	srcPath func(*ir.Node) (string, error)
	paths   map[string]string // label → source path
}

func newDigester(root string, g *ir.Graph, lf *lock.File, srcPath func(*ir.Node) (string, error)) *digester {
	d := &digester{g: g, byID: nodesByID(g), srcPath: srcPath, paths: map[string]string{}}
	if lf != nil {
		pin, _ := json.Marshal(lf.Nixpkgs)
		d.pin = string(pin)
	}
	sum := sha256.New()
	if b, err := os.ReadFile(lockPath(root)); err == nil {
		sum.Write(b)
	} else if !errors.Is(err, fs.ErrNotExist) {
		sum.Write([]byte("unreadable"))
	}
	d.lockSum = hex.EncodeToString(sum.Sum(nil))
	return d
}

// of is the inputs digest of label: for a codegen package its closure's declarations (kind,
// label, spec, nixpkgs attribute) and source paths plus the nixpkgs pin; for any other package
// its own declaration plus bob.lock. Positions are left out: moving a declaration changes
// nothing that is generated.
func (d *digester) of(label string) (string, error) {
	var start []*ir.Node
	codegen := false
	for _, n := range d.g.Nodes {
		if n.Label == label {
			start = append(start, n)
			codegen = codegen || engine.IsCodegenKind(n.Kind)
		}
	}
	nodes := start
	if codegen {
		nodes = closureOf(d.byID, start)
	}
	var parts []string
	for _, n := range nodes {
		src := ""
		if codegen {
			p, ok := d.paths[n.Label]
			if !ok {
				var err error
				if p, err = d.srcPath(n); err != nil {
					return "", err
				}
				d.paths[n.Label] = p
			}
			src = p
		}
		nix, _ := json.Marshal(n.Nix)
		parts = append(parts, strings.Join([]string{n.Kind, n.Label, string(n.Spec), string(nix), src}, "\x00"))
	}
	sort.Strings(parts)
	h := sha256.New()
	h.Write([]byte("bob-inputs-1\x00" + eval.BobVersion() + "\x00"))
	if codegen {
		h.Write([]byte(d.pin))
	} else {
		h.Write([]byte(d.lockSum))
	}
	prev := ""
	for _, p := range parts {
		if p != prev { // one entry per declaration, whatever the number of configurations
			h.Write([]byte{0})
			h.Write([]byte(p))
		}
		prev = p
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// closureOf is start and every node it depends on, transitively.
func closureOf(byID map[string]*ir.Node, start []*ir.Node) []*ir.Node {
	seen := map[string]bool{}
	var out []*ir.Node
	queue := append([]*ir.Node(nil), start...)
	for len(queue) > 0 {
		n := queue[0]
		queue = queue[1:]
		if n == nil || seen[n.ID] {
			continue
		}
		seen[n.ID] = true
		out = append(out, n)
		for _, id := range n.Deps {
			queue = append(queue, byID[id])
		}
	}
	return out
}
