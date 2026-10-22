//go:build bob

package dup

import "github.com/megakuul/bob/pkg/bob"

// X and Y both define the label //dup:x (label = directory of this file + name argument).
var X = bob.Define(func(p *bob.P) *bob.Step {
	return bob.Run(p, "x", bob.RunOpts{Cmd: []string{"true"}})
})

var Y = bob.Define(func(p *bob.P) *bob.Step {
	return bob.Run(p, "x", bob.RunOpts{Cmd: []string{"false"}})
})
