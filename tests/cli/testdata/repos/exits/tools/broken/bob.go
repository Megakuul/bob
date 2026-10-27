//go:build bob

package main

import (
	"example.com/exits/tools"
	"github.com/megakuul/bob/pkg/bob"
	"github.com/megakuul/bob/pkg/golang"
)

// Broken does not compile (label //tools/broken:broken).
var Broken = bob.Define(func(p *bob.P) *golang.Binary {
	return tools.Module(p).Binary(golang.BinaryOpts{CGo: golang.CGoOff})
})
