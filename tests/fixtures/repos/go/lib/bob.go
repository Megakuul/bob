//go:build bob

package lib

import (
	"github.com/megakuul/bob/pkg/bob"
	"github.com/megakuul/bob/pkg/golang"
)

// Module is example.com/lib (label //lib:module). It has no third-party requirements.
var Module = bob.Define(func(p *bob.P) *golang.Module {
	return golang.NewModule(p, golang.ModuleOpts{Path: "example.com/lib", Go: "1.26"})
})

// Tests runs one test unit per package of the module.
var Tests = bob.Define(func(p *bob.P) *golang.TestSet { return Module(p).Tests("./...", golang.TestOpts{}) })
