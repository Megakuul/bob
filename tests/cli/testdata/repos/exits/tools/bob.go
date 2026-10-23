//go:build bob

package tools

import (
	"github.com/megakuul/bob/pkg/bob"
	"github.com/megakuul/bob/pkg/golang"
)

// Module is example.com/exits (label //tools:module).
var Module = bob.Define(func(p *bob.P) *golang.Module {
	return golang.NewModule(p, golang.ModuleOpts{Path: "example.com/exits", Go: "1.26"})
})

// Exit3 exits with status 3 (label //tools/exit3:exit3).
var Exit3 = bob.Define(func(p *bob.P) *golang.Binary {
	return Module(p).Binary("./exit3", golang.BinaryOpts{CGo: golang.CGoOff})
})

// Selfkill kills itself with SIGKILL (label //tools/selfkill:selfkill).
var Selfkill = bob.Define(func(p *bob.P) *golang.Binary {
	return Module(p).Binary("./selfkill", golang.BinaryOpts{CGo: golang.CGoOff})
})

// Hello prints its arguments (label //tools/hello:hello).
var Hello = bob.Define(func(p *bob.P) *golang.Binary {
	return Module(p).Binary("./hello", golang.BinaryOpts{CGo: golang.CGoOff})
})

// Broken does not compile (label //tools/broken:broken).
var Broken = bob.Define(func(p *bob.P) *golang.Binary {
	return Module(p).Binary("./broken", golang.BinaryOpts{CGo: golang.CGoOff})
})
