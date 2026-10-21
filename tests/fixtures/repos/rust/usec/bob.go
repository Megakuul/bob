//go:build bob

package usec

import (
	"example.com/rustfix/cfoo"
	"github.com/megakuul/bob/pkg/bob"
	"github.com/megakuul/bob/pkg/cc"
	"github.com/megakuul/bob/pkg/rust"
)

// Bin is a Rust binary linking a bob C library (label //usec:usec).
var Bin = bob.Define(func(p *bob.P) *rust.Bin {
	return rust.Binary(p, rust.BinOpts{Native: []cc.Linkable{cfoo.Lib(p)}})
})
