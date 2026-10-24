//go:build bob

package early

import "github.com/megakuul/bob/pkg/bob"

// Lib is a defined constructor.
var Lib = bob.Define(func(p *bob.P) *bob.Step {
	return bob.Run(p, "lib", bob.RunOpts{Cmd: []string{"true"}})
})

// Early calls Lib outside evaluation, in a package-level initialiser.
var Early = Lib(nil)
