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

// Tests runs this package's tests (unit //tools/cmd/stamp:go_test).
var Tests = bob.Define(func(p *bob.P) *golang.TestSet {
	return tools.Module(p).Tests(golang.TestOpts{})
})
