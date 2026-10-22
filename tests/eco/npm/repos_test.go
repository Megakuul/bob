package npm

import (
	"path/filepath"
	"testing"

	"github.com/megakuul/bob/tests/eco/internal/ecotest"
)

// newRepo writes a repository of the given files (repo-relative path to content) plus go.mod,
// an empty bob.lock and the root bob.go. The SPEC's fastify, Vite+React and SvelteKit fixtures
// are not among tests/fixtures/repos/npm (ts-lib, site, native), so the tests spell them here.
func newRepo(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "root") // R2: root package = directory name
	files["go.mod"] = "module example.com/npmextra\n\ngo 1.26\n"
	files["bob.lock"] = "{\n  \"lockVersion\": 1\n}\n"
	files["bob.go"] = "//go:build bob\n\npackage root\n\nimport \"github.com/megakuul/bob/pkg/bob\"\n\nvar _ = bob.Repo(bob.RepoOpts{Nixpkgs: \"nixos-26.05\"})\n"
	for rel, body := range files {
		ecotest.WriteFile(t, filepath.Join(dir, filepath.FromSlash(rel)), body)
	}
	return dir
}

func header(pkg string) string {
	return "//go:build bob\n\npackage " + pkg + "\n\nimport (\n\t\"github.com/megakuul/bob/pkg/bob\"\n\t\"github.com/megakuul/bob/pkg/npm\"\n)\n\n"
}

// fastifyRepo is a fastify App bundled by esbuild (label //api:api); it answers one request to
// itself and exits 0, printing "fastify ok".
func fastifyRepo(t *testing.T) string {
	return newRepo(t, map[string]string{
		"api/bob.go": header("api") + `var Api = bob.Define(func(p *bob.P) *npm.App {
	return npm.App(p, npm.AppOpts{
		Entry: "src/server.ts",
		Deps: []npm.Dependency{
			npm.Dep("fastify", "^5"),
			npm.Dep("@types/node", "^24", npm.Dev()),
			npm.Dep("typescript", "~5.9.2", npm.Dev()),
		},
	})
})
`,
		"api/src/server.ts": `import Fastify from "fastify";
const app = Fastify();
app.get("/", async () => ({ ok: true }));
const res = await app.inject({ method: "GET", url: "/" });
console.log(res.statusCode === 200 ? "fastify ok" : "fastify failed");
await app.close();
`,
	})
}

// reactRepo is a Vite + React site (label //web:web).
func reactRepo(t *testing.T) string {
	return newRepo(t, map[string]string{
		"web/bob.go": header("web") + `var Web = bob.Define(func(p *bob.P) *npm.Site {
	return npm.Vite(p, npm.ViteOpts{Deps: []npm.Dependency{
		npm.Dep("react", "^19"), npm.Dep("react-dom", "^19"),
		npm.Dep("vite", "^7.1.0", npm.Dev()), npm.Dep("@vitejs/plugin-react", "^5", npm.Dev()),
		npm.Dep("@types/react", "^19", npm.Dev()), npm.Dep("@types/react-dom", "^19", npm.Dev()),
	}})
})
`,
		"web/index.html":     "<!doctype html><div id=\"root\"></div><script type=\"module\" src=\"/src/main.tsx\"></script>\n",
		"web/vite.config.ts": "import { defineConfig } from \"vite\";\nimport react from \"@vitejs/plugin-react\";\n\nexport default defineConfig({ plugins: [react()] });\n",
		"web/src/main.tsx":   "import { createRoot } from \"react-dom/client\";\n\ncreateRoot(document.getElementById(\"root\")!).render(<h1>hello react</h1>);\n",
	})
}

// kitRepo is a SvelteKit site with adapter-static (label //kit:kit); the build must write
// .svelte-kit only inside the build directory.
func kitRepo(t *testing.T) string {
	return newRepo(t, map[string]string{
		"kit/bob.go": header("kit") + `var Kit = bob.Define(func(p *bob.P) *npm.Site {
	return npm.Vite(p, npm.ViteOpts{Kit: true, Deps: []npm.Dependency{
		npm.Dep("svelte", "^5"),
		npm.Dep("@sveltejs/kit", "^2", npm.Dev()), npm.Dep("@sveltejs/adapter-static", "^3", npm.Dev()),
		npm.Dep("@sveltejs/vite-plugin-svelte", "^6", npm.Dev()), npm.Dep("vite", "^7.1.0", npm.Dev()),
	}})
})
`,
		"kit/vite.config.js":          "import { sveltekit } from \"@sveltejs/kit/vite\";\nimport { defineConfig } from \"vite\";\n\nexport default defineConfig({ plugins: [sveltekit()] });\n",
		"kit/svelte.config.js":        "import adapter from \"@sveltejs/adapter-static\";\n\nexport default { kit: { adapter: adapter() } };\n",
		"kit/src/app.html":            "<!doctype html><html><head>%sveltekit.head%</head><body>%sveltekit.body%</body></html>\n",
		"kit/src/routes/+layout.js":   "export const prerender = true;\n",
		"kit/src/routes/+page.svelte": "<h1>hello kit</h1>\n",
	})
}
