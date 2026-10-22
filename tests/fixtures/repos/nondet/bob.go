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

// ci runs on every push to a branch and builds //:nonce. SPEC §06 does not define the fields of
// bob.PipelineOpts, so the pipeline is registered by name only.
var _ = bob.Pipeline("ci", bob.PipelineOpts{})

// Nonce writes 16 random bytes (hex) to nonce.txt (label //:nonce).
var Nonce = bob.Define(func(p *bob.P) *bob.Step {
	return bob.Run(p, "nonce", bob.RunOpts{
		Tools:   []bob.Tool{nixpkgs.Tool(p, "bash"), nixpkgs.Tool(p, "coreutils")},
		Cmd:     []string{"bash", "-c", `head -c 16 /dev/urandom | od -An -tx1 | tr -d ' \n' > "$BOB_OUT/nonce.txt"`},
		Outputs: []string{"nonce.txt"},
	})
})
