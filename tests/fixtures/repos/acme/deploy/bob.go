//go:build bob

package deploy

import (
	"example.com/acme"
	"github.com/megakuul/bob/pkg/bob"
	"github.com/megakuul/bob/pkg/output/appimage"
	"github.com/megakuul/bob/pkg/output/oci"
	"github.com/megakuul/bob/pkg/release"
)

// Image is the OCI image of the api (label //deploy:api-image). The binary comes from the typed
// slot acme.API, bound by cmd/api/bob.go.
var Image = bob.Define(func(p *bob.P) *oci.Image {
	return oci.NewImage(p, "api-image", oci.ImageOpts{Program: acme.API.Get(p), Ports: []int{8080}})
})

// Bundle is the AppImage of the api (label //deploy:api-appimage).
var Bundle = bob.Define(func(p *bob.P) *appimage.Bundle {
	return appimage.NewBundle(p, "api-appimage", appimage.BundleOpts{Program: acme.API.Get(p)})
})

// Release is the release api (label //deploy:api): closures per platform, an OCI image and an
// AppImage; published by pipeline release on a tag v*.
var Release = bob.Define(func(p *bob.P) *release.Release {
	return release.New(p, "api", release.ReleaseOpts{
		Closures:  []bob.Executable{acme.API.Get(p)},
		Images:    []*oci.Image{Image(p)},
		AppImages: []*appimage.Bundle{Bundle(p)},
	})
})
