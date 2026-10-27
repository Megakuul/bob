package nixpkgs

import "github.com/megakuul/bob/pkg/nixpkgs/nixext"

// Attr is one nixpkgs package. Values exist only as the generated handles of this package
// (nixpkgs.Openssl, nixpkgs.Protobuf, …): no string converts to an Attr. The role is chosen by
// the method (R-FW-032):
//
//	nixpkgs.Protobuf.Tool(p)                             // build time, host package set: a bob.Tool, not a Linkable
//	nixpkgs.Openssl.Lib(p, nixpkgs.Modules("libcrypto")) // linked into target code: a bob.Linkable, not a bob.Tool
//	nixpkgs.Tzdata.Pkg(p)                                // needed at run time
//	nixpkgs.Curl.Exe(p)                                  // run as a program (meta.mainProgram or MainProgram)
type Attr = nixext.Attr

// Option changes how a nixpkgs package is used.
type Option = nixext.Option

// Output names a store output of a nixpkgs package (Outputs).
type Output = nixext.Output

// The common output names.
const (
	Out       = nixext.Out
	OutBin    = nixext.OutBin
	OutDev    = nixext.OutDev
	OutLib    = nixext.OutLib
	OutMan    = nixext.OutMan
	OutDoc    = nixext.OutDoc
	OutDebug  = nixext.OutDebug
	OutStatic = nixext.OutStatic
)

// Package is a nixpkgs package of the target set (Lib, Pkg): a bob.Tool, a bob.Tree and, for
// linking, a bob.Linkable (cc.Linkable).
type Package = nixext.Package

// ToolPackage is a nixpkgs package of the host set used at build time (Tool): a bob.Tool and a
// bob.Tree, not a library (R-FW-032).
type ToolPackage = nixext.ToolPackage

// Program is a nixpkgs package used as a bob.Executable (Exe).
type Program = nixext.Program

// Outputs selects the package's outputs; default: the package's default outputs.
//
//	nixpkgs.Openssl.Lib(p, nixpkgs.Outputs(nixpkgs.Out, nixpkgs.OutDev))
func Outputs(names ...Output) Option { return nixext.Outputs(names...) }

// Modules lists the pkg-config modules a Lib links; default: all modules of its dev output.
// The names are the package's own .pc files, checked when the build is planned.
func Modules(names ...string) Option { return nixext.Modules(names...) }

// Cross takes a target library from the cross package set (A19); default: the target system's
// own set. A Tool always comes from the host set.
func Cross() Option { return nixext.Cross() }

// AllowInsecure accepts these knownVulnerabilities entries of the package.
func AllowInsecure(ids ...string) Option { return nixext.AllowInsecure(ids...) }

// AllowUnfree accepts an unfree licence (the doc of an unfree handle says so).
func AllowUnfree() Option { return nixext.AllowUnfree() }

// MainProgram sets the program Exe runs, relative to the package's "bin" output when it has
// one, else to "out" ("bin/curl"). Default: "bin/" + the package's meta.mainProgram.
func MainProgram(path string) Option { return nixext.MainProgram(path) }
