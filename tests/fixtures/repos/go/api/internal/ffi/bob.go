//go:build bob

package ffi

import (
	"example.com/gofix/api"
	"example.com/gofix/native/foo"
	"example.com/gofix/native/rcore"
	"github.com/megakuul/bob/pkg/bob"
	"github.com/megakuul/bob/pkg/cc"
	"github.com/megakuul/bob/pkg/golang"
)

// FFI links this cgo package against a C shared library and a Rust staticlib.
var FFI = bob.Define(func(p *bob.P) *golang.Package {
	return api.Module(p).Cgo(golang.CgoOpts{Deps: []cc.Linkable{foo.Lib(p), rcore.Crate(p)}})
})

// Tests runs this package's tests (unit //api/internal/ffi:go_test).
var Tests = bob.Define(func(p *bob.P) *golang.TestSet {
	FFI(p)
	return api.Module(p).Tests(golang.TestOpts{})
})
