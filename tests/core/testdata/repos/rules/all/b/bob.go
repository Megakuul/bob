//go:build bob

package b

import (
	"github.com/megakuul/bob/pkg/bob"
	"github.com/megakuul/bob/pkg/engine/ir"
)

// Files is a source set.
var Files = bob.Glob("*.txt")

var _ = ir.MaxNodes
