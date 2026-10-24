//go:build bob

package lnk

import (
	"github.com/megakuul/bob/pkg/bob"
	"github.com/megakuul/bob/pkg/golang"
)

var mod = bob.Define(func(p *bob.P) *golang.Module {
	return golang.NewModule(p, golang.ModuleOpts{Path: "example.com/linkable", Go: "1.26"})
})

// Bad passes a Go binary where a native library (bob.Linkable) is expected.
var Bad = bob.Define(func(p *bob.P) *bob.Step {
	var l bob.Linkable = mod(p).Binary(golang.BinaryOpts{})
	_ = l
	return bob.Run(p, "bad", bob.RunOpts{Cmd: []string{"true"}})
})
