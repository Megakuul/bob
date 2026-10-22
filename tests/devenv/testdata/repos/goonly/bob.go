//go:build bob

package goonly

import (
	"github.com/megakuul/bob/pkg/bob"
	"github.com/megakuul/bob/pkg/golang"
)

var _ = bob.Repo(bob.RepoOpts{Nixpkgs: "nixos-26.05"})

// Module is the Go module example.com/goonly (label //:module); no cgo, no other language.
var Module = bob.Define(func(p *bob.P) *golang.Module {
	return golang.NewModule(p, golang.ModuleOpts{Path: "example.com/goonly", Go: "1.26"})
})

// Dev is the default development shell.
var Dev = bob.Define(func(p *bob.P) *bob.Shell {
	return bob.NewShell(p, bob.DevShell{})
})
