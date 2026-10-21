//go:build bob

package lib

import (
	"github.com/megakuul/bob/pkg/bob"
	"github.com/megakuul/bob/pkg/engine/ir"
)

// Files is a source set.
var Files = bob.Glob("*.txt")

var _ = ir.MaxNodes
