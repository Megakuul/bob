package rust_test

import (
	"github.com/megakuul/bob/pkg/bob"
	"github.com/megakuul/bob/pkg/nixpkgs"
	"github.com/megakuul/bob/pkg/rust"
)

// A library with a crates.io dependency, its own features and a native package for the
// openssl-sys build script of the graph.
func ExampleLibrary() {
	_ = bob.Define(func(p *bob.P) *rust.Lib {
		return rust.Library(p, rust.LibOpts{
			Deps:     []rust.Dep{rust.Crate("serde", "1", rust.Features("derive")), rust.Crate("openssl", "0.10")},
			Features: map[string][]string{"fast": {}, "json": {"dep:serde_json"}},
			Default:  []string{"fast"},
			Options:  []rust.Option{rust.Uses(rust.Indirect("openssl-sys"), nixpkgs.Openssl.Lib(p))},
		})
	})
}

// A derive macro; it is compiled for the host and loaded by rustc.
func ExampleProcMacro() {
	_ = bob.Define(func(p *bob.P) *rust.Lib {
		return rust.ProcMacro(p, rust.LibOpts{Deps: []rust.Dep{rust.Crate("syn", "2", rust.Features("full")), rust.Crate("quote", "1")}})
	})
}

// A static library for C and cgo consumers, with its header directory.
func ExampleStaticLib() {
	_ = bob.Define(func(p *bob.P) *rust.CLib {
		return rust.StaticLib(p, rust.LibOpts{Name: "rcore", Headers: bob.IncludeDir("include")})
	})
}

// A shared library for C consumers.
func ExampleCDylib() {
	_ = bob.Define(func(p *bob.P) *rust.CLib {
		return rust.CDylib(p, rust.LibOpts{Name: "plugin", Headers: bob.IncludeDir("include")})
	})
}

// A program using a local library with one of its features enabled.
func ExampleBinary() {
	core := bob.Define(func(p *bob.P) *rust.Lib {
		return rust.Library(p, rust.LibOpts{Name: "core", Features: map[string][]string{"hexout": {}}})
	})
	_ = bob.Define(func(p *bob.P) *rust.Bin {
		return rust.Binary(p, rust.BinOpts{Deps: []rust.Dep{rust.Crate("tokio", "1", rust.Features("rt", "macros")), core(p).With("hexout")}})
	})
}

// The unit tests and integration tests of a library.
func ExampleTest() {
	core := bob.Define(func(p *bob.P) *rust.Lib { return rust.Library(p, rust.LibOpts{}) })
	_ = bob.Define(func(p *bob.P) *rust.TestSet { return rust.Test(p, rust.TestOpts{Crate: core(p)}) })
}
