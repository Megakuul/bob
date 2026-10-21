//go:build bob

package app

import (
	"example.com/ocamlfix/geom"
	"github.com/megakuul/bob/pkg/bob"
	"github.com/megakuul/bob/pkg/ocaml"
)

// Demo is bin/demo (label //app:demo).
var Demo = bob.Define(func(p *bob.P) *ocaml.Exe {
	return ocaml.Executable(p, ocaml.ExeOpts{
		Name: "demo",
		Deps: []ocaml.Dep{
			ocaml.Opam("cmdliner", ""),
			ocaml.Opam("yojson", ""),
			ocaml.Opam("lwt", ">= 5.7").Lib("lwt.unix"),
			ocaml.Opam("fmt", ""),
			geom.Lib(p),
		},
	})
})
