//go:build bob

package api

import (
	"example.com/gofix/native/foo"
	"example.com/gofix/native/rcore"
	msgv1 "example.com/gofix/proto/msg/v1"
	"github.com/megakuul/bob/pkg/bob"
	"github.com/megakuul/bob/pkg/cc"
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

// FFI links the cgo package internal/ffi against a C shared library and a Rust staticlib.
var FFI = bob.Define(func(p *bob.P) *golang.Package {
	return Module(p).Cgo("./internal/ffi", golang.CgoOpts{Deps: []cc.Linkable{foo.Lib(p), rcore.Crate(p)}})
})

// API is the cgo binary (label //api/cmd/api:api).
var API = bob.Define(func(p *bob.P) *golang.Binary {
	return Module(p).Binary("./cmd/api", golang.BinaryOpts{X: map[string]string{"main.version": "1.0.0"}})
})

// Tests runs one test unit per package of the module.
var Tests = bob.Define(func(p *bob.P) *golang.TestSet { return Module(p).Tests("./...", golang.TestOpts{}) })
