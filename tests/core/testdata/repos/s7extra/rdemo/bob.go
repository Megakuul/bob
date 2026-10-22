//go:build bob

package rdemo

import (
	"example.com/polyglot/rmac"
	"github.com/megakuul/bob/pkg/bob"
	"github.com/megakuul/bob/pkg/rust"
)

// Bin is the demo binary (label //rdemo:rdemo): a proc-macro, a build script, itoa and the
// feature "shout" (R-FW-034, R-FW-035).
var Bin = bob.Define(func(p *bob.P) *rust.Bin {
	return rust.Binary(p, rust.BinOpts{
		Deps:     []rust.Dep{rust.Crate("itoa", "1"), rmac.Macro(p)},
		Features: map[string][]string{"shout": {}},
	})
})
