// Package rust (mock) builds crates.
package rust

import (
	"github.com/megakuul/bob/pkg/bob"
	"github.com/megakuul/bob/pkg/cc"
)

type Crate struct{ Name, Version string }

type StaticLib struct{ *bob.Base }

func (*StaticLib) LinkInfo() cc.LinkInfo { return cc.LinkInfo{Static: true} }

var _ cc.Linkable = (*StaticLib)(nil)

type CrateOpts struct {
	Edition string
	Deps    []Crate
}

// NewStaticLib defines a Rust staticlib crate.
func NewStaticLib(p *bob.P, name string, o CrateOpts) *StaticLib {
	return &StaticLib{bob.NewPackage(p, 1, "rust_staticlib", name, nil, map[string]any{"crates": o.Deps})}
}
