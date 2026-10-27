//go:build bob

package native

import (
	"github.com/megakuul/bob/pkg/bob"
	"github.com/megakuul/bob/pkg/cc"
)

// Lib is libadd.a (label //native:add).
var Lib = bob.Define(func(p *bob.P) *cc.Library {
	return cc.StaticLibrary(p, "add", cc.LibraryOpts{Srcs: bob.Glob("*.c"), Hdrs: bob.IncludeDir(".")})
})
