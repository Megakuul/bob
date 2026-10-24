//go:build bob

package main

import (
	"example.com/bigclosure"
	"github.com/megakuul/bob/pkg/bob"
	"github.com/megakuul/bob/pkg/golang"
)

// Big is the small program of the release (label //cmd/big:big).
var Big = bob.Define(func(p *bob.P) *golang.Binary {
	return bigclosure.Module(p).Binary(golang.BinaryOpts{})
})

// The program is bound to the typed slot bigclosure.Big, through which deploy/bob.go uses it.
var _ = bigclosure.Big.Bind(Big)
