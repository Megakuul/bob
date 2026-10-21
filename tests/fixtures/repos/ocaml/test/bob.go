//go:build bob

package test

import (
	"example.com/ocamlfix/geom"
	"github.com/megakuul/bob/pkg/bob"
	"github.com/megakuul/bob/pkg/ocaml"
)

// GeomTest runs test.ml (label //test:geom_test).
var GeomTest = bob.Define(func(p *bob.P) *ocaml.Test {
	return ocaml.Test(p, ocaml.ExeOpts{Name: "geom_test", Main: "test", Deps: []ocaml.Dep{geom.Lib(p)}})
})
