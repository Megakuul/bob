//go:build bob

package native

import (
	"github.com/megakuul/bob/pkg/bob"
	"github.com/megakuul/bob/pkg/npm"
)

var noBundle = false

// App is a Node program with a native addon whose install script is allowed (label //native:native).
var App = bob.Define(func(p *bob.P) *npm.App {
	sqlite, esbuild := npm.Dep("better-sqlite3", "^12.2.0"), npm.Dep("esbuild", "^0.25.0")
	return npm.NewApp(p, npm.AppOpts{
		Entry: "src/main.mjs",
		Deps: []npm.Dependency{
			sqlite,
			esbuild,
			npm.Alias("lodash4", "lodash", "^4.17.21"),
			npm.Dep("@types/better-sqlite3", "^7.6.0", npm.Dev()),
		},
		Bundle:  &noBundle,
		Options: []npm.Option{npm.AllowScripts(sqlite, esbuild)},
	})
})
