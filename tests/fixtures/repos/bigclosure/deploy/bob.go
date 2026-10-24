//go:build bob

package deploy

import (
	"example.com/bigclosure"
	"github.com/megakuul/bob/pkg/bob"
	"github.com/megakuul/bob/pkg/output/oci"
	"github.com/megakuul/bob/pkg/release"
)

// Image carries the bulk as an extra closure root (own layer) next to the program
// (label //deploy:big-image).
var Image = bob.Define(func(p *bob.P) *oci.Image {
	return oci.NewImage(p, "big-image", oci.ImageOpts{
		Program: bigclosure.Big.Get(p),
		Extra:   []bob.Package{bigclosure.Bulk(p)},
	})
})

// Release is the release "big" (label //deploy:big).
var Release = bob.Define(func(p *bob.P) *release.Release {
	return release.New(p, "big", release.ReleaseOpts{
		Closures: []bob.Executable{bigclosure.Big.Get(p)},
		Images:   []*oci.Image{Image(p)},
	})
})
