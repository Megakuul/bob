//go:build bob

package rcore

import (
	"github.com/megakuul/bob/pkg/bob"
	"github.com/megakuul/bob/pkg/rust"
)

// Crate is a staticlib for C consumers (label //rcore:rcore).
var Crate = bob.Define(func(p *bob.P) *rust.CLib {
	return rust.StaticLib(p, rust.LibOpts{
		Headers: "include",
		Deps:    []rust.Dep{rust.Crate("itoa", "1"), rust.Crate("hex", "0.4")},
	})
})
