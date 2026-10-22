//go:build bob

package rmac

import (
	"github.com/megakuul/bob/pkg/bob"
	"github.com/megakuul/bob/pkg/rust"
)

// Macro is the proc-macro crate rmac (label //rmac:rmac); a host unit (R-FW-035).
var Macro = bob.Define(func(p *bob.P) *rust.Lib {
	return rust.ProcMacro(p, rust.LibOpts{Deps: []rust.Dep{rust.Crate("itoa", "1")}})
})
