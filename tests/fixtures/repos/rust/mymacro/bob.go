//go:build bob

package mymacro

import (
	"github.com/megakuul/bob/pkg/bob"
	"github.com/megakuul/bob/pkg/rust"
)

// Macro is the local derive macro (label //mymacro:mymacro).
var Macro = bob.Define(func(p *bob.P) *rust.Lib {
	return rust.ProcMacro(p, rust.LibOpts{Deps: []rust.Dep{
		rust.Crate("syn", "2", rust.Features("full")),
		rust.Crate("quote", "1"),
		rust.Crate("proc-macro2", "1"),
	}})
})
