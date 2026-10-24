//go:build bob

package pipe

import (
	"github.com/megakuul/bob/pkg/bob"
	"github.com/megakuul/bob/pkg/golang"
)

var _ = bob.Repo(bob.RepoOpts{Nixpkgs: bob.NixOS2605})

// ci names no targets and no tests: a local run builds //... and then tests //... (A152).
var _ = bob.Pipeline("ci", bob.PipelineOpts{})

// Module is example.com/pipe, rooted next to go.mod (label //:module).
var Module = bob.Define(func(p *bob.P) *golang.Module {
	return golang.NewModule(p, golang.ModuleOpts{Path: "example.com/pipe", Go: "1.26"})
})

// Tests runs the module's tests (label //:tests): every Go package of the root's region; app/
// declares its own.
var Tests = bob.Define(func(p *bob.P) *golang.TestSet { return Module(p).Tests(golang.TestOpts{Recursive: true}) })
