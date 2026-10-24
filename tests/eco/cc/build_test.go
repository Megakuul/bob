package cc

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/megakuul/bob/pkg/bob"
	"github.com/megakuul/bob/tests/golden"
	"github.com/megakuul/bob/tests/harness"
	"github.com/megakuul/bob/tests/parity"
)

// R-CC-001a: bob build //... && bob test //... succeed on the cc fixture (static lib, shared
// lib using nixpkgs zlib and libcrypto, binary linking both, a TAP test, an External) for
// linux-arm64 and linux-amd64 (§04 cross); the binary runs under env -i (qemu-user for the
// other platform) and a rebuild on a second store is bit-identical.
func TestRCC001aBuildTestCrossAndReproducible(t *testing.T) {
	parity.Covers(t, "R-CC-001a")
	host, other, known := platforms(t)
	if !known {
		t.Fatalf("harness: unsupported host architecture")
	}
	for _, plat := range []bob.Platform{host, other} {
		t.Run(string(plat), func(t *testing.T) {
			srv := newArchiveServer(t, "original")
			homeA, homeB := t.TempDir(), t.TempDir()
			dir, locked := lockedCC(t, srv, homeA)
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
			app := paths["//app:app"]
			if len(app) == 0 {
				t.Fatalf("no output path for //app:app: %v", paths)
			}
			r := runGuest(t, plat, homeA, filepath.Join(app[0], "bin", "app"))
			if r.Exit != 0 || !strings.HasPrefix(r.Stdout, "gcd=12 isqrt=31 ") {
				t.Errorf("app under env -i exited %d and printed %q; want the gcd=12 isqrt=31 line", r.Exit, r.Stdout+r.Stderr)
			}
			assertTests(t, dir, homeA, plat, host, []string{"//libs/mathx:mathx_test"})
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

// ccJobs are the cc packages of the fixture whose bobSpec job is golden (R-CC-002a): derivation
// name prefix → golden file name. The External's job is covered by R-CC-005a, not here.
var ccJobs = map[string]string{
	"cc-mathx":      "mathx",
	"cc-pack":       "pack",
	"cc-app":        "app",
	"cc-mathx_test": "mathx_test",
}

// R-CC-002a: the bobSpec job of each fixture package is golden (flag order of §03.1.3); a
// Flags entry "-I/nix/store/x" fails at evaluation with
// `✗ //libs/mathx:mathx: flag "-I/nix/store/x" is not allowed; use Deps to add include paths`.
func TestRCC002aTypedOptionsMapToFlags(t *testing.T) {
	parity.Covers(t, "R-CC-002a")
	t.Run("job-goldens", func(t *testing.T) {
		srv := newArchiveServer(t, "original")
		home := t.TempDir()
		dir, locked := lockedCC(t, srv, home)
		if !locked {
			return
		}
		if r := bobIn(t, dir, home, "build", "//...", "--platform", string(hostPlatform(t))); !ok(t, r) {
			return
		}
		all, parsed := drvStore(t, home)
		if !parsed {
			return
		}
		found := map[string]bool{}
		var names []string
		for n := range bobDrvs(all) {
			names = append(names, n)
		}
		sort.Strings(names)
		nameRe := regexp.MustCompile(`^[a-z0-9]{32}-(cc-[A-Za-z0-9_]+)(?:-[A-Za-z0-9_.-]+)?\.drv$`)
		for _, n := range names {
			m := nameRe.FindStringSubmatch(n)
			if m == nil {
				continue
			}
			file, wanted := ccJobs[m[1]]
			if !wanted {
				continue
			}
			found[m[1]] = true
			var pretty bytes.Buffer
			spec := jobSpec(all[n])
			if err := json.Indent(&pretty, []byte(spec), "", "  "); err != nil {
				t.Errorf("%s: bobSpec is not JSON: %v", n, err)
				continue
			}
			pretty.WriteByte('\n')
			golden.Assert(t, "tests/goldens/eco/cc/jobs/"+file+".json", pretty.Bytes(), golden.Reqs("R-CC-002a"))
		}
		for prefix := range ccJobs {
			if !found[prefix] {
				t.Errorf("no derivation named %s-* in the store", prefix)
			}
		}
	})
	t.Run("flag-rejected", func(t *testing.T) {
		dir := harness.Fixture(t, "cc")
		replaceIn(t, filepath.Join(dir, "libs", "mathx", "bob.go"),
			`Hdrs: bob.IncludeDir("include"), Std: cc.C17})`, `Hdrs: bob.IncludeDir("include"), Std: cc.C17, Flags: []string{"-I/nix/store/x"}})`)
		r := bobIn(t, dir, t.TempDir(), "build", "//libs/mathx:mathx", "--json")
		if harness.Red(t, r) || !exitIs(t, r, 3) {
			return
		}
		const want = `//libs/mathx:mathx: flag "-I/nix/store/x" is not allowed; use Deps to add include paths`
		var msgs []string
		for _, e := range eventsNamed(events(t, r), "error") {
			b, _ := json.Marshal(e["error"])
			var be bobError
			_ = json.Unmarshal(b, &be)
			msgs = append(msgs, be.Message)
			if strings.Contains(be.Message, want) && !strings.Contains(string(b), "libs/mathx/bob.go") {
				t.Errorf("the evaluation error does not point to libs/mathx/bob.go: %s", b)
			}
		}
		if !strings.Contains(strings.Join(msgs, "\n"), want) {
			t.Errorf("no error says %q; got %q", want, msgs)
		}
		human := bobIn(t, dir, t.TempDir(), "build", "//libs/mathx:mathx")
		if !strings.Contains(human.Stderr, "✗ "+want) {
			t.Errorf("stderr lacks the line %q:\n%s", "✗ "+want, human.Stderr)
		}
	})
}
