//go:build bob

package deploy

import (
	"example.com/polyglot"
	"github.com/megakuul/bob/pkg/bob"
	"github.com/megakuul/bob/pkg/output/appimage"
	"github.com/megakuul/bob/pkg/output/oci"
	"github.com/megakuul/bob/pkg/output/tarball"
	"github.com/megakuul/bob/pkg/release"
)

// Image is the OCI image index of the api (label //deploy:api-image).
var Image = bob.Define(func(p *bob.P) *oci.Image {
	return oci.NewImage(p, "api-image", oci.ImageOpts{Program: polyglot.API(p), Ports: []int{8080}})
})

// Bundle is the AppImage of the api (label //deploy:api-appimage).
var Bundle = bob.Define(func(p *bob.P) *appimage.Bundle {
	return appimage.NewBundle(p, "api-appimage", appimage.BundleOpts{Program: polyglot.API(p)})
})

// Tar is a closure tarball of the api (label //deploy:api-tar).
var Tar = bob.Define(func(p *bob.P) *tarball.Tarball {
	return tarball.New(p, "api-tar", tarball.TarballOpts{Program: polyglot.API(p), Kind: tarball.Closure})
})

// Polyglot is the release the registry tests publish (label //deploy:polyglot): the OCI image of
// the api, versioned by the tag.
var Polyglot = bob.Define(func(p *bob.P) *release.Release {
	return release.New(p, "polyglot", release.ReleaseOpts{
		Images: []*oci.Image{Image(p)},
	})
})

// Release is the api release (label //deploy:api).
var Release = bob.Define(func(p *bob.P) *release.Release {
	return release.New(p, "api", release.ReleaseOpts{
		Closures:  []bob.Executable{polyglot.API(p)},
		Images:    []*oci.Image{Image(p)},
		AppImages: []*appimage.Bundle{Bundle(p)},
		Tarballs:  []*tarball.Tarball{Tar(p)},
	})
})
