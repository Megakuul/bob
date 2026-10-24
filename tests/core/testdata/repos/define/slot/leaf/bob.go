//go:build bob

package leaf

import (
	"example.com/slot"
	"github.com/megakuul/bob/pkg/bob"
)

// Prog is //leaf:prog.
var Prog = bob.Define(func(p *bob.P) *bob.Step {
	return bob.Run(p, "prog", bob.RunOpts{Cmd: []string{"true"}, Outputs: []string{"x"}})
})

var _ = slot.Prog.Bind(Prog)
