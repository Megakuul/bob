//go:build bob

package lib

import (
	"github.com/megakuul/bob/pkg/bob"
	"github.com/megakuul/bob/pkg/nixpkgs"
)

// Files is a source set.
var Files = bob.Glob("*.txt")

// Swap overwrites a typed handle of the framework (R8, ADR 0020).
var Swap = func() bool {
	nixpkgs.Jq = nixpkgs.Zlib
	return true
}()
