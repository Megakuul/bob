//go:build bob

package toy

import (
	"github.com/megakuul/bob/pkg/bob"
	"github.com/megakuul/bob/pkg/bob/ext"
)

func init() {
	ext.Register(ext.Kind{Name: "toy.thing", Axes: []ext.Axis{ext.AxisPlatform}})
	ext.Register(ext.Kind{Name: "toy.bad", Axes: []ext.Axis{ext.AxisPlatform}})
}

// Thing is a package of the toy ecosystem.
type Thing struct{ bob.Node }

type spec struct {
	Note string `json:"note"`
}

func newThing(p *bob.P, kind, name string, deps ...bob.Package) *Thing {
	return &Thing{Node: ext.New(p, kind, name, deps, nil, spec{Note: name})}
}

// A is //toy:a.
var A = bob.Define(func(p *bob.P) *Thing { return newThing(p, "toy.thing", "a") })

// B is //toy:b and depends on A.
var B = bob.Define(func(p *bob.P) *Thing { return newThing(p, "toy.thing", "b", A(p)) })

// Bad reads the profile although its kind declares only the platform axis.
var Bad = bob.Define(func(p *bob.P) *Thing {
	t := newThing(p, "toy.bad", "bad")
	_ = ext.ConfigOf(&t.Node).Profile()
	return t
})
