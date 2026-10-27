//go:build bob

package svc

import (
	"github.com/megakuul/bob/pkg/bob"
	"github.com/megakuul/bob/pkg/golang"
)

var _ = bob.Repo(bob.RepoOpts{Nixpkgs: bob.NixOS2605})

// Module is the Go module example.com/svc.
var Module = bob.Define(func(p *bob.P) *golang.Module {
	return golang.NewModule(p, golang.ModuleOpts{Path: "example.com/svc", Go: "1.26"})
})

// Sleeper is the program of cmd/sleeper, bound by cmd/sleeper/bob.go (a main package's bob.go
// cannot be imported, R-FW-002 R3; R-FW-071 typed slot).
var Sleeper = bob.NewSlot[*golang.Binary]()
