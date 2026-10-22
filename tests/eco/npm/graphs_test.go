package npm

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/megakuul/bob/tests/eco/internal/ecotest"
)

// dep is one declared dependency of a graph importer.
type dep struct {
	name, req string
	dev       bool
	alias     string // non-empty: npm.Alias(alias, name, req)
}

// importer is one importer directory of a graph. kind is "app", "vite" or "lib".
type importer struct {
	dir   string // repo-relative; "." is the repository root
	kind  string
	deps  []dep
	peers []dep // lib only
}

// graph is one resolution corpus entry. The first six are the S4 graphs (69-411 packages), the
// others are the SPEC's additions (SvelteKit, Vite+React is among the six, fastify likewise).
type graph struct {
	name      string
	importers []importer
	// peerSplit marks the graph where bob's lock gives each importer its own peer (asserted
	// explicitly in TestRNPM001aPeerSplit).
	peerSplit bool
}

func d(name, req string) dep      { return dep{name: name, req: req} }
func dd(name, req string) dep     { return dep{name: name, req: req, dev: true} }
func al(as, name, req string) dep { return dep{name: name, req: req, alias: as} }

// graphs is the corpus (decision: the S4 workspace definitions of docs/spikes/s04-npm.md §2).
func graphs() []graph {
	return []graph{
		{name: "vite-react", importers: []importer{{dir: "web", kind: "vite", deps: []dep{
			d("react", "^19"), d("react-dom", "^19"), dd("vite", "^7.1.0"), dd("@vitejs/plugin-react", "^5"),
			dd("typescript", "~5.9.2"), dd("@types/react", "^19"), dd("@types/react-dom", "^19")}}}},
		{name: "ts-lib", importers: []importer{{dir: "lib", kind: "lib", deps: []dep{
			d("zod", "^4.0.0"), dd("typescript", "~5.9.2"), dd("@types/node", "^24"), dd("vitest", "^3.2.0"),
			dd("eslint", "^9"), dd("typescript-eslint", "^8")}}}},
		{name: "fastify", importers: []importer{{dir: "api", kind: "app", deps: []dep{
			d("fastify", "^5"), d("@fastify/cors", "^11"), d("pino-pretty", "^13"), dd("esbuild", "^0.25.0")}}}},
		{name: "native", importers: []importer{{dir: "native", kind: "app", deps: []dep{
			d("better-sqlite3", "^12.2.0"), d("esbuild", "^0.25.0"), al("lodash4", "lodash", "^4.17.21"),
			dd("@types/better-sqlite3", "^7.6.0")}}}},
		{name: "mono", importers: []importer{
			{dir: ".", kind: "app", deps: []dep{dd("npm", "^11")}},
			{dir: "packages/ui", kind: "lib", deps: []dep{dd("typescript", "~5.9.2")}, peers: []dep{d("svelte", "^5")}},
			{dir: "apps/web", kind: "vite", deps: []dep{d("svelte", "^5"), dd("vite", "^7.1.0"), dd("@sveltejs/vite-plugin-svelte", "^6")}},
		}},
		{name: "stress", importers: []importer{{dir: "stress", kind: "app", deps: []dep{
			dd("jest", "^29"), dd("webpack", "^5"), dd("eslint", "^8"), dd("@babel/core", "^7"),
			dd("ts-jest", "^29"), dd("@babel/preset-env", "^7"), dd("babel-jest", "^29"), dd("webpack-cli", "^5")}}}},
		{name: "sveltekit", importers: []importer{{dir: "kit", kind: "vite", deps: []dep{
			d("svelte", "^5"), dd("@sveltejs/kit", "^2"), dd("@sveltejs/adapter-static", "^3"),
			dd("@sveltejs/vite-plugin-svelte", "^6"), dd("vite", "^7.1.0"), dd("typescript", "~5.9.2")}}}},
		{name: "vite-svelte", importers: []importer{{dir: "site", kind: "vite", deps: []dep{
			d("svelte", "^5"), dd("vite", "^7.1.0"), dd("@sveltejs/vite-plugin-svelte", "^6")}}}},
		{name: "bun-app", importers: []importer{{dir: "hono", kind: "app", deps: []dep{d("hono", "^4"), d("zod", "^4")}}}},
		{name: "peersplit", peerSplit: true, importers: []importer{
			{dir: ".", kind: "app", deps: []dep{d("react", "^18"), d("use-sync-external-store", "^1")}},
			{dir: "b", kind: "app", deps: []dep{d("react", "^19"), d("use-sync-external-store", "^1")}},
		}},
	}
}

func goDep(x dep) string {
	opts := ""
	if x.dev {
		opts = ", npm.Dev()"
	}
	if x.alias != "" {
		return fmt.Sprintf("npm.Alias(%q, %q, %q)", x.alias, x.name, x.req)
	}
	return fmt.Sprintf("npm.Dep(%q, %q%s)", x.name, x.req, opts)
}

func goDeps(ds []dep) string {
	var parts []string
	for _, x := range ds {
		parts = append(parts, goDep(x))
	}
	return "[]npm.Dependency{" + strings.Join(parts, ", ") + "}"
}

// writeGraph writes a repository for g: go.mod, a root bob.go with the repo declaration and
// one bob.go per importer (the way the npm fixture spells them) plus the minimal source files.
func writeGraph(t *testing.T, g graph) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "root") // R2: root package = directory name
	w := func(rel, body string) { ecotest.WriteFile(t, filepath.Join(dir, filepath.FromSlash(rel)), body) }
	w("go.mod", "module example.com/"+strings.ReplaceAll(g.name, "-", "")+"\n\ngo 1.26\n")
	w("bob.lock", "{\n  \"lockVersion\": 1\n}\n")
	rootDecl := "var _ = bob.Repo(bob.RepoOpts{Nixpkgs: \"nixos-26.05\"})\n"
	for i, im := range g.importers {
		pkg := "g" + fmt.Sprint(i)
		body := ""
		switch im.kind {
		case "app":
			w(im.dir+"/src/main.ts", "export {};\n")
			body = fmt.Sprintf("var Importer = bob.Define(func(p *bob.P) *npm.App {\n\treturn npm.App(p, npm.AppOpts{Entry: \"src/main.ts\", Deps: %s})\n})\n", goDeps(im.deps))
		case "vite":
			w(im.dir+"/index.html", "<!doctype html><script type=\"module\" src=\"/src/main.ts\"></script>\n")
			w(im.dir+"/src/main.ts", "export {};\n")
			body = fmt.Sprintf("var Importer = bob.Define(func(p *bob.P) *npm.Site {\n\treturn npm.Vite(p, npm.ViteOpts{Deps: %s})\n})\n", goDeps(im.deps))
		case "lib":
			w(im.dir+"/src/index.ts", "export const x = 1;\n")
			name := "@graph/" + filepath.Base(im.dir)
			body = fmt.Sprintf("var Importer = bob.Define(func(p *bob.P) *npm.Lib {\n\treturn npm.Library(p, npm.LibOpts{Name: %q, Deps: %s, Peers: %s})\n})\n", name, goDeps(im.deps), goDeps(im.peers))
		}
		if im.dir == "." {
			rootDecl += "\n" + body
			continue
		}
		w(im.dir+"/bob.go", "//go:build bob\n\npackage "+pkg+"\n\nimport (\n\t\"github.com/megakuul/bob/pkg/bob\"\n\t\"github.com/megakuul/bob/pkg/npm\"\n)\n\n"+body)
	}
	w("bob.go", "//go:build bob\n\npackage root\n\nimport (\n\t\"github.com/megakuul/bob/pkg/bob\"\n\t\"github.com/megakuul/bob/pkg/npm\"\n)\n\n"+rootDecl)
	return dir
}

// pkgJSON writes the importer's package.json for the npm oracle.
func oraclePackageJSON(g graph, im importer) map[string]any {
	deps, dev, peers := map[string]string{}, map[string]string{}, map[string]string{}
	for _, x := range im.deps {
		spec := x.req
		name := x.name
		if x.alias != "" {
			name, spec = x.alias, "npm:"+x.name+"@"+x.req
		}
		if x.dev {
			dev[name] = spec
		} else {
			deps[name] = spec
		}
	}
	for _, x := range im.peers {
		peers[x.name] = x.req
	}
	m := map[string]any{"name": "@graph/" + strings.ReplaceAll(filepath.Base(im.dir), ".", "root"), "version": "0.0.0", "private": true}
	if len(deps) > 0 {
		m["dependencies"] = deps
	}
	if len(dev) > 0 {
		m["devDependencies"] = dev
	}
	if len(peers) > 0 {
		m["peerDependencies"] = peers
	}
	return m
}
