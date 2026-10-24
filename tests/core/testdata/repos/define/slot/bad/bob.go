//go:build bob

package bad

import (
	"example.com/slot"
	"github.com/megakuul/bob/pkg/bob"
)

// Bad is //bad:bad; it uses a slot nobody binds.
var Bad = bob.Define(func(p *bob.P) *bob.Step {
	return bob.Run(p, "bad", bob.RunOpts{
		Cmd:     []string{"true"},
		Inputs:  map[string]bob.Tree{"X": slot.Unbound.Get(p)},
		Outputs: []string{"z"},
	})
})
