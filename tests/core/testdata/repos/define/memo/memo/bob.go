//go:build bob

package memo

import (
	"strconv"

	"github.com/megakuul/bob/pkg/bob"
)

// calls counts how often the constructor body ran; memoisation makes it one per *bob.P.
var calls int

// Lib is //memo:lib; its environment records which call produced it.
var Lib = bob.Define(func(p *bob.P) *bob.Step {
	calls++
	return bob.Run(p, "lib", bob.RunOpts{
		Cmd:     []string{"true"},
		Env:     map[string]string{"CALL": strconv.Itoa(calls)},
		Outputs: []string{"x"},
	})
})

// Use calls Lib three times with the same *bob.P: all three must be the same value.
var Use = bob.Define(func(p *bob.P) *bob.Step {
	a, b, c := Lib(p), Lib(p), Lib(p)
	if a != b || b != c {
		panic("Lib(p) is not memoised: different values for one *bob.P")
	}
	return bob.Run(p, "use", bob.RunOpts{
		Cmd:    []string{"true"},
		Inputs: map[string]bob.Tree{"lib": a},
	})
})
