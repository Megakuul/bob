//go:build bob

package app

import "github.com/megakuul/bob/pkg/bob"

var _ = bob.Repo(bob.RepoOpts{Nixpkgs: bob.NixOS2605})

var _ = bob.Import("example.com/dep", "v1.0.0")
