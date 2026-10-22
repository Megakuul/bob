//go:build bob

package cycle

import "github.com/megakuul/bob/pkg/bob"

// aRef breaks Go's own initialisation-cycle check; the cycle exists only at evaluation time.
var aRef func(*bob.P) *bob.Step

func init() { aRef = a }

// A needs B and B needs A: the constructors call each other while being constructed.
var a = bob.Define(func(p *bob.P) *bob.Step {
	return bob.Run(p, "a", bob.RunOpts{Cmd: []string{"true"}, Inputs: map[string]bob.Tree{"b": b(p)}})
})

var b = bob.Define(func(p *bob.P) *bob.Step {
	return bob.Run(p, "b", bob.RunOpts{Cmd: []string{"true"}, Inputs: map[string]bob.Tree{"a": aRef(p)}})
})
