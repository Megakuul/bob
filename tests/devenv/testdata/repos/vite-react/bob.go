//go:build bob

package web

import (
	"github.com/megakuul/bob/pkg/bob"
	"github.com/megakuul/bob/pkg/npm"
)

var _ = bob.Repo(bob.RepoOpts{Nixpkgs: bob.NixOS2605})

// Site is a Vite + React app (label //:web); its dev server runs under bob dev (R-DEV-002).
var Site = bob.Define(func(p *bob.P) *npm.Site {
	return npm.Vite(p, npm.ViteOpts{Deps: []npm.Dependency{
		npm.Dep("react", "^19.1.0"),
		npm.Dep("react-dom", "^19.1.0"),
		npm.Dep("vite", "^7.1.0", npm.Dev()),
		npm.Dep("@vitejs/plugin-react", "^5.0.0", npm.Dev()),
		npm.Dep("typescript", "~5.9.2", npm.Dev()),
		npm.Dep("@types/react", "^19.1.0", npm.Dev()),
		npm.Dep("@types/react-dom", "^19.1.0", npm.Dev()),
	}})
})

// Dev is the default development shell: the dev server of Site runs in it.
var Dev = bob.Define(func(p *bob.P) *bob.Shell {
	return bob.NewShell(p, bob.DevShell{Servers: []bob.Package{Site(p)}})
})
