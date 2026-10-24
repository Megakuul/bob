//go:build bob

package main

import (
	"example.com/acme"
	"example.com/acme/crates/rcore"
	"example.com/acme/libs/foo"
	"github.com/megakuul/bob/pkg/bob"
	"github.com/megakuul/bob/pkg/cc"
	"github.com/megakuul/bob/pkg/golang"
)

// FFI gives the cgo code of this package its C and Rust libraries.
var FFI = bob.Define(func(p *bob.P) *golang.Package {
	return acme.Module(p).Cgo("./cmd/api", golang.CgoOpts{Deps: []cc.Linkable{foo.Lib(p), rcore.Crate(p)}})
})

// Binary is the api server (label //cmd/api:api).
var Binary = bob.Define(func(p *bob.P) *golang.Binary {
	return acme.Module(p).Binary("./cmd/api", golang.BinaryOpts{
		X: map[string]string{"main.version": "dev"},
	})
})

// Tests runs this package's tests (unit //cmd/api:go_test).
var Tests = bob.Define(func(p *bob.P) *golang.TestSet {
	return acme.Module(p).Tests("./cmd/api", golang.TestOpts{Name: "api_tests"})
})
