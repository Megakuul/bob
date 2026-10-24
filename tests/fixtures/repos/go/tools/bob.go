//go:build bob

package tools

import (
	"github.com/megakuul/bob/pkg/bob"
	"github.com/megakuul/bob/pkg/golang"
)

// Module is example.com/tools (label //tools:module).
var Module = bob.Define(func(p *bob.P) *golang.Module {
	return golang.NewModule(p, golang.ModuleOpts{
		Path:    "example.com/tools",
		Go:      "1.26",
		Require: []golang.Requirement{golang.Require("golang.org/x/sync", "v0.20.0")},
	})
})

// Stamp is a pure-Go binary (label //tools/cmd/stamp:stamp).
var Stamp = bob.Define(func(p *bob.P) *golang.Binary {
	return Module(p).Binary("./cmd/stamp", golang.BinaryOpts{CGo: golang.CGoOff})
})

// Tests runs one test unit per package of the module.
var Tests = bob.Define(func(p *bob.P) *golang.TestSet { return Module(p).Tests("./...", golang.TestOpts{}) })
