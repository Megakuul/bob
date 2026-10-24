//go:build bob

package greet

import (
	"example.com/acme"
	"github.com/megakuul/bob/pkg/bob"
	"github.com/megakuul/bob/pkg/golang"
)

// Tests runs the tests of this package (unit //internal/greet:go_test).
var Tests = bob.Define(func(p *bob.P) *golang.TestSet {
	return acme.Module(p).Tests(golang.TestOpts{Name: "greet_tests"})
})
