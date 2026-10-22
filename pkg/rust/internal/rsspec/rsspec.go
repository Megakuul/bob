// Package rsspec holds the IR payloads ("spec") of the Rust node kinds. pkg/rust writes them
// during evaluation, pkg/rust/gen reads them when planning. Dependency references are indices
// into the node's deps.
package rsspec

import "github.com/megakuul/bob/pkg/bob/ext"

// Node kinds of the Rust ecosystem; "rust" is also the bob.lock section key.
const (
	KindCrate = "rust.crate" // a local crate: Library, ProcMacro, StaticLib, CDylib, Binary
	KindTest  = "rust.test"  // one test unit: the unit tests of a crate or one tests/*.rs file
	KindTests = "rust.tests" // a set of test units (rust.Test of a crate with integration tests)
)

// Crate types (rustc --crate-type).
const (
	TypeLib       = "lib"
	TypeProcMacro = "proc-macro"
	TypeBin       = "bin"
	TypeStaticLib = "staticlib"
	TypeCDylib    = "cdylib"
)

// Crate is the spec of a rust.crate node. The crate's directory is the directory of the
// node's label. Deps of the node: the toolchain tools (Toolchain, CC, Binutils), the local
// crates of Deps/BuildDeps/DevDeps, the Native libraries and the Uses libraries.
type Crate struct {
	Name        string              `json:"name"`        // package name (Cargo's, '-' kept)
	Type        string              `json:"type"`        // Type*
	Edition     string              `json:"edition"`     // "2021"
	Root        string              `json:"root"`        // crate root relative to the crate dir
	Deps        []Dep               `json:"deps"`        // [dependencies]
	BuildDeps   []Dep               `json:"buildDeps"`   // [build-dependencies]
	DevDeps     []Dep               `json:"devDeps"`     // [dev-dependencies]
	Features    map[string][]string `json:"features"`    // the crate's feature table
	Default     []string            `json:"default"`     // default features
	Enable      []string            `json:"enable"`      // features enabled when built as a root
	BuildScript string              `json:"buildScript"` // relative to the crate dir; "" = none
	Native      []int               `json:"native"`      // dep indices of bob.Linkable libraries
	Headers     string              `json:"headers"`     // staticlib/cdylib include dir; "" = none
	Toolchain   int                 `json:"toolchain"`   // dep index of rustc
	CC          int                 `json:"cc"`          // dep index of the C compiler (linker)
	Binutils    int                 `json:"binutils"`    // dep index of binutils (ar for build scripts)
	PkgConfig   int                 `json:"pkgConfig"`   // dep index of pkg-config; -1 = none (no Uses)
	Uses        []Use               `json:"uses"`        // native packages of registry crates' build scripts
	Cross       *Cross              `json:"cross,omitempty"` // set when the crate is built for another platform than the host
}

// Cross is what a crate built for a foreign platform needs besides the host toolchain (SPEC
// R-FW-033 Rust row, R-FW-034): the cross C compiler (linker) and binutils of the target, and
// the target system's rustc whose standard library becomes the sysroot.
type Cross struct {
	Triple   string `json:"triple"`   // target triple ("x86_64-unknown-linux-gnu")
	Prefix   string `json:"prefix"`   // command prefix of the cross C tools ("x86_64-unknown-linux-gnu-")
	CC       int    `json:"cc"`       // dep index of the cross C compiler wrapper
	Binutils int    `json:"binutils"` // dep index of its binutils
	Std      int    `json:"std"`      // dep index of the target system's rustc (unwrapped)
}

// Dep is one dependency edge of a local crate.
type Dep struct {
	Name      string   `json:"name"`            // name in code (rename key, '-' kept)
	Crate     string   `json:"crate,omitempty"` // registry crate; "" for a local crate
	Req       string   `json:"req,omitempty"`   // version requirement of a registry crate
	Local     int      `json:"local"`           // dep index of the local crate node; -1 = registry
	Features  []string `json:"features"`
	NoDefault bool     `json:"noDefault,omitempty"`
}

// Use gives a registry crate's build script native packages (rust.Uses).
type Use struct {
	Crate string `json:"crate"`
	Libs  []int  `json:"libs"` // dep indices of bob.Linkable packages
}

// Test is the spec of a rust.test node: the unit tests of the crate (File "") or one
// integration test file. Deps: the crate node first.
type Test struct {
	Crate    int           `json:"crate"` // dep index of the rust.crate node
	File     string        `json:"file"`  // "" or "tests/<stem>.rs" (relative to the crate dir)
	Args     []string      `json:"args"`
	Services []ext.Service `json:"services"`
	TimeoutS int64         `json:"timeoutSeconds"`
	// Emulator is the dep index of the user-mode emulator that runs a test of another platform
	// (qemu-user, R-FW-039); 0 = none (index 0 is the crate).
	Emulator int `json:"emulator,omitempty"`
	// EmulatorCmd is its command in bin/ ("qemu-x86_64").
	EmulatorCmd string `json:"emulatorCmd,omitempty"`
}

// Tests is the spec of a rust.tests node; its deps are its units.
type Tests struct {
	Crate string `json:"crate"`
}
