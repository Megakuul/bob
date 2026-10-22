//go:build bob

package deploy

import (
	"example.com/acme"
	"github.com/megakuul/bob/pkg/bob"
	"github.com/megakuul/bob/pkg/golang"
	"github.com/megakuul/bob/pkg/output/appimage"
	"github.com/megakuul/bob/pkg/output/oci"
	"github.com/megakuul/bob/pkg/release"
)

// api is the same program as //cmd/api:api (identical declaration, so one node).
func api(p *bob.P) *golang.Binary {
	return acme.Module(p).Binary("./cmd/api", golang.BinaryOpts{
		X: map[string]string{"main.version": "dev"},
	})
}

// Image is the OCI image of the api (label //deploy:api-image).
var Image = bob.Define(func(p *bob.P) *oci.Image {
	return oci.NewImage(p, "api-image", oci.ImageOpts{Program: api(p), Ports: []int{8080}})
})

// Bundle is the AppImage of the api (label //deploy:api-appimage).
var Bundle = bob.Define(func(p *bob.P) *appimage.Bundle {
	return appimage.NewBundle(p, "api-appimage", appimage.BundleOpts{Program: api(p)})
})

// Release is the release api (label //deploy:api): closures per platform, an OCI image and an
// AppImage; published by pipeline release on a tag v*.
var Release = bob.Define(func(p *bob.P) *release.Release {
	return release.New(p, "api", release.ReleaseOpts{
		Closures:  []bob.Executable{api(p)},
		Images:    []*oci.Image{Image(p)},
		AppImages: []*appimage.Bundle{Bundle(p)},
	})
})
