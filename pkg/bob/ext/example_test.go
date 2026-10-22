//go:build bob

package ext_test

import (
	"github.com/megakuul/bob/pkg/bob"
	"github.com/megakuul/bob/pkg/bob/ext"
)

// Thing is a package of the example kind "example.thing".
type Thing struct{ bob.Node }

func ExampleNew() {
	ext.Register(ext.Kind{Name: "example.thing", Axes: []ext.Axis{ext.AxisPlatform}})
	_ = bob.Define(func(p *bob.P) *Thing {
		return &Thing{Node: ext.New(p, "example.thing", "thing", nil, nil, struct{}{})}
	})
}

// A recipe made in one call (ext.Here) becomes a package later, defined at the recipe's line.
func ExampleNewAt() {
	ext.Register(ext.Kind{Name: "example.recipe"})
	at := ext.Here()
	_ = bob.Define(func(p *bob.P) *Thing {
		return &Thing{Node: ext.NewAt(p, at, "example.recipe", "gen-thing", nil, nil, struct{}{})}
	})
}
