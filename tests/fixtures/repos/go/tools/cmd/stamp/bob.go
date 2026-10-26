//go:build bob

package main

import (
	"example.com/gofix/tools"
	"github.com/megakuul/bob/pkg/bob"
	"github.com/megakuul/bob/pkg/golang"
)

// Stamp is a pure-Go binary (label //tools/cmd/stamp:stamp).
var Stamp = bob.Define(func(p *bob.P) *golang.Binary {
	return tools.Module(p).Binary(golang.BinaryOpts{CGo: golang.CGoOff})
})

// The binary is bound to the slot tools.Stamp, through which other bob.go files use it.
var _ = tools.Stamp.Bind(Stamp)

// Tests runs this package's tests (unit //tools/cmd/stamp:go_test).
var Tests = bob.Define(func(p *bob.P) *golang.TestSet {
	return tools.Module(p).Tests(golang.TestOpts{})
})
