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

// Stamp is the slot through which other bob.go files use the stamp binary, which is defined in
// cmd/stamp/bob.go (a main package cannot be imported).
var Stamp = bob.NewSlot[*golang.Binary]()

// Tests runs one test unit per Go package of the module region; cmd/stamp has its own bob.go.
var Tests = bob.Define(func(p *bob.P) *golang.TestSet {
	return Module(p).Tests(golang.TestOpts{Recursive: true})
})
