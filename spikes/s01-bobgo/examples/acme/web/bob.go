//go:build bob

package web

import (
	"github.com/megakuul/bob/pkg/bob"
	"github.com/megakuul/bob/pkg/npm"
)

// App is the Svelte frontend (label //web:web). package.json and tsconfig.json are generated.
var App = bob.Define(func(p *bob.P) *npm.App {
	return npm.ViteApp(p, "web", npm.AppOpts{
		Srcs:    bob.Glob("src/**", "vite.config.ts", "svelte.config.js"),
		Deps:    npm.Deps{"svelte": "^5.0.0"},
		DevDeps: npm.Deps{"vite": "^6.0.0", "@sveltejs/vite-plugin-svelte": "^5.0.0"},
		Runtime: npm.Node,
	})
})
