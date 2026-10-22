package release

import (
	"github.com/megakuul/bob/pkg/bob"
	"github.com/megakuul/bob/pkg/bob/ext"
	"github.com/megakuul/bob/pkg/output/appimage"
	"github.com/megakuul/bob/pkg/output/oci"
	"github.com/megakuul/bob/pkg/output/tarball"
)

// Release is a versioned set of artifacts. Publishing (immutability, visibility, promotion)
// happens on the server (§06); this type only describes and builds it.
type Release struct{ bob.Node }

// ReleaseOpts configures a release.
type ReleaseOpts struct {
	Public    bool               // installable and downloadable without authentication
	Closures  []bob.Executable   // installable with bob install; built for each of Platforms
	Images    []*oci.Image       //
	AppImages []*appimage.Bundle //
	Tarballs  []*tarball.Tarball //
	Platforms []bob.Platform     // default {LinuxAMD64, LinuxARM64}, applies to Closures
	Notes     bob.Sources        // optional Markdown file shown in the UI (SPEC names the type bob.Source)
}

// New declares a release.
func New(p *bob.P, name string, opts ReleaseOpts) *Release {
	var deps []bob.Package
	for _, c := range opts.Closures {
		deps = append(deps, c)
	}
	for _, i := range opts.Images {
		deps = append(deps, i)
	}
	for _, a := range opts.AppImages {
		deps = append(deps, a)
	}
	for _, t := range opts.Tarballs {
		deps = append(deps, t)
	}
	return &Release{Node: ext.New(p, "release", name, deps, []bob.Sources{opts.Notes}, opts.Public)}
}
