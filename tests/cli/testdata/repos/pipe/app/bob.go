//go:build bob

package main

import (
	"example.com/pipe"
	"github.com/megakuul/bob/pkg/bob"
	"github.com/megakuul/bob/pkg/golang"
)

// App is the binary (label //app:app).
var App = bob.Define(func(p *bob.P) *golang.Binary {
	return pipe.Module(p).Binary(golang.BinaryOpts{CGo: golang.CGoOff})
})

// Tests runs this package's tests (unit //app:go_test).
var Tests = bob.Define(func(p *bob.P) *golang.TestSet { return pipe.Module(p).Tests(golang.TestOpts{}) })
