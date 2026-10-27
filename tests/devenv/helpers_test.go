package devenv

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/megakuul/bob/tests/errors/leakcheck"
	"github.com/megakuul/bob/tests/harness"
	"github.com/megakuul/bob/tests/serverenv"
)

// account is the unprivileged user every devenv test runs as (HARNESS.md: non-root runs use
// bobtest when the suite runs as root).
type account struct{ uid, gid uint32 }

func unprivileged(t testing.TB) account {
	t.Helper()
	if os.Geteuid() != 0 {
		return account{uint32(os.Getuid()), uint32(os.Getgid())}
	}
	u, err := user.Lookup("bobtest")
	if err != nil {
		t.Fatalf("harness error: the test user bobtest is missing (docs/environment.md): %v", err)
	}
	ui, _ := strconv.Atoi(u.Uid)
	gi, _ := strconv.Atoi(u.Gid)
	return account{uint32(ui), uint32(gi)}
}

// chownTree hands dir to the account and makes every parent traversable.
func (a account) chownTree(t testing.TB, dir string) {
	t.Helper()
	if os.Geteuid() != 0 {
		return
	}
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err := os.Lchown(p, int(a.uid), int(a.gid)); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err // git's background maintenance may remove a lock file under us
		}
		return nil
	})
	if err != nil {
		t.Fatalf("harness error: chown %s: %v", dir, err)
	}
	for p := filepath.Dir(dir); p != "/"; p = filepath.Dir(p) {
		if info, err := os.Stat(p); err == nil && info.Mode().Perm()&0o001 == 0 {
			_ = os.Chmod(p, info.Mode().Perm()|0o011)
		}
	}
}

func (a account) cred(cmd *exec.Cmd) {
	if os.Geteuid() != 0 {
		return
	}
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Credential = &syscall.Credential{Uid: a.uid, Gid: a.gid}
}

// world is one test's sandbox: a fresh HOME with all XDG directories, owned by the account.
type world struct {
	t    testing.TB
	acc  account
	home string
	env  []string
}

func newWorld(t testing.TB) *world {
	t.Helper()
	h := filepath.Join(t.TempDir(), "home")
	for _, d := range []string{".config", ".local/share", ".local/state", ".cache"} {
		if err := os.MkdirAll(filepath.Join(h, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	w := &world{t: t, acc: unprivileged(t), home: h}
	w.env = []string{
		"HOME=" + h,
		"XDG_CONFIG_HOME=" + filepath.Join(h, ".config"),
		"XDG_DATA_HOME=" + filepath.Join(h, ".local", "share"),
		"XDG_STATE_HOME=" + filepath.Join(h, ".local", "state"),
		"XDG_CACHE_HOME=" + filepath.Join(h, ".cache"),
		"TERM=dumb",
	}
	w.acc.chownTree(t, h)
	return w
}

// stateHome is bob's state home <home> of SPEC §01 (R-NIX-024).
func (w *world) stateHome() string { return filepath.Join(w.home, ".local", "share", "bob") }

// fixture copies a fixture repository, hands it to the account and returns its directory.
func (w *world) fixture(name string) string {
	w.t.Helper()
	d := harness.Fixture(w.t, name)
	w.acc.chownTree(w.t, filepath.Dir(d))
	return d
}

var (
	binOnce sync.Once
	binPath string
	binErr  error
)

// bobBin returns a bob binary the unprivileged account may execute: harness.Bin lives in a 0700
// temporary directory, so it is copied once into a world-readable one.
func bobBin(t testing.TB) string {
	t.Helper()
	src := harness.Bin(t, "bob")
	binOnce.Do(func() {
		dir, err := os.MkdirTemp("", "bob-devenv-bin-")
		if err != nil {
			binErr = err
			return
		}
		if binErr = os.Chmod(dir, 0o755); binErr != nil {
			return
		}
		b, err := os.ReadFile(src)
		if err != nil {
			binErr = err
			return
		}
		binPath = filepath.Join(dir, "bob")
		binErr = os.WriteFile(binPath, b, 0o755)
	})
	if binErr != nil {
		t.Fatalf("harness error: copy bob: %v", binErr)
	}
	return binPath
}

// hideNix wraps argv so that it runs in a mount namespace where /nix is an empty tmpfs: "a root
// without /nix" (SPEC R-DEV-008 -hide-nix client mode). The outer user namespace maps root so
// that mount works; the inner one maps the account back to its own uid, so the command (bob)
// sees the uid it would have on such a host. (mount(8) refuses a non-root caller, so the S1
// `--map-current-user` shortcut of tests/core does not work.)
func (a account) hideNix(argv []string) []string {
	inner := fmt.Sprintf(`mount -t tmpfs tmpfs /nix && exec unshare --user --map-user=%d --map-group=%d -- "$0" "$@"`, a.uid, a.gid)
	return append([]string{"/usr/bin/unshare", "--user", "--map-root-user", "--mount", "--", "/bin/sh", "-c", inner}, argv...)
}

// runOpts tune run.
type runOpts struct {
	dir     string
	env     []string
	stdin   string
	hide    bool // run in a root without /nix
	timeout time.Duration
	path    string // PATH (default /usr/bin:/bin)
}

// run runs argv as the account in a minimal environment plus the world's variables.
func (w *world) run(o runOpts, argv ...string) harness.Result {
	w.t.Helper()
	if o.timeout == 0 {
		o.timeout = 4 * time.Minute
	}
	if o.path == "" {
		o.path = "/usr/bin:/bin"
	}
	orig := argv
	if o.hide {
		argv = w.acc.hideNix(argv)
	}
	ctx, cancel := context.WithTimeout(context.Background(), o.timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir = o.dir
	cmd.Env = append(append([]string{"PATH=" + o.path, "TZ=UTC", "LC_ALL=C"}, w.env...), o.env...)
	if o.stdin != "" {
		cmd.Stdin = strings.NewReader(o.stdin)
	}
	w.acc.cred(cmd)
	var so, se bytes.Buffer
	cmd.Stdout, cmd.Stderr = &so, &se
	r := harness.Result{Args: orig[1:]}
	err := cmd.Run()
	r.Stdout, r.Stderr = so.String(), se.String()
	var ee *exec.ExitError
	switch {
	case err == nil:
	case errors.As(err, &ee):
		r.Exit = ee.ExitCode()
		if ee.ExitCode() == -1 { // killed by a signal
			r.Exit = -1
		}
	default:
		r.Exit = -1
		r.Stderr += "\nharness: " + err.Error()
	}
	return r
}

// bob runs bob as the account.
func (w *world) bob(dir string, args ...string) harness.Result {
	w.t.Helper()
	return w.run(runOpts{dir: dir}, append([]string{bobBin(w.t)}, args...)...)
}

// bobHide runs bob in a root without /nix.
func (w *world) bobHide(dir string, args ...string) harness.Result {
	w.t.Helper()
	return w.run(runOpts{dir: dir, hide: true}, append([]string{bobBin(w.t)}, args...)...)
}

// noLeak fails t when r shows raw Nix text to the user (AGENTS.md §5.5).
func noLeak(t testing.TB, r harness.Result) {
	t.Helper()
	for _, l := range leakcheck.Check([]byte(r.Combined())) {
		t.Errorf("raw Nix text in output of %s: %s", strings.Join(r.Args, " "), l)
	}
}

// exitIs reports whether r exited with want; otherwise it fails t with the output.
func exitIs(t testing.TB, r harness.Result, want int) bool {
	t.Helper()
	if r.Exit == want {
		return true
	}
	t.Errorf("%s: exit %d, want %d\nstdout: %s\nstderr: %s", strings.Join(r.Args, " "), r.Exit, want, r.Stdout, r.Stderr)
	return false
}

func readFile(t testing.TB, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read %s: %v", p, err)
	}
	return string(b)
}

func writeFile(t testing.TB, p, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// pinned realises a nixpkgs attribute of the harness pin through the host Nix and returns the
// path of its bin/<exe>. Names that tools/pins.json lists under "nix" go through
// serverenv.NixTool; shells and strace are not listed there, so they are realised from the same
// nixpkgs pin directly (reported to the coordinator: pins.json should list them).
func pinned(t testing.TB, name, attr, exe string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	if p, err := serverenv.NixTool(ctx, name); err == nil {
		if b := filepath.Join(p, "bin", exe); exists(b) {
			return b
		}
	}
	pins, err := serverenv.LoadPins()
	if err != nil {
		t.Fatalf("harness error: %v", err)
	}
	out, err := exec.CommandContext(ctx, "nix", "--extra-experimental-features", "nix-command flakes",
		"build", "--no-link", "--print-out-paths", pins.Nixpkgs+"#"+attr).Output()
	if err != nil {
		t.Fatalf("harness error: nix build %s: %v", attr, err)
	}
	for _, p := range strings.Fields(string(out)) {
		if b := filepath.Join(p, "bin", exe); exists(b) {
			return b
		}
	}
	t.Fatalf("harness error: %s has no bin/%s (%s)", attr, exe, out)
	return ""
}

func exists(p string) bool { _, err := os.Stat(p); return err == nil }

// profile returns the budget that applies to this machine: the [8c/15G] value on a host with at
// least 8 CPUs, else the [2c/4G] value (SPEC §1.17 profiles).
func profile(big, small float64) float64 {
	if runtime.NumCPU() >= 8 {
		return big
	}
	return small
}

// p95 returns the 95th percentile of ds (nearest rank).
func p95(ds []time.Duration) time.Duration {
	s := append([]time.Duration(nil), ds...)
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	return s[(len(s)*95+99)/100-1]
}

// projectB writes a small nested bob project (own bob.lock, so FindRoot stops there) into
// parent/name with the dev shell variable B_ENV=b, and hands it to the account.
func (w *world) projectB(parent, name string) string {
	w.t.Helper()
	d := filepath.Join(parent, name)
	writeFile(w.t, filepath.Join(d, "bob.go"), `//go:build bob

package sub

import (
	"github.com/megakuul/bob/pkg/bob"
)

var _ = bob.Repo(bob.RepoOpts{Nixpkgs: bob.NixOS2605})

// Dev is the default development shell of the nested project.
var Dev = bob.Define(func(p *bob.P) *bob.Shell {
	return bob.NewShell(p, bob.DevShell{Env: map[string]string{"B_ENV": "b"}})
})
`)
	writeFile(w.t, filepath.Join(d, "bob.lock"), "{\n  \"lockVersion\": 1\n}\n")
	writeFile(w.t, filepath.Join(d, "go.mod"), "module example.com/sub\n\ngo 1.26.0\n")
	w.acc.chownTree(w.t, d)
	return d
}

// exports parses the `export K='V';` lines of a bash enter script (SPEC §02.7.1 example).
func exports(script string) map[string]string {
	m := map[string]string{}
	for _, l := range strings.Split(script, "\n") {
		rest, ok := strings.CutPrefix(strings.TrimSpace(l), "export ")
		if !ok {
			continue
		}
		k, v, ok := strings.Cut(strings.TrimSuffix(rest, ";"), "=")
		if !ok {
			continue
		}
		m[k] = strings.ReplaceAll(strings.TrimSuffix(strings.TrimPrefix(v, "'"), "'"), `'\''`, "'")
	}
	return m
}

// harnessResult abbreviates harness.Result in test code.
type harnessResult = harness.Result

// redIf marks t red when r is the stub answer.
func redIf(t testing.TB, r harness.Result) bool { return harness.Red(t, r) }

// scratch returns an empty directory the account owns.
func (w *world) scratch() string {
	w.t.Helper()
	d := filepath.Join(w.t.TempDir(), "scratch")
	if err := os.MkdirAll(d, 0o755); err != nil {
		w.t.Fatal(err)
	}
	w.acc.chownTree(w.t, d)
	return d
}

// local copies tests/devenv/testdata/repos/<name> into a fresh git repository owned by the
// account (fixtures that only this suite needs).
func (w *world) local(name string) string {
	w.t.Helper()
	src := filepath.Join(harness.Root(w.t), "tests", "devenv", "testdata", "repos", name)
	dst := filepath.Join(w.t.TempDir(), name)
	err := filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		out := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(out, 0o755)
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(out, b, 0o644)
	})
	if err != nil {
		w.t.Fatalf("harness error: copy %s: %v", name, err)
	}
	for _, args := range [][]string{
		{"init", "-q"}, {"add", "-A"},
		{"-c", "user.name=bob-harness", "-c", "user.email=harness@bob.invalid", "commit", "-q", "-m", "fixture"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dst
		if b, err := cmd.CombinedOutput(); err != nil {
			w.t.Fatalf("harness error: git %v: %v: %s", args, err, b)
		}
	}
	w.acc.chownTree(w.t, filepath.Dir(dst))
	return dst
}

func jsonUnmarshal(s string, v any) error { return json.Unmarshal([]byte(s), v) }
