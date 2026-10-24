// Package bob is a throwaway mock (spike S1) of the framework core: just enough API for
// bob.go files to be written, type-checked by gopls and evaluated into a package graph.
package bob

import (
	"encoding/json"
	"fmt"
	"os"
	"runtime"
	"sort"
	"strings"
	"sync"
)

// P is the evaluation context (platform, variant). One P per (platform, variant).
type P struct {
	Platform string
	Variant  string
	memo     map[int]any
	graph    *graph
}

// Package is implemented by every typed package value (cc.Library, golang.Binary, ...).
type Package interface {
	Label() Label
	base() *Base
}

// Label is the CLI name of a package: //dir:name (own repo) or module//dir:name (other bob repo).
type Label struct {
	Module string // "" for the main module
	Dir    string // slash-separated, relative to the module root; "" for the root
	Name   string
}

func (l Label) String() string {
	if l.Module == "nixpkgs" {
		return "nixpkgs:" + l.Name
	}
	return l.Module + "//" + l.Dir + ":" + l.Name
}

// Nixpkgs returns the shared package for a nixpkgs attribute (used by the nixpkgs package).
func Nixpkgs(p *P, attr string) *Base {
	l := Label{Module: "nixpkgs", Name: attr}
	if b, ok := p.graph.pkgs[l.String()]; ok {
		return b
	}
	b := &Base{label: l, kind: "nixpkgs", attrs: map[string]any{"attr": attr}}
	p.graph.pkgs[l.String()] = b
	return b
}

// Base is embedded by every package type.
type Base struct {
	label     Label
	kind      string
	definedAt string
	deps      []Package
	attrs     map[string]any
}

func (b *Base) Label() Label { return b.label }
func (b *Base) base() *Base  { return b }

// Source is a set of repo files (directory or glob) relative to the bob.go directory.
type Source struct{ Patterns []string }

func Glob(patterns ...string) Source { return Source{Patterns: patterns} }

type definer struct {
	fn  func(*P) any
	pos string
}

var (
	regMu    sync.Mutex
	registry []definer // filled during package initialisation only (memoised constructors)
)

// Define turns a constructor into a memoised, registered constructor (R-FW-011): calling the
// result twice with the same P returns the same package. All registered constructors are
// evaluated, whether or not anything references them.
func Define[T any](fn func(p *P) T) func(p *P) T {
	regMu.Lock()
	defer regMu.Unlock()
	id := len(registry)
	_, file, line, _ := runtime.Caller(1)
	memo := func(p *P) T {
		if v, ok := p.memo[id]; ok {
			return v.(T)
		}
		v := fn(p)
		p.memo[id] = v
		return v
	}
	registry = append(registry, definer{fn: func(p *P) any { return memo(p) }, pos: fmt.Sprintf("%s:%d", file, line)})
	return memo
}

type graph struct {
	pkgs map[string]*Base
	errs []string
}

// NewPackage is called by ecosystem builders. skip = stack frames between the bob.go call
// and this function.
func NewPackage(p *P, skip int, kind, name string, deps []Package, attrs map[string]any) *Base {
	_, file, line, _ := runtime.Caller(skip + 1)
	mod, dir := splitPos(file)
	b := &Base{label: Label{Module: mod, Dir: dir, Name: name}, kind: kind, definedAt: fmt.Sprintf("%s:%d", file, line), deps: deps, attrs: attrs}
	if old, dup := p.graph.pkgs[b.label.String()]; dup {
		p.graph.errs = append(p.graph.errs, fmt.Sprintf("%s: package %s is already defined at %s", b.definedAt, b.label, old.definedAt))
	} else {
		p.graph.pkgs[b.label.String()] = b
	}
	return b
}

// MainModule is set by the generated evaluator (the module path of the repo).
var MainModule string

// splitPos maps a -trimpath source position "example.com/acme/libs/foo/bob.go" to a label.
func splitPos(file string) (mod, dir string) {
	d := file[:strings.LastIndex(file, "/")]
	if MainModule != "" && (d == MainModule || strings.HasPrefix(d, MainModule+"/")) {
		return "", strings.TrimPrefix(strings.TrimPrefix(d, MainModule), "/")
	}
	// foreign module: path@version/dir
	if i := strings.Index(d, "@"); i >= 0 {
		rest := d[i:]
		j := strings.Index(rest, "/")
		if j < 0 {
			return d[:i], ""
		}
		return d[:i], rest[j+1:]
	}
	return d, ""
}

type jsonPkg struct {
	Label     string         `json:"label"`
	Kind      string         `json:"kind"`
	DefinedAt string         `json:"definedAt"`
	Deps      []string       `json:"deps,omitempty"`
	Attrs     map[string]any `json:"attrs,omitempty"`
}

// Main is called by the generated evaluator after all bob.go packages are initialised.
func Main() {
	p := &P{Platform: os.Getenv("BOB_PLATFORM"), Variant: "default", memo: map[int]any{}, graph: &graph{pkgs: map[string]*Base{}}}
	for _, d := range registry {
		d.fn(p)
	}
	if len(p.graph.errs) > 0 {
		for _, e := range p.graph.errs {
			fmt.Fprintln(os.Stderr, e)
		}
		os.Exit(1)
	}
	out := make([]jsonPkg, 0, len(p.graph.pkgs))
	for _, b := range p.graph.pkgs {
		j := jsonPkg{Label: b.label.String(), Kind: b.kind, DefinedAt: b.definedAt, Attrs: b.attrs}
		for _, d := range b.deps {
			j.Deps = append(j.Deps, d.Label().String())
		}
		sort.Strings(j.Deps)
		out = append(out, j)
	}
	sort.Slice(out, func(i, k int) bool { return out[i].Label < out[k].Label })
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", " ")
	if err := enc.Encode(map[string]any{"definitions": len(registry), "packages": out}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
