//go:build bob

package cpplib

import (
	"github.com/megakuul/bob/pkg/bob"
	"github.com/megakuul/bob/pkg/cc"
)

var _ = bob.Repo(bob.RepoOpts{Nixpkgs: "nixos-26.05"})

// Ring is libring.a (label //:ring).
var Ring = bob.Define(func(p *bob.P) *cc.Library {
	return cc.StaticLibrary(p, "ring", cc.LibraryOpts{Srcs: bob.Glob("src/*.cpp"), Hdrs: "include", Std: cc.CXX20})
})

// Tool is bin/ringtool (label //:ringtool).
var Tool = bob.Define(func(p *bob.P) *cc.Binary {
	return cc.NewBinary(p, "ringtool", cc.BinaryOpts{Srcs: bob.Glob("tool/*.cpp"), Std: cc.CXX20, Deps: []cc.Linkable{Ring(p)}})
})

// Test runs the TAP test (label //:ring_test).
var Test = bob.Define(func(p *bob.P) *cc.Test {
	return cc.NewTest(p, "ring_test", cc.BinaryOpts{Srcs: bob.Files("test/ring_test.cpp"), Std: cc.CXX20, Deps: []cc.Linkable{Ring(p)}})
})
