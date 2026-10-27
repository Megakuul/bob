package rootless

import (
	"bufio"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/megakuul/bob/tests/harness"
	"github.com/megakuul/bob/tests/parity"
)

// The hooks R-NIX-022's simulated cases need are compiled into bob only with -tags bobtest
// (HARNESS.md §7.1 "fault hook in bobns (test build tag)"). They are driven by the environment:
//
//	BOB_TEST_CLONE_ERRNO=ENOSPC|EPERM|EACCES   the namespace clone fails with that errno
//	BOB_TEST_MOUNT_ERRNO=EPERM                 the first mount inside the namespace fails (child exit 125)
const (
	hookClone = "BOB_TEST_CLONE_ERRNO"
	hookMount = "BOB_TEST_MOUNT_ERRNO"
)

// decoyDir writes decoy nix binaries that log to the guest's HOME (host: home/decoy.log).
func (w *world) decoyDir(t testing.TB) string {
	t.Helper()
	d := w.dir(t, "decoy")
	for _, n := range []string{"nix", "nix-store", "nix-build", "nix-instantiate", "nix-env", "nix-hash", "nix-daemon"} {
		p := filepath.Join(d, n)
		if err := os.WriteFile(p, []byte("#!/bin/sh\necho \"$0 $*\" >> "+guestHome+"/decoy.log\nexit 97\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return d
}

func fileHasContent(p string) (string, bool) {
	b, err := os.ReadFile(p)
	return string(b), err == nil && len(b) > 0
}

// TestRNIX020RootlessMatrix is the first row of HARNESS.md §7.1 for R-NIX-020/R-NIX-022 (positive
// path): in each of the 5 pinned distro images, as an unprivileged user, with no /nix on the host
// and `env -i PATH=/usr/bin:/bin`, bob builds cpp-lib and the Go fixture and runs `bob dev --
// true`; the host's nix (decoys first on PATH) is never executed.
func TestRNIX020RootlessMatrix(t *testing.T) {
	parity.Covers(t, "R-NIX-020", "R-NIX-022")
	w := mustWorld(t)
	for _, im := range Images {
		t.Run(im.Name, func(t *testing.T) {
			w.checkNamespaceHonest(t, im, false)
			if r := w.run(t, opts{Image: im, Timeout: time.Minute}, "/bin/sh", "-c", "test ! -e /nix"); r.Exit != 0 {
				t.Fatalf("harness: the %s rootfs has a /nix", im.Name)
			}
			home := w.dir(t, "home")
			decoy := w.decoyDir(t)
			run := func(fixture string, args ...string) harness.Result {
				return w.run(t, opts{Image: im, Home: home, Work: w.repo(t, fixture), Decoy: decoy}, append([]string{guestBob}, args...)...)
			}
			r := run("cpp-lib", "build", "//...")
			if harness.Red(t, r) {
				return
			}
			if r.Exit != 0 {
				t.Fatalf("bob build //... (cpp-lib) exit %d:\n%s", r.Exit, r.Combined())
			}
			if r := run("go", "build", "//..."); r.Exit != 0 {
				t.Fatalf("bob build //... (go) exit %d:\n%s", r.Exit, r.Combined())
			}
			if r := run("cpp-lib", "dev", "--", "true"); r.Exit != 0 {
				t.Fatalf("bob dev -- true exit %d:\n%s", r.Exit, r.Combined())
			}
			if log, bad := fileHasContent(filepath.Join(home, "decoy.log")); bad {
				t.Errorf("the host's nix was executed:\n%s", log)
			}
			if _, err := os.Stat(filepath.Join(home, ".local", "share", "bob")); err != nil {
				t.Errorf("bob's state home is not <HOME>/.local/share/bob: %v", err)
			}
		})
	}
}

// probe files: the S0b probe repository (same as tests/nix).
func probeRepoFiles() map[string]string {
	return map[string]string{
		"go.mod":   "module example.com/synth\n\ngo 1.26.0\n",
		"bob.lock": "{\n  \"lockVersion\": 1\n}\n",
		"bob.go":   "//go:build bob\n\npackage synth\n\nimport \"github.com/megakuul/bob/pkg/bob\"\n\nvar _ = bob.Repo(bob.RepoOpts{Nixpkgs: bob.NixOS2605})\n",
		"probe/bob.go": `//go:build bob

package probe

import (
	"github.com/megakuul/bob/pkg/bob"
	"github.com/megakuul/bob/pkg/nixpkgs"
)

// Probe records facts about its own sandbox.
var Probe = bob.Define(func(p *bob.P) *bob.Step {
	return bob.Run(p, "probe", bob.RunOpts{
		Tools: []bob.Tool{nixpkgs.Bash.Tool(p), nixpkgs.Coreutils.Tool(p)},
		Cmd: []string{"bash", "-c", "id -u > $BOB_OUT/uid; ls -A /home > $BOB_OUT/home; tail -n +3 /proc/net/dev | cut -d: -f1 | tr -d ' ' > $BOB_OUT/ifaces; if timeout 3 bash -c 'exec 3<>/dev/tcp/1.1.1.1/53'; then echo up; else echo blocked; fi > $BOB_OUT/net"},
		Outputs: []string{"uid", "home", "ifaces", "net"},
	})
})
`,
	}
}

// probeRepo writes the probe repository, git-initialised, below share.
func (w *world) probeRepo(t testing.TB) string {
	t.Helper()
	tmp := filepath.Join(t.TempDir(), "synth")
	for p, c := range probeRepoFiles() {
		full := filepath.Join(tmp, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, args := range [][]string{{"init", "-q"}, {"add", "-A"}, {"-c", "user.name=h", "-c", "user.email=h@bob.invalid", "commit", "-q", "-m", "x"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = tmp
		if b, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("harness: git %v: %v: %s", args, err, b)
		}
	}
	dst := w.dir(t, "repo-probe")
	if err := os.CopyFS(dst, os.DirFS(tmp)); err != nil {
		t.Fatalf("harness: %v", err)
	}
	if err := w.chown(dst); err != nil {
		t.Fatal(err)
	}
	return dst
}

// printedPath reads the host path of the output of label from bob's --json events.
func printedPath(t testing.TB, stdout, label string) string {
	t.Helper()
	sc := bufio.NewScanner(strings.NewReader(stdout))
	sc.Buffer(make([]byte, 1<<20), 16<<20)
	for sc.Scan() {
		var ev struct {
			Event string   `json:"event"`
			Label string   `json:"label"`
			Paths []string `json:"paths"`
		}
		if json.Unmarshal(sc.Bytes(), &ev) == nil && ev.Event == "package.finish" && ev.Label == label && len(ev.Paths) > 0 {
			return ev.Paths[0]
		}
	}
	t.Fatalf("no paths[] for %s in:\n%s", label, stdout)
	return ""
}

// TestRNIX021PivotProbes is S0b's probes F1 and F3 in pivot mode (the rootfs has no /nix): the
// probe step's packages are substituted from the cache (F1) and inside its build sandbox the user
// is not root, the network is blocked, /home is hidden and only loopback exists (F3).
func TestRNIX021PivotProbes(t *testing.T) {
	parity.Covers(t, "R-NIX-021")
	w := mustWorld(t)
	for _, im := range Images {
		t.Run(im.Name, func(t *testing.T) {
			home := w.dir(t, "home")
			work := w.probeRepo(t)
			r := w.run(t, opts{Image: im, Home: home, Work: work}, guestBob, "build", "--json", "--print-paths", "//probe:probe")
			if harness.Red(t, r) {
				return
			}
			if r.Exit != 0 {
				t.Fatalf("bob build exit %d:\n%s", r.Exit, r.Combined())
			}
			out := filepath.Join(home, ".local", "share", "bob", "nix", "store", filepath.Base(printedPath(t, r.Stdout, "//probe:probe")))
			read := func(f string) string {
				b, err := os.ReadFile(filepath.Join(out, f))
				if err != nil {
					t.Fatalf("probe output %s: %v", f, err)
				}
				return strings.TrimSpace(string(b))
			}
			for f, want := range map[string]string{"uid": "1000", "home": "", "ifaces": "lo", "net": "blocked"} {
				if got := read(f); got != want {
					t.Errorf("%s inside the build = %q, want %q", f, got, want)
				}
			}
		})
	}
}

// blockText checks the shape of the BOB-E-7005 refusal (SPEC R-NIX-022) and returns its parts.
func blockText(t testing.TB, r harness.Result) (cause, fix string, ok bool) {
	t.Helper()
	if r.Exit != 7 {
		t.Errorf("exit %d, want 7:\n%s", r.Exit, r.Combined())
		return "", "", false
	}
	lines := strings.Split(strings.TrimRight(r.Combined(), "\n"), "\n")
	const head = "✗ bob cannot create its private /nix: unprivileged user namespaces are blocked on this system"
	i := -1
	for k, l := range lines {
		if l == head {
			i = k
		}
	}
	if i < 0 || len(lines) < i+4 {
		t.Errorf("the refusal block is missing or short:\n%s", r.Combined())
		return "", "", false
	}
	if !strings.HasPrefix(lines[i+1], "  cause: ") || lines[i+2] != "  fix (run once as an administrator):" || !strings.HasPrefix(lines[i+3], "    ") {
		t.Errorf("the refusal does not have the cause / fix layout:\n%s", strings.Join(lines[i:], "\n"))
		return "", "", false
	}
	if strings.Contains(r.Combined(), "/nix/store/") {
		t.Errorf("the refusal shows a store path:\n%s", r.Combined())
	}
	return strings.TrimPrefix(lines[i+1], "  cause: "), strings.Join(lines[i+3:], "\n"), true
}

const (
	fixMaxNS     = "    echo 'user.max_user_namespaces = 15000' | sudo tee /etc/sysctl.d/60-bob-userns.conf && sudo sysctl --system"
	fixNixOS     = "    set security.allowUserNamespaces = true; in configuration.nix and run nixos-rebuild switch"
	fixContainer = "    bob runs only where unprivileged user namespaces are allowed; inside containers, allow them (e.g. podman --security-opt seccomp=unconfined, or docker --security-opt seccomp=unconfined)"
)

// TestRNIX022Refusal covers the simulated rows of HARNESS.md §7.1 in every image: a real
// `max_user_namespaces = 0` (written inside a throwaway namespace; the host is untouched) and the
// fault-hook cases (ENOSPC, EPERM, EACCES clone errors; mount EPERM with child exit 125; NixOS).
// The AppArmor row is verified on a real Ubuntu 24.04 host (manual); here only the mount-EPERM
// mapping is checked.
func TestRNIX022Refusal(t *testing.T) {
	parity.Covers(t, "R-NIX-022")
	w := mustWorld(t)
	for _, im := range Images {
		t.Run(im.Name, func(t *testing.T) {
			w.checkNamespaceHonest(t, im, true)
			build := func(o opts, args ...string) harness.Result {
				o.Image = im
				o.Home = w.dir(t, "home")
				o.Work = w.repo(t, "cpp-lib")
				o.Timeout = 5 * time.Minute
				if o.Bob == "" {
					o.Bob = w.bob
				}
				return w.run(t, o, append([]string{guestBob}, args...)...)
			}
			t.Run("max_user_namespaces=0", func(t *testing.T) {
				r := build(opts{BlockUserns: true}, "build", "//...")
				if harness.Red(t, r) {
					return
				}
				cause, fix, ok := blockText(t, r)
				if ok && (!strings.Contains(cause, "max_user_namespaces") || fix != fixMaxNS) {
					t.Errorf("cause %q, fix %q: want the max_user_namespaces cause and fix %q", cause, fix, fixMaxNS)
				}
				j := build(opts{BlockUserns: true}, "build", "--json", "//...")
				if j.Exit != 7 || !strings.Contains(j.Stdout, "BOB-E-7005") {
					t.Errorf("--json: exit %d, want 7 with an error event BOB-E-7005:\n%s", j.Exit, j.Combined())
				}
			})
			for _, c := range []struct {
				name, errno string
				wantCause   string
				wantFix     string
			}{
				{"ENOSPC", "ENOSPC", "max_user_namespaces", fixMaxNS},
				{"EPERM", "EPERM", "seccomp", fixContainer},
				{"EACCES", "EACCES", "seccomp", fixContainer},
			} {
				t.Run("clone-"+c.name, func(t *testing.T) {
					r := build(opts{Bob: w.bobTest, Env: []string{hookClone + "=" + c.errno}}, "build", "//...")
					if harness.Red(t, r) {
						return
					}
					cause, fix, ok := blockText(t, r)
					if ok && (!strings.Contains(cause, c.wantCause) || fix != c.wantFix) {
						t.Errorf("clone %s: cause %q, fix %q; want cause containing %q and fix %q", c.errno, cause, fix, c.wantCause, c.wantFix)
					}
				})
			}
			t.Run("mount-EPERM", func(t *testing.T) {
				r := build(opts{Bob: w.bobTest, Env: []string{hookMount + "=EPERM"}}, "build", "//...")
				if harness.Red(t, r) {
					return
				}
				cause, fix, ok := blockText(t, r)
				if !ok {
					return
				}
				if !strings.Contains(cause, "AppArmor") {
					t.Errorf("cause %q does not name AppArmor", cause)
				}
				for _, want := range []string{guestBob, "sudo apparmor_parser -r /etc/apparmor.d/bob", "60-bob-userns.conf"} {
					if !strings.Contains(fix, want) {
						t.Errorf("the AppArmor fix lacks %q (bob's real path is readlink /proc/self/exe):\n%s", want, fix)
					}
				}
			})
			t.Run("NixOS", func(t *testing.T) {
				r := build(opts{Bob: w.bobTest, NixOS: true, Env: []string{hookClone + "=ENOSPC"}}, "build", "//...")
				if harness.Red(t, r) {
					return
				}
				cause, fix, ok := blockText(t, r)
				if ok && (!strings.Contains(cause, "security.allowUserNamespaces") || fix != fixNixOS) {
					t.Errorf("NixOS: cause %q, fix %q; want the NixOS cause and fix %q", cause, fix, fixNixOS)
				}
			})
			t.Run("no-fallback", func(t *testing.T) {
				home := w.dir(t, "home")
				r := w.run(t, opts{Image: im, Home: home, Work: w.repo(t, "cpp-lib"), BlockUserns: true}, guestBob, "build", "//...")
				if harness.Red(t, r) {
					return
				}
				if _, err := os.Stat(filepath.Join(home, ".local", "share", "bob", "nix", "store")); err == nil {
					t.Errorf("a store exists after the refusal: there is no fallback mode")
				}
			})
		})
	}
}
