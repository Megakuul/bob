//go:build bob

package corelib

import (
	"github.com/megakuul/bob/pkg/bob"
	"github.com/megakuul/bob/pkg/rust"
)

// Lib is a library with a build script and features (label //corelib:corelib).
var Lib = bob.Define(func(p *bob.P) *rust.Lib {
	return rust.Library(p, rust.LibOpts{
		Deps:     []rust.Dep{rust.Crate("itoa", "1"), rust.Crate("hex", "0.4")},
		Features: map[string][]string{"fast": {}, "hexout": {}},
		Default:  []string{"fast"},
	})
})

// Test runs the unit tests and tests/*.rs (units //corelib:corelib_test, //corelib:corelib_test_num).
var Test = bob.Define(func(p *bob.P) *rust.Test { return rust.Test(p, rust.TestOpts{Crate: Lib(p)}) })
