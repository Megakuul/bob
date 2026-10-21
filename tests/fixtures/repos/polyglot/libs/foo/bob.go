//go:build bob

package foo

import (
	"github.com/megakuul/bob/pkg/bob"
	"github.com/megakuul/bob/pkg/cc"
	"github.com/megakuul/bob/pkg/nixpkgs"
)

// Lib is libfoo.so (label //libs/foo:foo).
var Lib = bob.Define(func(p *bob.P) *cc.Library {
	return cc.SharedLibrary(p, "foo", cc.LibraryOpts{
		Srcs: bob.Glob("src/*.cpp"),
		Hdrs: "include",
		Std:  cc.CXX20,
		Deps: []cc.Linkable{nixpkgs.Lib(p, "openssl")},
	})
})

// Test checks the digest (label //libs/foo:foo_test).
var Test = bob.Define(func(p *bob.P) *cc.Test {
	return cc.Test(p, "foo_test", cc.BinaryOpts{Srcs: bob.Files("test/foo_test.cpp"), Deps: []cc.Linkable{Lib(p)}})
})
