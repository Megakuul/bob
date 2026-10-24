package ocaml_test

import (
	"github.com/megakuul/bob/pkg/bob"
	"github.com/megakuul/bob/pkg/ocaml"
)

// A library with an opam dependency; its modules are Geom.X for consumers (wrapped).
func ExampleLibrary() {
	_ = bob.Define(func(p *bob.P) *ocaml.Lib {
		return ocaml.Library(p, ocaml.LibOpts{Name: "geom", Deps: []ocaml.Dep{ocaml.Opam("fmt", "")}})
	})
}

// A native program using a local library and one findlib library of lwt (bin/demo).
func ExampleExecutable() {
	geom := bob.Define(func(p *bob.P) *ocaml.Lib { return ocaml.Library(p, ocaml.LibOpts{Name: "geom"}) })
	_ = bob.Define(func(p *bob.P) *ocaml.Exe {
		return ocaml.Executable(p, ocaml.ExeOpts{Name: "demo", Deps: []ocaml.Dep{
			ocaml.Opam("cmdliner", ""), ocaml.Opam("lwt", ">= 5.7").Lib("lwt.unix"), geom(p)}})
	})
}

// A test executable whose main module is test.ml; building it runs it.
func ExampleNewTest() {
	geom := bob.Define(func(p *bob.P) *ocaml.Lib { return ocaml.Library(p, ocaml.LibOpts{Name: "geom"}) })
	_ = bob.Define(func(p *bob.P) *ocaml.Test {
		return ocaml.NewTest(p, ocaml.ExeOpts{Name: "geom_test", Main: "test", Deps: []ocaml.Dep{geom(p)}})
	})
}
