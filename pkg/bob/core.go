package bob

import "reflect"

// P is the configuration a constructor builds for: one platform and one variant.
// bob creates one P per requested configuration; constructors must not keep it.
type P struct {
	ev       *evaluation
	platform Platform
	variant  Variant // normalised
	id       string  // canonical "<platform>[+token...]"
	host     *P
}

// Platform returns the platform packages built with p run on.
func (p *P) Platform() Platform {
	if p == nil {
		return ""
	}
	return p.platform
}

// Variant returns the variant of p (profile, sanitisers, features, named variants).
func (p *P) Variant() Variant {
	if p == nil {
		return Variant{}.normal()
	}
	return p.variant.normal()
}

// Host returns the configuration for build-time tools of p: the host platform, release
// profile, no sanitisers, no features. Host().Host() == Host().
func (p *P) Host() *P {
	if p == nil || p.ev == nil {
		return p
	}
	if p.host == nil {
		p.host = p.ev.config(p.ev.hostPlatform, Variant{})
	}
	return p.host
}

// Package is a node of the build graph. It is implemented only by types that embed Node,
// and a Node is created only by ext.New; the unexported method seals the interface.
type Package interface {
	Label() Label
	DefinedAt() Position
	// Outputs lists the package's store outputs ("out" first, then e.g. "dev", "lib").
	Outputs() []Output
	bobNode() *Node
}

// Node is embedded by every ecosystem package type. Its fields are unexported; ext.New
// creates it (through an internal hook in pkg/bob/internal/graph, which avoids an import
// cycle). A Node is a handle: copies of it refer to the same graph node.
//
// Node provides the marker methods of Tree, Tool and Test, so a type that embeds it is any of
// those; the exported methods (MainProgram, LinkInfo) are what distinguish the kinds.
type Node struct{ n *node }

// Label returns the package's label.
func (n *Node) Label() Label {
	if n == nil || n.n == nil {
		return Label{}
	}
	return n.n.label
}

// DefinedAt returns the position of the constructor call in a bob.go (R-FW-016).
func (n *Node) DefinedAt() Position {
	if n == nil || n.n == nil {
		return Position{}
	}
	return n.n.pos
}

// Outputs lists the package's store outputs.
func (n *Node) Outputs() []Output {
	if n == nil || n.n == nil {
		return nil
	}
	return []Output{{Package: n, Name: "out"}}
}

func (n *Node) bobNode() *Node { return n }
func (n *Node) tree()          {}
func (n *Node) tool()          {}
func (n *Node) test()          {}

// Output names one store output of a package, e.g. the "dev" output of a library.
type Output struct {
	Package Package
	Name    string // "out", "dev", "lib", "debug"
}

// Tree is a package whose "out" output is a directory tree that other steps can read.
type Tree interface {
	Package
	tree()
}

// Tool is a package that provides executables in bin/ for build steps and dev shells.
type Tool interface {
	Package
	tool()
}

// Executable is a package with one main program, e.g. a Go or Rust binary.
// MainProgram is a path relative to the "out" output, e.g. "bin/api".
type Executable interface {
	Tool
	MainProgram() string
}

// Linkable is a native (C ABI) library. cc.Linkable is an alias of it (§03).
type Linkable interface {
	Package
	LinkInfo() LinkInfo
}

// LinkInfo is what a Linkable gives its consumers (cgo, Rust Native, OCaml Native, cc Deps).
// Paths are store paths; they are filled by bob's generators, not by the evaluator
// (the evaluator only records the Linkable's identity and Modules/Libs choices).
type LinkInfo struct {
	IncludeDirs []string   // -I
	LinkDirs    []string   // -L
	Libs        []string   // -l names, in link order
	Archives    []string   // static archives, absolute paths
	LinkArgs    []string   // extra linker arguments
	RuntimeDirs []string   // RUNPATH entries
	PkgConfig   []string   // pkg-config dirs (PKG_CONFIG_PATH)
	Public      []Linkable // transitive public set, needed at the final link
}

// Test is a package whose build runs tests (R-FW-024). Every ecosystem's Test constructor
// returns one; running a test is building it.
type Test interface {
	Package
	test()
}

// nodeOf returns the graph node behind pkg, or nil for a nil package (also a typed nil
// pointer) or a Node that was never created.
func nodeOf(pkg Package) *node {
	if isNil(pkg) {
		return nil
	}
	n := pkg.bobNode()
	if n == nil {
		return nil
	}
	return n.n
}

// isNil reports whether v is nil or a typed nil (a nil *T stored in an interface).
func isNil(v any) bool {
	if v == nil {
		return true
	}
	switch rv := reflect.ValueOf(v); rv.Kind() {
	case reflect.Pointer, reflect.Map, reflect.Slice, reflect.Func, reflect.Interface, reflect.Chan:
		return rv.IsNil()
	}
	return false
}
