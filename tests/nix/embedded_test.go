package nix

import (
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/megakuul/bob/pkg/bob/diag"
	"github.com/megakuul/bob/tests/harness"
	"github.com/megakuul/bob/tests/parity"
)

// decoyNix writes an executable `nix` (and friends) that records every execution, to be put first
// on PATH: a bob that executes the host's nix leaves a trace.
func decoyNix(t testing.TB) (dir, log string) {
	t.Helper()
	dir = filepath.Join(t.TempDir(), "decoy")
	log = filepath.Join(t.TempDir(), "decoy.log")
	for _, n := range []string{"nix", "nix-store", "nix-build", "nix-instantiate", "nix-env", "nix-hash", "nix-daemon"} {
		mustWrite(t, filepath.Join(dir, n), []byte("#!/bin/sh\necho \"$0 $*\" >> "+log+"\nexit 97\n"), 0o755)
	}
	return dir, log
}

func nixBinDir(t testing.TB, home string) string {
	t.Helper()
	m, _ := filepath.Glob(filepath.Join(home, "bin", "nix-*", "bin"))
	if len(m) != 1 {
		t.Fatalf("want exactly one <home>/bin/nix-<hash>/bin, found %v", m)
	}
	return m[0]
}

// TestRNIX020EmbeddedStaticNix: the first bob command extracts Nix to
// <home>/bin/nix-<12 hex>/bin/nix with the six symlinks; later runs only stat; the host's nix is
// never executed (decoys first on PATH); ATTRIBUTION.md lists Nix and busybox.
func TestRNIX020EmbeddedStaticNix(t *testing.T) {
	parity.Covers(t, "R-NIX-020")
	e := newBobEnv(t)
	decoy, log := decoyNix(t)
	path := "PATH=" + decoy + ":/usr/bin:/bin"
	r := e.runEnv(t, "cpp-lib", []string{path}, "build", "//...")
	if harness.Red(t, r) {
		return
	}
	if r.Exit != 0 {
		t.Fatalf("bob build exit %d:\n%s", r.Exit, r.Combined())
	}
	bin := nixBinDir(t, e.Home)
	if !regexp.MustCompile(`/nix-[0-9a-f]{12}/bin$`).MatchString(bin) {
		t.Errorf("extraction directory %s is not nix-<12 hex>/bin", bin)
	}
	if fi, err := os.Stat(filepath.Join(bin, "nix")); err != nil || fi.Mode()&0o111 == 0 {
		t.Errorf("%s/nix is not an executable file: %v", bin, err)
	}
	for _, l := range []string{"nix-store", "nix-build", "nix-instantiate", "nix-env", "nix-hash", "nix-daemon"} {
		if to, err := os.Readlink(filepath.Join(bin, l)); err != nil || to != "nix" {
			t.Errorf("%s -> %q (%v), want a symlink to nix", l, to, err)
		}
	}
	if b, err := os.ReadFile(log); err == nil && len(b) > 0 {
		t.Errorf("the host's nix was executed:\n%s", b)
	}
	before, _ := os.Stat(filepath.Join(bin, "nix"))
	dirBefore, _ := os.Stat(filepath.Dir(bin))
	time.Sleep(20 * time.Millisecond)
	if r2 := e.runEnv(t, "cpp-lib", []string{path}, "build", "//..."); r2.Exit != 0 {
		t.Fatalf("second build exit %d:\n%s", r2.Exit, r2.Combined())
	}
	after, _ := os.Stat(filepath.Join(bin, "nix"))
	dirAfter, _ := os.Stat(filepath.Dir(bin))
	if !before.ModTime().Equal(after.ModTime()) || !dirBefore.ModTime().Equal(dirAfter.ModTime()) {
		t.Errorf("a later run rewrote the extracted Nix (mtime changed): it must only stat")
	}
	if b, err := os.ReadFile(log); err == nil && len(b) > 0 {
		t.Errorf("the host's nix was executed by the second run:\n%s", b)
	}
}

// TestRNIX020FirstExtractionBudget is the budget clause of the acceptance: the first extraction
// takes <= 500 ms [8c/15G] (measured 294 ms). No row of tests/budgets/budgets.json carries it
// (B-009 is the namespace entry), so it is asserted here. The temporary entry is the one whose
// name starts with "." or contains "nix". Extraction writes a temporary entry
// in <home>/bin and renames it atomically to nix-<12 hex>: the time between the first entry
// appearing and the final directory appearing, polled every millisecond, is the extraction.
func TestRNIX020FirstExtractionBudget(t *testing.T) {
	parity.Covers(t, "R-NIX-020")
	e := newBobEnv(t)
	cmd := exec.Command(harness.Bin(t, "bob"), "build", "//...")
	cmd.Dir = e.dir(t, "cpp-lib")
	cmd.Env = append([]string{"PATH=/usr/bin:/bin", "HOME=" + t.TempDir(), "TZ=UTC", "LC_ALL=C"}, e.env()...)
	var out strings.Builder
	cmd.Stdout, cmd.Stderr = &out, &out
	final := regexp.MustCompile(`^nix-[0-9a-f]{12}$`)
	binDir := filepath.Join(e.Home, "bin")
	if err := cmd.Start(); err != nil {
		t.Fatalf("harness: %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	var first, last time.Time
	var werr error
poll:
	for {
		select {
		case werr = <-done:
			break poll
		case <-time.After(time.Millisecond):
		}
		ents, err := os.ReadDir(binDir)
		if err != nil {
			continue
		}
		for _, de := range ents {
			if final.MatchString(de.Name()) {
				if last.IsZero() {
					last = time.Now()
				}
			} else if first.IsZero() && (strings.HasPrefix(de.Name(), ".") || strings.Contains(de.Name(), "nix")) {
				first = time.Now()
			}
		}
	}
	if strings.Contains(out.String(), "BOB-E-9008") {
		t.Errorf("red: not implemented yet: bob build //... -> %s", strings.SplitN(out.String(), "\n", 2)[0])
		return
	}
	if werr != nil {
		t.Fatalf("bob build //...: %v\n%s", werr, out.String())
	}
	if last.IsZero() {
		t.Fatalf("no <home>/bin/nix-<12 hex> appeared during the first bob command")
	}
	if !first.IsZero() && last.Sub(first) > 500*time.Millisecond+5*time.Millisecond {
		t.Errorf("first extraction took %v, want <= 500 ms (R-NIX-020; measured 294 ms)", last.Sub(first))
	}
}

// TestRNIX020Attribution: ATTRIBUTION.md lists Nix (LGPL-2.1-or-later) and busybox (GPL-2.0).
func TestRNIX020Attribution(t *testing.T) {
	parity.Covers(t, "R-NIX-020")
	text := string(mustRead(t, filepath.Join(harness.Root(t), "ATTRIBUTION.md")))
	for _, want := range []string{"Nix", "LGPL-2.1-or-later", "busybox", "GPL-2.0"} {
		if !strings.Contains(text, want) {
			t.Errorf("ATTRIBUTION.md does not mention %q", want)
		}
	}
	if !strings.Contains(text, "https://") {
		harness.RedFeature(t, "ATTRIBUTION.md gets the source locations of Nix, its static dependencies and busybox when embedding lands")
	}
}

const nixConfTemplate = `# Generated by bob. DO NOT EDIT.
build-users-group =
sandbox = true
sandbox-fallback = false
require-drop-supplementary-groups = false
experimental-features = nix-command
substituters = https://cache.nixos.org
trusted-public-keys = cache.nixos.org-1:6NCHdD59X431o0gWypbMrAURkbJ16ZPMQFGspcDShjY=
netrc-file = <home>/etc/nix/netrc
narinfo-cache-negative-ttl = 60
fallback = true
connect-timeout = 5
max-jobs = auto
cores = 0
eval-cache = false
allow-import-from-derivation = false
auto-optimise-store = false
warn-dirty = false
`

// TestRNIX024StateLayoutAndNixConf: the state home layout and the generated nix.conf (dev
// profile) are exactly as specified, with modes.
func TestRNIX024StateLayoutAndNixConf(t *testing.T) {
	parity.Covers(t, "R-NIX-024")
	e := newBobEnv(t)
	r := e.run(t, "cpp-lib", "build", "//...")
	if harness.Red(t, r) {
		return
	}
	if r.Exit != 0 {
		t.Fatalf("bob build exit %d:\n%s", r.Exit, r.Combined())
	}
	for _, d := range []string{"nix", "xdg/config", "xdg/cache", "xdg/data", "xdg/state", "cache/go-build", "cache/drv", "cache/nixpkgs", "cache/eval", "etc/nix"} {
		if fi, err := os.Stat(filepath.Join(e.Home, filepath.FromSlash(d))); err != nil || !fi.IsDir() {
			t.Errorf("<home>/%s is not a directory: %v", d, err)
		}
	}
	// SPEC gives modes for nix.conf (0644) and netrc (0600) only; user.conf is just "empty".
	for f, mode := range map[string]os.FileMode{"etc/nix/nix.conf": 0o644, "etc/nix/netrc": 0o600} {
		fi, err := os.Stat(filepath.Join(e.Home, filepath.FromSlash(f)))
		if err != nil {
			t.Errorf("<home>/%s: %v", f, err)
			continue
		}
		if fi.Mode().Perm() != mode {
			t.Errorf("<home>/%s mode %v, want %v", f, fi.Mode().Perm(), mode)
		}
	}
	if fi, err := os.Stat(filepath.Join(e.Home, "etc", "nix", "user.conf")); err != nil {
		t.Errorf("<home>/etc/nix/user.conf: %v", err)
	} else if fi.Size() != 0 {
		t.Errorf("user.conf must be empty, has %d bytes", fi.Size())
	}
	if _, err := os.Stat(filepath.Join(e.Home, "lock")); err != nil {
		t.Errorf("<home>/lock: %v", err)
	}
	want := strings.ReplaceAll(nixConfTemplate, "<home>", e.Home)
	if got := string(mustRead(t, filepath.Join(e.Home, "etc", "nix", "nix.conf"))); got != want {
		t.Errorf("nix.conf differs from the specification:\n--- got\n%s--- want\n%s", got, want)
	}
}

// printEnv parses `bob doctor --print-nix-env` (KEY=VALUE lines).
func printEnv(t testing.TB, r harness.Result) map[string]string {
	t.Helper()
	m := map[string]string{}
	for _, l := range strings.Split(strings.TrimSpace(r.Stdout), "\n") {
		if k, v, ok := strings.Cut(l, "="); ok {
			m[k] = v
		}
	}
	return m
}

// TestRNIX024NixEnvironment: `bob doctor --print-nix-env` shows exactly the specified variables;
// NIX_PATH, NIX_CONFIG and a user's ~/.config/nix/nix.conf have no effect.
func TestRNIX024NixEnvironment(t *testing.T) {
	parity.Covers(t, "R-NIX-024")
	e := newBobEnv(t)
	userHome := t.TempDir()
	bogusHits := atomic.Int64{}
	bogus := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { bogusHits.Add(1) }))
	defer bogus.Close()
	mustWrite(t, filepath.Join(userHome, ".config", "nix", "nix.conf"), []byte("substituters = "+bogus.URL+"\nsandbox = false\n"), 0o644)
	hostile := []string{"HOME=" + userHome, "NIX_PATH=nixpkgs=/nonexistent", "NIX_CONFIG=substituters = " + bogus.URL,
		"NIX_REMOTE=daemon", "NIX_STORE_DIR=/elsewhere", "TERM=xterm", "TMPDIR=" + t.TempDir()}
	r := e.runEnv(t, "cpp-lib", hostile, "doctor", "--print-nix-env")
	if harness.Red(t, r) {
		return
	}
	if r.Exit != 0 {
		t.Fatalf("doctor exit %d:\n%s", r.Exit, r.Combined())
	}
	got := printEnv(t, r)
	home := e.Home
	exact := map[string]string{
		"NIX_CONF_DIR":        filepath.Join(home, "etc", "nix"),
		"NIX_USER_CONF_FILES": filepath.Join(home, "etc", "nix", "user.conf"),
		"XDG_CONFIG_HOME":     filepath.Join(home, "xdg", "config"),
		"XDG_CACHE_HOME":      filepath.Join(home, "xdg", "cache"),
		"XDG_DATA_HOME":       filepath.Join(home, "xdg", "data"),
		"XDG_STATE_HOME":      filepath.Join(home, "xdg", "state"),
		"TERM":                "xterm",
	}
	for k, v := range exact {
		if got[k] != v {
			t.Errorf("%s = %q, want %q", k, got[k], v)
		}
	}
	if !regexp.MustCompile(`^` + regexp.QuoteMeta(filepath.Join(home, "bin")) + `/nix-[0-9a-f]{12}/bin$`).MatchString(got["PATH"]) {
		t.Errorf("PATH = %q, want <home>/bin/nix-<hash>/bin only", got["PATH"])
	}
	for _, k := range []string{"HOME", "TMPDIR"} {
		if got[k] == "" {
			t.Errorf("%s is missing", k)
		}
	}
	allowed := map[string]bool{"HOME": true, "TMPDIR": true, "PATH": true}
	for k := range got {
		if _, ok := exact[k]; !ok && !allowed[k] {
			t.Errorf("unexpected variable %s=%s (NIX_PATH and every other NIX_* variable are removed)", k, got[k])
		}
	}
	// And a build with the hostile user configuration works and never asks the bogus cache.
	if b := e.runEnv(t, "cpp-lib", hostile, "build", "//..."); b.Exit != 0 {
		t.Errorf("build with a hostile user nix.conf and NIX_PATH: exit %d\n%s", b.Exit, b.Combined())
	}
	if n := bogusHits.Load(); n != 0 {
		t.Errorf("the user's substituter was contacted %d times", n)
	}
	if c := string(mustRead(t, filepath.Join(e.Home, "etc", "nix", "nix.conf"))); strings.Contains(c, bogus.URL) {
		t.Errorf("bob's nix.conf picked up the user's substituter")
	}
}

// TestRNIX025SystemNixCoexistence: with the host's /nix present (a system Nix), bob's namespace
// hides it: bob's outputs do not exist in the host store, and the host's store lists none of bob's
// paths.
func TestRNIX025SystemNixCoexistence(t *testing.T) {
	parity.Covers(t, "R-NIX-025")
	if _, err := os.Stat("/nix/store"); err != nil {
		t.Fatalf("harness: R-NIX-025 needs a host with a system Nix store at /nix: %v", err)
	}
	e := newBobEnv(t)
	p, r := e.buildPath(t, "cpp-lib", "//:ringtool")
	if harness.Red(t, r) {
		return
	}
	if _, err := os.Stat(p); err == nil {
		t.Errorf("bob's output %s exists in the host's store", p)
	}
	if out, err := exec.Command("nix-store", "--check-validity", p).CombinedOutput(); err == nil {
		t.Errorf("the host store says bob's path %s is valid: %s", p, out)
	}
	if _, err := os.Stat(filepath.Join(e.Home, p)); err != nil {
		t.Errorf("bob's output is not in its own store at <home>%s: %v", p, err)
	}
	names, _ := e.closureOf(t, p)
	for _, n := range names {
		if out, err := exec.Command("nix-store", "--check-validity", n).CombinedOutput(); err == nil && !strings.Contains(string(out), "error") {
			// A path both stores hold legitimately (a substituted nixpkgs path) is fine; a bob-made
			// path (names starting with the package) is not.
			if strings.Contains(n, "ringtool") || strings.Contains(n, "-src") || strings.Contains(n, "bob-builder") {
				t.Errorf("bob-made path %s is valid in the host store", n)
			}
		}
	}
}

// TestRNIX026ConcurrentCommands: 4 concurrent `bob build //...` on one state all succeed without
// lock errors, and 8 concurrent `bob lock` on one repository produce one consistent bob.lock.
func TestRNIX026ConcurrentCommands(t *testing.T) {
	parity.Covers(t, "R-NIX-026")
	e := newBobEnv(t)
	// The four builds start on an empty state (SPEC: "4 concurrent bob build //... on
	// examples/polyglot all succeed"), so creating the state is itself concurrent. The checkout
	// is made first: bobEnv caches fixture directories in a map.
	e.dir(t, "polyglot")
	var wg sync.WaitGroup
	results := make([]harness.Result, 4)
	for i := range results {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i] = e.run(t, "polyglot", "build", "//...")
		}()
	}
	wg.Wait()
	if harness.Red(t, results[0]) {
		return
	}
	for i, r := range results {
		out := r.Combined()
		if r.Exit != 0 || strings.Contains(out, "BOB-E-7004") || strings.Contains(strings.ToLower(out), "lock") && strings.Contains(strings.ToLower(out), "error") {
			t.Errorf("concurrent build %d: exit %d:\n%s", i, r.Exit, out)
		}
	}
	// Eight concurrent `bob lock` on a repository with an empty lock.
	e2 := newBobEnv(t)
	dir := e2.dir(t, "polyglot")
	empty := []byte("{\n  \"lockVersion\": 1\n}\n")
	mustWrite(t, filepath.Join(dir, "bob.lock"), empty, 0o644)
	locks := make([]harness.Result, 8)
	for i := range locks {
		wg.Add(1)
		go func() {
			defer wg.Done()
			locks[i] = e2.run(t, "polyglot", "lock")
		}()
	}
	wg.Wait()
	for i, r := range locks {
		if r.Exit != 0 {
			t.Errorf("concurrent lock %d: exit %d:\n%s", i, r.Exit, r.Combined())
		}
	}
	got := mustRead(t, filepath.Join(dir, "bob.lock"))
	if string(got) == string(empty) {
		t.Errorf("bob.lock was not written by 8 concurrent bob lock runs")
	}
	// One consistent file: the result of a single run in a fresh copy of the same repository.
	e3 := newBobEnv(t)
	ref := e3.dir(t, "polyglot")
	mustWrite(t, filepath.Join(ref, "bob.lock"), empty, 0o644)
	if r := e3.run(t, "polyglot", "lock"); r.Exit != 0 {
		t.Fatalf("reference lock: exit %d:\n%s", r.Exit, r.Combined())
	}
	if want := mustRead(t, filepath.Join(ref, "bob.lock")); string(got) != string(want) {
		t.Errorf("concurrent locks produced a different bob.lock than one sequential run")
	}
}

// whoAmIServer counts requests and answers WhoAmI with status.
type whoAmIServer struct {
	*httptest.Server
	hits atomic.Int64
}

func newWhoAmI(status int) *whoAmIServer {
	s := &whoAmIServer{}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/bob.auth.v1.AuthService/WhoAmI" {
			s.hits.Add(1)
			w.WriteHeader(status)
			return
		}
		w.WriteHeader(404)
	}))
	return s
}

// configureServer writes bob's config and credentials so that url is the default server with a
// service token (SPEC §02.8 files).
func (e *bobEnv) configureServer(t testing.TB, url string) {
	t.Helper()
	cfg := filepath.Join(e.XDG, "config", "bob")
	mustWrite(t, filepath.Join(cfg, "config.json"), []byte(fmt.Sprintf(`{"v":1,"defaultServer":%q}`, url)), 0o644)
	mustWrite(t, filepath.Join(cfg, "credentials.json"),
		[]byte(fmt.Sprintf(`{"v":1,"servers":{%q:{"kind":"service","accessToken":"service-token"}}}`, url)), 0o600)
}

// TestRNIX029CredentialPrecheck: a server answering 401 on WhoAmI makes `bob build //...` exit 4
// with BOB-E-4008 before any Nix process starts (a counting stand-in replaces the extracted Nix);
// 403 gives 4010; an unreachable server gives warning 4009 and a successful build.
func TestRNIX029CredentialPrecheck(t *testing.T) {
	parity.Covers(t, "R-NIX-029")
	e := newBobEnv(t)
	warm := e.run(t, "cpp-lib", "build", "//...")
	if harness.Red(t, warm) {
		return
	}
	if warm.Exit != 0 {
		t.Fatalf("warm-up build exit %d:\n%s", warm.Exit, warm.Combined())
	}
	// Replace the extracted nix by a counter: any Nix process bob starts from now on is recorded.
	bin := nixBinDir(t, e.Home)
	counter := filepath.Join(t.TempDir(), "spawned")
	if err := os.Remove(filepath.Join(bin, "nix")); err != nil {
		t.Fatalf("harness: %v", err)
	}
	mustWrite(t, filepath.Join(bin, "nix"), []byte("#!/bin/sh\necho \"$0 $*\" >> "+counter+"\nexit 97\n"), 0o755)
	for _, c := range []struct {
		status int
		code   string
	}{{401, "BOB-E-4008"}, {403, "BOB-E-4010"}} {
		srv := newWhoAmI(c.status)
		e.configureServer(t, srv.URL)
		r := e.run(t, "cpp-lib", "build", "//...")
		srv.Close()
		out := r.Combined()
		if r.Exit != 4 || !strings.Contains(out, c.code) {
			t.Errorf("WhoAmI %d: exit %d, want 4 with %s:\n%s", c.status, r.Exit, c.code, out)
		}
		if b, err := os.ReadFile(counter); err == nil && len(b) > 0 {
			t.Errorf("WhoAmI %d: a Nix process was spawned before the credential check failed:\n%s", c.status, b)
		}
		if n := srv.hits.Load(); n != 1 {
			t.Errorf("WhoAmI %d: called %d times, want once per process", c.status, n)
		}
	}
	// Unreachable server: warning 4009, the server's substituter is left out, the build works.
	e2 := newBobEnv(t)
	if w := e2.run(t, "cpp-lib", "build", "//..."); w.Exit != 0 {
		t.Fatalf("warm-up build exit %d:\n%s", w.Exit, w.Combined())
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	dead := "http://" + l.Addr().String()
	l.Close()
	e2.configureServer(t, dead)
	r := e2.run(t, "cpp-lib", "build", "//...")
	if r.Exit != 0 || !strings.Contains(r.Combined(), "warning[BOB-E-4009]") {
		t.Errorf("unreachable server: exit %d, want 0 with warning[BOB-E-4009]:\n%s", r.Exit, r.Combined())
	}
	if _, ok := diag.Lookup(4009); !ok {
		t.Errorf("code 4009 is not in the table")
	}
}
