//go:build bob

package rcore

import (
	"github.com/megakuul/bob/pkg/bob"
	"github.com/megakuul/bob/pkg/rust"
)

// Crate is the rcore staticlib (label //crates/rcore:rcore).
var Crate = bob.Define(func(p *bob.P) *rust.StaticLib {
	return rust.NewStaticLib(p, "rcore", rust.CrateOpts{
		Edition: "2021",
		Deps:    []rust.Crate{{Name: "itoa", Version: "1.0.11"}, {Name: "hex", Version: "0.4.3"}},
	})
})
