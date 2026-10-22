package oci

import (
	"github.com/megakuul/bob/pkg/bob"
	"github.com/megakuul/bob/pkg/bob/ext"
)

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

// NewImage declares an image.
func NewImage(p *bob.P, name string, opts ImageOpts) *Image {
	deps := append([]bob.Package{opts.Program}, opts.Extra...)
	return &Image{Node: ext.New(p, "oci.image", name, deps, nil, opts.Ports)}
}
