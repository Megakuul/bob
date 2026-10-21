// Package npm (mock) builds TypeScript / npm packages.
package npm

import "github.com/megakuul/bob/pkg/bob"

type Runtime string

const (
	Node Runtime = "node"
	Bun  Runtime = "bun"
)

// Deps maps npm package names to semver ranges; bob's resolver pins them in bob.lock.
type Deps map[string]string

type AppOpts struct {
	Srcs    bob.Source
	Deps    Deps
	DevDeps Deps
	Libs    []*Library
	Runtime Runtime
}

type App struct{ *bob.Base }
type Library struct{ *bob.Base }

// ViteApp defines a Vite frontend build.
func ViteApp(p *bob.P, name string, o AppOpts) *App {
	var d []bob.Package
	for _, l := range o.Libs {
		d = append(d, l)
	}
	return &App{bob.NewPackage(p, 1, "npm_vite_app", name, d, map[string]any{"deps": o.Deps, "devDeps": o.DevDeps, "runtime": o.Runtime})}
}

func NewLibrary(p *bob.P, name string, deps Deps) *Library {
	return &Library{bob.NewPackage(p, 1, "npm_library", name, nil, map[string]any{"deps": deps})}
}
