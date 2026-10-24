//go:build bob

package main

import (
	"example.com/gofix/api"
	"example.com/gofix/api/internal/ffi"
	"github.com/megakuul/bob/pkg/bob"
	"github.com/megakuul/bob/pkg/golang"
)

// API is the cgo binary (label //api/cmd/api:api); the cgo declaration of internal/ffi comes
// first, so the binary links its C and Rust libraries.
var API = bob.Define(func(p *bob.P) *golang.Binary {
	ffi.FFI(p)
	return api.Module(p).Binary(golang.BinaryOpts{X: map[string]string{"main.version": "1.0.0"}})
})
