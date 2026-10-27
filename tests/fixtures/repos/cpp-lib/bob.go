//go:build bob

package cpplib

import (
	"github.com/megakuul/bob/pkg/bob"
	"github.com/megakuul/bob/pkg/cc"
)

var _ = bob.Repo(bob.RepoOpts{Nixpkgs: bob.NixOS2605})

// Ring is libring.a (label //:ring): the sources in src/, the public headers in include/.
var Ring = bob.Define(func(p *bob.P) *cc.Library {
	return cc.StaticLibrary(p, "ring", cc.LibraryOpts{Srcs: bob.Glob("src/*.cpp"), Hdrs: bob.IncludeDir("include"), Std: cc.CXX20})
})

// Test runs the TAP test of the library (label //:ring_test). The program bin/ringtool is its
// own package, tool/bob.go (one library or binary per directory package, R-CC-006).
var Test = bob.Define(func(p *bob.P) *cc.Test {
	return cc.NewTest(p, "ring_test", cc.BinaryOpts{Srcs: bob.Files("test/ring_test.cpp"), Std: cc.CXX20, Deps: []cc.Linkable{Ring(p)}})
})
