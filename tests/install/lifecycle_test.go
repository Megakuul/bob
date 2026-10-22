package install

import (
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/megakuul/bob/tests/cli/fakeserver"
	"github.com/megakuul/bob/tests/harness"
	"github.com/megakuul/bob/tests/parity"
)

// setup starts a registry with releases acme/cli 1.0.0 and 1.1.0 and registers it as `default`.
func setup(t *testing.T) (env, *fakeserver.Server) {
	t.Helper()
	srv := fakeserver.New(t, fakeserver.Options{Name: "default"})
	publish(t, srv, "acme", "cli", false, []string{"acme"}, "1.0.0", "1.1.0")
	e := newEnv(t)
	return e, srv
}

// TestRINST001Lifecycle: install acme/cli@1.0.0 → `acme` prints 1.0.0; upgrade → 1.1.0; rollback →
// 1.0.0; uninstall acme/cli → shim gone and list empty; `bob list --json` matches the current
// generation file.
func TestRINST001Lifecycle(t *testing.T) {
	parity.Covers(t, "R-INST-001")
	e, srv := setup(t)
	if r := e.addRegistry(t, "default", srv); r.Exit != 0 && !r.IsNotImplemented() {
		t.Fatalf("registry add: exit %d\n%s", r.Exit, r.Combined())
	}
	r := e.bob(t, "", "install", "acme/cli@1.0.0")
	if harness.Red(t, r) {
		return
	}
	if r.Exit != 0 {
		t.Fatalf("install: exit %d\n%s", r.Exit, r.Combined())
	}
	if !strings.HasPrefix(r.Stdout, "installed acme/cli 1.0.0 (generation 1): acme") || !strings.Contains(r.Stdout, "→ ~/.local/bin") {
		t.Errorf("install output is not `installed acme/cli 1.0.0 (generation 1): acme → ~/.local/bin`:\n%s", r.Stdout)
	}
	noLeak(t, r)
	step := func(want string, label string) {
		t.Helper()
		v, rr := e.version(t, "acme")
		if rr.Exit != 0 || v != want {
			t.Errorf("%s: `acme --version` = %q (exit %d), want %s\n%s", label, v, rr.Exit, want, rr.Stderr)
		}
	}
	step("1.0.0", "after install")

	r = e.bob(t, "", "upgrade")
	if r.Exit != 0 || !strings.Contains(r.Stdout, "acme/cli  1.0.0 → 1.1.0") || !strings.Contains(r.Stdout, "bob: 1 upgraded (generation 2)") {
		t.Errorf("upgrade: exit %d, want the `acme/cli  1.0.0 → 1.1.0` and `bob: 1 upgraded (generation 2)` lines:\n%s", r.Exit, r.Combined())
	}
	step("1.1.0", "after upgrade")

	r = e.bob(t, "", "rollback")
	if r.Exit != 0 || !strings.Contains(r.Stdout, "generation 2 → 1: acme/cli 1.1.0 → 1.0.0") {
		t.Errorf("rollback: exit %d, want `generation 2 → 1: acme/cli 1.1.0 → 1.0.0`:\n%s", r.Exit, r.Combined())
	}
	step("1.0.0", "after rollback")
	if e.current(t) != 1 {
		t.Errorf("current -> generation %d after rollback, want 1", e.current(t))
	}

	// bob list --json matches the current generation file.
	checkListMatches(t, e)

	r = e.bob(t, "", "uninstall", "acme/cli")
	if r.Exit != 0 {
		t.Fatalf("uninstall: exit %d\n%s", r.Exit, r.Combined())
	}
	if _, err := os.Lstat(e.shim("acme")); !os.IsNotExist(err) {
		t.Errorf("shim still there after uninstall: %v", err)
	}
	l := e.bob(t, "", "list", "--json")
	for _, ev := range events(t, l.Stdout) {
		if ev["event"] == "result" {
			d, _ := ev["data"].(map[string]any)
			if in, _ := d["installed"].([]any); len(in) != 0 {
				t.Errorf("list is not empty after uninstall: %v", in)
			}
		}
	}
	if h := e.bob(t, "", "list"); strings.TrimSpace(h.Stdout) != "" {
		t.Errorf("bob list is not empty after uninstall:\n%s", h.Stdout)
	}
}

// checkListMatches compares `bob list --json` with the entries of the current generation file.
func checkListMatches(t *testing.T, e env) {
	t.Helper()
	g := e.readGeneration(t, e.current(t))
	r := e.bob(t, "", "list", "--json")
	var installed []map[string]any
	for _, ev := range events(t, r.Stdout) {
		if ev["event"] == "result" {
			d, _ := ev["data"].(map[string]any)
			b, _ := json.Marshal(d["installed"])
			_ = json.Unmarshal(b, &installed)
		}
	}
	if len(installed) != len(g.Entries) {
		t.Errorf("list --json has %d entries, the current generation has %d", len(installed), len(g.Entries))
		return
	}
	for i, en := range g.Entries {
		got := installed[i]
		var bins []string
		if b, ok := got["bins"].([]any); ok {
			for _, x := range b {
				bins = append(bins, fmt.Sprint(x))
			}
		}
		want := sortedKeys(en.Bins)
		if got["name"] != en.Name || got["ref"] != en.Ref || got["version"] != en.Version || int(got["generation"].(float64)) != g.N || !reflect.DeepEqual(bins, want) {
			t.Errorf("list entry %d = %v, generation file says %+v (bins %v)", i, got, en, want)
		}
	}
}

// TestRINST001KillPoints: killing bob (SIGKILL) at any step of `upgrade` leaves `current`
// pointing to a complete generation, and every shim works. The kill points are offsets from the
// start of the process, dense enough to hit resolving, substituting, writing the generation,
// flipping `current` and writing shims.
func TestRINST001KillPoints(t *testing.T) {
	parity.Covers(t, "R-INST-001")
	offsets := []time.Duration{0, 2, 5, 10, 20, 35, 50, 75, 100, 150, 200, 300, 450, 700, 1000, 1500}
	probe, srv := setup(t)
	if r := probe.addRegistry(t, "default", srv); r.IsNotImplemented() {
		harness.Red(t, r)
		return
	}
	for _, ms := range offsets {
		t.Run(fmt.Sprintf("kill-after-%dms", ms), func(t *testing.T) {
			e, srv := setup(t)
			if r := e.addRegistry(t, "default", srv); r.Exit != 0 {
				t.Fatalf("registry add: %s", r.Combined())
			}
			if r := e.bob(t, "", "install", "acme/cli@1.0.0"); r.Exit != 0 {
				t.Fatalf("install: exit %d\n%s", r.Exit, r.Combined())
			}
			proc := e.start(t, "upgrade")
			time.Sleep(ms * time.Millisecond)
			_ = proc.Process.Signal(syscall.SIGKILL)
			_ = proc.Wait()

			cur := e.current(t)
			if cur < 1 {
				t.Fatalf("`current` does not point to a generation after the kill (current=%d)", cur)
			}
			g := e.readGeneration(t, cur)
			if g.N != cur || len(g.Entries) != 1 || g.Entries[0].Ref == "" || len(g.Entries[0].StorePaths) == 0 {
				t.Errorf("current generation %d is incomplete: %+v", cur, g)
			}
			want := map[int]string{1: "1.0.0", 2: "1.1.0"}[cur]
			if want == "" {
				t.Fatalf("unexpected current generation %d", cur)
			}
			if v, r := e.version(t, "acme"); r.Exit != 0 || v != want {
				t.Errorf("after the kill (current=%d) `acme --version` = %q (exit %d), want %s\n%s", cur, v, r.Exit, want, r.Stderr)
			}
			// a later run finishes the job (no stale lock or temp file blocks it).
			if r := e.bob(t, "", "upgrade"); r.Exit != 0 {
				t.Errorf("upgrade after a killed upgrade: exit %d\n%s", r.Exit, r.Combined())
			}
			if v, r := e.version(t, "acme"); v != "1.1.0" {
				t.Errorf("after the retried upgrade `acme --version` = %q (exit %d), want 1.1.0", v, r.Exit)
			}
		})
	}
}
