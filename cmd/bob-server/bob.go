//go:build bob

package main

import (
	bobrepo "github.com/megakuul/bob"
	"github.com/megakuul/bob/pkg/bob"
	"github.com/megakuul/bob/pkg/golang"
)

// Server is the build and cache server (label //cmd/bob-server:bob-server).
var Server = bob.Define(func(p *bob.P) *golang.Binary {
	return bobrepo.Module(p).Binary(golang.BinaryOpts{CGo: golang.CGoOff})
})

// Tests are the test units of this directory's region (//cmd/bob-server/...:go_test).
var Tests = bob.Define(func(p *bob.P) *golang.TestSet {
	return bobrepo.Module(p).Tests(golang.TestOpts{Recursive: true, CGo: golang.CGoOff})
})
