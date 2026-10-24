//go:build bob

package steps

import (
	"github.com/megakuul/bob/pkg/bob"
	"github.com/megakuul/bob/pkg/nixpkgs"
)

func shell(p *bob.P) []bob.Tool {
	return []bob.Tool{nixpkgs.Bash.Tool(p), nixpkgs.Coreutils.Tool(p)}
}

// Env records the environment the step sees (label //steps:env).
var Env = bob.Define(func(p *bob.P) *bob.Step {
	return bob.Run(p, "env", bob.RunOpts{
		Tools:   shell(p),
		Cmd:     []string{"bash", "-c", `env | sort > "$BOB_OUT/env.txt"; pwd > "$BOB_OUT/pwd.txt"`},
		Outputs: []string{"env.txt", "pwd.txt"},
	})
})

// Home lists /home and the network interfaces (label //steps:home).
var Home = bob.Define(func(p *bob.P) *bob.Step {
	return bob.Run(p, "home", bob.RunOpts{
		Tools:   shell(p),
		Cmd:     []string{"bash", "-c", `ls -A /home | wc -l > "$BOB_OUT/home-count.txt"; ls -A /home /root /etc 2>&1 | head -50 > "$BOB_OUT/listing.txt"`},
		Outputs: []string{"home-count.txt"},
	})
})

// Curl tries to download something (label //steps:curl); builds are offline.
var Curl = bob.Define(func(p *bob.P) *bob.Step {
	return bob.Run(p, "curl", bob.RunOpts{
		Tools:   append(shell(p), nixpkgs.Curl.Tool(p)),
		Cmd:     []string{"curl", "--max-time", "10", "-sS", "-o", "x", "https://example.com/"},
		Outputs: []string{"x"},
	})
})

// Missing declares an output the command never creates (label //steps:missing).
var Missing = bob.Define(func(p *bob.P) *bob.Step {
	return bob.Run(p, "missing", bob.RunOpts{
		Tools:   shell(p),
		Cmd:     []string{"bash", "-c", `echo hi > "$BOB_OUT/other.txt"`},
		Outputs: []string{"promised.txt"},
	})
})

// Extra creates its declared output and one undeclared file (label //steps:extra).
var Extra = bob.Define(func(p *bob.P) *bob.Step {
	return bob.Run(p, "extra", bob.RunOpts{
		Tools:   shell(p),
		Cmd:     []string{"bash", "-c", `echo a > "$BOB_OUT/declared.txt"; echo b > "$BOB_OUT/undeclared.txt"`},
		Outputs: []string{"declared.txt"},
	})
})

// Stable is a deterministic step used for the cross-machine identity check (//steps:stable).
var Stable = bob.Define(func(p *bob.P) *bob.Step {
	return bob.Run(p, "stable", bob.RunOpts{
		Tools:   shell(p),
		Cmd:     []string{"bash", "-c", `mkdir -p "$BOB_OUT/d" && echo one > "$BOB_OUT/d/a.txt" && echo two > "$BOB_OUT/b.txt"`},
		Outputs: []string{"b.txt"},
	})
})
