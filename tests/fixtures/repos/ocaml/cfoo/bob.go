//go:build bob

package cfoo

import (
	"github.com/megakuul/bob/pkg/bob"
	"github.com/megakuul/bob/pkg/cc"
)

// Lib is libcfoo.a, linked into geom's C stubs (label //cfoo:cfoo).
var Lib = bob.Define(func(p *bob.P) *cc.Library {
	return cc.StaticLibrary(p, "cfoo", cc.LibraryOpts{Srcs: bob.Glob("src/*.c"), Hdrs: "include"})
})
