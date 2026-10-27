//go:build bob

package api

import (
	msgv1 "example.com/gofix/proto/msg/v1"
	"github.com/megakuul/bob/pkg/bob"
	"github.com/megakuul/bob/pkg/connect"
	"github.com/megakuul/bob/pkg/golang"
)

// Module is example.com/api (label //api:module).
var Module = bob.Define(func(p *bob.P) *golang.Module {
	return golang.NewModule(p, golang.ModuleOpts{
		Path:      "example.com/api",
		Go:        "1.26",
		Require:   []golang.Requirement{golang.Require("github.com/google/uuid", "v1.6.0")},
		Generated: []golang.Generated{connect.Go(msgv1.Msg(p), connect.GoOpts{})},
	})
})

// Tests runs one test unit per Go package of the module region (internal/msg, …); cmd/api and
// internal/ffi have bob.go files of their own.
var Tests = bob.Define(func(p *bob.P) *golang.TestSet {
	return Module(p).Tests(golang.TestOpts{Recursive: true})
})
