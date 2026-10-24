package tarball_test

import (
	"github.com/megakuul/bob/pkg/bob"
	"github.com/megakuul/bob/pkg/nixpkgs"
	"github.com/megakuul/bob/pkg/output/tarball"
)

// The closure of a program as jq-<platform>.tar.gz for the host platform.
func ExampleNew() {
	_ = bob.Define(func(p *bob.P) *tarball.Tarball {
		return tarball.New(p, "jq", tarball.TarballOpts{Program: nixpkgs.Exe(p, "jq", "bin/jq"), Kind: tarball.Closure})
	})
}
