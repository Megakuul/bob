//go:build bob

package main

import (
	"example.com/svc"
	"github.com/megakuul/bob/pkg/bob"
	"github.com/megakuul/bob/pkg/golang"
)

// Sleeper is a program that never listens (label //cmd/sleeper:sleeper).
var Sleeper = bob.Define(func(p *bob.P) *golang.Binary {
	return svc.Module(p).Binary(golang.BinaryOpts{})
})

var _ = svc.Sleeper.Bind(Sleeper)
