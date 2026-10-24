//go:build bob

package devshell

import (
	"github.com/megakuul/bob/pkg/bob"
	"github.com/megakuul/bob/pkg/golang"
)

var _ = bob.Repo(bob.RepoOpts{Nixpkgs: "nixos-26.05"})

// Module is the Go module example.com/devshell (label //:module); its toolchain is part of both shells.
var Module = bob.Define(func(p *bob.P) *golang.Module {
	return golang.NewModule(p, golang.ModuleOpts{Path: "example.com/devshell", Go: "1.26"})
})

// Dev is the default development shell: a variable, and a hook that leaves a marker (R-DEV-001).
var Dev = bob.Define(func(p *bob.P) *bob.Shell {
	return bob.NewShell(p, bob.DevShell{
		Env:   map[string]string{"CGO_ENABLED": "0", "DEVSHELL_NAME": "default"},
		Hooks: []string{"echo hooked > .bob/hooked"},
	})
})

// CI is a second shell, selected with --shell ci.
var CI = bob.Define(func(p *bob.P) *bob.Shell {
	return bob.NewShell(p, bob.DevShell{
		Name: "ci",
		Env:  map[string]string{"CGO_ENABLED": "0", "DEVSHELL_NAME": "ci"},
	})
})
