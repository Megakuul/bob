//go:build bob

package acme

import (
	"github.com/megakuul/bob/pkg/bob"
	"github.com/megakuul/bob/pkg/golang"
	"github.com/megakuul/bob/pkg/nixpkgs"
)

var _ = bob.Repo(bob.RepoOpts{Nixpkgs: "nixos-26.05"})

// Pipeline ci runs the tests on every push to main (concurrency: the per-ref default of SPEC
// §05.6.2). Pipeline release publishes release api (//deploy:api) on a tag v*.
var _ = bob.Pipeline("ci", bob.PipelineOpts{
	Triggers: []bob.Trigger{bob.OnPush("main")},
	Tests:    []bob.Pattern{"//..."},
})

var _ = bob.Pipeline("release", bob.PipelineOpts{
	Triggers: []bob.Trigger{bob.OnTag("v*")},
	Releases: []string{"api"},
})

// Module is the Go module example.com/acme (label //:module).
var Module = bob.Define(func(p *bob.P) *golang.Module {
	return golang.NewModule(p, golang.ModuleOpts{
		Path:    "example.com/acme",
		Go:      "1.26",
		Require: []golang.Requirement{golang.Require("github.com/google/uuid", "v1.6.0")},
	})
})

// Dev is the default development shell (bob dev).
var Dev = bob.Define(func(p *bob.P) *bob.Shell {
	return bob.NewShell(p, bob.DevShell{
		Packages: []bob.Tool{nixpkgs.Tool(p, "jq")},
		Env:      map[string]string{"ACME_ENV": "dev"},
	})
})
