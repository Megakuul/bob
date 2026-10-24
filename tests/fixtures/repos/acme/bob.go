//go:build bob

package acme

import (
	"github.com/megakuul/bob/pkg/bob"
	"github.com/megakuul/bob/pkg/golang"
	"github.com/megakuul/bob/pkg/nixpkgs"
)

var _ = bob.Repo(bob.RepoOpts{Nixpkgs: bob.NixOS2605})

// Pipeline ci runs the tests on every push to main (concurrency: the per-ref default of SPEC
// §05.6.2). Pipeline release publishes release api (//deploy:api) on a tag v*.
var _ = bob.Pipeline("ci", bob.PipelineOpts{
	Triggers: []bob.Trigger{bob.OnPush("main")},
	Tests:    []bob.Pattern{"//..."},
})

var _ = bob.Pipeline("release", bob.PipelineOpts{
	Triggers: []bob.Trigger{bob.OnTag("v*")},
	Releases: []bob.Pattern{"//deploy:api"},
})

// Module is the Go module example.com/acme (label //:module).
var Module = bob.Define(func(p *bob.P) *golang.Module {
	return golang.NewModule(p, golang.ModuleOpts{
		Path:    "example.com/acme",
		Go:      "1.26",
		Require: []golang.Requirement{golang.Require("github.com/google/uuid", "v1.6.0")},
	})
})

// API is the api server binary (//cmd/api:api). cmd/api is a main package, whose bob.go cannot be
// imported (R-FW-002 R3): cmd/api/bob.go binds its constructor to this typed slot and
// deploy/bob.go gets the binary from it.
var API = bob.NewSlot[*golang.Binary]()

// Dev is the default development shell (bob dev).
var Dev = bob.Define(func(p *bob.P) *bob.Shell {
	return bob.NewShell(p, bob.DevShell{
		Packages: []bob.Tool{nixpkgs.Jq.Tool(p)},
		Env:      map[string]string{"ACME_ENV": "dev"},
	})
})
