//go:build bob

package lib

import (
	"github.com/megakuul/bob/pkg/bob"
	"github.com/megakuul/bob/pkg/nixpkgs/nixext"
)

// Files is a source set.
var Files = bob.Glob("*.txt")

// Gcc builds a nixpkgs attribute from a string, which only bob's own packages may do (R7, ADR 0020).
var Gcc = nixext.New("gcc")
