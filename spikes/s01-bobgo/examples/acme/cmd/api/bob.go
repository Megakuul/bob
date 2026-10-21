//go:build bob

package main

import (
	"example.com/acme/crates/rcore"
	"example.com/acme/libs/foo"
	"github.com/megakuul/bob/pkg/bob"
	"github.com/megakuul/bob/pkg/cc"
	"github.com/megakuul/bob/pkg/golang"
)

// Binary is the api server (label //cmd/api:api).
var Binary = bob.Define(func(p *bob.P) *golang.Binary {
	return golang.NewBinary(p, "api", golang.BinaryOpts{
		CGo:  true,
		Link: []cc.Linkable{foo.Lib(p), rcore.Crate(p)},
		Requires: []golang.Module{
			{Path: "github.com/google/uuid", Version: "v1.6.0"},
		},
	})
})
