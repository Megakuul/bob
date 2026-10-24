package release

import (
	"slices"

	"github.com/megakuul/bob/pkg/bob"
	"github.com/megakuul/bob/pkg/bob/ext"
	"github.com/megakuul/bob/pkg/output/appimage"
	"github.com/megakuul/bob/pkg/output/oci"
	"github.com/megakuul/bob/pkg/output/tarball"
	"github.com/megakuul/bob/pkg/release/internal/relspec"
)

func init() { ext.Register(ext.Kind{Name: relspec.KindRelease}) }

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

// DefaultPlatforms are the platforms of a release's closures without ReleaseOpts.Platforms.
var DefaultPlatforms = []bob.Platform{bob.LinuxAMD64, bob.LinuxARM64}

// New declares a release. The release name is the label's name; artifact names must be unique
// within it. Building it (with bob build --version) builds every artifact and the release
// manifest release.json (bob.release/v1, R-OUT-023). Example:
//
//	var API = bob.Define(func(p *bob.P) *release.Release {
//		return release.New(p, "api", release.ReleaseOpts{
//			Public:    true,
//			Closures:  []bob.Executable{api.Binary(p)},
//			Images:    []*oci.Image{deploy.Image(p)},
//			AppImages: []*appimage.Bundle{deploy.Bundle(p)},
//		})
//	})
func New(p *bob.P, name string, opts ReleaseOpts) *Release {
	dir, _ := ext.CallerDir()
	key := relspec.KindRelease + ":" + dir + ":" + name
	if ext.Nested(p) {
		// The outer run asked for the closures on this platform.
		ext.Record(p, key, slices.Clone(opts.Closures))
		return &Release{}
	}
	plats := slices.Clone(opts.Platforms)
	if len(plats) == 0 {
		plats = slices.Clone(DefaultPlatforms)
	}
	slices.Sort(plats)
	plats = slices.Compact(plats)
	spec := relspec.Release{Name: name, Public: opts.Public, Platforms: []relspec.Platform{},
		Images: []int{}, AppImages: []int{}, Tarballs: []int{}}
	var deps []bob.Package
	add := func(pk bob.Package) int {
		deps = append(deps, pk)
		return len(deps) - 1
	}
	if len(opts.Closures) > 0 {
		for _, pl := range plats {
			closures := opts.Closures
			if pl != p.Platform() {
				q := ext.Elsewhere(p, pl)
				if q == nil {
					continue // unknown platform: Elsewhere recorded the definition error
				}
				v, _ := ext.Recorded(q, key)
				closures, _ = v.([]bob.Executable)
			}
			sp := relspec.Platform{Platform: string(pl), Closures: []int{}}
			for _, c := range closures {
				sp.Closures = append(sp.Closures, add(c))
			}
			spec.Platforms = append(spec.Platforms, sp)
		}
	}
	for _, i := range opts.Images {
		spec.Images = append(spec.Images, add(i))
	}
	for _, a := range opts.AppImages {
		spec.AppImages = append(spec.AppImages, add(a))
	}
	for _, t := range opts.Tarballs {
		spec.Tarballs = append(spec.Tarballs, add(t))
	}
	// Notes, when given, is the node's only source group (an empty Sources adds none).
	r := &Release{Node: ext.New(p, relspec.KindRelease, name, deps, []bob.Sources{opts.Notes}, spec)}
	var names []string
	for _, c := range opts.Closures {
		names = append(names, nameOf(c))
	}
	for _, i := range opts.Images {
		names = append(names, nameOf(i))
	}
	for _, a := range opts.AppImages {
		names = append(names, nameOf(a))
	}
	for _, t := range opts.Tarballs {
		names = append(names, nameOf(t))
	}
	seen := map[string]bool{}
	for _, n := range names {
		if seen[n] && n != "" {
			ext.Errorf("%s: %s: artifact name %q is used twice; artifact names must be unique within a release", r.DefinedAt(), r.Label(), n)
		}
		seen[n] = true
	}
	return r
}

// nameOf is the label name of pk; "" for a nil package (also a typed nil pointer, whose
// methods cannot be called).
func nameOf(pk bob.Package) (name string) {
	defer func() {
		if recover() != nil {
			name = ""
		}
	}()
	if pk == nil {
		return ""
	}
	return pk.Label().Name
}
