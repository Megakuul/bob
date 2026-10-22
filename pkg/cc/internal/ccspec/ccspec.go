// Package ccspec holds the IR payloads ("spec") of the C/C++ node kinds. pkg/cc writes them
// during evaluation, pkg/cc/gen reads them when planning. Dependency references are indices
// into the node's deps.
package ccspec

// Node kinds of the C/C++ ecosystem; "cc" is also the bob.lock section key.
const (
	KindLibrary  = "cc.library"  // a static or shared library (StaticLibrary, SharedLibrary)
	KindBinary   = "cc.binary"   // a program (Binary)
	KindTest     = "cc.test"     // a test program and its run (Test)
	KindExternal = "cc.external" // a third-party project built with its own build system
)

// Target types of a cc.library, cc.binary or cc.test node.
const (
	TypeStatic = "static"
	TypeShared = "shared"
	TypeBinary = "binary"
	TypeTest   = "test"
)

// Tools are the dep indices of the host tools every C/C++ derivation uses.
type Tools struct {
	CC       int `json:"cc"`       // nixpkgs gcc (cc-wrapper: gcc, g++)
	Binutils int `json:"binutils"` // nixpkgs binutils (ar, strip, objcopy)
	Patchelf int `json:"patchelf"` // nixpkgs patchelf (fixup)
}

// Target is the spec of cc.library, cc.binary and cc.test nodes. The node's sources are its
// Srcs, Private and Hdrs files in one source path; files with a C/C++ source extension are
// compiled, the rest are headers.
type Target struct {
	Name     string            `json:"name"`
	Type     string            `json:"type"`     // static | shared | binary | test
	Std      string            `json:"std"`      // "" = per language default (c17, c++20)
	Defines  map[string]string `json:"defines"`  // -DKEY=VALUE ("" = -DKEY)
	Flags    []string          `json:"flags"`    // extra compile flags (validated)
	LinkOpts []string          `json:"linkOpts"` // extra link flags (shared libraries and programs)
	Hdrs     string            `json:"hdrs"`     // public include dir relative to the label dir; "" = none
	Private  bool              `json:"private"`  // private headers given: -I<label dir>
	Deps     []int             `json:"deps"`     // public deps (bob.Linkable)
	PrivDeps []int             `json:"privDeps"` // private deps
	Tools    Tools             `json:"tools"`
}

// Build systems of an External.
const (
	BuildCMake     = "cmake"
	BuildAutotools = "autotools"
	BuildMeson     = "meson"
	BuildMake      = "make"
)

// External is the spec of a cc.external node. Its source is pinned in bob.lock's cc section
// (keyed by the node's label), never in the spec: the spec carries what the lock must match.
type External struct {
	Name    string   `json:"name"`
	URL     string   `json:"url"`
	Strip   int      `json:"strip"`
	Build   string   `json:"build"` // cmake | autotools | meson | make
	Args    []string `json:"args"`
	Deps    []int    `json:"deps"`    // bob.Linkable deps available to the build
	Tools   []int    `json:"tools"`   // host tools: the build system's, then ExternalOpts.Tools
	Libs    []string `json:"libs"`    // link names; empty = from the installed .pc files
	Include string   `json:"include"` // include dir under the output
	CC      Tools    `json:"cc"`
}
