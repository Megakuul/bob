//go:build bob

package use

import (
	"example.com/dep/libs/bar"
	"github.com/megakuul/bob/pkg/bob"
	"github.com/megakuul/bob/pkg/nixpkgs"
)

// Consumer uses the dependency's package by its typed handle (label //use:consumer).
var Consumer = bob.Define(func(p *bob.P) *bob.Step {
	return bob.Run(p, "consumer", bob.RunOpts{
		Tools:   []bob.Tool{nixpkgs.Tool(p, "bash"), nixpkgs.Tool(p, "coreutils")},
		Cmd:     []string{"bash", "-c", `echo consumer > "$BOB_OUT/out.txt"`},
		Inputs:  map[string]bob.Tree{"bar": bar.Lib(p)},
		Outputs: []string{"out.txt"},
	})
})
