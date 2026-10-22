// Package gospec holds the IR payloads ("spec") of the Go node kinds. pkg/golang writes them
// during evaluation, pkg/golang/gen reads them when planning. Dependency references are indices
// into the node's deps.
package gospec

import "github.com/megakuul/bob/pkg/bob/ext"

// Node kinds of the Go ecosystem; "go" is also the bob.lock section key.
const (
	KindModule  = "go.module"  // one Go module (go.mod is generated from it)
	KindBinary  = "go.binary"  // a main package linked into a program or a C library
	KindPackage = "go.package" // a cgo package with typed C dependencies (Module.Cgo)
	KindTests   = "go.tests"   // a set of test units (Module.Tests with a "..." pattern)
	KindTest    = "go.test"    // one test unit: the tests of one package
)

// Module is the spec of a go.module node. Deps: Toolchain, then the generated packages. The
// module root is the directory of the node's label.
type Module struct {
	Path      string        `json:"path"`      // module path
	Go        string        `json:"go"`        // "1.26"
	Tags      []string      `json:"tags"`      // build tags of every package
	Require   []Requirement `json:"require"`   // direct requirements, sorted by path
	Generated []Generated   `json:"generated"` // codegen packages realised into the module
	Toolchain int           `json:"toolchain"` // dep index of the Go toolchain
}

// Requirement is one direct module requirement.
type Requirement struct {
	Path    string `json:"path"`
	Version string `json:"version"`
}

// Generated is one realised codegen recipe: the package in Dir (module-relative) is the output
// of dep Dep.
type Generated struct {
	Dir     string        `json:"dir"`
	Dep     int           `json:"dep"`
	Require []Requirement `json:"require"` // runtime libraries the generated code needs
}

// Build modes of a binary.
const (
	ModeExe      = "exe"
	ModeCArchive = "c-archive"
	ModeCShared  = "c-shared"
)

// CGo settings of a binary or test.
const (
	CgoAuto = "auto" // on iff a non-std package of the closure imports "C"
	CgoOff  = "off"  // pure Go; importing "C" is an error
)

// Build is what binaries and test units share: where the C toolchain and the cgo packages are.
type Build struct {
	Module int      `json:"module"` // dep index of the go.module node
	CC     int      `json:"cc"`     // dep index of the C compiler tool, -1 = none (cgo off)
	Cgo    []int    `json:"cgo"`    // dep indices of the module's go.package nodes known at evaluation
	Tags   []string `json:"tags"`   // added to the module's tags
	CGo    string   `json:"cgoMode"`
}

// Binary is the spec of a go.binary node.
type Binary struct {
	Build
	Dir  string            `json:"dir"` // module-relative package directory ("." = module root)
	Name string            `json:"name"`
	X    map[string]string `json:"x"` // -X importpath.name=value
	Mode string            `json:"mode"`
}

// Package is the spec of a go.package node (Module.Cgo). Deps: Module, then the C libraries.
type Package struct {
	Module int    `json:"module"`
	Dir    string `json:"dir"`
	Libs   []int  `json:"libs"` // dep indices of the C libraries (bob.Linkable)
}

// Test is the spec of a go.test node: the tests of the package in Dir.
type Test struct {
	Build
	Dir      string        `json:"dir"`
	Args     []string      `json:"args"`
	Services []ext.Service `json:"services"`
	TimeoutS int64         `json:"timeoutSeconds"`
}

// Tests is the spec of a go.tests node; its deps are its units.
type Tests struct {
	Pattern string `json:"pattern"`
}
