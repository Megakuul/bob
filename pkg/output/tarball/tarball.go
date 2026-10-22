package tarball

import (
	"github.com/megakuul/bob/pkg/bob"
	"github.com/megakuul/bob/pkg/bob/ext"
	"github.com/megakuul/bob/pkg/output/tarball/internal/tarspec"
)

func init() { ext.Register(ext.Kind{Name: tarspec.KindTarball}) }

// Tarball is a `<name>-<platform>.tar.gz` output (R-OUT-004).
type Tarball struct{ bob.Node }

// Kind selects what a tarball contains.
type Kind int

const (
	Closure Kind = iota // whole closure under nix/store
	Static              // a single static executable
)

// TarballOpts configures a tarball.
type TarballOpts struct {
	Program  bob.Executable
	Kind     Kind
	Platform bob.Platform // default host
}

// New declares a tarball. Example:
//
//	var Tar = bob.Define(func(p *bob.P) *tarball.Tarball {
//		return tarball.New(p, "cli", tarball.TarballOpts{Program: cli.Binary(p), Kind: tarball.Static})
//	})
func New(p *bob.P, name string, opts TarballOpts) *Tarball {
	dir, _ := ext.CallerDir()
	key := tarspec.KindTarball + ":" + dir + ":" + name
	if ext.Nested(p) {
		ext.Record(p, key, opts.Program)
		return &Tarball{}
	}
	plat := opts.Platform
	if plat == "" {
		plat = p.Host().Platform()
	}
	prog := opts.Program
	if plat != p.Platform() {
		q := ext.Elsewhere(p, plat)
		if q == nil {
			return &Tarball{} // unknown platform: Elsewhere recorded the definition error
		}
		v, _ := ext.Recorded(q, key)
		prog, _ = v.(bob.Executable)
	}
	kind := tarspec.Closure
	if opts.Kind == Static {
		kind = tarspec.Static
	}
	if prog == nil {
		ext.Errorf("tarball.New(%q): Program is required", name)
		return &Tarball{}
	}
	spec := tarspec.Tarball{Name: name, Kind: kind, Platform: string(plat)}
	return &Tarball{Node: ext.New(p, tarspec.KindTarball, name, []bob.Package{prog}, nil, spec)}
}
