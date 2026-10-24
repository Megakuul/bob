//go:build bob

package tool

import (
	"example.com/cpplib"
	"github.com/megakuul/bob/pkg/bob"
	"github.com/megakuul/bob/pkg/cc"
)

// Tool is bin/ringtool (label //tool:ringtool); it gets the ring headers and library only
// through the typed handle cpplib.Ring.
var Tool = bob.Define(func(p *bob.P) *cc.Binary {
	return cc.NewBinary(p, "ringtool", cc.BinaryOpts{Srcs: bob.Glob("*.cpp"), Std: cc.CXX20, Deps: []cc.Linkable{cpplib.Ring(p)}})
})
