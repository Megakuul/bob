// Package golang (mock) builds Go modules and binaries.
package golang

import (
	"github.com/megakuul/bob/pkg/bob"
	"github.com/megakuul/bob/pkg/cc"
)

// Module is a third-party Go module pinned in bob.lock.
type Module struct{ Path, Version string }

type BinaryOpts struct {
	Main     string // package directory relative to this bob.go, default "."
	CGo      bool
	Link     []cc.Linkable
	Requires []Module
}

// Binary is a Go executable.
type Binary struct{ *bob.Base }

// NewBinary defines a Go binary.
func NewBinary(p *bob.P, name string, o BinaryOpts) *Binary {
	var d []bob.Package
	for _, l := range o.Link {
		d = append(d, l)
	}
	return &Binary{bob.NewPackage(p, 1, "go_binary", name, d, map[string]any{"cgo": o.CGo, "requires": o.Requires})}
}

// Test is a Go test package.
type Test struct{ *bob.Base }

func NewTest(p *bob.P, name string, link ...cc.Linkable) *Test {
	var d []bob.Package
	for _, l := range link {
		d = append(d, l)
	}
	return &Test{bob.NewPackage(p, 1, "go_test", name, d, nil)}
}
