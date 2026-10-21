//go:build bob

package web

import (
	greetingv1 "example.com/polyglot/proto/greeting/v1"
	"github.com/megakuul/bob/pkg/bob"
	"github.com/megakuul/bob/pkg/connect"
	"github.com/megakuul/bob/pkg/npm"
)

// Site is the Svelte frontend (label //web:web).
var Site = bob.Define(func(p *bob.P) *npm.Site {
	return npm.Vite(p, npm.ViteOpts{
		Deps: []npm.Dependency{
			npm.Dep("svelte", "^5"),
			npm.Dep("vite", "^7.1.0", npm.Dev()),
			npm.Dep("@sveltejs/vite-plugin-svelte", "^6", npm.Dev()),
		},
		Generated: []npm.Generated{connect.TS(greetingv1.Greeting(p), connect.TSOpts{})},
	})
})
