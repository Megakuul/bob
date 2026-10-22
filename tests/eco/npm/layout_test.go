package npm

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/megakuul/bob/tests/eco/internal/ecotest"
	"github.com/megakuul/bob/tests/parity"
)

// siteImporter probes the Vite fixture's importer. The SPEC names a Vite+React fixture; the npm
// fixture's site is Vite+Svelte, so svelte is the symlinked direct dependency and acorn (a
// dependency of svelte, not declared by the site) is the undeclared package.
const siteImporter = "site"

// R-NPM-003a: node_modules/<direct dep> is a symlink into the store, an undeclared package
// cannot be imported (ERR_MODULE_NOT_FOUND), adding one dependency rebuilds at most 4
// derivations (S4 quiet: 4), and npm.Hoisted() builds one hoisted derivation for the importer
// whose app output is still produced.
func TestRNPM003aIsolatedLayoutAndHoisted(t *testing.T) {
	parity.Covers(t, "R-NPM-003a")
	home := t.TempDir()
	dir, env, ok := lockedFixture(t, "npm", home)
	if !ok {
		return
	}
	if r := ecotest.BobEnv(t, dir, env, "gen"); !ecotest.OK(t, r) {
		return
	}
	if r := ecotest.BobEnv(t, dir, env, "build", "//site:site", "--json"); !ecotest.OK(t, r) {
		return
	}
	nm := filepath.Join(dir, siteImporter, "node_modules")
	if fi := lstat(t, nm); fi != nil && !fi.IsDir() {
		t.Errorf("node_modules is %v, want a real directory of symlinks (R-NPM-002)", fi.Mode())
	}
	link := filepath.Join(nm, "svelte")
	target, err := os.Readlink(link)
	if err != nil {
		t.Errorf("node_modules/svelte is not a symlink: %v", err)
	} else if !inStore(target, home) {
		t.Errorf("node_modules/svelte -> %s, want a path in the store", target)
	}
	if _, err := os.Stat(filepath.Join(dir, siteImporter, ".bob", "node_modules.path")); err != nil {
		t.Errorf(".bob/node_modules.path of the importer is missing: %v", err)
	}

	site := filepath.Join(dir, siteImporter)
	if r := runNode(t, site, "--input-type=module", "-e", "await import('svelte'); console.log('declared ok')"); r.Exit != 0 || !strings.Contains(r.Stdout, "declared ok") {
		t.Errorf("importing the declared svelte failed (exit %d):\n%s%s", r.Exit, r.Stdout, r.Stderr)
	}
	r := runNode(t, site, "--input-type=module", "-e", "await import('acorn')")
	if r.Exit == 0 || !strings.Contains(r.Stderr, "ERR_MODULE_NOT_FOUND") {
		t.Errorf("importing the undeclared acorn must fail with ERR_MODULE_NOT_FOUND (exit %d):\n%s%s", r.Exit, r.Stdout, r.Stderr)
	}

	// One more dependency: lock again and rebuild; at most 4 derivations are built.
	ecotest.ReplaceIn(t, filepath.Join(site, "bob.go"), `npm.Dep("svelte", "^5"),`, `npm.Dep("svelte", "^5"),
			npm.Dep("left-pad", "^1.3.0"),`)
	if r := ecotest.BobEnv(t, dir, env, "lock"); !ecotest.OK(t, r) {
		return
	}
	r = ecotest.BobEnv(t, dir, env, "build", "//site:site", "--json")
	if !ecotest.OK(t, r) {
		return
	}
	if n := ecotest.BuiltCount(t, r); n > 4 {
		t.Errorf("adding one dependency rebuilt %d derivations (%v), want <= 4", n, ecotest.BuiltLabels(t, r))
	}

	// Hoisted: one hoisted derivation for the importer, same output.
	ecotest.ReplaceIn(t, filepath.Join(site, "bob.go"), "\t\t},\n\t})\n})", "\t\t},\n\t\tOptions: []npm.Option{npm.Hoisted()},\n\t})\n})")
	r = ecotest.BobEnv(t, dir, env, "build", "//site:site", "--json")
	if !ecotest.OK(t, r) {
		return
	}
	hoisted := hoistedDrvs(t, home)
	if len(hoisted) != 1 {
		t.Errorf("hoisted derivations in the store: %v, want exactly one (node_modules-site-hoisted)", hoisted)
	}
	paths := ecotest.FinishPaths(t, r)["//site:site"]
	if len(paths) == 0 {
		t.Fatalf("no output path for //site:site in the package.finish events")
	}
	out := filepath.Join(home, "nix", "store", filepath.Base(paths[0]))
	if _, err := os.Stat(filepath.Join(out, "index.html")); err != nil {
		t.Errorf("hoisted build of the site has no index.html: %v", err)
	}
}

// hoistedDrvs lists the hoisted node_modules derivations in the store of home.
func hoistedDrvs(t *testing.T, home string) []string {
	t.Helper()
	ents, err := os.ReadDir(filepath.Join(home, "nix", "store"))
	if err != nil {
		t.Fatalf("bob home has no store: %v", err)
	}
	var out []string
	for _, e := range ents {
		if strings.HasSuffix(e.Name(), ".drv") && strings.Contains(e.Name(), "-node_modules-site-hoisted") {
			out = append(out, e.Name())
		}
	}
	return out
}
