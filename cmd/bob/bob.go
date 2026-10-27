//go:build bob

package main

import (
	bobrepo "github.com/megakuul/bob"
	"github.com/megakuul/bob/pkg/bob"
	"github.com/megakuul/bob/pkg/golang"
)

// Bob is the bob command (label //cmd/bob:bob).
var Bob = bob.Define(func(p *bob.P) *golang.Binary {
	return bobrepo.Module(p).Binary(golang.BinaryOpts{CGo: golang.CGoOff})
})

// The root bob.go bundles it as the AppImage through the slot bobrepo.Bob.
var _ = bobrepo.Bob.Bind(Bob)

// Tests are the test units of this directory's region (//cmd/bob/...:go_test).
var Tests = bob.Define(func(p *bob.P) *golang.TestSet {
	return bobrepo.Module(p).Tests(golang.TestOpts{Recursive: true, CGo: golang.CGoOff})
})
