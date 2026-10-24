//go:build bob

package main

import (
	"example.com/polyglot"
	"example.com/polyglot/crates/rcore"
	"example.com/polyglot/libs/foo"
	"github.com/megakuul/bob/pkg/bob"
	"github.com/megakuul/bob/pkg/cc"
	"github.com/megakuul/bob/pkg/golang"
)

// FFI gives the cgo code of this package its C++ and Rust libraries.
var FFI = bob.Define(func(p *bob.P) *golang.Package {
	return polyglot.Module(p).Cgo(golang.CgoOpts{Deps: []cc.Linkable{foo.Lib(p), rcore.Crate(p)}})
})

// API is the cgo binary (label //cmd/api:api). FFI is declared first: a Go binary picks up the
// module's cgo packages declared so far.
var API = bob.Define(func(p *bob.P) *golang.Binary {
	FFI(p)
	return polyglot.Module(p).Binary(golang.BinaryOpts{})
})

// The binary is bound to the typed slot polyglot.API, through which deploy/bob.go uses it.
var _ = polyglot.API.Bind(API)

// Tests runs this package's tests (unit //cmd/api:go_test).
var Tests = bob.Define(func(p *bob.P) *golang.TestSet {
	return polyglot.Module(p).Tests(golang.TestOpts{Name: "api_tests"})
})
