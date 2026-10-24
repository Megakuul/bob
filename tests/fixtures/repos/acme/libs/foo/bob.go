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
		Deps: []cc.Linkable{nixpkgs.Lib(p, "openssl", nixpkgs.Modules("libcrypto"))},
	})
})
