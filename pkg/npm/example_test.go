package npm_test

import (
	"github.com/megakuul/bob/pkg/bob"
	"github.com/megakuul/bob/pkg/npm"
)

// A TypeScript library published to its consumers' node_modules as @demo/ui; react comes from
// the consumer.
func ExampleLibrary() {
	_ = bob.Define(func(p *bob.P) *npm.Lib {
		return npm.Library(p, npm.LibOpts{
			Name:  "@demo/ui",
			Deps:  []npm.Dependency{npm.Dep("typescript", "~5.9.2", npm.Dev())},
			Peers: []npm.Dependency{npm.Dep("react", "^19")},
		})
	})
}

// A Node server bundled with esbuild; better-sqlite3's install script is allowed to build its
// native addon.
func ExampleNewApp() {
	_ = bob.Define(func(p *bob.P) *npm.App {
		return npm.NewApp(p, npm.AppOpts{
			Entry:   "src/server.ts",
			Deps:    []npm.Dependency{npm.Dep("fastify", "^5"), npm.Dep("better-sqlite3", "^12.2.0")},
			Options: []npm.Option{npm.AllowScripts("better-sqlite3")},
		})
	})
}

// A Svelte site built with vite build; bob dev runs its dev server on port 5173.
func ExampleVite() {
	_ = bob.Define(func(p *bob.P) *npm.Site {
		return npm.Vite(p, npm.ViteOpts{Deps: []npm.Dependency{
			npm.Dep("svelte", "^5"),
			npm.Dep("vite", "^7.1.0", npm.Dev()),
			npm.Dep("@sveltejs/vite-plugin-svelte", "^6", npm.Dev()),
		}})
	})
}

// vitest over a library's src/**/*.test.ts (label //<dir>:test).
func ExampleNewTest() {
	lib := bob.Define(func(p *bob.P) *npm.Lib {
		return npm.Library(p, npm.LibOpts{Name: "@demo/util", Deps: []npm.Dependency{npm.Dep("vitest", "^3.2.0", npm.Dev())}})
	})
	_ = bob.Define(func(p *bob.P) *npm.Test { return npm.NewTest(p, npm.TestOpts{Of: lib(p)}) })
}
