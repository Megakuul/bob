package rust

import (
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/megakuul/bob/pkg/bob"
	"github.com/megakuul/bob/tests/golden"
	"github.com/megakuul/bob/tests/harness"
	"github.com/megakuul/bob/tests/parity"
)

var cargoPathRe = regexp.MustCompile(`/nix/store/[a-z0-9]{32}-cargo-`)

// R-RS-001a: on the Rust fixture, bob build //... and bob test //... succeed natively and
// cross; the outputs (demo, usec, the C binary cmain linking a Rust cdylib and staticlib) run
// under env -i; cargo is never an input of any derivation; a second-store rebuild is
// bit-identical. (The cgo binary calling a StaticLib is the Go fixture's R-GO-001a.)
func TestRRS001aCratesBuildScriptsProcMacrosAndCInterop(t *testing.T) {
	parity.Covers(t, "R-RS-001a")
	host, other, known := platforms(t)
	if !known {
		t.Fatalf("harness: unsupported host architecture")
	}
	for _, plat := range []bob.Platform{host, other} {
		t.Run(string(plat), func(t *testing.T) {
			homeA, homeB := t.TempDir(), t.TempDir()
			dir := harness.Fixture(t, "rust")
			if r := bobEnv(t, dir, registryEnv(homeA), "lock"); !ok(t, r) {
				return
			}
			args := []string{"//..."}
			if plat != host {
				args = append(args, "--platform", string(plat))
			}
			paths, built := outputPaths(t, dir, homeA, args...)
			if !built {
				return
			}
			for label, bin := range map[string]string{"//demo:demo": "demo", "//usec:usec": "usec", "//cmain:cmain": "cmain"} {
				ps := paths[label]
				if len(ps) == 0 {
					t.Errorf("no output path for %s: %v", label, paths)
					continue
				}
				if r := runGuest(t, plat, homeA, filepath.Join(ps[0], "bin", bin)); r.Exit != 0 {
					t.Errorf("%s under env -i exited %d:\n%s%s", label, r.Exit, r.Stdout, r.Stderr)
				}
			}
			assertTests(t, dir, homeA, plat, host, []string{"//corelib:corelib_test", "//corelib:corelib_test_num"})
			if plat == host {
				if all, parsed := drvStore(t, homeA); parsed {
					for n, d := range bobDrvs(all) {
						text := jobSpec(d) + strings.Join(d.InputSrcs, "\n")
						for _, in := range d.InputDrvs {
							text += "\n" + in.Path
						}
						if cargoPathRe.MatchString(text) {
							t.Errorf("derivation %s has cargo among its inputs", n)
						}
					}
				}
			}
			paths2, built2 := outputPaths(t, dir, homeB, args...)
			if !built2 {
				return
			}
			for label, ps := range paths {
				ps2 := paths2[label]
				if len(ps) != len(ps2) {
					t.Errorf("%s: %d outputs on the first store, %d on the second", label, len(ps), len(ps2))
					continue
				}
				for i := range ps {
					a, b := strings.TrimPrefix(ps[i], homeA), strings.TrimPrefix(ps2[i], homeB)
					if a != b {
						t.Errorf("%s: output path %s differs between stores (%s)", label, a, b)
					} else if treeDigest(t, ps[i]) != treeDigest(t, ps2[i]) {
						t.Errorf("%s: output %s is not bit-identical on a second store", label, a)
					}
				}
			}
		})
	}
}

// R-RS-005: with the fixture locked, no network and an empty crate cache,
// `bob build --dry-run //...` (plan only) succeeds and prints the same derivation set as with
// sources present; the lock carries the graph facts (edges, features, links, meta).
func TestRRS005LockCarriesCrateGraphFacts(t *testing.T) {
	parity.Covers(t, "R-RS-005")
	warm := t.TempDir()
	dir := harness.Fixture(t, "rust")
	if r := bobEnv(t, dir, registryEnv(warm), "lock"); !ok(t, r) {
		return
	}
	crates := lockedCrates(t, dir)
	var withDeps, withFeatures int
	links := map[string]string{}
	byName := map[string]map[string]any{}
	for _, c := range crates {
		name, _ := c["name"].(string)
		byName[name] = c
		meta, _ := c["meta"].(map[string]any)
		if ed, _ := meta["edition"].(string); ed == "" {
			t.Errorf("crate %s has no meta.edition", name)
		}
		if _, has := c["deps"]; has {
			withDeps++
		}
		if _, has := c["features"]; has {
			withFeatures++
		}
		if l, _ := c["links"].(string); l != "" {
			links[name] = l
		}
	}
	if len(crates) == 0 || withDeps == 0 || withFeatures == 0 {
		t.Errorf("the rust section lacks graph facts: %d crates, %d with deps, %d with features", len(crates), withDeps, withFeatures)
	}
	// Each crate entry stores its edges and feature table: spot-check crates whose graph
	// facts are known (serde has a derive feature, serde_derive depends on proc-macro2/syn,
	// clap_derive and zstd-sys likewise carry edges or features).
	for name, keys := range map[string][]string{"serde": {"features"}, "serde_derive": {"deps"}, "zstd-sys": {"features"}, "tokio": {"deps", "features"}} {
		c, found := byName[name]
		if !found {
			continue
		}
		for _, k := range keys {
			if _, has := c[k]; !has {
				t.Errorf("crate %s has no %q in the lock: every crate entry stores edges and its feature table", name, k)
			}
		}
	}
	if links["zstd-sys"] != "zstd" {
		t.Errorf("zstd-sys must record links = \"zstd\", got %v", links)
	}
	withSources := bobIn(t, dir, warm, "build", "--dry-run", "//...")
	if !ok(t, withSources) {
		return
	}
	cold := noNetwork(t, dir, t.TempDir(), "build", "--dry-run", "//...")
	if !ok(t, cold) {
		return
	}
	if cold.Stdout != withSources.Stdout {
		t.Errorf("the dry-run plan differs without network and crate sources\nwith sources:\n%s\nwithout:\n%s", withSources.Stdout, cold.Stdout)
	}
	if strings.TrimSpace(cold.Stdout) == "" {
		t.Errorf("the dry-run printed no derivations")
	}
}

// R-RS-004a (a): a crate with Toolchain from another rustc attribute compiles with it (the
// job is golden and shows the toolchain store path). The attribute "rust_1_94" is the decision
// for "<other rustc attr>"; it needs a second pin or nixpkgs attribute (SPEC [open]).
func TestRRS004aToolchainPerPackage(t *testing.T) {
	parity.Covers(t, "R-RS-004a")
	home := t.TempDir()
	dir := harness.Fixture(t, "rust")
	replaceIn(t, filepath.Join(dir, "rcore", "bob.go"), "\t\tHeaders: \"include\",", "\t\tHeaders:   \"include\",\n\t\tToolchain: nixpkgs.Tool(p, \"rust_1_94\"),")
	replaceIn(t, filepath.Join(dir, "rcore", "bob.go"), "\"github.com/megakuul/bob/pkg/bob\"", "\"github.com/megakuul/bob/pkg/bob\"\n\t\"github.com/megakuul/bob/pkg/nixpkgs\"")
	if r := bobEnv(t, dir, registryEnv(home), "lock"); !ok(t, r) {
		return
	}
	if r := bobIn(t, dir, home, "build", "//rcore:rcore"); !ok(t, r) {
		return
	}
	all, parsed := drvStore(t, home)
	if !parsed {
		return
	}
	toolchain := regexp.MustCompile(`/nix/store/[a-z0-9]{32}-rustc-wrapper-1\.94\.\d+|/nix/store/[a-z0-9]{32}-rustc-1\.94\.\d+`)
	nameRe := regexp.MustCompile(`^[a-z0-9]{32}-rcore-[0-9.]+(-[a-z0-9]+)?\.drv$|^[a-z0-9]{32}-rcore(-[a-z0-9]+)?\.drv$`)
	found := false
	for n, d := range bobDrvs(all) {
		if !nameRe.MatchString(n) {
			continue
		}
		found = true
		spec := jobSpec(d)
		if !toolchain.MatchString(spec) {
			t.Errorf("the compile job of %s does not name the rustc 1.94 toolchain", n)
		}
		golden.Assert(t, "tests/goldens/eco/rust/jobs/rcore-alt-toolchain.json", []byte(spec+"\n"), golden.Reqs("R-RS-004a"))
	}
	if !found {
		t.Errorf("no compile derivation of the rcore crate in the store")
	}
}

// R-RS-004a (b): mixing toolchains along a dependency edge fails at evaluation with the
// "need the same toolchain" error.
func TestRRS004aMixedToolchainsRejected(t *testing.T) {
	parity.Covers(t, "R-RS-004a")
	home := t.TempDir()
	dir := harness.Fixture(t, "rust")
	// corelib (a dependency of demo) gets the other toolchain; demo keeps the default.
	replaceIn(t, filepath.Join(dir, "corelib", "bob.go"), "\t\tDefault:  []string{\"fast\"},", "\t\tDefault:  []string{\"fast\"},\n\t\tToolchain: nixpkgs.Tool(p, \"rust_1_94\"),")
	replaceIn(t, filepath.Join(dir, "corelib", "bob.go"), "\"github.com/megakuul/bob/pkg/bob\"", "\"github.com/megakuul/bob/pkg/bob\"\n\t\"github.com/megakuul/bob/pkg/nixpkgs\"")
	r := bobEnv(t, dir, registryEnv(home), "build", "//demo:demo", "--lock", "--json")
	if harness.Red(t, r) || !exitIs(t, r, 3) {
		return
	}
	want := regexp.MustCompile(`//demo:demo \(rustc \d+\.\d+\.\d+\) depends on //corelib:corelib built with rustc \d+\.\d+\.\d+; Rust crates linked together need the same toolchain`)
	var msgs []string
	for _, e := range eventsNamed(events(t, r), "error") {
		msgs = append(msgs, strings.TrimSpace(toString(e["error"])))
	}
	if !want.MatchString(strings.Join(msgs, "\n")) {
		t.Errorf("no error matches %q; errors: %v", want, msgs)
	}
}
