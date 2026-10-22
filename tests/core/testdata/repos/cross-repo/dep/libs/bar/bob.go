//go:build bob

package bar

import "github.com/megakuul/bob/pkg/bob"

// Lib is the dependency's package (label example.com/dep//libs/bar:lib).
var Lib = bob.Define(func(p *bob.P) *bob.Step {
	return bob.Run(p, "lib", bob.RunOpts{
		Cmd:     []string{"true"},
		Outputs: []string{"lib.txt"},
	})
})
