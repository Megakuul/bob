//go:build bob

package foo

import (
	"github.com/megakuul/bob/pkg/bob"
	"github.com/megakuul/bob/pkg/cc"
)

// Lib is libfoo.so (label //native/foo:foo).
var Lib = bob.Define(func(p *bob.P) *cc.Library {
	return cc.SharedLibrary(p, "foo", cc.LibraryOpts{Srcs: bob.Glob("src/*.c"), Hdrs: "include"})
})
