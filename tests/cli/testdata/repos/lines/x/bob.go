//go:build bob

package x

import (
	"github.com/megakuul/bob/pkg/bob"
	"github.com/megakuul/bob/pkg/nixpkgs"
)

// Y prints 1,000 numbered lines at once (label //x:y).
var Y = bob.Define(func(p *bob.P) *bob.Step {
	return bob.Run(p, "y", bob.RunOpts{
		Tools:   []bob.Tool{nixpkgs.Bash.Tool(p), nixpkgs.Coreutils.Tool(p)},
		Cmd:     []string{"bash", "-c", "seq 1 1000; echo ok > $BOB_OUT/done"},
		Outputs: []string{"done"},
	})
})

// Slow prints 1,000 numbered lines over about 20 seconds (label //x:slow).
var Slow = bob.Define(func(p *bob.P) *bob.Step {
	return bob.Run(p, "slow", bob.RunOpts{
		Tools:   []bob.Tool{nixpkgs.Bash.Tool(p), nixpkgs.Coreutils.Tool(p)},
		Cmd:     []string{"bash", "-c", "for i in $(seq 1 1000); do echo $i; sleep 0.02; done; echo ok > $BOB_OUT/done"},
		Outputs: []string{"done"},
	})
})
