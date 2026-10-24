//go:build bob

package use

import (
	"example.com/slot"
	"github.com/megakuul/bob/pkg/bob"
)

// Use is //use:use; it reads //leaf:prog through the slot, which is memoised like Prog itself.
var Use = bob.Define(func(p *bob.P) *bob.Step {
	if slot.Prog.Get(p) != slot.Prog.Get(p) {
		panic("slot.Prog.Get(p) is not memoised")
	}
	return bob.Run(p, "use", bob.RunOpts{
		Cmd:     []string{"true"},
		Inputs:  map[string]bob.Tree{"PROG": slot.Prog.Get(p)},
		Outputs: []string{"y"},
	})
})
