package ocaml

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/megakuul/bob/tests/eco/internal/ecotest"
	"github.com/megakuul/bob/tests/parity"
)

const maxClosureBytes = 80 << 20 // R-ML-001a (S5 measured 55 MB)

// demoRoots are the roots of the S5 demo graph (11 opam packages with their dependencies, minus
// the toolchain packages bob provides).
var demoRoots = []string{"cmdliner", "fmt", "yojson", "lwt"}

// checkDemo runs bin/demo under env -i and checks its output (n defaults to 3: (3+1, 2+1)).
func checkDemo(t *testing.T, out, mode string) {
	t.Helper()
	r := ecotest.RunEnvI(t, filepath.Join(out, "bin", "demo"))
	if r.Exit != 0 || !strings.Contains(r.Stdout, `"x":4,"y":3`) || !strings.Contains(r.Stdout, `"hash":`) || !strings.Contains(r.Stdout, "vec ") {
		t.Errorf("%s: bin/demo under env -i exited %d and printed %q\n%s, want exit 0, the JSON line with x=4 y=3 and a hash, and the Fmt line", mode, r.Exit, r.Stdout, r.Stderr)
	}
}

// R-ML-001a: on the ocaml fixture the executable runs under env -i, its closure is <= 80 MB,
// each opam package is its own derivation, the geom test passes, a library change rebuilds
// only the library and its dependents, and Dune: true gives a working executable too.
func TestRML001aFixtureBuildsFineGrained(t *testing.T) {
	parity.Covers(t, "R-ML-001a")
	home := t.TempDir()
	dir, env, ok := lockedFixture(t, home)
	if !ok {
		return
	}
	paths, ok := ecotest.OutputPaths(t, dir, env, "//app:demo")
	if !ok || len(paths["//app:demo"]) == 0 {
		return
	}
	out := paths["//app:demo"][0]
	checkDemo(t, out, "direct")

	names, size := ecotest.Closure(t, home, out)
	if size > maxClosureBytes {
		t.Errorf("runtime closure is %d bytes, want <= %d (%d store paths)", size, maxClosureBytes, len(names))
	}
	for _, n := range names {
		for _, banned := range []string{"-ocaml-", "-gcc-wrapper-", "-binutils-"} {
			if strings.Contains(n, banned) {
				t.Errorf("runtime closure contains the build-only %s (scrubbing, SPEC §03.5.3)", n)
			}
		}
	}

	// Every locked opam package has its own derivation opam-<name>-<version>.
	lock := readOpam(t, dir)
	if len(lock.Packages) < 11 {
		t.Errorf("bob.lock holds %d opam packages, the S5 demo graph has 11", len(lock.Packages))
	}
	for _, root := range demoRoots {
		if _, ok := lock.Packages[root]; !ok {
			t.Errorf("bob.lock has no opam package %s", root)
		}
	}
	drvs := storeDrvs(t, home)
	for name, p := range lock.Packages {
		want := "-opam-" + name + "-" + p.Version + ".drv"
		found := false
		for _, d := range drvs {
			found = found || strings.HasSuffix(d, want)
		}
		if !found {
			t.Errorf("no derivation %s for opam package %s.%s", want, name, p.Version)
		}
	}

	if tr := ecotest.BobEnv(t, dir, env, "test", "//test:geom_test", "--json"); ecotest.OK(t, tr) {
		passed := false
		for _, e := range ecotest.Named(ecotest.Events(t, tr), "test.result") {
			passed = passed || (e.Str("label") == "//test:geom_test" && (e.Str("status") == "pass" || e.Str("status") == "cached"))
		}
		if !passed {
			t.Errorf("//test:geom_test did not pass:\n%s", tr.Stdout)
		}
	}

	// A change in the library rebuilds the library and the executable, nothing else.
	vec := filepath.Join(dir, "geom", "vec.ml")
	ecotest.WriteFile(t, vec, ecotest.ReadFile(t, vec)+"\n(* edited by the granularity test *)\n")
	r := ecotest.BobEnv(t, dir, env, "build", "//app:demo", "--json")
	if ecotest.OK(t, r) {
		got := ecotest.BuiltLabels(t, r)
		if strings.Join(got, ",") != "//app:demo,//geom:geom" || ecotest.BuiltCount(t, r) != 2 {
			t.Errorf("a library change rebuilt %v (%d derivations), want exactly //geom:geom and //app:demo", got, ecotest.BuiltCount(t, r))
		}
	}

	// Dune mode.
	ecotest.ReplaceIn(t, filepath.Join(dir, "app", "bob.go"), `Name: "demo",`, `Name: "demo", Dune: true,`)
	dune, ok := ecotest.OutputPaths(t, dir, env, "//app:demo")
	if ok && len(dune["//app:demo"]) > 0 {
		checkDemo(t, dune["//app:demo"][0], "Dune: true")
	}
}
