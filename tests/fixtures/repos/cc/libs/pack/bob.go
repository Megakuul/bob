//go:build bob

package pack

import (
	"github.com/megakuul/bob/pkg/bob"
	"github.com/megakuul/bob/pkg/cc"
	"github.com/megakuul/bob/pkg/nixpkgs"
)

// Lib is libpack.so, linking nixpkgs zlib and libcrypto (label //libs/pack:pack).
var Lib = bob.Define(func(p *bob.P) *cc.Library {
	return cc.SharedLibrary(p, "pack", cc.LibraryOpts{
		Srcs: bob.Glob("src/*.cpp"),
		Hdrs: "include",
		Std:  cc.CXX20,
		PrivDeps: []cc.Linkable{
			nixpkgs.Lib(p, "zlib"),
			nixpkgs.Lib(p, "openssl", nixpkgs.Modules("libcrypto")),
		},
	})
})
