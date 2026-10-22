package platforms

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/megakuul/bob/pkg/bob"
	"github.com/megakuul/bob/pkg/engine/drv"
	"github.com/megakuul/bob/tests/harness"
	"github.com/megakuul/bob/tests/parity"
)

// R-PLAT-001: an unknown platform is a usage error before any Nix process starts; an
// unsupported host architecture (uname -m) is refused.
func TestRPLAT001SupportedSystems(t *testing.T) {
	parity.Covers(t, "R-PLAT-001")
	w := newWorld(t, "polyglot")
	r := w.bob("build", "//cmd/api:api@linux-riscv64")
	if harness.Red(t, r) {
		return
	}
	want := `✗ unknown platform "linux-riscv64" (known: linux-amd64, linux-arm64)`
	if r.Exit != 2 || !strings.Contains(r.Stderr, want) {
		t.Errorf("exit %d, stderr %q, want exit 2 and %q", r.Exit, r.Stderr, want)
	}
	if _, err := os.Stat(filepath.Join(w.home, "nix")); err == nil {
		t.Errorf("the bob home has a nix directory: bob started the store for a platform it rejects")
	}

	// uname -m is neither x86_64 nor aarch64: setarch makes the kernel report a 32-bit machine.
	persona, uname := "linux32", map[bob.Platform]string{bob.LinuxARM64: "armv8l", bob.LinuxAMD64: "i686"}[hostPlatform(t)]
	setarch, err := exec.LookPath("setarch")
	if err != nil {
		t.Fatalf("harness: setarch not found: %v", err)
	}
	chk := harness.RunPath(t, setarch, harness.Spec{}, persona, "uname", "-m")
	if strings.TrimSpace(chk.Stdout) != uname {
		t.Fatalf("harness: setarch %s reports %q, want %q", persona, strings.TrimSpace(chk.Stdout), uname)
	}
	bin := harness.Bin(t, "bob")
	r = harness.RunPath(t, setarch, harness.Spec{Dir: w.dir, Env: w.env()}, persona, bin, "build", "//cmd/api:api")
	want = fmt.Sprintf(`✗ unsupported host architecture %q (bob runs on linux-amd64 and linux-arm64)`, uname)
	if r.Exit != 2 || !strings.Contains(r.Stderr, want) {
		t.Errorf("on uname -m %s: exit %d, stderr %q, want exit 2 and %q", uname, r.Exit, r.Stderr, want)
	}
}

// R-PLAT-004: every derivation of a cross build has the host Nix system; no emulated or remote
// builder is involved (extra-platforms stays unset); outputs are ELF files of the target machine.
func TestRPLAT004HostBuildsTargetCompiles(t *testing.T) {
	parity.Covers(t, "R-PLAT-004")
	w := newWorld(t, "polyglot")
	other := otherPlatform(t)
	paths, _, ok := w.printPaths("--platform", string(other), "//cmd/api:api")
	if !ok {
		return
	}
	out := paths["//cmd/api:api"]
	if out == "" {
		t.Fatalf("no output path for //cmd/api:api: %v", paths)
	}
	bin := filepath.Join(out, "bin", "api")
	if m, err := elfMachineOf(bin); err != nil || m != machine(other) {
		t.Errorf("%s: machine %v (err %v), want %v", bin, m, err, machine(other))
	}
	store := filepath.Join(w.home, "nix", "store")
	ents, err := os.ReadDir(store)
	if err != nil {
		t.Fatalf("bob home has no store: %v", err)
	}
	host, n := nixSystem(hostPlatform(t)), 0
	for _, e := range ents {
		if !strings.HasSuffix(e.Name(), ".drv") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(store, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		d, err := drv.Parse(b)
		if err != nil {
			if strings.Contains(err.Error(), harness.NotImplemented) || strings.Contains(err.Error(), "not implemented") {
				harness.RedFeature(t, "drv.Parse")
				return
			}
			t.Errorf("%s: %v", e.Name(), err)
			continue
		}
		n++
		if d.System != host && d.System != "builtin" {
			t.Errorf("derivation %s has system %q, want the host system %q", e.Name(), d.System, host)
		}
	}
	if n == 0 {
		t.Errorf("no derivation in the store")
	}
	// No emulated builder: the guest binary is not runnable natively, only through qemu-user.
	if r := harness.RunPath(t, bin, harness.Spec{}); r.Exit != -1 && !strings.Contains(r.Stderr, "exec format error") {
		t.Errorf("the %s binary ran natively on a %s host (exit %d): it is not a cross build", other, hostPlatform(t), r.Exit)
	}
}

// R-PLAT-005: every fixture builds for both platforms from this host (arm64 native and
// arm64→amd64; the amd64 host half is (manual)); cross outputs have the target machine and run
// under qemu-user; npm output is platform-independent; OCaml cross requests are refused.
func TestRPLAT005PlatformEcosystemMatrix(t *testing.T) {
	parity.Covers(t, "R-PLAT-005")
	host, other := hostPlatform(t), otherPlatform(t)
	for _, fixture := range []string{"polyglot", "go", "cc", "rust", "npm", "ocaml"} {
		t.Run(fixture, func(t *testing.T) {
			w := newWorld(t, fixture)
			for _, p := range []bob.Platform{host, other} {
				if fixture == "ocaml" && p == other {
					continue
				}
				paths, _, ok := w.printPaths("--platform", string(p), "//...")
				if !ok {
					return
				}
				if fixture == "npm" {
					continue
				}
				seen := 0
				for label, out := range paths {
					for _, b := range binsOf(out) {
						m, err := elfMachineOf(b)
						if err != nil {
							continue // scripts and data
						}
						seen++
						if m != machine(p) {
							t.Errorf("%s@%s: %s has machine %v, want %v", label, p, b, m, machine(p))
						}
					}
				}
				if fixture != "ocaml" && seen == 0 {
					t.Errorf("%s@%s: no ELF binary among the outputs %v", fixture, p, paths)
				}
				if fixture == "polyglot" && p == other {
					api := paths["//cmd/api:api"]
					if r := qemuRun(t, p, w.home, filepath.Join(api, "bin", "api")); r.Exit != 0 || !strings.Contains(r.Stdout, apiLine) {
						t.Errorf("api@%s under qemu-%s printed (exit %d) %q%s, want the golden line", p, qemuName(p), r.Exit, r.Stdout, r.Stderr)
					}
				}
				if fixture == "cc" && p == other {
					app := paths["//app:app"]
					if r := qemuRun(t, p, w.home, filepath.Join(app, "bin", "app")); r.Exit != 0 || !strings.HasPrefix(r.Stdout, "gcd=12 isqrt=31 ") {
						t.Errorf("cc app@%s under qemu printed (exit %d) %q%s", p, r.Exit, r.Stdout, r.Stderr)
					}
				}
			}
			switch fixture {
			case "npm":
				a, _, ok1 := w.printPaths("--platform", string(host), "//site:site")
				b, _, ok2 := w.printPaths("--platform", string(other), "//site:site")
				if ok1 && ok2 && a["//site:site"] != b["//site:site"] {
					t.Errorf("the JS bundle differs by platform (%s vs %s); npm outputs are platform-independent", a["//site:site"], b["//site:site"])
				}
			case "ocaml":
				r := w.bob("build", "--platform", string(other), "//app:demo")
				want := fmt.Sprintf("✗ //app:demo: OCaml packages cannot be cross-compiled yet (requested %s on a %s host)", other, host)
				hint := fmt.Sprintf("build it on a %s worker with --remote", other)
				if r.Exit != 2 || !strings.Contains(r.Stderr, want) || !strings.Contains(r.Stderr, hint) {
					t.Errorf("OCaml cross request: exit %d, want 2, %q and hint %q\n%s", r.Exit, want, hint, r.Stderr)
				}
			}
		})
	}
}

const restrictedFooGo = `//go:build bob

package restricted

import (
	"github.com/megakuul/bob/pkg/bob"
	"github.com/megakuul/bob/pkg/cc"
)

var Foo = bob.Define(func(p *bob.P) *cc.Library {
	return cc.StaticLibrary(p, "foo", cc.LibraryOpts{
		Srcs:      bob.Files("foo.c"),
		Platforms: []bob.Platform{bob.LinuxARM64},
	})
})
`

const restrictedAppGo = `//go:build bob

package restrictedapp

import (
	"example.com/cc/libs/restricted"
	"github.com/megakuul/bob/pkg/bob"
	"github.com/megakuul/bob/pkg/cc"
)

var App = bob.Define(func(p *bob.P) *cc.Binary {
	return cc.NewBinary(p, "app", cc.BinaryOpts{Srcs: bob.Files("main.c"), Deps: []cc.Linkable{restricted.Foo(p)}})
})
`

// R-PLAT-006: a dependency restricted to linux-arm64 fails for linux-amd64 with its allowed list,
// foo's bob.go position and the dependency chain; the allowed platform builds.
func TestRPLAT006PerPackagePlatformRestriction(t *testing.T) {
	parity.Covers(t, "R-PLAT-006")
	w := newWorld(t, "cc")
	w.overlay(map[string]string{
		"libs/restricted/bob.go": restrictedFooGo,
		"libs/restricted/foo.c":  "int foo(void) { return 1; }\n",
		"apps/app/bob.go":        restrictedAppGo,
		"apps/app/main.c":        "int foo(void);\nint main(void) { return foo() - 1; }\n",
	})
	r := w.bob("build", "--json", "--platform", "linux-amd64", "//apps/app:app")
	if harness.Red(t, r) || !exitIs(t, r, 2) {
		return
	}
	var found map[string]any
	for _, line := range strings.Split(r.Stdout, "\n") {
		var e map[string]any
		if json.Unmarshal([]byte(line), &e) == nil && e["event"] == "error" {
			found, _ = e["error"].(map[string]any)
		}
	}
	if found == nil {
		t.Fatalf("no error event:\n%s", r.Stdout)
	}
	msg, _ := found["message"].(string)
	if want := "//libs/restricted:foo does not support linux-amd64 (it allows: linux-arm64)"; msg != want {
		t.Errorf("message %q, want %q", msg, want)
	}
	if at, _ := found["definedAt"].(string); !strings.HasPrefix(at, "libs/restricted/bob.go:") {
		t.Errorf("definedAt %q, want foo's libs/restricted/bob.go position", at)
	}
	human := w.bob("build", "--platform", "linux-amd64", "//apps/app:app")
	if !strings.Contains(human.Stderr, "✗ //libs/restricted:foo does not support linux-amd64 (it allows: linux-arm64)") ||
		!strings.Contains(human.Stderr, "//apps/app:app → //libs/restricted:foo") {
		t.Errorf("human error lacks the message or the chain `//apps/app:app → //libs/restricted:foo`:\n%s", human.Stderr)
	}
	if ok := w.bob("build", "--platform", "linux-arm64", "//apps/app:app"); ok.Exit != 0 {
		t.Errorf("the allowed platform failed: exit %d\n%s", ok.Exit, ok.Stderr)
	}
}

// R-PLAT-007: before generating derivations for a cross target, bob compares the glibc and rustc
// versions of the target set and the cross toolchain from the nixpkgs index. The test index is
// injected through BOB_TEST_NIXPKGS_INDEX (a decision: SPEC gives no mechanism; see report): a
// JSON file {"native": {"glibc": v, "rustc": v}, "cross": {...}} replacing the looked-up versions.
func TestRPLAT007ConsistencyPreCheck(t *testing.T) {
	parity.Covers(t, "R-PLAT-007")
	other := otherPlatform(t)
	cases := []struct {
		name, native, cross string // native/cross: "glibc,rustc" versions
		msg, hint           string
	}{
		{"glibc", "2.42,1.95.0", "2.43,1.95.0",
			fmt.Sprintf("✗ nixpkgs mismatch: target libraries for %s use glibc 2.42 but the cross toolchain uses glibc 2.43", other),
			"set Libs: cc.CrossLibs on the packages that link them, or move the nixpkgs pin"},
		{"rustc", "2.42,1.95.0", "2.42,1.96.0",
			"nixpkgs mismatch", "the nixpkgs pin must provide the same rustc for both systems"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := newWorld(t, "polyglot")
			vers := func(s string) map[string]string {
				p := strings.Split(s, ",")
				return map[string]string{"glibc": p[0], "rustc": p[1]}
			}
			idx, _ := json.Marshal(map[string]any{"native": vers(c.native), "cross": vers(c.cross)})
			file := filepath.Join(t.TempDir(), "index.json")
			if err := os.WriteFile(file, idx, 0o644); err != nil {
				t.Fatal(err)
			}
			r := w.bobEnv([]string{"BOB_TEST_NIXPKGS_INDEX=" + file}, "build", "--platform", string(other), "//cmd/api:api")
			if harness.Red(t, r) {
				return
			}
			if r.Exit == 0 {
				t.Fatalf("a %s mismatch was not refused", c.name)
			}
			if !strings.Contains(r.Stderr, c.msg) {
				t.Errorf("stderr lacks %q:\n%s", c.msg, r.Stderr)
			}
			if c.name == "rustc" && !regexp.MustCompile(`rustc 1\.95\.0.*1\.96\.0|1\.96\.0.*rustc 1\.95\.0`).MatchString(strings.ReplaceAll(r.Stderr, "\n", " ")) {
				t.Errorf("the rustc mismatch message does not show both versions:\n%s", r.Stderr)
			}
			if !strings.Contains(r.Stderr, c.hint) {
				t.Errorf("stderr lacks the hint %q:\n%s", c.hint, r.Stderr)
			}
			if _, err := os.Stat(filepath.Join(w.home, "nix", "store")); err == nil {
				ents, _ := os.ReadDir(filepath.Join(w.home, "nix", "store"))
				for _, e := range ents {
					if strings.HasSuffix(e.Name(), ".drv") && strings.Contains(e.Name(), "cmd-api") {
						t.Errorf("a derivation %s was generated before the pre-check failed", e.Name())
					}
				}
			}
		})
	}
}
