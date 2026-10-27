//go:build bob

package ext_test

import (
	"github.com/megakuul/bob/pkg/bob"
	"github.com/megakuul/bob/pkg/bob/ext"
	"github.com/megakuul/bob/pkg/nixpkgs"
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

// A package whose label directory is below the declaring bob.go: //tools/gen:gen, declared in
// the repository root's bob.go, like a Go binary declared in its module's bob.go.
func ExampleNewIn() {
	ext.Register(ext.Kind{Name: "example.thing", Axes: []ext.Axis{ext.AxisPlatform}})
	_ = bob.Define(func(p *bob.P) *Thing {
		return &Thing{Node: ext.NewIn(p, "example.thing", "tools/gen", "gen", nil, []bob.Sources{bob.Dir("tools/gen")}, struct{}{})}
	})
}

// Bundle is a package of the example kind "example.bundle": one program per platform.
type Bundle struct{ bob.Node }

// An output constructor that needs its program on every platform: the outer run asks for the
// other platform with Elsewhere, which runs this constructor once more there; that nested run
// hands its program over with Record, and the outer run reads it with Recorded.
func ExampleElsewhere() {
	ext.Register(ext.Kind{Name: "example.bundle"})
	_ = bob.Define(func(p *bob.P) *Bundle {
		const key = "example.bundle:jq"
		jq := nixpkgs.Jq.Exe(p)
		if ext.Nested(p) {
			ext.Record(p, key, jq)
			return &Bundle{}
		}
		deps := []bob.Package{jq}
		for _, pl := range []bob.Platform{bob.LinuxAMD64, bob.LinuxARM64} {
			if pl == p.Platform() {
				continue
			}
			if v, ok := ext.Recorded(ext.Elsewhere(p, pl), key); ok {
				deps = append(deps, v.(bob.Package))
			}
		}
		return &Bundle{Node: ext.New(p, "example.bundle", "jq-bundle", deps, nil, struct{}{})}
	})
}

// The nested run of a constructor (made by Elsewhere) creates no node of its own.
func ExampleNested() {
	ext.Register(ext.Kind{Name: "example.bundle"})
	_ = bob.Define(func(p *bob.P) *Bundle {
		if ext.Nested(p) {
			return &Bundle{}
		}
		return &Bundle{Node: ext.New(p, "example.bundle", "empty-bundle", nil, nil, struct{}{})}
	})
}

// The nested run records the packages it got for its platform under a key of the kind.
func ExampleRecord() {
	ext.Register(ext.Kind{Name: "example.bundle"})
	_ = bob.Define(func(p *bob.P) *Bundle {
		curl := nixpkgs.Curl.Exe(p)
		if ext.Nested(p) {
			ext.Record(p, "example.bundle:curl", curl)
			return &Bundle{}
		}
		return &Bundle{Node: ext.New(p, "example.bundle", "curl-bundle", []bob.Package{curl}, nil, struct{}{})}
	})
}

// The outer run reads what the nested run for linux-amd64 recorded.
func ExampleRecorded() {
	ext.Register(ext.Kind{Name: "example.bundle"})
	_ = bob.Define(func(p *bob.P) *Bundle {
		const key = "example.bundle:curl-amd64"
		curl := nixpkgs.Curl.Exe(p)
		if ext.Nested(p) {
			ext.Record(p, key, curl)
			return &Bundle{}
		}
		deps := []bob.Package{curl}
		if p.Platform() != bob.LinuxAMD64 {
			if v, ok := ext.Recorded(ext.Elsewhere(p, bob.LinuxAMD64), key); ok {
				deps = append(deps, v.(bob.Package))
			}
		}
		return &Bundle{Node: ext.New(p, "example.bundle", "curl-amd64", deps, nil, struct{}{})}
	})
}
