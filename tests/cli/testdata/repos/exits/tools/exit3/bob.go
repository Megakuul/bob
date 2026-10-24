//go:build bob

package main

import (
	"example.com/exits/tools"
	"github.com/megakuul/bob/pkg/bob"
	"github.com/megakuul/bob/pkg/golang"
)

// Exit3 exits with status 3 (label //tools/exit3:exit3).
var Exit3 = bob.Define(func(p *bob.P) *golang.Binary {
	return tools.Module(p).Binary(golang.BinaryOpts{CGo: golang.CGoOff})
})
