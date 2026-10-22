package npm

import (
	"go/ast"
	goimporter "go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/megakuul/bob/tests/eco/internal/ecotest"
	"github.com/megakuul/bob/tests/harness"
	"github.com/megakuul/bob/tests/parity"
)

// buildTwice builds label in two fresh stores (two workers) and checks the outputs are
// bit-identical: same store path and same tree digest. It returns the outputs of the first.
func buildTwice(t *testing.T, dir string, envA, envB []string, homeA, homeB, label string) []string {
	t.Helper()
	a, ok := ecotest.OutputPaths(t, dir, envA, label)
	if !ok {
		return nil
	}
	b, ok := ecotest.OutputPaths(t, dir, envB, label)
	if !ok {
		return nil
	}
	if len(a[label]) == 0 || len(a[label]) != len(b[label]) {
		t.Errorf("%s: outputs %v on the first store, %v on the second", label, a[label], b[label])
		return nil
	}
	for i := range a[label] {
		if filepath.Base(a[label][i]) != filepath.Base(b[label][i]) {
			t.Errorf("%s: output %s differs from %s: the build is not reproducible", label, a[label][i], b[label][i])
			continue
		}
		pa := filepath.Join(homeA, "nix", "store", filepath.Base(a[label][i]))
		pb := filepath.Join(homeB, "nix", "store", filepath.Base(b[label][i]))
		if ecotest.TreeDigest(t, pa) != ecotest.TreeDigest(t, pb) {
			t.Errorf("%s: output contents differ between two builds", label)
		}
	}
	return a[label]
}

func storePath(home, out string) string {
	return filepath.Join(home, "nix", "store", filepath.Base(out))
}

func exists(t *testing.T, p, what string) {
	t.Helper()
	if _, err := os.Stat(p); err != nil {
		t.Errorf("%s: %v", what, err)
	}
}

// R-NPM-007a (fixture): the tsc library has dist and .d.ts, vitest passes, the Vite site with a
// workspace library builds, and every output rebuilds bit-identically on a second store.
func TestRNPM007aFixtureBuilds(t *testing.T) {
	parity.Covers(t, "R-NPM-007a")
	homeA, homeB := t.TempDir(), t.TempDir()
	dir, envA, ok := lockedFixture(t, "npm", homeA)
	if !ok {
		return
	}
	envB := ecotest.Registry(t, homeB)
	lib := buildTwice(t, dir, envA, envB, homeA, homeB, "//ts-lib:ts-lib")
	if len(lib) > 0 {
		base := filepath.Join(storePath(homeA, lib[0]), "node_modules", "@fix", "ts-lib")
		exists(t, filepath.Join(base, "dist", "index.js"), "library dist")
		exists(t, filepath.Join(base, "dist", "index.d.ts"), "library declarations")
	}
	site := buildTwice(t, dir, envA, envB, homeA, homeB, "//site:site")
	if len(site) > 0 {
		exists(t, filepath.Join(storePath(homeA, site[0]), "index.html"), "site index.html")
	}
	tr := ecotest.BobEnv(t, dir, envA, "test", "//ts-lib:test", "--json")
	if ecotest.OK(t, tr) {
		passed := false
		for _, e := range ecotest.Named(ecotest.Events(t, tr), "test.result") {
			passed = passed || (e.Str("label") == "//ts-lib:test" && (e.Str("status") == "pass" || e.Str("status") == "cached"))
		}
		if !passed {
			t.Errorf("//ts-lib:test (vitest) did not pass:\n%s", tr.Stdout)
		}
	}
}

// R-NPM-007a (fastify): the esbuild-bundled App runs under env -i and rebuilds bit-identically.
func TestRNPM007aFastifyBundle(t *testing.T) {
	parity.Covers(t, "R-NPM-007a")
	homeA, homeB := t.TempDir(), t.TempDir()
	dir := fastifyRepo(t)
	envA := ecotest.Registry(t, homeA)
	if r := ecotest.BobEnv(t, dir, envA, "lock"); !ecotest.OK(t, r) {
		return
	}
	out := buildTwice(t, dir, envA, ecotest.Registry(t, homeB), homeA, homeB, "//api:api")
	if len(out) == 0 {
		return
	}
	r := ecotest.RunEnvI(t, filepath.Join(out[0], "bin", "api"))
	if r.Exit != 0 || strings.TrimSpace(r.Stdout) != "fastify ok" {
		t.Errorf("bin/api under env -i exited %d and printed %q, want exit 0 and \"fastify ok\"\n%s", r.Exit, r.Stdout, r.Stderr)
	}
}

// R-NPM-007a (Vite+React and SvelteKit): both build hermetically and reproducibly; SvelteKit
// (adapter-static) writes .svelte-kit only inside the build directory, never in the tree.
func TestRNPM007aReactAndSvelteKit(t *testing.T) {
	parity.Covers(t, "R-NPM-007a")
	for _, c := range []struct {
		name, label string
		repo        func(*testing.T) string
		page        string
	}{
		{"vite-react", "//web:web", reactRepo, "index.html"},
		{"sveltekit", "//kit:kit", kitRepo, "index.html"},
	} {
		t.Run(c.name, func(t *testing.T) {
			homeA, homeB := t.TempDir(), t.TempDir()
			dir := c.repo(t)
			envA := ecotest.Registry(t, homeA)
			if r := ecotest.BobEnv(t, dir, envA, "lock"); !ecotest.OK(t, r) {
				return
			}
			out := buildTwice(t, dir, envA, ecotest.Registry(t, homeB), homeA, homeB, c.label)
			if len(out) > 0 {
				exists(t, filepath.Join(storePath(homeA, out[0]), c.page), c.name+" "+c.page)
			}
			if c.name == "sveltekit" {
				if _, err := os.Stat(filepath.Join(dir, "kit", ".svelte-kit")); err == nil {
					t.Errorf("the build wrote .svelte-kit into the working tree")
				}
			}
		})
	}
}

// R-NPM-009a (Bun): npm.App with Runtime: npm.Bun produces bin/<name> that runs with nixpkgs
// bun (its closure has bun, no node).
func TestRNPM009aBunApp(t *testing.T) {
	parity.Covers(t, "R-NPM-009a")
	home := t.TempDir()
	dir := newRepo(t, map[string]string{
		"hono/bob.go": header("hono") + `var Hono = bob.Define(func(p *bob.P) *npm.App {
	return npm.NewApp(p, npm.AppOpts{Name: "hono", Entry: "src/main.ts", Runtime: npm.Bun,
		Deps: []npm.Dependency{npm.Dep("hono", "^4")}})
})
`,
		"hono/src/main.ts": "import { Hono } from \"hono\";\nconst app = new Hono();\napp.get(\"/\", (c) => c.text(\"ok\"));\nconst res = await app.request(\"/\");\nconsole.log(\"bun \" + (await res.text()) + \" \" + typeof Bun);\n",
	})
	env := ecotest.Registry(t, home)
	if r := ecotest.BobEnv(t, dir, env, "lock"); !ecotest.OK(t, r) {
		return
	}
	paths, ok := ecotest.OutputPaths(t, dir, env, "//hono:hono")
	if !ok || len(paths["//hono:hono"]) == 0 {
		return
	}
	out := paths["//hono:hono"][0]
	r := ecotest.RunEnvI(t, filepath.Join(out, "bin", "hono"))
	if r.Exit != 0 || strings.TrimSpace(r.Stdout) != "bun ok object" {
		t.Errorf("bin/hono under env -i exited %d and printed %q, want \"bun ok object\" (running under bun)\n%s", r.Exit, r.Stdout, r.Stderr)
	}
	names, _ := ecotest.Closure(t, home, out)
	var bun, node bool
	for _, n := range names {
		bun = bun || strings.Contains(n, "-bun-")
		node = node || regexp.MustCompile(`-nodejs(-slim)?-[0-9]`).MatchString(n)
	}
	if !bun || node {
		t.Errorf("Bun app closure: bun=%v node=%v, want bun and no node: %v", bun, node, names)
	}
}

// R-NPM-009a (Node): the default Node App's runtime closure uses nodejs-slim, not full nodejs.
func TestRNPM009aNodeAppUsesNodejsSlim(t *testing.T) {
	parity.Covers(t, "R-NPM-009a")
	home := t.TempDir()
	dir := fastifyRepo(t)
	env := ecotest.Registry(t, home)
	if r := ecotest.BobEnv(t, dir, env, "lock"); !ecotest.OK(t, r) {
		return
	}
	paths, ok := ecotest.OutputPaths(t, dir, env, "//api:api")
	if !ok || len(paths["//api:api"]) == 0 {
		return
	}
	names, _ := ecotest.Closure(t, home, paths["//api:api"][0])
	var slim bool
	for _, n := range names {
		slim = slim || strings.Contains(n, "-nodejs-slim-")
		if regexp.MustCompile(`-nodejs-[0-9]`).MatchString(n) {
			t.Errorf("runtime closure contains the full nodejs: %s", n)
		}
	}
	if !slim {
		t.Errorf("runtime closure has no nodejs-slim: %v", names)
	}
}

// typeCheck type-checks one Go file importing the real bob packages (resolved from source in
// the repository) and returns the errors.
func typeCheck(t *testing.T, src string) []string {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, filepath.Join(harness.Root(t), "probe.go"), src, 0)
	if err != nil {
		t.Fatalf("harness: %v", err)
	}
	var errs []string
	conf := types.Config{
		Importer: goimporter.ForCompiler(fset, "source", nil),
		Error:    func(err error) { errs = append(errs, err.Error()) },
	}
	_, _ = conf.Check("probe", fset, []*ast.File{f}, nil)
	return errs
}

// R-NPM-009a (compile time): Runtime is a field of AppOpts only; it does not exist on LibOpts
// or ViteOpts, so setting it there does not compile.
func TestRNPM009aRuntimeIsNotAFieldOfLibraryOrVite(t *testing.T) {
	parity.Covers(t, "R-NPM-009a")
	const head = "package probe\n\nimport \"github.com/megakuul/bob/pkg/npm\"\n\n"
	if errs := typeCheck(t, head+"var _ = npm.AppOpts{Runtime: npm.Bun}\n"); len(errs) > 0 {
		t.Fatalf("AppOpts{Runtime: npm.Bun} does not compile: %v", errs)
	}
	for _, c := range []struct{ name, src string }{
		{"LibOpts", "var _ = npm.LibOpts{Runtime: npm.Bun}\n"},
		{"ViteOpts", "var _ = npm.ViteOpts{Runtime: npm.Bun}\n"},
	} {
		errs := typeCheck(t, head+c.src)
		if len(errs) == 0 || !strings.Contains(strings.Join(errs, "\n"), "Runtime") {
			t.Errorf("%s{Runtime: npm.Bun} must not compile, errors: %v", c.name, errs)
		}
	}
}
