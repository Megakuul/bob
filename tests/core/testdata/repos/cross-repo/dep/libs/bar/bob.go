//go:build bob

package bar

import (
	"github.com/megakuul/bob/pkg/bob"
	"github.com/megakuul/bob/pkg/nixpkgs"
)

// Lib is the dependency's package (label example.com/dep//libs/bar:lib).
var Lib = bob.Define(func(p *bob.P) *bob.Step {
	return bob.Run(p, "lib", bob.RunOpts{
		Tools:   []bob.Tool{nixpkgs.Bash.Tool(p), nixpkgs.Coreutils.Tool(p)},
		Cmd:     []string{"bash", "-c", `echo lib > "$BOB_OUT/lib.txt"`},
		Outputs: []string{"lib.txt"},
	})
})
