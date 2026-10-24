//go:build bob

package main

import (
	"example.com/devshell"
	"github.com/megakuul/bob/pkg/bob"
	"github.com/megakuul/bob/pkg/golang"
)

// Binary is the hello command (label //cmd/hello:hello).
var Binary = bob.Define(func(p *bob.P) *golang.Binary {
	return devshell.Module(p).Binary(golang.BinaryOpts{})
})
