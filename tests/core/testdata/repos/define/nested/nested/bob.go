//go:build bob

package nested

import "github.com/megakuul/bob/pkg/bob"

// Outer calls bob.Define inside a constructor, which is not allowed.
var Outer = bob.Define(func(p *bob.P) *bob.Step {
	inner := bob.Define(func(q *bob.P) *bob.Step {
		return bob.Run(q, "inner", bob.RunOpts{Cmd: []string{"true"}})
	})
	return inner(p)
})
