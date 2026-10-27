//go:build bob

// Package bigclosure is the fixture of a release "big" (1.0.0) whose closure holds one
// incompressible output of about 1.1 GiB (R-REG-151/-152 slow tests). Nothing large is committed:
// the bulk is generated at build time from a fixed seed, so it is reproducible.
package bigclosure

import (
	"github.com/megakuul/bob/pkg/bob"
	"github.com/megakuul/bob/pkg/golang"
	"github.com/megakuul/bob/pkg/nixpkgs"
)

var _ = bob.Repo(bob.RepoOpts{Nixpkgs: bob.NixOS2605})

// The release pipeline runs on a tag push matching v* and publishes //deploy:big. SPEC §06 does
// not define the fields of bob.PipelineOpts, so the pipeline is registered by name only.
var _ = bob.Pipeline("release", bob.PipelineOpts{})

// Module is the Go module example.com/bigclosure (label //:module).
var Module = bob.Define(func(p *bob.P) *golang.Module {
	return golang.NewModule(p, golang.ModuleOpts{Path: "example.com/bigclosure", Go: "1.26"})
})

// Big is the small program of the release (//cmd/big:big), bound by cmd/big/bob.go (a main
// package's bob.go cannot be imported, R-FW-002 R3).
var Big = bob.NewSlot[*golang.Binary]()

// BulkBytes is the size of bulk.bin: 1.1 GiB.
const BulkBytes = "1181116006"

// Bulk writes bulk.bin: BulkBytes of AES-128-CTR keystream (encrypting zeros) under a fixed key and
// IV, i.e. incompressible and identical on every build (label //:bulk).
var Bulk = bob.Define(func(p *bob.P) *bob.Step {
	return bob.Run(p, "bulk", bob.RunOpts{
		Tools: []bob.Tool{nixpkgs.Bash.Tool(p), nixpkgs.Coreutils.Tool(p), nixpkgs.Openssl.Tool(p)},
		Cmd: []string{"bash", "-c",
			`head -c ` + BulkBytes + ` /dev/zero | openssl enc -aes-128-ctr -K 000102030405060708090a0b0c0d0e0f -iv 00000000000000000000000000000000 -nosalt > "$BOB_OUT/bulk.bin"`},
		Outputs: []string{"bulk.bin"},
	})
})
