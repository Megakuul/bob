//go:build bob

package nt

import (
	"github.com/megakuul/bob/pkg/bob"
	"github.com/megakuul/bob/pkg/cc"
	"github.com/megakuul/bob/pkg/nixpkgs"
)

// Bad puts a build-time tool (nixpkgs.Tool) where a linked library is expected.
var Bad = bob.Define(func(p *bob.P) *cc.Library {
	return cc.StaticLibrary(p, "bad", cc.LibraryOpts{
		Srcs: bob.Glob("*.c"),
		Deps: []cc.Linkable{nixpkgs.Openssl.Tool(p)},
	})
})
