package devenv

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/megakuul/bob/tests/parity"
)

var (
	goBinPath = regexp.MustCompile(`^/nix/store/[0-9a-z]{32}-go-[0-9][0-9.]*/bin/go$`)
	goRoot    = regexp.MustCompile(`^/nix/store/[0-9a-z]{32}-go-[0-9][0-9.]*(/.*)?$`)
)

// TestRDEV001DevShell: bob dev enters the dev shell on a host without /nix.
func TestRDEV001DevShell(t *testing.T) {
	parity.Covers(t, "R-DEV-001")
	w := newWorld(t)
	dir := w.local("devshell")

	r := w.bobHide(dir, "dev", "--", "sh", "-c", "command -v go; go env GOROOT; echo $CGO_ENABLED $BOB_DEV")
	if redIf(t, r) {
		return
	}
	noLeak(t, r)
	if exitIs(t, r, 0) {
		lines := strings.Split(strings.TrimRight(r.Stdout, "\n"), "\n")
		if len(lines) != 3 {
			t.Fatalf("bob dev -- sh -c … printed %d lines, want 3:\n%s\nstderr: %s", len(lines), r.Stdout, r.Stderr)
		}
		if !goBinPath.MatchString(lines[0]) {
			t.Errorf("command -v go = %q, want /nix/store/…-go-<pinned>/bin/go", lines[0])
		}
		if !goRoot.MatchString(lines[1]) {
			t.Errorf("go env GOROOT = %q, want a /nix/store path", lines[1])
		}
		if lines[2] != "0 1" {
			t.Errorf("$CGO_ENABLED $BOB_DEV = %q, want %q", lines[2], "0 1")
		}
		// The pinned version is the one in env.json.
		var e envJSON
		if err := jsonUnmarshal(readFile(t, filepath.Join(dir, ".bob", "env.json")), &e); err != nil {
			t.Errorf(".bob/env.json after bob dev: %v", err)
		} else if e.Tools["go"].Path != lines[0] {
			t.Errorf("go inside the shell is %q, env.json says %q", lines[0], e.Tools["go"].Path)
		}
	}
	if b, err := os.ReadFile(filepath.Join(dir, ".bob", "hooked")); err != nil || string(b) != "hooked\n" {
		t.Errorf("the dev shell hook did not run before the command: %q %v", b, err)
	}

	// Hooks run before the command, every time.
	if err := os.Remove(filepath.Join(dir, ".bob", "hooked")); err != nil {
		t.Errorf("hook marker: %v", err)
	}
	r = w.bobHide(dir, "dev", "--", "sh", "-c", "cat .bob/hooked")
	if exitIs(t, r, 0) && r.Stdout != "hooked\n" {
		t.Errorf("the command saw %q, want the hook's marker \"hooked\"", r.Stdout)
	}

	// Variables of the session.
	r = w.bobHide(dir, "dev", "--", "sh", "-c", `echo "$BOB_PROJECT|$BOB_DEV_SHELL|$DEVSHELL_NAME"`)
	if exitIs(t, r, 0) && strings.TrimSpace(r.Stdout) != dir+"|default|default" {
		t.Errorf("BOB_PROJECT|BOB_DEV_SHELL|DEVSHELL_NAME = %q, want %q", strings.TrimSpace(r.Stdout), dir+"|default|default")
	}
	r = w.bobHide(dir, "dev", "--shell", "ci", "--", "sh", "-c", `echo "$BOB_DEV_SHELL|$DEVSHELL_NAME"`)
	if exitIs(t, r, 0) && strings.TrimSpace(r.Stdout) != "ci|ci" {
		t.Errorf("--shell ci: BOB_DEV_SHELL|DEVSHELL_NAME = %q, want ci|ci", strings.TrimSpace(r.Stdout))
	}

	// The command's exit status is bob dev's.
	if r = w.bobHide(dir, "dev", "--", "false"); r.Exit != 1 {
		t.Errorf("bob dev -- false: exit %d, want 1", r.Exit)
	}
	if r = w.bobHide(dir, "dev", "--", "sh", "-c", "exit 7"); r.Exit != 7 {
		t.Errorf("bob dev -- sh -c 'exit 7': exit %d, want 7", r.Exit)
	}

	// An unknown shell lists the names.
	r = w.bobHide(dir, "dev", "--shell", "nope", "--", "true")
	noLeak(t, r)
	if r.Exit != 3 || !strings.Contains(r.Stderr, "BOB-E-1014") {
		t.Errorf("bob dev --shell nope: exit %d, stderr %q, want exit 3 with BOB-E-1014", r.Exit, r.Stderr)
	}
	for _, name := range []string{"default", "ci"} {
		if !strings.Contains(r.Stderr, name) {
			t.Errorf("BOB-E-1014 does not list the shell %q:\n%s", name, r.Stderr)
		}
	}

	// A failing hook: its exit code is reported and stderr is shown, bob exits 1, the command
	// does not run.
	bobgo := filepath.Join(dir, "bob.go")
	src := readFile(t, bobgo)
	writeFile(t, bobgo, strings.Replace(src, `"echo hooked > .bob/hooked"`, `"echo hook-failed-loudly >&2; exit 4"`, 1))
	w.acc.chownTree(t, dir)
	r = w.bobHide(dir, "dev", "--", "sh", "-c", "echo command-ran")
	if r.Exit != 1 || !strings.Contains(r.Stderr, "hook-failed-loudly") || strings.Contains(r.Stdout, "command-ran") {
		t.Errorf("failing hook: exit %d, stdout %q, stderr %q, want exit 1, the hook's stderr, and no command", r.Exit, r.Stdout, r.Stderr)
	}
}

// TestRDEV006DevNoop: `bob dev -- true` with nothing changed takes at most 1.0 s [2c/4G] /
// 0.5 s [8c/15G], median of 5 (budget B-014). The other rows of R-DEV-006 belong to the budgets
// stage (B-015 Vite ready is measured in TestRDEV002ViteLiveReload, B-016/B-017/B-020 need the
// Go build fixture and a server cache, B-018 is TestRDEV008ShimOverhead, B-019 is
// TestRDEV007HookCost).
func TestRDEV006DevNoop(t *testing.T) {
	parity.Covers(t, "R-DEV-006")
	w := newWorld(t)
	dir := w.local("devshell")
	if r := w.bobHide(dir, "dev", "--", "true"); redIf(t, r) || !exitIs(t, r, 0) {
		return // the first run evaluates and realises
	}
	var ds []time.Duration
	for range 5 {
		t0 := time.Now()
		r := w.bobHide(dir, "dev", "--", "true")
		ds = append(ds, time.Since(t0))
		if r.Exit != 0 {
			t.Fatalf("bob dev -- true: exit %d: %s", r.Exit, r.Stderr)
		}
	}
	sort.Slice(ds, func(i, j int) bool { return ds[i] < ds[j] })
	limit := time.Duration(profile(0.5, 1.0) * float64(time.Second))
	if med := ds[len(ds)/2]; med > limit {
		t.Errorf("bob dev -- true, nothing changed: median %s, budget (B-014) %s", med, limit)
	}
}
