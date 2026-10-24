//go:build bob

package b

import (
	"example.com/rules/r6/a"
	"github.com/megakuul/bob/pkg/bob"
)

// Files reuses a's definitions.
var Files = bob.Glob("*.go").Union(a.Files)
