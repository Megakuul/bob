package bob

import "github.com/megakuul/bob/pkg/bob/diag"

// The declarations below are the L0 surface of SPEC §1.4. They are stubs until M3.2: every
// function that can fail returns diag.NotImplemented, the others return zero values.

// P is the configuration a constructor builds for: one platform and one variant.
// bob creates one P per requested configuration; constructors must not keep it.
type P struct{ _ [0]func() }

// Platform returns the platform packages built with p run on.
func (p *P) Platform() Platform { return "" }

// Variant returns the variant of p (profile, sanitisers, features, named variants).
func (p *P) Variant() Variant { return Variant{} }

// Host returns the configuration for build-time tools of p: the host platform, release
// profile, no sanitisers, no features. Host().Host() == Host().
func (p *P) Host() *P { return p }

// Define turns a constructor into a memoised, registered constructor. It must be called in a
// package-level variable declaration of a bob.go file. The returned function calls fn at most
// once per P and returns the same value on every later call with that P (R-FW-011).
// Every value reachable from fn's result that implements Package becomes part of the graph.
func Define[T any](fn func(p *P) T) func(p *P) T {
	return func(*P) T {
		var zero T
		return zero
	}
}

// Main is called by the generated evaluator. bob.go files never call it.
func Main() {}

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
// creates it.
type Node struct {
	label Label
	pos   Position
	outs  []string
}

// Label returns the package's label.
func (n *Node) Label() Label { return n.label }

// DefinedAt returns the position of the outermost constructor call in a bob.go (R-FW-016).
func (n *Node) DefinedAt() Position { return n.pos }

// Outputs lists the package's store outputs.
func (n *Node) Outputs() []Output {
	var out []Output
	for _, name := range n.outs {
		out = append(out, Output{Name: name})
	}
	return out
}

func (n *Node) bobNode() *Node { return n }

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
// Paths are store paths; they are filled by bob's generators, not by the evaluator.
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
// returns one.
type Test interface {
	Package
	test()
}

// Label identifies a package, e.g. //libs/foo:foo (see SPEC §1.3).
type Label struct {
	Module, Dir, Name string
	Nix               string
}

// ParseLabel parses a label of the grammar of SPEC §1.3.
func ParseLabel(s string) (Label, error) { return Label{}, diag.NotImplemented("bob.ParseLabel") }

// String returns the canonical form of l.
func (l Label) String() string { return "" }

// Position is a file and line in a bob.go file, module-relative.
type Position struct {
	File string
	Line int
}

// String returns "libs/foo/bob.go:12".
func (p Position) String() string { return "" }
