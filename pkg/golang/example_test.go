package golang_test

import (
	"github.com/megakuul/bob/pkg/bob"
	"github.com/megakuul/bob/pkg/golang"
)

// A module with one third-party requirement and one test unit per package of the module root's
// region. A binary is declared in its main package's own bob.go (see ExampleModule_Binary).
func ExampleNewModule() {
	module := bob.Define(func(p *bob.P) *golang.Module {
		return golang.NewModule(p, golang.ModuleOpts{
			Path:    "example.com/api",
			Go:      "1.26",
			Require: []golang.Requirement{golang.Require("github.com/google/uuid", "v1.6.0")},
		})
	})
	_ = bob.Define(func(p *bob.P) *golang.TestSet { return module(p).Tests(golang.TestOpts{Recursive: true}) })
}

// The binary of the main package cmd/api, written in cmd/api/bob.go; module is the root
// bob.go's Module (imported as api.Module).
func ExampleModule_Binary() {
	module := bob.Define(func(p *bob.P) *golang.Module {
		return golang.NewModule(p, golang.ModuleOpts{Path: "example.com/api", Go: "1.26"})
	})
	_ = bob.Define(func(p *bob.P) *golang.Binary {
		return module(p).Binary(golang.BinaryOpts{X: map[string]string{"main.version": "1.0.0"}})
	})
}
