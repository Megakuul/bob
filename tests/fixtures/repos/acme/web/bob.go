//go:build bob

package web

import (
	"github.com/megakuul/bob/pkg/bob"
	"github.com/megakuul/bob/pkg/npm"
)

// Site is the Svelte frontend (label //web:web). package.json and tsconfig.json are generated.
var Site = bob.Define(func(p *bob.P) *npm.Site {
	return npm.Vite(p, npm.ViteOpts{Deps: []npm.Dependency{
		npm.Dep("svelte", "^5"),
		npm.Dep("vite", "^7.1.0", npm.Dev()),
		npm.Dep("@sveltejs/vite-plugin-svelte", "^6", npm.Dev()),
	}})
})
