package golang_test

import (
	"github.com/megakuul/bob/pkg/bob"
	"github.com/megakuul/bob/pkg/golang"
)

// A module with one third-party requirement, a binary and one test unit per package.
func ExampleNewModule() {
	module := bob.Define(func(p *bob.P) *golang.Module {
		return golang.NewModule(p, golang.ModuleOpts{
			Path:    "example.com/api",
			Go:      "1.26",
			Require: []golang.Requirement{golang.Require("github.com/google/uuid", "v1.6.0")},
		})
	})
	_ = bob.Define(func(p *bob.P) *golang.Binary {
		return module(p).Binary("./cmd/api", golang.BinaryOpts{X: map[string]string{"main.version": "1.0.0"}})
	})
	_ = bob.Define(func(p *bob.P) *golang.TestSet { return module(p).Tests("./...", golang.TestOpts{}) })
}
