//go:build bob

package b

import (
	"example.com/slot2"
	"github.com/megakuul/bob/pkg/bob"
)

// Prog is //b:prog.
var Prog = bob.Define(func(p *bob.P) *bob.Step {
	return bob.Run(p, "prog", bob.RunOpts{Cmd: []string{"true"}, Outputs: []string{"x"}})
})

var _ = slot2.Prog.Bind(Prog)
