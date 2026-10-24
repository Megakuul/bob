//go:build bob

package npm

import "github.com/megakuul/bob/pkg/bob"

var _ = bob.Repo(bob.RepoOpts{Nixpkgs: bob.NixOS2605})
