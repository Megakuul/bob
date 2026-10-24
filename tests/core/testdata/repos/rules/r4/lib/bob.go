//go:build bob

package lib

import "github.com/megakuul/bob/pkg/bob"

// Files is a source set.
var Files = bob.Glob("*.go")
