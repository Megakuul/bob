package release_test

import (
	"github.com/megakuul/bob/pkg/bob"
	"github.com/megakuul/bob/pkg/golang"
	"github.com/megakuul/bob/pkg/output/oci"
	"github.com/megakuul/bob/pkg/release"
)

// A public release of a Go program: its closure for both platforms (bob install) and its
// image. bob build --version 1.4.0 builds every artifact and release.json.
func ExampleNew() {
	module := bob.Define(func(p *bob.P) *golang.Module {
		return golang.NewModule(p, golang.ModuleOpts{Path: "example.com/api", Go: "1.26"})
	})
	api := func(p *bob.P) *golang.Binary { return module(p).Binary("./cmd/api", golang.BinaryOpts{}) }
	image := bob.Define(func(p *bob.P) *oci.Image {
		return oci.NewImage(p, "api-image", oci.ImageOpts{Program: api(p)})
	})
	_ = bob.Define(func(p *bob.P) *release.Release {
		return release.New(p, "api", release.ReleaseOpts{
			Public:   true,
			Closures: []bob.Executable{api(p)},
			Images:   []*oci.Image{image(p)},
		})
	})
}
