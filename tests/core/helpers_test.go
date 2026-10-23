package core

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/megakuul/bob/pkg/bob"
	"github.com/megakuul/bob/pkg/bob/diag"
	_ "github.com/megakuul/bob/pkg/cli" // links the kinds and ecosystems the bob binary links (R-FW-051)
	"github.com/megakuul/bob/pkg/engine/eval"
	_ "github.com/megakuul/bob/tests/core/toyeco" // registers the toy kinds in the test binary (A68)
	"github.com/megakuul/bob/tests/errors/leakcheck"
	"github.com/megakuul/bob/tests/harness"
)

// repo copies testdata/repos/<name> into a fresh directory, makes it a git repository with one
// commit (like harness.Fixture) and returns the directory.
func repo(t *testing.T, name string) string {
	t.Helper()
	dst := filepath.Join(t.TempDir(), filepath.Base(name))
	copyTree(t, filepath.Join(harness.Root(t), "tests", "core", "testdata", "repos", name), dst)
	gitInit(t, dst)
	return dst
}

func copyTree(t *testing.T, src, dst string) {
	t.Helper()
	err := filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		out := filepath.Join(dst, rel)
		info, err := d.Info()
		if err != nil {
			return err
		}
		switch {
		case d.IsDir():
			return os.MkdirAll(out, 0o755)
		case info.Mode()&fs.ModeSymlink != 0:
			l, err := os.Readlink(p)
			if err != nil {
				return err
			}
			return os.Symlink(l, out)
		default:
			b, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			return os.WriteFile(out, b, info.Mode().Perm())
		}
	})
	if err != nil {
		t.Fatalf("harness: copy %s: %v", src, err)
	}
}

func gitInit(t *testing.T, dir string) {
	t.Helper()
	for _, args := range [][]string{
		{"init", "-q"}, {"add", "-A"},
		{"-c", "user.name=bob-harness", "-c", "user.email=harness@bob.invalid", "commit", "-q", "--allow-empty", "-m", "fixture"},
	} {
		git(t, dir, args...)
	}
}

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	b, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("harness: git %v: %v: %s", args, err, b)
	}
	return string(b)
}

// bobRun runs bob in dir and fails t if the output shows raw Nix text (AGENTS.md §5.5).
func bobRun(t *testing.T, dir string, args ...string) harness.Result {
	t.Helper()
	return bobRunEnv(t, dir, nil, args...)
}

func bobRunEnv(t *testing.T, dir string, env []string, args ...string) harness.Result {
	t.Helper()
	r := harness.Run(t, "bob", harness.Spec{Dir: dir, Env: env, Timeout: 5 * time.Minute}, args...)
	if !hasArg(args, "-v") {
		noLeak(t, r)
	}
	return r
}

func hasArg(args []string, a string) bool {
	for _, x := range args {
		if x == a {
			return true
		}
	}
	return false
}

func noLeak(t *testing.T, r harness.Result) {
	t.Helper()
	for _, l := range leakcheck.Check([]byte(r.Combined())) {
		t.Errorf("bob %s shows raw Nix text: %s", strings.Join(r.Args, " "), l)
	}
}

// exitIs fails t unless r exited with want.
func exitIs(t *testing.T, r harness.Result, want int) bool {
	t.Helper()
	if r.Exit != want {
		t.Errorf("bob %s exited %d, want %d\nstdout:\n%s\nstderr:\n%s", strings.Join(r.Args, " "), r.Exit, want, r.Stdout, r.Stderr)
		return false
	}
	return true
}

// event is one --json line (SPEC §02.4).
type event map[string]any

func (e event) str(k string) string { s, _ := e[k].(string); return s }

// events parses the NDJSON of a --json run; every line must be a schema-v1 event.
func events(t *testing.T, r harness.Result) []event {
	t.Helper()
	var out []event
	for _, line := range strings.Split(r.Stdout, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var e event
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			t.Errorf("bob %s: --json line is not JSON: %q", strings.Join(r.Args, " "), line)
			continue
		}
		if v, _ := e["v"].(float64); v != 1 || e.str("event") == "" {
			t.Errorf("bob %s: --json line is not a v1 event: %q", strings.Join(r.Args, " "), line)
		}
		out = append(out, e)
	}
	return out
}

func eventsNamed(evs []event, name string) []event {
	var out []event
	for _, e := range evs {
		if e.str("event") == name {
			out = append(out, e)
		}
	}
	return out
}

// bobError is the error object of an error event.
type bobError struct {
	Code      string            `json:"code"`
	Class     string            `json:"class"`
	Message   string            `json:"message"`
	Label     string            `json:"label"`
	DefinedAt string            `json:"definedAt"`
	Location  string            `json:"location"`
	Hint      string            `json:"hint"`
	Excerpt   []string          `json:"excerpt"`
	Args      map[string]string `json:"args"`
}

func errorsOf(t *testing.T, evs []event) []bobError {
	t.Helper()
	var out []bobError
	for _, e := range eventsNamed(evs, "error") {
		b, err := json.Marshal(e["error"])
		if err != nil {
			t.Fatalf("harness: %v", err)
		}
		var be bobError
		if err := json.Unmarshal(b, &be); err != nil {
			t.Errorf("error event has a malformed error object: %s", b)
			continue
		}
		out = append(out, be)
	}
	return out
}

// errorWithCode returns the first error event with code, failing t when there is none.
func errorWithCode(t *testing.T, r harness.Result, code string) (bobError, bool) {
	t.Helper()
	errs := errorsOf(t, events(t, r))
	for _, e := range errs {
		if e.Code == code {
			return e, true
		}
	}
	t.Errorf("bob %s: no error event with code %s; errors: %+v\nstderr:\n%s", strings.Join(r.Args, " "), code, errs, r.Stderr)
	return bobError{}, false
}

// isNotImplemented reports whether err is the BOB-E-9008 answer of a stub. Engine layer E3
// cannot import diag, so its stubs carry the code in the text.
func isNotImplemented(err error) bool {
	var d *diag.Error
	if errors.As(err, &d) {
		return d.Code == diag.CodeNotImplemented
	}
	return err != nil && strings.Contains(err.Error(), harness.NotImplemented)
}

// evaluate runs the evaluator of the repository at root with patterns (default //...) and
// returns the IR bytes. ok is false when t has failed (red or a real failure).
func evaluate(t *testing.T, root string, patterns ...string) (res *eval.Result, ok bool) {
	t.Helper()
	if len(patterns) == 0 {
		patterns = []string{"//..."}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	res, err := eval.Evaluate(ctx, eval.Request{Root: root, Patterns: patterns})
	if isNotImplemented(err) {
		harness.RedFeature(t, "eval.Evaluate")
		return nil, false
	}
	if err != nil {
		t.Errorf("evaluate %s: %v", root, err)
		return nil, false
	}
	return res, true
}

// hostPlatform is the bob platform of this machine.
func hostPlatform(t *testing.T) bob.Platform {
	t.Helper()
	switch runtime.GOARCH {
	case "arm64":
		return bob.LinuxARM64
	case "amd64":
		return bob.LinuxAMD64
	}
	t.Fatalf("harness: unsupported host architecture %s", runtime.GOARCH)
	return ""
}

// otherPlatform is the platform bob cross-builds for from this machine.
func otherPlatform(t *testing.T) bob.Platform {
	if hostPlatform(t) == bob.LinuxARM64 {
		return bob.LinuxAMD64
	}
	return bob.LinuxARM64
}

// nixSystem maps a bob platform to its Nix system.
func nixSystem(p bob.Platform) string {
	if p == bob.LinuxAMD64 {
		return "x86_64-linux"
	}
	return "aarch64-linux"
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("harness: %v", err)
	}
	// Generated files are read-only (R-FW-060); a hand edit makes them writable first.
	_ = os.Chmod(path, 0o644)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("harness: %v", err)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}

// replaceIn replaces the first occurrence of old in file, failing t when old is absent.
func replaceIn(t *testing.T, path, old, new string) {
	t.Helper()
	s := readFile(t, path)
	if !strings.Contains(s, old) {
		t.Fatalf("harness: %s does not contain %q", path, old)
	}
	writeFile(t, path, strings.Replace(s, old, new, 1))
}

// treeHash summarises every file of dir except .git (path, mode, content) for "changes no
// file" checks.
func treeHash(t *testing.T, dir string) string {
	t.Helper()
	return treeHashSkip(t, dir)
}

// treeHashSkip is treeHash without the repo-relative paths that start with one of skip.
func treeHashSkip(t *testing.T, dir string, skip ...string) string {
	t.Helper()
	var b strings.Builder
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		if d.IsDir() && rel == ".git" {
			return filepath.SkipDir
		}
		for _, s := range skip {
			if strings.HasPrefix(rel, s) {
				if d.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		b.WriteString(rel + " " + info.Mode().String())
		if info.Mode().IsRegular() {
			c, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			b.WriteString(" " + strconv.Itoa(len(c)) + " " + string(c))
		}
		b.WriteString("\n")
		return nil
	})
	if err != nil {
		t.Fatalf("harness: %v", err)
	}
	return b.String()
}

// runCmd runs cmd to completion (≤ 5 min) and returns its result like harness.Run.
func runCmd(t *testing.T, cmd *exec.Cmd, args []string) harness.Result {
	t.Helper()
	var so, se bytes.Buffer
	cmd.Stdout, cmd.Stderr = &so, &se
	r := harness.Result{Args: args}
	done := make(chan error, 1)
	if err := cmd.Start(); err != nil {
		r.Exit = -1
		r.Stderr = "harness: " + err.Error()
		return r
	}
	go func() { done <- cmd.Wait() }()
	var err error
	select {
	case err = <-done:
	case <-time.After(5 * time.Minute):
		_ = cmd.Process.Kill()
		err = <-done
		se.WriteString("\nharness: killed after 5 minutes")
	}
	r.Stdout, r.Stderr = so.String(), se.String()
	var ee *exec.ExitError
	switch {
	case err == nil:
	case errors.As(err, &ee):
		r.Exit = ee.ExitCode()
	default:
		r.Exit = -1
		r.Stderr += "\nharness: " + err.Error()
	}
	return r
}

// bobNoNetworkHome runs bob in a new user and network namespace that has no interface up and
// no route, so any network request fails (§03.0.3 harness rule), with BOB_HOME=bobHome (caches
// warmed by an earlier run with the same BOB_HOME); an empty bobHome means a fresh, empty home.
func bobNoNetworkHome(t *testing.T, dir, bobHome string, args ...string) harness.Result {
	t.Helper()
	cmd := exec.Command(harness.Bin(t, "bob"), args...)
	cmd.Dir = dir
	home := filepath.Join(t.TempDir(), "home")
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatalf("harness: %v", err)
	}
	cmd.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + home, "TZ=UTC", "LC_ALL=C"}
	if bobHome != "" {
		cmd.Env = append(cmd.Env, "BOB_HOME="+bobHome)
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Cloneflags:  syscall.CLONE_NEWUSER | syscall.CLONE_NEWNET,
		UidMappings: []syscall.SysProcIDMap{{ContainerID: os.Getuid(), HostID: os.Getuid(), Size: 1}},
		GidMappings: []syscall.SysProcIDMap{{ContainerID: os.Getgid(), HostID: os.Getgid(), Size: 1}},
	}
	r := runCmd(t, cmd, args)
	noLeak(t, r)
	return r
}

// unprivileged returns the uid/gid of the harness test user (bobtest) when the tests run as
// root, or the current ids otherwise (HARNESS.md: non-root runs use bobtest).
func unprivileged(t *testing.T) (uid, gid uint32) {
	t.Helper()
	if os.Geteuid() != 0 {
		return uint32(os.Getuid()), uint32(os.Getgid())
	}
	u, err := user.Lookup("bobtest")
	if err != nil {
		t.Fatalf("harness: the test user bobtest is missing (docs/environment.md): %v", err)
	}
	ui, _ := strconv.Atoi(u.Uid)
	gi, _ := strconv.Atoi(u.Gid)
	return uint32(ui), uint32(gi)
}

// chownTree hands dir to uid/gid so an unprivileged run can use it.
func chownTree(t *testing.T, dir string, uid, gid uint32) {
	t.Helper()
	if os.Geteuid() != 0 {
		return
	}
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		return os.Lchown(p, int(uid), int(gid))
	})
	if err != nil {
		t.Fatalf("harness: chown %s: %v", dir, err)
	}
	// Every parent must be traversable by the user.
	for p := filepath.Dir(dir); p != "/"; p = filepath.Dir(p) {
		if info, err := os.Stat(p); err == nil && info.Mode().Perm()&0o001 == 0 {
			_ = os.Chmod(p, info.Mode().Perm()|0o011)
		}
	}
}

// lines returns the non-empty lines of s.
func lines(s string) []string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		if strings.TrimSpace(l) != "" {
			out = append(out, l)
		}
	}
	return out
}

// execShim returns the gopls command of the .bob/bin shim of dir (written by bob env) with env.
func execShim(t *testing.T, dir string, env []string) *exec.Cmd {
	t.Helper()
	home := filepath.Join(t.TempDir(), "home")
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatalf("harness: %v", err)
	}
	cmd := exec.Command(filepath.Join(dir, ".bob", "bin", "gopls"))
	cmd.Dir = dir
	cmd.Env = append([]string{"PATH=/usr/bin:/bin", "HOME=" + home, "TZ=UTC", "LC_ALL=C"}, env...)
	return cmd
}
