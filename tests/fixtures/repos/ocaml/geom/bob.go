//go:build bob

package geom

import (
	"example.com/ocamlfix/cfoo"
	"github.com/megakuul/bob/pkg/bob"
	"github.com/megakuul/bob/pkg/cc"
	"github.com/megakuul/bob/pkg/ocaml"
)

// Lib is the geom library with a C stub (label //geom:geom).
var Lib = bob.Define(func(p *bob.P) *ocaml.Lib {
	return ocaml.Library(p, ocaml.LibOpts{
		Deps:   []ocaml.Dep{ocaml.Opam("fmt", "")},
		Stubs:  bob.Files("geom_stubs.c"),
		Native: []cc.Linkable{cfoo.Lib(p)},
	})
})
