//go:build bob

package a

import "github.com/megakuul/bob/pkg/bob"

// Files is a source set.
var Files = bob.Glob("*.go")
