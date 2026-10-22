package golang

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/megakuul/bob/pkg/bob"
	"github.com/megakuul/bob/tests/parity"
)

// goTestUnits are the test units of the Go fixture: one per package with _test.go files
// (§03.0.5; label //<pkg dir>:go_test).
var goTestUnits = []string{
	"//api/internal/ffi:go_test",
	"//api/internal/msg:go_test",
	"//lib/fastadd:go_test",
	"//lib/strutil:go_test",
	"//tools/cmd/stamp:go_test",
}

// R-GO-001a: bob build //... and bob test //... succeed on the Go fixture (3 modules, cgo
// package linking a cc shared library and a Rust staticlib, protobuf codegen, embeds, an
// assembly package, a test per package), natively and cross; the binaries run under env -i
// (qemu-user for the other platform); every output rebuilds bit-identically on a second store.
func TestRGO001aBuildTestCrossAndReproducible(t *testing.T) {
	parity.Covers(t, "R-GO-001a")
	host, other, known := platforms(t)
	if !known {
		t.Fatalf("harness: unsupported host architecture")
	}
	for _, plat := range []bob.Platform{host, other} {
		t.Run(string(plat), func(t *testing.T) {
			homeA, homeB := t.TempDir(), t.TempDir()
			dir, locked := lockedFixture(t, "go", homeA)
			if !locked {
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
			api, stamp := paths["//api/cmd/api:api"], paths["//tools/cmd/stamp:stamp"]
			if len(api) == 0 || len(stamp) == 0 {
				t.Fatalf("no output path for //api/cmd/api:api or //tools/cmd/stamp:stamp: %v", paths)
			}
			r := runGuest(t, plat, homeA, filepath.Join(api[0], "bin", "api"))
			if r.Exit != 0 || !strings.Contains(r.Stdout, "version=1.0.0") || !strings.Contains(r.Stdout, "numhex=") {
				t.Errorf("api under env -i exited %d and printed %q; want exit 0 and the version=1.0.0 line (cgo into the C and Rust libraries)", r.Exit, r.Stdout+r.Stderr)
			}
			r = runGuest(t, plat, homeA, filepath.Join(stamp[0], "bin", "stamp"))
			if r.Exit != 0 || !strings.Contains(r.Stdout, "(42)") {
				t.Errorf("stamp under env -i exited %d and printed %q; want exit 0 and the assembly sum (42)", r.Exit, r.Stdout+r.Stderr)
			}
			if plat == host {
				tr := bobIn(t, dir, homeA, "test", "//...", "--json")
				if ok(t, tr) {
					got := map[string]string{}
					for _, e := range eventsNamed(events(t, tr), "test.result") {
						got[e.str("label")] = e.str("status")
					}
					for _, u := range goTestUnits {
						if s := got[u]; s != "pass" && s != "cached" {
							t.Errorf("test unit %s has status %q, want pass", u, s)
						}
					}
				}
			}
			// Second store: same derivations, so the same output paths with identical content.
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
					a := strings.TrimPrefix(ps[i], homeA)
					b := strings.TrimPrefix(ps2[i], homeB)
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
