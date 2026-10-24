//go:build ignore

// Illustrative bob.go (S15). The IR records the position of the constructor call as definedAt.
package libfoo

import "github.com/megakuul/bob/pkg/cc"

var Foo = cc.SharedLibrary("foo", cc.Opts{
	Lang: cc.CXX20,
	Srcs: []string{"src/foo.cpp"},
	Hdrs: "include",
	Deps: cc.Deps{cc.Nixpkgs("openssl")},
})
