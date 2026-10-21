//go:build bob

package tslib

import (
	"github.com/megakuul/bob/pkg/bob"
	"github.com/megakuul/bob/pkg/npm"
)

// Lib is the TypeScript library @fix/ts-lib (label //ts-lib:ts-lib).
var Lib = bob.Define(func(p *bob.P) *npm.Lib {
	return npm.Library(p, npm.LibOpts{
		Name: "@fix/ts-lib",
		Deps: []npm.Dependency{
			npm.Dep("zod", "^4.0.0"),
			npm.Dep("typescript", "~5.9.2", npm.Dev()),
			npm.Dep("@types/node", "^24", npm.Dev()),
			npm.Dep("vitest", "^3.2.0", npm.Dev()),
		},
	})
})

// Test runs vitest over src/**/*.test.ts.
var Test = bob.Define(func(p *bob.P) *npm.Test { return npm.Test(p, npm.TestOpts{Of: Lib(p)}) })
