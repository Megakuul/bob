package greet

import (
	"github.com/megakuul/bob/pkg/bob"
	"github.com/megakuul/bob/pkg/golang"
)

// Test runs this package's tests (label //internal/greet:test).
var Test = bob.Define(func(p *bob.P) *golang.Test {
	return golang.NewTest(p, "test")
})
