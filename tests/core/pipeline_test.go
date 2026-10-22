package core

// SPEC §1.16 build pipeline and §03.0.4–§03.0.5: R-FW-049, R-FW-009, R-FW-063, R-FW-064.

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/megakuul/bob/pkg/engine"
	"github.com/megakuul/bob/pkg/engine/drv"
	"github.com/megakuul/bob/tests/golden"
	"github.com/megakuul/bob/tests/harness"
	"github.com/megakuul/bob/tests/parity"
)

// buildStages are the stages of one bob build in the order of R-FW-049; each emits one
// --json event named like the stage with a durationMs (decision: SPEC fixes the order and
// "an event with its duration" but not the event names).
var buildStages = []string{"namespace", "scan", "eval", "ir", "lock", "index", "sources", "plan", "fetch", "drv", "nix", "validity", "translate"}

// R-FW-049: the --json event sequence of a cold polyglot build matches the stage list, each
// stage event carries its duration, and the names-and-order golden matches.
func TestRFW049StageEventSequence(t *testing.T) {
	parity.Covers(t, "R-FW-049")
	dir := harness.Fixture(t, "polyglot")
	r := bobRunEnv(t, dir, []string{"BOB_HOME=" + t.TempDir()}, "build", "//...", "--json")
	if harness.Red(t, r) || !exitIs(t, r, 0) {
		return
	}
	evs := events(t, r)
	isStage := map[string]bool{}
	for _, s := range buildStages {
		isStage[s] = true
	}
	var seen []string
	var names []string
	for _, e := range evs {
		names = append(names, e.str("event"))
		if !isStage[e.str("event")] {
			continue
		}
		seen = append(seen, e.str("event"))
		if d, ok := e["durationMs"].(float64); !ok || d < 0 {
			t.Errorf("stage event %s has no durationMs: %v", e.str("event"), e)
		}
	}
	if strings.Join(seen, ",") != strings.Join(buildStages, ",") {
		t.Errorf("stage events %v, want %v", seen, buildStages)
	}
	golden.Assert(t, "tests/goldens/events/polyglot-cold.jsonl", []byte(strings.Join(names, "\n")+"\n"), golden.Reqs("R-FW-049"))
}

// R-FW-009: the framework has no server concept beyond the engine.Substituter and
// engine.RemoteExecutor interfaces; with a fresh home (no login, no registry, no substituter
// beyond cache.nixos.org) every example builds.
func TestRFW009WorksWithoutServer(t *testing.T) {
	parity.Covers(t, "R-FW-009")
	var _ engine.RemoteExecutor
	_ = engine.Substituter{URL: "https://cache.nixos.org", PublicKey: "cache.nixos.org-1:6NCHdD59X431o0gWypbMrAURkbJ16ZPMQFGspcDShjY="}
	for _, name := range append(append([]string{}, exampleRepos[:5]...), "acme", "codegen") {
		t.Run(name, func(t *testing.T) {
			env := []string{"BOB_HOME=" + t.TempDir(), "BOB_SERVER=", "BOB_TOKEN="}
			r := bobRunEnv(t, harness.Fixture(t, name), env, "build", "//...", "--json")
			if harness.Red(t, r) || !exitIs(t, r, 0) {
				return
			}
			for _, e := range events(t, r) {
				if strings.HasPrefix(e.str("event"), "remote.") {
					t.Errorf("a server event without a server: %v", e)
				}
				if e.str("event") == "package.start" {
					if c, _ := e["cache"].(string); c != "" && c != "cache.nixos.org" {
						t.Errorf("package %s was served from %q; only cache.nixos.org is configured", e.str("label"), c)
					}
				}
			}
		})
	}
}

// R-FW-063 (a): no derivation bob generates (builder bob-builder) is a fixed-output
// derivation (no outputHash), for each ecosystem fixture with an empty store.
// R-FW-063 (b): every narHash recorded in bob.lock is a store path bob imported at exactly
// drv.FixedOutputPath(sha256, narHash, recursive, name) (path identity with a FOD).
func TestRFW063FetchesAreImportedNotBuilt(t *testing.T) {
	parity.Covers(t, "R-FW-063")
	for _, name := range lockFixtures {
		t.Run(name, func(t *testing.T) {
			dir := harness.Fixture(t, name)
			home := t.TempDir()
			env := []string{"BOB_HOME=" + home}
			r := bobRunEnv(t, dir, env, "build", "//...", "--json", "--lock")
			if harness.Red(t, r) || !exitIs(t, r, 0) {
				return
			}
			store := filepath.Join(home, "nix", "store")
			ents, err := os.ReadDir(store)
			if err != nil {
				t.Fatalf("bob home has no store at %s: %v", store, err)
			}
			var names []string
			for _, e := range ents {
				if i := strings.IndexByte(e.Name(), '-'); i == 32 {
					names = append(names, e.Name()[33:])
				}
			}
			inspected := 0
			for _, e := range ents {
				if !strings.HasSuffix(e.Name(), ".drv") {
					continue
				}
				d, err := drv.Parse([]byte(readFile(t, filepath.Join(store, e.Name()))))
				if isNotImplemented(err) {
					harness.RedFeature(t, "drv.Parse")
					return
				}
				if err != nil {
					t.Errorf("%s: %v", e.Name(), err)
					continue
				}
				if !strings.HasSuffix(d.Builder, "/bin/bob-builder") {
					continue // a nixpkgs derivation; its fixed-output inputs are nixpkgs' own
				}
				inspected++
				for _, o := range d.Outputs {
					if o.Hash != "" || o.Algo != "" {
						t.Errorf("bob generated a fixed-output derivation %s (output %s has outputHash)", e.Name(), o.Name)
					}
				}
				if _, has := d.Env["outputHash"]; has {
					t.Errorf("%s has outputHash in its environment", e.Name())
				}
			}
			if inspected == 0 {
				t.Errorf("no bob-builder derivation found in %s after a cold build; the fixed-output check above saw nothing", store)
			}
			// Path identity for every recursive hash of the lock.
			var lockDoc any
			if err := json.Unmarshal([]byte(readFile(t, filepath.Join(dir, "bob.lock"))), &lockDoc); err != nil {
				t.Fatal(err)
			}
			var narHashes []string
			var walk func(k string, v any)
			walk = func(k string, v any) {
				switch x := v.(type) {
				case map[string]any:
					for kk, vv := range x {
						walk(kk, vv)
					}
				case []any:
					for _, vv := range x {
						walk(k, vv)
					}
				case string:
					if k == "narHash" || k == "tree" {
						narHashes = append(narHashes, x)
					}
				}
			}
			walk("", lockDoc)
			// Flat hashes: crate "checksum", npm "integrity", sources/opam "hash" (SRI or hex).
			var flatHashes []string
			var walkFlat func(k string, v any)
			walkFlat = func(k string, v any) {
				switch x := v.(type) {
				case map[string]any:
					for kk, vv := range x {
						walkFlat(kk, vv)
					}
				case []any:
					for _, vv := range x {
						walkFlat(k, vv)
					}
				case string:
					if k == "checksum" || k == "integrity" || k == "hash" {
						flatHashes = append(flatHashes, x)
					}
				}
			}
			walkFlat("", lockDoc)
			have := map[string]bool{}
			for _, e := range ents {
				have[e.Name()] = true
			}
			for _, nh := range narHashes {
				raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(nh, "sha256-"))
				if err != nil || len(raw) != sha256.Size {
					t.Errorf("lock narHash %q is not an SRI sha256", nh)
					continue
				}
				found := false
				for _, n := range names {
					p, err := drv.FixedOutputPath("sha256", hex.EncodeToString(raw), true, n)
					if isNotImplemented(err) {
						harness.RedFeature(t, "drv.FixedOutputPath")
						return
					}
					if err == nil && have[strings.TrimPrefix(p, "/nix/store/")] {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("no store path equals the fixed-output path of lock narHash %s", nh)
				}
			}
			// The same for flat hashes: the source path is drv.FixedOutputPath(algo, hash,
			// recursive=false, name) (R-FW-063: "flat: --mode flat").
			for _, fh := range flatHashes {
				algo, hexSum, ok := splitFlatHash(fh)
				if !ok {
					t.Errorf("lock hash %q is neither an SRI string nor a hex digest", fh)
					continue
				}
				found := false
				for _, n := range names {
					p, err := drv.FixedOutputPath(algo, hexSum, false, n)
					if isNotImplemented(err) {
						harness.RedFeature(t, "drv.FixedOutputPath")
						return
					}
					if err == nil && have[strings.TrimPrefix(p, "/nix/store/")] {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("no store path equals the flat fixed-output path of lock hash %s", fh)
				}
			}
		})
	}
}

// splitFlatHash returns the algorithm and the hex digest of an SRI hash ("sha512-<base64>") or a
// bare hex digest (sha1: 40, sha256: 64, sha512: 128 digits).
func splitFlatHash(h string) (algo, hexSum string, ok bool) {
	if i := strings.IndexByte(h, '-'); i > 0 {
		raw, err := base64.StdEncoding.DecodeString(h[i+1:])
		if err != nil {
			return "", "", false
		}
		switch h[:i] {
		case "sha1", "sha256", "sha512":
			return h[:i], hex.EncodeToString(raw), true
		}
		return "", "", false
	}
	switch len(h) {
	case 40:
		algo = "sha1"
	case 64:
		algo = "sha256"
	case 128:
		algo = "sha512"
	default:
		return "", "", false
	}
	if _, err := hex.DecodeString(h); err != nil {
		return "", "", false
	}
	return algo, h, true
}

// R-FW-063 (c): the Nix log of a build shows no `build` activity for a source: no derivation
// Nix builds is a fetch of a Go module, crate, npm tarball, opam source or cc.External source
// (its environment names no ecosystem registry or lock URL host). The internal-json stream is
// shown by -vv.
func TestRFW063NoNixBuildActivityForSources(t *testing.T) {
	parity.Covers(t, "R-FW-063")
	for _, name := range []string{"go", "rust", "npm", "ocaml", "cc"} {
		t.Run(name, func(t *testing.T) {
			dir := harness.Fixture(t, name)
			home := t.TempDir()
			r := bobRunEnv(t, dir, []string{"BOB_HOME=" + home}, "build", "//...", "--lock", "-vv")
			if harness.Red(t, r) || !exitIs(t, r, 0) {
				return
			}
			hosts := []string{"proxy.golang.org", "crates.io", "registry.npmjs.org", "opam.ocaml.org", "github.com/ocaml/opam-repository"}
			for _, m := range regexp.MustCompile(`"url":\s*"https?://([^/"]+)`).FindAllStringSubmatch(readFile(t, filepath.Join(dir, "bob.lock")), -1) {
				hosts = append(hosts, m[1])
			}
			building := regexp.MustCompile(`@nix \{[^\n]*"text":"building '/nix/store/([0-9a-z]{32}-[^']+\.drv)'`)
			lines := building.FindAllStringSubmatch(r.Stderr, -1)
			if len(lines) == 0 {
				t.Errorf("-vv shows no `building '<drv>'` activity of the Nix log for a cold build; the check below would be vacuous:\n%s", r.Stderr)
			}
			for _, m := range lines {
				d, err := drv.Parse([]byte(readFile(t, filepath.Join(home, "nix", "store", m[1]))))
				if isNotImplemented(err) {
					harness.RedFeature(t, "drv.Parse")
					return
				}
				if err != nil {
					t.Errorf("%s: %v", m[1], err)
					continue
				}
				env := fmt.Sprint(d.Env)
				for _, h := range hosts {
					if strings.Contains(env, h) {
						t.Errorf("Nix ran a build of %s that fetches from %s", m[1], h)
					}
				}
			}
		})
	}
}

// R-FW-064: bob test //... twice on polyglot: the second run builds 0 derivations, exits 0 and
// reports every unit cached; after editing one Go test file only that package's test
// derivation (compile + run) rebuilds.
func TestRFW064TestResultsCachedPerUnit(t *testing.T) {
	parity.Covers(t, "R-FW-064")
	dir := harness.Fixture(t, "polyglot")
	env := []string{"BOB_HOME=" + t.TempDir()}
	run := func() (harness.Result, []event) {
		r := bobRunEnv(t, dir, env, "test", "//...", "--json")
		return r, events(t, r)
	}
	r, first := run()
	if harness.Red(t, r) || !exitIs(t, r, 0) {
		return
	}
	units := eventsNamed(first, "test.result")
	if len(units) == 0 {
		t.Fatalf("the first run reported no test unit:\n%s", r.Stdout)
	}
	r, second := run()
	if !exitIs(t, r, 0) {
		return
	}
	if c := counts(t, second); c["built"] != 0 {
		t.Errorf("the second bob test built %v derivations, want 0", c["built"])
	}
	cached := eventsNamed(second, "test.result")
	if len(cached) != len(units) {
		t.Errorf("second run reports %d units, first %d", len(cached), len(units))
	}
	for _, e := range cached {
		if e.str("status") != "cached" {
			t.Errorf("second run: %s is %s, want cached", e.str("label"), e.str("status"))
		}
	}
	// Edit one Go test file: only its package's test derivations rebuild.
	testFile := filepath.Join(dir, "cmd", "api", "main_test.go")
	src := readFile(t, testFile)
	writeFile(t, testFile, strings.Replace(src, "package main", "package main\n\n// edited: new source hash", 1))
	if bytes.Equal([]byte(src), []byte(readFile(t, testFile))) {
		t.Fatalf("harness: main_test.go has no package clause to edit")
	}
	r, third := run()
	if !exitIs(t, r, 0) {
		return
	}
	if c := counts(t, third); c["built"] != 2 {
		t.Errorf("after editing one Go test file %v derivations were built, want 2 (test compile + run)", c["built"])
	}
	for _, e := range eventsNamed(third, "test.result") {
		if strings.Contains(e.str("label"), "cmd/api") || strings.HasPrefix(e.str("label"), "//cmd/api") {
			if e.str("status") == "cached" {
				t.Errorf("%s was served from the cache after its test file changed", e.str("label"))
			}
		} else if e.str("status") != "cached" {
			t.Errorf("%s is %s although its sources did not change", e.str("label"), e.str("status"))
		}
	}
}
