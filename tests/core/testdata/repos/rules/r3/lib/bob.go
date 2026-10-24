//go:build bob

package lib

import (
	app "example.com/rules/r3/cmd/app"
	"github.com/megakuul/bob/pkg/bob"
)

// Files reuses the main package's definitions.
var Files = bob.Glob("*.txt").Union(app.Files)
