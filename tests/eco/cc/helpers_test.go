package cc

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
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
	"github.com/megakuul/bob/pkg/engine/drv"
	"github.com/megakuul/bob/tests/errors/leakcheck"
	"github.com/megakuul/bob/tests/harness"
	"github.com/megakuul/bob/tests/serverenv"
)

// bobIn runs bob in dir with its own bob home; calls sharing a home share one store. Output
// that shows raw Nix text fails t (AGENTS.md §5.5) unless -v is given.
func bobIn(t *testing.T, dir, home string, args ...string) harness.Result {
	t.Helper()
	return bobEnv(t, dir, []string{"BOB_HOME=" + home}, args...)
}

func bobEnv(t *testing.T, dir string, env []string, args ...string) harness.Result {
	t.Helper()
	// The archive server of this package is plain http on loopback (A109).
	env = append([]string{"BOB_TEST_HTTP_LOOPBACK=1"}, env...)
	r := harness.Run(t, "bob", harness.Spec{Dir: dir, Env: env, Timeout: 5 * time.Minute}, args...)
	noLeak(t, r, args)
	return r
}

func noLeak(t *testing.T, r harness.Result, args []string) {
	t.Helper()
	for _, a := range args {
		if a == "-v" || a == "-vv" {
			return
		}
	}
	for _, l := range leakcheck.Check([]byte(withoutFinishPaths(r.Stdout) + r.Stderr)) {
		t.Errorf("bob %s shows raw Nix text: %s", strings.Join(r.Args, " "), l)
	}
}

// withoutFinishPaths drops the "paths" of package.finish events from --json output: with
// --print-paths they are the outputs' host store paths by contract (SPEC §02.4, same as
// TestRFW051, A116), not leaked Nix text.
func withoutFinishPaths(stdout string) string {
	var b strings.Builder
	for _, line := range strings.Split(stdout, "\n") {
		var e map[string]any
		if json.Unmarshal([]byte(line), &e) == nil && e["event"] == "package.finish" {
			delete(e, "paths")
			if nb, err := json.Marshal(e); err == nil {
				line = string(nb)
			}
		}
		b.WriteString(line + "\n")
	}
	return b.String()
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

// ok is the usual first check after a bob run that must succeed: false (and t failed) when the
// feature is not built yet (red) or the run failed.
func ok(t *testing.T, r harness.Result) bool {
	t.Helper()
	return !harness.Red(t, r) && exitIs(t, r, 0)
}

// event is one --json line (SPEC §02.4).
type event map[string]any

func (e event) str(k string) string { s, _ := e[k].(string); return s }

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
	Code    string   `json:"code"`
	Message string   `json:"message"`
	Label   string   `json:"label"`
	Hint    string   `json:"hint"`
	Excerpt []string `json:"excerpt"`
}

// errorWithCode returns the first error event of r with code, failing t when there is none.
func errorWithCode(t *testing.T, r harness.Result, code string) (bobError, bool) {
	t.Helper()
	var all []bobError
	for _, e := range eventsNamed(events(t, r), "error") {
		b, _ := json.Marshal(e["error"])
		var be bobError
		if err := json.Unmarshal(b, &be); err != nil {
			continue
		}
		if be.Code == code {
			return be, true
		}
		all = append(all, be)
	}
	t.Errorf("bob %s: no error event with code %s; errors: %+v\nstderr:\n%s", strings.Join(r.Args, " "), code, all, r.Stderr)
	return bobError{}, false
}

// outputPaths builds args with --print-paths and returns label → output paths.
func outputPaths(t *testing.T, dir, home string, args ...string) (map[string][]string, bool) {
	t.Helper()
	r := bobIn(t, dir, home, append([]string{"build", "--print-paths", "--json"}, args...)...)
	if !ok(t, r) {
		return nil, false
	}
	out := map[string][]string{}
	for _, e := range eventsNamed(events(t, r), "package.finish") {
		ps, _ := e["paths"].([]any)
		for _, p := range ps {
			s, _ := p.(string)
			out[e.str("label")] = append(out[e.str("label")], s)
		}
	}
	return out, true
}

// treeDigest hashes names, modes, link targets and contents below path.
func treeDigest(t *testing.T, path string) string {
	t.Helper()
	h := sha256.New()
	err := filepath.WalkDir(path, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(path, p)
		info, err := d.Info()
		if err != nil {
			return err
		}
		h.Write([]byte(rel + "\x00" + info.Mode().String() + "\x00"))
		switch {
		case info.Mode()&fs.ModeSymlink != 0:
			l, err := os.Readlink(p)
			if err != nil {
				return err
			}
			h.Write([]byte(l))
		case info.Mode().IsRegular():
			b, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			h.Write(b)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("harness: digest %s: %v", path, err)
	}
	return hex.EncodeToString(h.Sum(nil))
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}

// writeFile writes a user edit; generated files are read-only (R-FW-060) so it chmods first.
func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("harness: %v", err)
	}
	_ = os.Chmod(path, 0o644)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("harness: %v", err)
	}
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

// runEnvI runs binary with an empty environment (env -i) and returns the result.
func runEnvI(t *testing.T, binary string, args ...string) harness.Result {
	t.Helper()
	cmd := exec.Command(binary, args...)
	cmd.Env = []string{}
	cmd.Dir = t.TempDir()
	return runCmd(t, cmd, append([]string{binary}, args...))
}

// hostPlatform is the bob platform of this machine; otherPlatform the one bob cross-builds for.
func hostPlatform(t *testing.T) bob.Platform {
	t.Helper()
	p, _, ok := platforms(t)
	if !ok {
		t.Fatalf("harness: unsupported host architecture")
	}
	return p
}

func platforms(t *testing.T) (host, other bob.Platform, ok bool) {
	t.Helper()
	switch runtime.GOARCH {
	case "arm64":
		return bob.LinuxARM64, bob.LinuxAMD64, true
	case "amd64":
		return bob.LinuxAMD64, bob.LinuxARM64, true
	}
	return "", "", false
}

// pinnedTool returns the store path of a nixpkgs tool of the pin (HARNESS.md §1.4).
func pinnedTool(t *testing.T, name string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	p, err := serverenv.NixTool(ctx, name)
	if err != nil {
		t.Fatalf("%s from the nixpkgs pin: %v", name, err)
	}
	return p
}

// runGuest runs the binary of platform plat: directly for the host platform, under qemu-user
// (store of home as the guest root) for the other one, always with an empty environment.
func runGuest(t *testing.T, plat bob.Platform, home, binary string, args ...string) harness.Result {
	t.Helper()
	host, _, _ := platforms(t)
	if plat == host {
		return runEnvI(t, binary, args...)
	}
	arch := map[bob.Platform]string{bob.LinuxAMD64: "x86_64", bob.LinuxARM64: "aarch64"}[plat]
	qemu := filepath.Join(pinnedTool(t, "qemu-user"), "bin", "qemu-"+arch)
	cmd := exec.Command(qemu, append([]string{"-L", home, binary}, args...)...)
	cmd.Env = []string{}
	cmd.Dir = t.TempDir()
	return runCmd(t, cmd, append([]string{"qemu-" + arch, binary}, args...))
}

// unprivileged returns the uid/gid of the harness test user (bobtest) when the tests run as
// root, or the current ids otherwise.
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

// chownTree hands dir to uid/gid and makes its parents traversable (a no-op when not root).
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
	for p := filepath.Dir(dir); p != "/"; p = filepath.Dir(p) {
		if info, err := os.Stat(p); err == nil && info.Mode().Perm()&0o001 == 0 {
			_ = os.Chmod(p, info.Mode().Perm()|0o011)
		}
	}
}

// runAs runs path as uid/gid (when the test runs as root) with HOME=home in dir.
func runAs(t *testing.T, uid, gid uint32, home, dir string, env []string, path string, args ...string) harness.Result {
	t.Helper()
	cmd := exec.Command(path, args...)
	cmd.Dir = dir
	cmd.Env = append([]string{"PATH=/usr/bin:/bin", "HOME=" + home, "TZ=UTC", "LC_ALL=C", "BOB_TEST_HTTP_LOOPBACK=1"}, env...)
	if os.Geteuid() == 0 {
		cmd.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: uid, Gid: gid}}
		if info, err := os.Stat(filepath.Dir(path)); err == nil && info.Mode().Perm()&0o055 != 0o055 {
			_ = os.Chmod(filepath.Dir(path), 0o755)
		}
	}
	r := runCmd(t, cmd, append([]string{path}, args...))
	noLeak(t, r, args)
	return r
}

// jobSpec is the bobSpec of a bob derivation (structured attribute or environment variable).
func jobSpec(d *drv.Derivation) string {
	if v, ok := d.Structured["bobSpec"]; ok {
		b, _ := json.Marshal(v)
		return string(b)
	}
	return d.Env["bobSpec"]
}

// drvStore parses every .drv of the store of home: file name → derivation. ok is false when
// drv.Parse is not implemented (t is then red).
func drvStore(t *testing.T, home string) (map[string]*drv.Derivation, bool) {
	t.Helper()
	store := filepath.Join(home, "nix", "store")
	ents, err := os.ReadDir(store)
	if err != nil {
		t.Fatalf("bob home has no store: %v", err)
	}
	out := map[string]*drv.Derivation{}
	for _, e := range ents {
		if !strings.HasSuffix(e.Name(), ".drv") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(store, e.Name()))
		if err != nil {
			t.Fatalf("read %s: %v", e.Name(), err)
		}
		d, err := drv.Parse(b)
		if err != nil && strings.Contains(err.Error(), harness.NotImplemented) {
			harness.RedFeature(t, "drv.Parse")
			return nil, false
		}
		if err != nil {
			t.Errorf("%s: %v", e.Name(), err)
			continue
		}
		out[e.Name()] = d
	}
	return out, true
}

// bobDrvs keeps the derivations built by bob-builder.
func bobDrvs(all map[string]*drv.Derivation) map[string]*drv.Derivation {
	out := map[string]*drv.Derivation{}
	for n, d := range all {
		if strings.HasSuffix(d.Builder, "/bin/bob-builder") {
			out[n] = d
		}
	}
	return out
}

// assertTests runs `bob test` for the test units on platform plat: natively with //..., for
// the foreign platform with the units' labels suffixed @<platform> (R-FW-039: `bob test` has
// no --platform flag; a foreign-architecture test runs under qemu-user and its test event
// names the runner qemu-<arch>). Every unit must pass.
func assertTests(t *testing.T, dir, home string, plat, host bob.Platform, units []string) {
	t.Helper()
	args := []string{"test"}
	if plat == host {
		args = append(args, "//...")
	} else {
		for _, u := range units {
			args = append(args, u+"@"+string(plat))
		}
	}
	tr := bobIn(t, dir, home, append(args, "--json")...)
	if !ok(t, tr) {
		return
	}
	evs := events(t, tr)
	got := map[string]string{}
	for _, e := range eventsNamed(evs, "test.result") {
		got[strings.SplitN(e.str("label"), "@", 2)[0]] = e.str("status")
	}
	for _, u := range units {
		if s := got[u]; s != "pass" && s != "cached" {
			t.Errorf("test unit %s on %s has status %q, want pass", u, plat, s)
		}
	}
	if plat != host {
		runner := "qemu-" + map[bob.Platform]string{bob.LinuxAMD64: "x86_64", bob.LinuxARM64: "aarch64"}[plat]
		found := false
		for _, e := range evs {
			if e.str("runner") == runner {
				found = true
			}
		}
		if !found {
			t.Errorf("no test event with runner %q: foreign-platform tests must run under qemu-user", runner)
		}
	}
}
