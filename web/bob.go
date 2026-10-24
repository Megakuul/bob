//go:build bob

package web

import (
	"github.com/megakuul/bob/pkg/bob"
	"github.com/megakuul/bob/pkg/npm"
)

// UI is the web UI (SPEC §6, R-UI-001): a static SvelteKit build (adapter-static into build/,
// 200.html fallback) that internal/ui embeds. Label //web:web.
var UI = bob.Define(func(p *bob.P) *npm.Site {
	return npm.Vite(p, npm.ViteOpts{
		Kit: true,
		Deps: []npm.Dependency{
			npm.Dep("@bufbuild/protobuf", "2.16.0"),
			npm.Dep("@connectrpc/connect", "2.2.0"),
			npm.Dep("@connectrpc/connect-web", "2.2.0"),
			npm.Dep("@sveltejs/adapter-static", "3.0.10", npm.Dev()),
			npm.Dep("@sveltejs/kit", "2.70.3", npm.Dev()),
			npm.Dep("@sveltejs/vite-plugin-svelte", "7.3.1", npm.Dev()),
			npm.Dep("@tailwindcss/vite", "4.3.3", npm.Dev()),
			npm.Dep("svelte", "5.57.2", npm.Dev()),
			npm.Dep("svelte-check", "4.7.7", npm.Dev()),
			npm.Dep("tailwindcss", "4.3.3", npm.Dev()),
			npm.Dep("typescript", "6.0.3", npm.Dev()),
			npm.Dep("vite", "8.3.5", npm.Dev()),
		},
		TSConfig: npm.TSConfig{Extra: map[string]any{
			"skipLibCheck": true, "sourceMap": true, "checkJs": true, "allowJs": true,
		}},
	})
})

// Check is svelte-check over the UI, a test of the ci pipeline (M9.5). Label //web:svelte-check.
var Check = bob.Define(func(p *bob.P) *npm.Test {
	return npm.NewTest(p, npm.TestOpts{Of: UI(p), Runner: npm.SvelteCheck})
})
