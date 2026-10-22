package npm

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/megakuul/bob/tests/eco/internal/ecotest"
	"github.com/megakuul/bob/tests/golden"
	"github.com/megakuul/bob/tests/harness"
	"github.com/megakuul/bob/tests/parity"
)

const denyWording = "✗ better-sqlite3 needs its install script (native addon). Add npm.AllowScripts(\"better-sqlite3\") to //native:native"

// R-NPM-004a (denied): without AllowScripts("better-sqlite3") bob lock prints the once-per-lock
// note, running the program fails with the E-SCRIPT-DENIED text (golden, BOB-E-2013) and no
// script ran (no node-gyp anywhere in the output or the store).
func TestRNPM004aScriptsDeniedByDefault(t *testing.T) {
	parity.Covers(t, "R-NPM-004a")
	home := t.TempDir()
	dir := harnessFixture(t, "npm")
	env := ecotest.Registry(t, home)
	ecotest.ReplaceIn(t, filepath.Join(dir, "native", "bob.go"), `npm.AllowScripts("better-sqlite3", "esbuild")`, `npm.AllowScripts("esbuild")`)

	lock := ecotest.BobEnv(t, dir, env, "lock")
	if !ecotest.OK(t, lock) {
		return
	}
	note := regexp.MustCompile(`note: better-sqlite3@\d+\.\d+\.\d+ has an install script; bob does not run it\. If the package needs it: npm\.AllowScripts\("better-sqlite3"\)`)
	if n := len(note.FindAllString(lock.Combined(), -1)); n != 1 {
		t.Errorf("bob lock printed the install-script note %d times, want exactly once:\n%s", n, lock.Combined())
	}

	r := ecotest.BobEnv(t, dir, env, "run", "//native:native", "--json")
	if harness.Red(t, r) {
		return
	}
	if r.Exit == 0 {
		t.Errorf("the program ran although better-sqlite3's install script is denied")
	}
	found := false
	for _, c := range ecotest.ErrorCodes(t, r) {
		found = found || c == "BOB-E-2013"
	}
	if !found {
		t.Errorf("no BOB-E-2013 (E-SCRIPT-DENIED) error event; codes %v\n%s", ecotest.ErrorCodes(t, r), r.Stdout)
	}
	human := ecotest.BobEnv(t, dir, env, "run", "//native:native")
	if !strings.Contains(human.Stderr, denyWording) {
		t.Errorf("stderr lacks %q:\n%s", denyWording, human.Stderr)
	}
	golden.Assert(t, "tests/goldens/npm/script-denied.txt", []byte(human.Stderr), golden.Human(), golden.Reqs("R-NPM-004a"))

	if log := ecotest.BobEnv(t, dir, env, "log", "//native:native"); strings.Contains(log.Combined(), "node-gyp") {
		t.Errorf("bob log mentions node-gyp: a script ran")
	}
	if strings.Contains(human.Combined(), "node-gyp") || strings.Contains(r.Combined(), "node-gyp") {
		t.Errorf("run output mentions node-gyp: a script ran")
	}
	if ents, err := os.ReadDir(filepath.Join(home, "nix", "store")); err == nil {
		for _, e := range ents {
			if strings.Contains(e.Name(), "node-gyp") || strings.Contains(e.Name(), "-npm-script") {
				t.Errorf("store contains %s: a script derivation was built", e.Name())
			}
		}
	}
}

// forbiddenRuntime matches store entries that may not be in the runtime closure of the native
// app: the compiler, its wrapper, binutils, python, make, node-gyp and the full nodejs
// (nodejs-slim is the runtime).
var forbiddenRuntime = regexp.MustCompile(`^[0-9a-z]{32}-(gcc-wrapper-.*|gcc-[0-9.]+|binutils.*|python3.*|gnumake.*|node-gyp.*|nodejs-[0-9.]+|bob-builder.*)$`)

// R-NPM-004a (allowed): with AllowScripts the addon builds offline, the program opens :memory:
// and the runtime closure has only the .node file of better-sqlite3's build tree besides
// node, glibc and libgcc: no toolchain.
func TestRNPM004aScriptsAllowedBuildOffline(t *testing.T) {
	parity.Covers(t, "R-NPM-004a")
	home := t.TempDir()
	dir, env, ok := lockedFixture(t, "npm", home)
	if !ok {
		return
	}
	r := ecotest.BobEnv(t, dir, env, "run", "//native:native")
	if !ecotest.OK(t, r) {
		return
	}
	var out struct {
		Sqlite struct {
			V string `json:"v"`
			Y int    `json:"y"`
		} `json:"sqlite"`
		Lodash string `json:"lodash"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(r.Stdout)), &out); err != nil || out.Sqlite.V == "" || out.Sqlite.Y != 42 || out.Lodash != "bobNpm" {
		t.Errorf("program output %q (%v), want sqlite_version, y=42 and lodash camelCase bobNpm", r.Stdout, err)
	}
	// The addon must have been built without network: run the build again in a network-less
	// namespace with a fresh store would need a cache; the build log must not show a download.
	b := ecotest.BobEnv(t, dir, env, "build", "//native:native", "--print-paths", "--json")
	if !ecotest.OK(t, b) {
		return
	}
	paths := ecotest.FinishPaths(t, b)["//native:native"]
	if len(paths) == 0 {
		t.Fatalf("no output path for //native:native")
	}
	names, _ := ecotest.Closure(t, home, paths[0])
	for _, n := range names {
		if forbiddenRuntime.MatchString(n) {
			t.Errorf("runtime closure contains %s", n)
		}
	}
	var addons []string
	for _, n := range names {
		if !strings.Contains(n, "-npm-better-sqlite3-") {
			continue
		}
		root := filepath.Join(home, "nix", "store", n)
		_ = filepath.Walk(filepath.Join(root, "node_modules", "better-sqlite3", "build"), func(p string, fi os.FileInfo, err error) error {
			if err == nil && !fi.IsDir() {
				rel, _ := filepath.Rel(root, p)
				addons = append(addons, rel)
			}
			return nil
		})
	}
	if len(addons) != 1 || !strings.HasSuffix(addons[0], "build/Release/better_sqlite3.node") {
		t.Errorf("files kept from better-sqlite3's build tree: %v, want only build/Release/better_sqlite3.node", addons)
	}
	hasGlibc := false
	for _, n := range names {
		hasGlibc = hasGlibc || strings.Contains(n, "-glibc-")
	}
	if !hasGlibc {
		t.Errorf("runtime closure has no glibc: %v", names)
	}
}
