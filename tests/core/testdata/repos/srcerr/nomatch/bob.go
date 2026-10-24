//go:build bob

package nomatch

import "github.com/megakuul/bob/pkg/bob"

var _ = bob.Repo(bob.RepoOpts{Nixpkgs: bob.NixOS2605})
