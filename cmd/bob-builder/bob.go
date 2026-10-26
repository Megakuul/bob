//go:build bob

package main

import (
	bobrepo "github.com/megakuul/bob"
	"github.com/megakuul/bob/pkg/bob"
	"github.com/megakuul/bob/pkg/golang"
)

// Builder is bob-builder; the copy bob embeds is the one go generate ./pkg/engine/store builds
// with the flags of R-NIX-015 (label //cmd/bob-builder:bob-builder).
var Builder = bob.Define(func(p *bob.P) *golang.Binary {
	return bobrepo.Module(p).Binary(golang.BinaryOpts{CGo: golang.CGoOff})
})

// Tests are the test units of this directory's region (//cmd/bob-builder/...:go_test).
var Tests = bob.Define(func(p *bob.P) *golang.TestSet {
	return bobrepo.Module(p).Tests(golang.TestOpts{Recursive: true, CGo: golang.CGoOff})
})
