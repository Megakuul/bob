//go:build bob

package main

import (
	"example.com/exits/tools"
	"github.com/megakuul/bob/pkg/bob"
	"github.com/megakuul/bob/pkg/golang"
)

// Selfkill kills itself with SIGKILL (label //tools/selfkill:selfkill).
var Selfkill = bob.Define(func(p *bob.P) *golang.Binary {
	return tools.Module(p).Binary(golang.BinaryOpts{CGo: golang.CGoOff})
})
