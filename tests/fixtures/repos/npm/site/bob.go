//go:build bob

package site

import (
	tslib "example.com/npmfix/ts-lib"
	"github.com/megakuul/bob/pkg/bob"
	"github.com/megakuul/bob/pkg/npm"
)

// Site is a Vite + Svelte static site using the local library (label //site:site).
var Site = bob.Define(func(p *bob.P) *npm.Site {
	return npm.Vite(p, npm.ViteOpts{
		Deps: []npm.Dependency{
			npm.Dep("svelte", "^5"),
			npm.Dep("vite", "^7.1.0", npm.Dev()),
			npm.Dep("@sveltejs/vite-plugin-svelte", "^6", npm.Dev()),
			tslib.Lib(p),
		},
	})
})
