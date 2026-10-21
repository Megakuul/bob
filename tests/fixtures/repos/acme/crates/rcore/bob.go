//go:build bob

package rcore

import (
	"github.com/megakuul/bob/pkg/bob"
	"github.com/megakuul/bob/pkg/rust"
)

// Crate is the rcore staticlib (label //crates/rcore:rcore).
var Crate = bob.Define(func(p *bob.P) *rust.CLib {
	return rust.StaticLib(p, rust.LibOpts{
		Name:    "rcore",
		Headers: "include",
		Deps:    []rust.Dep{rust.Crate("itoa", "1"), rust.Crate("hex", "0.4")},
	})
})
