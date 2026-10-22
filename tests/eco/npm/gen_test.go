package npm

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/megakuul/bob/tests/eco/internal/ecotest"
	"github.com/megakuul/bob/tests/golden"
	"github.com/megakuul/bob/tests/harness"
	"github.com/megakuul/bob/tests/parity"
)

// importers of the npm fixture and whether each is a library (package.json then has exports).
var fixtureImporters = []struct {
	dir string
	lib bool
}{{"ts-lib", true}, {"site", false}, {"native", false}}

// topKeys returns the top-level keys of a JSON object in file order.
func topKeys(t *testing.T, raw string) []string {
	t.Helper()
	dec := json.NewDecoder(strings.NewReader(raw))
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		t.Fatalf("not a JSON object: %v", err)
	}
	var keys []string
	for dec.More() {
		k, err := dec.Token()
		if err != nil {
			t.Fatalf("bad JSON: %v", err)
		}
		keys = append(keys, k.(string))
		var skip json.RawMessage
		if err := dec.Decode(&skip); err != nil {
			t.Fatalf("bad JSON: %v", err)
		}
	}
	return keys
}

// inOrder reports whether want occurs in keys as a subsequence.
func inOrder(keys, want []string) bool {
	i := 0
	for _, k := range keys {
		if i < len(want) && k == want[i] {
			i++
		}
	}
	return i == len(want)
}

// R-NPM-008a (goldens): `bob gen` writes package.json and tsconfig.json for each fixture
// importer; both are golden, package.json has the SPEC key order, version "0.0.0", 2-space
// indent and a trailing newline.
func TestRNPM008aGeneratedFilesGolden(t *testing.T) {
	parity.Covers(t, "R-NPM-008a")
	home := t.TempDir()
	dir, env, ok := lockedFixture(t, "npm", home)
	if !ok {
		return
	}
	if r := ecotest.BobEnv(t, dir, env, "gen"); !ecotest.OK(t, r) {
		return
	}
	for _, im := range fixtureImporters {
		for _, f := range []string{"package.json", "tsconfig.json"} {
			got := ecotest.ReadFile(t, filepath.Join(dir, im.dir, f))
			golden.Assert(t, "tests/goldens/native/npm/"+im.dir+"/"+f, []byte(got), golden.Reqs("R-NPM-008a"))
		}
		raw := ecotest.ReadFile(t, filepath.Join(dir, im.dir, "package.json"))
		want := []string{"//", "name", "version", "private", "type"}
		if im.lib {
			want = append(want, "exports", "types")
		}
		if !inOrder(topKeys(t, raw), want) {
			t.Errorf("%s/package.json keys %v, want order %v first", im.dir, topKeys(t, raw), want)
		}
		var m map[string]any
		_ = json.Unmarshal([]byte(raw), &m)
		if m["version"] != "0.0.0" || m["private"] != true || m["type"] != "module" {
			t.Errorf("%s/package.json: version/private/type = %v/%v/%v", im.dir, m["version"], m["private"], m["type"])
		}
		var indented bytes.Buffer
		if err := json.Indent(&indented, []byte(raw), "", "  "); err != nil || !strings.HasSuffix(raw, "\n") || strings.TrimSuffix(raw, "\n") != indented.String() {
			t.Errorf("%s/package.json is not 2-space indented with a trailing newline", im.dir)
		}
	}
}

// R-NPM-008a (tsc): `tsc --noEmit -p .` through the shim passes in the library importer, and
// editing "strict": false into tsconfig.json is rejected as drift (R-FW-061, BOB-E-1011).
func TestRNPM008aTscShimAndDrift(t *testing.T) {
	parity.Covers(t, "R-NPM-008a")
	home := t.TempDir()
	dir, env, ok := lockedFixture(t, "npm", home)
	if !ok {
		return
	}
	if r := ecotest.BobEnv(t, dir, env, "gen"); !ecotest.OK(t, r) {
		return
	}
	if r := ecotest.BobEnv(t, dir, env, "env", "--format", "json"); !ecotest.OK(t, r) {
		return
	}
	lib := filepath.Join(dir, "ts-lib")
	shim := filepath.Join(dir, ".bob", "bin", "tsc")
	r := harness.RunPath(t, shim, harness.Spec{Dir: lib, Env: []string{"BOB_HOME=" + home}}, "--noEmit", "-p", ".")
	if r.Exit != 0 {
		t.Errorf("tsc --noEmit -p . through the shim exited %d:\n%s%s", r.Exit, r.Stdout, r.Stderr)
	}

	tsconfig := filepath.Join(lib, "tsconfig.json")
	generated := ecotest.ReadFile(t, tsconfig)
	if !strings.Contains(generated, `"strict": true`) {
		t.Fatalf("harness: generated tsconfig.json has no \"strict\": true line:\n%s", generated)
	}
	ecotest.WriteFile(t, tsconfig, strings.Replace(generated, `"strict": true`, `"strict": false`, 1))
	b := ecotest.BobEnv(t, dir, env, "build", "//...", "--json")
	if !ecotest.ExitIs(t, b, 3) {
		return
	}
	found := false
	for _, c := range ecotest.ErrorCodes(t, b) {
		found = found || c == "BOB-E-1011"
	}
	if !found || !strings.Contains(b.Combined(), "tsconfig.json") {
		t.Errorf("want BOB-E-1011 naming tsconfig.json; codes %v\n%s", ecotest.ErrorCodes(t, b), b.Stdout)
	}
	if got := ecotest.ReadFile(t, tsconfig); !strings.Contains(got, `"strict": false`) {
		t.Errorf("bob build overwrote the drifted tsconfig.json")
	}
	if r := ecotest.BobEnv(t, dir, env, "gen", "--force"); ecotest.ExitIs(t, r, 0) {
		if got := ecotest.ReadFile(t, tsconfig); got != generated {
			t.Errorf("bob gen --force did not restore tsconfig.json")
		}
	}
}
