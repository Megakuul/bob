package npm

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/megakuul/bob/tests/eco/internal/ecotest"
	"github.com/megakuul/bob/tests/parity"
)

// siteImporter is the Vite+React importer of reactRepo (label //web:web), the fixture the SPEC
// names. react is the symlinked direct dependency and scheduler (a dependency of react-dom,
// not declared by the site) is the undeclared package.
const siteImporter = "web"

// R-NPM-003a: node_modules/<direct dep> is a symlink into the store, an undeclared package
// cannot be imported (ERR_MODULE_NOT_FOUND), adding one dependency rebuilds at most 4
// derivations (S4 quiet: 4), and npm.Hoisted() builds one hoisted derivation for the importer
// whose app output is identical to the isolated build's (same tree digest).
func TestRNPM003aIsolatedLayoutAndHoisted(t *testing.T) {
	parity.Covers(t, "R-NPM-003a")
	home := t.TempDir()
	dir := reactRepo(t)
	env := ecotest.Registry(t, home)
	if r := ecotest.BobEnv(t, dir, env, "lock"); !ecotest.OK(t, r) {
		return
	}
	if r := ecotest.BobEnv(t, dir, env, "gen"); !ecotest.OK(t, r) {
		return
	}
	if r := ecotest.BobEnv(t, dir, env, "build", "//web:web", "--json"); !ecotest.OK(t, r) {
		return
	}
	nm := filepath.Join(dir, siteImporter, "node_modules")
	if fi := lstat(t, nm); fi != nil && !fi.IsDir() {
		t.Errorf("node_modules is %v, want a real directory of symlinks (R-NPM-002)", fi.Mode())
	}
	link := filepath.Join(nm, "react")
	target, err := os.Readlink(link)
	if err != nil {
		t.Errorf("node_modules/react is not a symlink: %v", err)
	} else if !inStore(target, home) {
		t.Errorf("node_modules/react -> %s, want a path in the store", target)
	}
	if _, err := os.Stat(filepath.Join(dir, siteImporter, ".bob", "node_modules.path")); err != nil {
		t.Errorf(".bob/node_modules.path of the importer is missing: %v", err)
	}

	site := filepath.Join(dir, siteImporter)
	if r := runNode(t, site, "--input-type=module", "-e", "await import('react'); console.log('declared ok')"); r.Exit != 0 || !strings.Contains(r.Stdout, "declared ok") {
		t.Errorf("importing the declared react failed (exit %d):\n%s%s", r.Exit, r.Stdout, r.Stderr)
	}
	r := runNode(t, site, "--input-type=module", "-e", "await import('scheduler')")
	if r.Exit == 0 || !strings.Contains(r.Stderr, "ERR_MODULE_NOT_FOUND") {
		t.Errorf("importing the undeclared scheduler must fail with ERR_MODULE_NOT_FOUND (exit %d):\n%s%s", r.Exit, r.Stdout, r.Stderr)
	}

	// One more dependency: lock again and rebuild; at most 4 derivations are built.
	ecotest.ReplaceIn(t, filepath.Join(site, "bob.go"), `npm.Dep("react", "^19"),`, `npm.Dep("react", "^19"), npm.Dep("left-pad", "^1.3.0"),`)
	if r := ecotest.BobEnv(t, dir, env, "lock"); !ecotest.OK(t, r) {
		return
	}
	r = ecotest.BobEnv(t, dir, env, "build", "//web:web", "--json")
	if !ecotest.OK(t, r) {
		return
	}
	isolated := ecotest.FinishPaths(t, r)["//web:web"]
	if n := ecotest.BuiltCount(t, r); n > 4 {
		t.Errorf("adding one dependency rebuilt %d derivations (%v), want <= 4", n, ecotest.BuiltLabels(t, r))
	}

	// Hoisted: one hoisted derivation for the importer, same output.
	ecotest.ReplaceIn(t, filepath.Join(site, "bob.go"), "\t}})\n})", "\t}, Options: []npm.Option{npm.Hoisted()}})\n})")
	r = ecotest.BobEnv(t, dir, env, "build", "//web:web", "--json")
	if !ecotest.OK(t, r) {
		return
	}
	hoisted := hoistedDrvs(t, home)
	if len(hoisted) != 1 {
		t.Errorf("hoisted derivations in the store: %v, want exactly one (node_modules-web-hoisted)", hoisted)
	}
	paths := ecotest.FinishPaths(t, r)["//web:web"]
	if len(paths) == 0 {
		t.Fatalf("no output path for //web:web in the package.finish events")
	}
	out := filepath.Join(home, "nix", "store", filepath.Base(paths[0]))
	if _, err := os.Stat(filepath.Join(out, "index.html")); err != nil {
		t.Errorf("hoisted build of the site has no index.html: %v", err)
	}
	if len(isolated) == 0 {
		t.Fatalf("no output path of the isolated build of //web:web")
	}
	if ecotest.TreeDigest(t, out) != ecotest.TreeDigest(t, filepath.Join(home, "nix", "store", filepath.Base(isolated[0]))) {
		t.Errorf("the hoisted build output of //web:web differs from the isolated build output: the same app output is required")
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
		if strings.HasSuffix(e.Name(), ".drv") && strings.Contains(e.Name(), "-node_modules-web-hoisted") {
			out = append(out, e.Name())
		}
	}
	return out
}
