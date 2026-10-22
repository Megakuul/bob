//go:build bob

package use

import (
	"example.com/dep/libs/bar"
	"github.com/megakuul/bob/pkg/bob"
)

// Consumer uses the dependency's package by its typed handle (label //use:consumer).
var Consumer = bob.Define(func(p *bob.P) *bob.Step {
	return bob.Run(p, "consumer", bob.RunOpts{
		Cmd:     []string{"true"},
		Inputs:  map[string]bob.Tree{"bar": bar.Lib(p)},
		Outputs: []string{"out.txt"},
	})
})
