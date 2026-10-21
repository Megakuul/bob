//go:build bob

package mathx

import (
	"github.com/megakuul/bob/pkg/bob"
	"github.com/megakuul/bob/pkg/cc"
)

// Lib is libmathx.a (label //libs/mathx:mathx).
var Lib = bob.Define(func(p *bob.P) *cc.Library {
	return cc.StaticLibrary(p, "mathx", cc.LibraryOpts{Srcs: bob.Glob("src/*.c"), Hdrs: "include", Std: cc.C17})
})

// Test runs the TAP test (label //libs/mathx:mathx_test).
var Test = bob.Define(func(p *bob.P) *cc.Test {
	return cc.Test(p, "mathx_test", cc.BinaryOpts{Srcs: bob.Files("test/mathx_test.c"), Deps: []cc.Linkable{Lib(p)}})
})
