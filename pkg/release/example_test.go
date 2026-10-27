package release_test

import (
	"github.com/megakuul/bob/pkg/bob"
	"github.com/megakuul/bob/pkg/golang"
	"github.com/megakuul/bob/pkg/output/oci"
	"github.com/megakuul/bob/pkg/release"
)

// API is the typed slot of the program, declared in an importable bob.go (acme/bob.go) and
// bound by the main package's own bob.go (cmd/api/bob.go: var _ = acme.API.Bind(Binary)).
var API = bob.NewSlot[*golang.Binary]()

// A public release of a Go program: its closure for both platforms (bob install) and its
// image. bob build --version 1.4.0 builds every artifact and release.json.
func ExampleNew() {
	image := bob.Define(func(p *bob.P) *oci.Image {
		return oci.NewImage(p, "api-image", oci.ImageOpts{Program: API.Get(p)})
	})
	_ = bob.Define(func(p *bob.P) *release.Release {
		return release.New(p, "api", release.ReleaseOpts{
			Public:   true,
			Closures: []bob.Executable{API.Get(p)},
			Images:   []*oci.Image{image(p)},
		})
	})
}
