//go:build bob

package x

import (
	"github.com/megakuul/bob/pkg/bob"
	"github.com/megakuul/bob/pkg/nixpkgs"
)

// Quick builds in a moment (label //x:quick).
var Quick = bob.Define(func(p *bob.P) *bob.Step {
	return bob.Run(p, "quick", bob.RunOpts{
		Tools:   []bob.Tool{nixpkgs.Tool(p, "bash"), nixpkgs.Tool(p, "coreutils")},
		Cmd:     []string{"bash", "-c", "echo quick > $BOB_OUT/done"},
		Outputs: []string{"done"},
	})
})

// Slow runs for 90 seconds, long enough to detach from it and cancel it (label //x:slow).
var Slow = bob.Define(func(p *bob.P) *bob.Step {
	return bob.Run(p, "slow", bob.RunOpts{
		Tools:   []bob.Tool{nixpkgs.Tool(p, "bash"), nixpkgs.Tool(p, "coreutils")},
		Cmd:     []string{"bash", "-c", "sleep 90; echo slow > $BOB_OUT/done"},
		Outputs: []string{"done"},
	})
})
