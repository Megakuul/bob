package npm

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/megakuul/bob/pkg/bob/diag"
	"github.com/megakuul/bob/pkg/npm"
	"github.com/megakuul/bob/tests/eco/internal/ecotest"
	"github.com/megakuul/bob/tests/harness"
	"github.com/megakuul/bob/tests/parity"
)

func notImplemented(err error) bool {
	var de *diag.Error
	return errors.As(err, &de) && de.Code == diag.CodeNotImplemented
}

// R-NPM-005a (table): every shipped entry has Package, Why and Test, and the v1 table has the
// two verified entries (esbuild >=0.15 skipping its postinstall, better-sqlite3 >=8 building
// from source).
func TestRNPM005aQuirkTableIsComplete(t *testing.T) {
	parity.Covers(t, "R-NPM-005a")
	table, err := npm.Quirks()
	if notImplemented(err) {
		harness.RedFeature(t, "npm.Quirks")
		return
	}
	if err != nil {
		t.Fatalf("npm.Quirks: %v", err)
	}
	seen := map[string]npm.Quirk{}
	for _, q := range table {
		seen[q.Package] = q
		if q.Package == "" || strings.TrimSpace(q.Why) == "" || strings.TrimSpace(q.Test) == "" || q.Range == "" {
			t.Errorf("quirk %+v lacks Package, Range, Why or Test", q)
		}
	}
	if q, ok := seen["esbuild"]; !ok || q.Range != ">=0.15" || !q.Skip {
		t.Errorf("v1 entry esbuild missing or wrong: %+v", q)
	}
	if q, ok := seen["better-sqlite3"]; !ok || q.Range != ">=8" || q.Env["npm_config_build_from_source"] != "true" {
		t.Errorf("v1 entry better-sqlite3 missing or wrong: %+v", q)
	}
}

// R-NPM-005a (CI check): an entry without Why or Test fails with E-QUIRK-TEST (BOB-E-9006) that
// names the entry; a complete table passes.
func TestRNPM005aIncompleteEntryFailsCI(t *testing.T) {
	parity.Covers(t, "R-NPM-005a")
	good := npm.Quirk{Package: "x", Range: ">=1", Why: "postinstall only re-checks a binary", Test: "node -e 0"}
	if err := npm.CheckQuirks([]npm.Quirk{good}); notImplemented(err) {
		harness.RedFeature(t, "npm.CheckQuirks")
		return
	} else if err != nil {
		t.Errorf("a complete table was rejected: %v", err)
	}
	noWhy, noTest := good, good
	noWhy.Package, noWhy.Why = "no-why", ""
	noTest.Package, noTest.Test = "no-test", ""
	for _, q := range []npm.Quirk{noWhy, noTest} {
		err := npm.CheckQuirks([]npm.Quirk{good, q})
		if err == nil {
			t.Errorf("entry %s without Why/Test was accepted", q.Package)
			continue
		}
		if !strings.Contains(err.Error(), "BOB-E-9006") || !strings.Contains(err.Error(), q.Package) {
			t.Errorf("error %q must carry BOB-E-9006 and name %s", err, q.Package)
		}
	}
}

// R-NPM-005a (fixture): the Test of each entry passes in a fixture that depends on the package.
// Every entry of the table must be exercised by the native fixture (a package it declares);
// the fixture is built (with better-sqlite3's script allowed and esbuild's postinstall
// skipped), then the entry's Test is run as the SPEC defines it: "command run in the package
// dir after the build", i.e. in node_modules/<package> of the built app, in bob's tool
// environment. It must exit 0. A deliberately failing Test (a copy of the table entry whose
// command is `false`) is not injectable without the implementation; the tests of
// R-NPM-005a (CI check) cover the rejection side.
func TestRNPM005aQuirkTestsPassInFixture(t *testing.T) {
	parity.Covers(t, "R-NPM-005a")
	table, err := npm.Quirks()
	if notImplemented(err) {
		harness.RedFeature(t, "npm.Quirks")
		return
	}
	if err != nil {
		t.Fatalf("npm.Quirks: %v", err)
	}
	home := t.TempDir()
	dir, env, ok := lockedFixture(t, "npm", home)
	if !ok {
		return
	}
	declared := ecotest.ReadFile(t, filepath.Join(dir, "native", "bob.go"))
	b := ecotest.BobEnv(t, dir, env, "build", "//native:native", "--print-paths", "--json")
	if !ecotest.OK(t, b) {
		return
	}
	paths := ecotest.FinishPaths(t, b)["//native:native"]
	if len(paths) == 0 {
		t.Fatalf("no output path for //native:native")
	}
	app := paths[0]
	for _, q := range table {
		if !strings.Contains(declared, `"`+q.Package+`"`) {
			t.Errorf("quirk entry %s has no fixture that depends on it (native fixture declares %q only)", q.Package, "better-sqlite3, esbuild")
			continue
		}
		pkgDir := filepath.Join(app, "node_modules", q.Package)
		script := "cd " + shellQuote(pkgDir) + " && " + q.Test
		r := ecotest.BobEnv(t, filepath.Join(dir, "native"), env, "exec", "--", "sh", "-c", script)
		if r.Exit != 0 {
			t.Errorf("Test of quirk %s exited %d in %s:\n%s%s", q.Package, r.Exit, pkgDir, r.Stdout, r.Stderr)
		}
	}
}

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }
