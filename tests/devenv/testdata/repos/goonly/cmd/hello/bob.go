//go:build bob

package main

import (
	"example.com/goonly"
	"github.com/megakuul/bob/pkg/bob"
	"github.com/megakuul/bob/pkg/golang"
)

// Binary is the hello command (label //cmd/hello:hello).
var Binary = bob.Define(func(p *bob.P) *golang.Binary {
	return goonly.Module(p).Binary("./cmd/hello", golang.BinaryOpts{})
})
