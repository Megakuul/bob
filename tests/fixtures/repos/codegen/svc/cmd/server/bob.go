//go:build bob

package main

import (
	"example.com/codegen/svc"
	"github.com/megakuul/bob/pkg/bob"
	"github.com/megakuul/bob/pkg/golang"
)

// Server is //svc/cmd/server:server.
var Server = bob.Define(func(p *bob.P) *golang.Binary {
	return svc.Module(p).Binary(golang.BinaryOpts{})
})
