//go:build bob

package p

import "github.com/megakuul/bob/pkg/bob"

// Step reads a pattern that leaves the repository.
var Step = bob.Define(func(p *bob.P) *bob.Step {
	return bob.Run(p, "step", bob.RunOpts{Cmd: []string{"true"}, Srcs: bob.Glob("../../secret.txt")})
})
