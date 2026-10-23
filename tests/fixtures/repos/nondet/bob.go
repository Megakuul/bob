//go:build bob

// Package nondet is the fixture of a non-reproducible package (R-REG-152): its only output
// contains a nonce drawn from the kernel at build time, so two builds of the same inputs give
// the same store path with different bytes.
package nondet

import (
	"github.com/megakuul/bob/pkg/bob"
	"github.com/megakuul/bob/pkg/nixpkgs"
)

var _ = bob.Repo(bob.RepoOpts{Nixpkgs: "nixos-26.05"})

// ci runs on every push to a branch and builds //:nonce (SPEC §1.4.4 PipelineOpts: no trigger
// would mean manual runs only, no target nothing to build).
var _ = bob.Pipeline("ci", bob.PipelineOpts{
	Triggers: []bob.Trigger{bob.OnPush("*")},
	Targets:  []bob.Pattern{"//:nonce"},
})

// Nonce writes 16 random bytes (hex) to nonce.txt (label //:nonce).
var Nonce = bob.Define(func(p *bob.P) *bob.Step {
	return bob.Run(p, "nonce", bob.RunOpts{
		Tools:   []bob.Tool{nixpkgs.Tool(p, "bash"), nixpkgs.Tool(p, "coreutils")},
		Cmd:     []string{"bash", "-c", `head -c 16 /dev/urandom | od -An -tx1 | tr -d ' \n' > "$BOB_OUT/nonce.txt"`},
		Outputs: []string{"nonce.txt"},
	})
})
