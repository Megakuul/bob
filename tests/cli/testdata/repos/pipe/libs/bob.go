//go:build bob

package libs

import (
	"github.com/megakuul/bob/pkg/bob"
	"github.com/megakuul/bob/pkg/golang"
)

// Module is example.com/pipe (label //libs:module).
var Module = bob.Define(func(p *bob.P) *golang.Module {
	return golang.NewModule(p, golang.ModuleOpts{Path: "example.com/pipe", Go: "1.26"})
})

// Greet is the library (label //libs/greet:greet).
var Greet = bob.Define(func(p *bob.P) *golang.Binary {
	return Module(p).Binary("./app", golang.BinaryOpts{CGo: golang.CGoOff})
})

// Tests runs the module's tests (label //libs:tests).
var Tests = bob.Define(func(p *bob.P) *golang.TestSet { return Module(p).Tests("./...", golang.TestOpts{}) })
