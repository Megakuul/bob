package oci

import (
	"fmt"
	"maps"
	"slices"

	"github.com/megakuul/bob/pkg/bob"
	"github.com/megakuul/bob/pkg/bob/ext"
	"github.com/megakuul/bob/pkg/nixpkgs"
	"github.com/megakuul/bob/pkg/nixpkgs/nixext"
	"github.com/megakuul/bob/pkg/output/oci/internal/ocispec"
)

// The image kind reads no variant axis itself: its programs carry their own configuration.
func init() { ext.Register(ext.Kind{Name: ocispec.KindImage}) }

// Image is an OCI image built from closures, without Docker. Each platform gets one image
// manifest; the result is an image index. Program and Extra are built for each platform.
type Image struct{ bob.Node }

// ImageOpts configures an image. Zero values give the defaults in the field comments.
type ImageOpts struct {
	Program    bob.Executable    // closure root; Entrypoint defaults to its MainProgram
	Extra      []bob.Package     // more roots (e.g. a debug shell); each gets its own layer
	Entrypoint []string          // default: []string{<Program out>/<MainProgram>}
	Cmd        []string          //
	Env        map[string]string // merged over the defaults PATH, SSL_CERT_FILE, TZDIR
	User       string            // default "65534:65534"; "0:0" must be written explicitly
	WorkDir    string            // default "/"
	Ports      []int             // ExposedPorts "<n>/tcp"
	Labels     map[string]string // org.opencontainers.image.* from a release are added
	NoCACerts  bool              // default false: nss-cacert + /etc/ssl/certs symlinks
	NoTZData   bool              // default false: tzdata + /usr/share/zoneinfo symlink
	Platforms  []bob.Platform    // default {LinuxAMD64, LinuxARM64}
	MaxLayers  int               // default 100, allowed 2..125
	Prune      []string          // R-OUT-007; default none
}

// DefaultPlatforms are the platforms of an image without ImageOpts.Platforms.
var DefaultPlatforms = []bob.Platform{bob.LinuxAMD64, bob.LinuxARM64}

// roots are what a nested evaluation (ext.Elsewhere) records for the outer one: the image's
// roots on the nested platform.
type roots struct {
	program bob.Executable
	extra   []bob.Package
}

// NewImage declares an image. Example:
//
//	var Image = bob.Define(func(p *bob.P) *oci.Image {
//		return oci.NewImage(p, "api", oci.ImageOpts{
//			Program: api.Binary(p),
//			Ports:   []int{8080},
//		})
//	})
func NewImage(p *bob.P, name string, opts ImageOpts) *Image {
	dir, _ := ext.CallerDir()
	key := ocispec.KindImage + ":" + dir + ":" + name
	if ext.Nested(p) {
		// The outer run of this constructor asked for the roots on this platform.
		ext.Record(p, key, roots{program: opts.Program, extra: opts.Extra})
		return &Image{}
	}
	plats := slices.Clone(opts.Platforms)
	if len(plats) == 0 {
		plats = slices.Clone(DefaultPlatforms)
	}
	slices.Sort(plats)
	plats = slices.Compact(plats)
	spec := ocispec.Image{Name: name, Entrypoint: nonNil(opts.Entrypoint), Cmd: nonNil(opts.Cmd),
		Env: maps.Clone(opts.Env), User: opts.User, WorkDir: opts.WorkDir, Ports: nonNil(opts.Ports),
		Labels: maps.Clone(opts.Labels), MaxLayers: opts.MaxLayers, Prune: nonNil(opts.Prune), CACert: -1, TZData: -1}
	var deps []bob.Package
	add := func(pk bob.Package) int {
		deps = append(deps, pk)
		return len(deps) - 1
	}
	for _, pl := range plats {
		r := roots{program: opts.Program, extra: opts.Extra}
		if pl != p.Platform() {
			q := ext.Elsewhere(p, pl)
			if q == nil {
				continue // unknown platform: Elsewhere recorded the definition error
			}
			v, _ := ext.Recorded(q, key)
			r, _ = v.(roots)
		}
		if r.program == nil {
			ext.Errorf("oci.NewImage(%q): Program is required (for %s)", name, pl)
			continue
		}
		sp := ocispec.Platform{Platform: string(pl), Program: add(r.program), Extra: []int{}}
		for _, e := range r.extra {
			sp.Extra = append(sp.Extra, add(e))
		}
		spec.Platforms = append(spec.Platforms, sp)
	}
	// CA certificates and time zones are data: the host's package serves every platform, so
	// all images share their layers.
	if !opts.NoCACerts {
		spec.CACert = add(nixpkgs.Cacert.Pkg(p.Host(), nixext.Outputs(nixext.Out)))
	}
	if !opts.NoTZData {
		spec.TZData = add(nixpkgs.Tzdata.Pkg(p.Host(), nixext.Outputs(nixext.Out)))
	}
	img := &Image{Node: ext.New(p, ocispec.KindImage, name, deps, nil, spec)}
	at := fmt.Sprintf("%s: %s", img.DefinedAt(), img.Label())
	switch n := 1 + len(opts.Extra); {
	case opts.MaxLayers != 0 && (opts.MaxLayers < 2 || opts.MaxLayers > 125):
		ext.Errorf("%s: invalid MaxLayers: must be between 2 and 125 (got %d)", at, opts.MaxLayers)
	case opts.MaxLayers != 0 && opts.MaxLayers < n+2:
		ext.Errorf("%s: invalid MaxLayers: %d roots need MaxLayers ≥ %d", at, n, n+2)
	}
	for _, port := range opts.Ports {
		if port < 1 || port > 65535 {
			ext.Errorf("%s: invalid Ports: %d is not a TCP port", at, port)
		}
	}
	return img
}

func nonNil[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return slices.Clone(s)
}
