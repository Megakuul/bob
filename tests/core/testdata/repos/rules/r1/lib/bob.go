// Package lib has a bob.go without the build constraint on line 1.
//go:build bob

package lib

import "github.com/megakuul/bob/pkg/bob"

// Files is a source set.
var Files = bob.Glob("*.txt")
