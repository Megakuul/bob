//go:build bob

package cmain

import (
	"example.com/rustfix/rcore"
	"example.com/rustfix/rdyn"
	"github.com/megakuul/bob/pkg/bob"
	"github.com/megakuul/bob/pkg/cc"
)

// Main is a C binary linking a Rust cdylib and staticlib (label //cmain:cmain).
var Main = bob.Define(func(p *bob.P) *cc.Binary {
	return cc.Binary(p, "cmain", cc.BinaryOpts{Srcs: bob.Glob("*.c"), Deps: []cc.Linkable{rdyn.Lib(p), rcore.Crate(p)}})
})
