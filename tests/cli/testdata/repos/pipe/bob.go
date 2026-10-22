//go:build bob

package pipe

import "github.com/megakuul/bob/pkg/bob"

var _ = bob.Repo(bob.RepoOpts{Nixpkgs: "nixos-26.05"})

// ci builds and tests everything. The step list is SPEC §06's PipelineOpts (see the report:
// SPEC §06 does not define the type's fields, so the fixture registers the name only).
var _ = bob.Pipeline("ci", bob.PipelineOpts{})
