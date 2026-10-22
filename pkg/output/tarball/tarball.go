package tarball

import (
	"github.com/megakuul/bob/pkg/bob"
	"github.com/megakuul/bob/pkg/bob/ext"
)

// Tarball is a `<name>-<platform>.tar.gz` output.
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

// New declares a tarball.
func New(p *bob.P, name string, opts TarballOpts) *Tarball {
	return &Tarball{Node: ext.New(p, "tarball", name, []bob.Package{opts.Program}, nil, opts.Kind)}
}
