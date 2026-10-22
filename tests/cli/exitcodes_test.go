package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/megakuul/bob/pkg/bob/diag"
	"github.com/megakuul/bob/tests/harness"
	"github.com/megakuul/bob/tests/parity"
)

// exitByClass is the table of SPEC §02.2 in priority order, highest first: first digit of the
// code (the class), exit status.
func exitByClass() []struct{ Class, Exit int } {
	return []struct{ Class, Exit int }{
		{8, 130}, // interrupt
		{9, 70},  // internal
		{7, 7},   // store/host
		{6, 6},   // resource
		{4, 4},   // cache/server
		{3, 4},   // fetch
		{5, 5},   // upstream
		{1, 3},   // definition
		{2, 1},   // build
		{0, 2},   // usage
	}
}

// expectedExit applies §02.2 to a set of codes the way the SPEC states it, independently of
// diag.ExitCode.
func expectedExit(codes []int) int {
	for _, p := range exitByClass() {
		for _, c := range codes {
			if c/1000 == p.Class {
				return p.Exit
			}
		}
	}
	return 0
}

// TestRCLI006ExitFunction: the exit status is a function of the classes raised, in priority
// order interrupt > internal > store > resource > cache > fetch > upstream > definition > build >
// usage (§02.2); warnings never count.
func TestRCLI006ExitFunction(t *testing.T) {
	parity.Covers(t, "R-CLI-006")
	reps := map[int]diag.Code{0: 1, 1: 1001, 2: 2001, 3: 3001, 4: 4001, 5: 5001, 6: 6001, 7: 7001, 8: 8001, 9: 9001}
	errs := func(classes ...int) []*diag.Error {
		var out []*diag.Error
		for _, c := range classes {
			out = append(out, &diag.Error{Code: reps[c], Severity: diag.SeverityError})
		}
		return out
	}
	for _, p := range exitByClass() {
		if got := diag.ExitCode(errs(p.Class)); got != p.Exit {
			t.Errorf("class %d alone: exit %d, want %d", p.Class, got, p.Exit)
		}
	}
	prio := exitByClass()
	for i := range prio {
		for j := i + 1; j < len(prio); j++ {
			want := prio[i].Exit
			if got := diag.ExitCode(errs(prio[j].Class, prio[i].Class)); got != want {
				t.Errorf("classes %d and %d: exit %d, want %d (higher priority wins)", prio[j].Class, prio[i].Class, got, want)
			}
		}
	}
	if got := diag.ExitCode(nil); got != 0 {
		t.Errorf("no errors: exit %d, want 0", got)
	}
	w := []*diag.Error{{Code: 4001, Severity: diag.SeverityWarning}, {Code: 2001, Severity: diag.SeverityError}}
	if got := diag.ExitCode(w); got != 1 {
		t.Errorf("warning 4001 + error 2001: exit %d, want 1 (warnings do not count)", got)
	}
	if got := diag.ExitCode(w[:1]); got != 0 {
		t.Errorf("warnings only: exit %d, want 0", got)
	}
}

// TestRCLI006CatalogueExpectations: every entry of the fault catalogue (tests/faults/expect.json)
// states the exit status that §02.2 gives for its codes, so the live check in tests/errors compares
// bob against a table that is itself consistent with the SPEC.
func TestRCLI006CatalogueExpectations(t *testing.T) {
	parity.Covers(t, "R-CLI-006")
	b, err := os.ReadFile(filepath.Join(harness.Root(t), "tests", "faults", "expect.json"))
	if err != nil {
		t.Fatal(err)
	}
	var cat map[string]struct {
		Codes []string `json:"codes"`
		Exit  *int     `json:"exit"`
		Alt   []struct {
			Codes []string `json:"codes"`
			Exit  *int     `json:"exit"`
		} `json:"alt"`
	}
	if err := json.Unmarshal(b, &cat); err != nil {
		t.Fatal(err)
	}
	conv := func(codes []string) []int {
		var out []int
		for _, c := range codes {
			n, err := strconv.Atoi(strings.TrimPrefix(c, "BOB-E-"))
			if err != nil {
				t.Errorf("bad code %q", c)
			}
			out = append(out, n)
		}
		return out
	}
	for id, e := range cat {
		if e.Exit != nil && len(e.Codes) > 0 {
			if want := expectedExit(conv(e.Codes)); *e.Exit != want {
				t.Errorf("fault %s: catalogue says exit %d for %v, §02.2 gives %d", id, *e.Exit, e.Codes, want)
			}
		}
		for _, a := range e.Alt {
			if a.Exit != nil && len(a.Codes) > 0 {
				if want := expectedExit(conv(a.Codes)); *a.Exit != want {
					t.Errorf("fault %s (alt): catalogue says exit %d for %v, §02.2 gives %d", id, *a.Exit, a.Codes, want)
				}
			}
		}
	}
}

// TestRCLI006ProgramExit: `bob run //tools:exit3` exits 3 (the program's own status); a program
// killed by SIGKILL gives 137 (128+9). bob's own codes apply only to failures before the start.
func TestRCLI006ProgramExit(t *testing.T) {
	parity.Covers(t, "R-CLI-006")
	dir := repo(t, "exits")
	env := home(t)
	for _, c := range []struct {
		label string
		exit  int
		args  []string
	}{
		{"//tools:exit3", 3, nil},
		{"//tools:selfkill", 137, nil},
		{"//tools:hello", 0, []string{"--", "a", "b"}},
	} {
		r := bob(t, dir, env, append([]string{"run", c.label}, c.args...)...)
		if harness.Red(t, r) {
			continue
		}
		if r.Exit != c.exit {
			t.Errorf("bob run %s: exit %d, want %d\n%s", c.label, r.Exit, c.exit, r.Combined())
		}
		if c.label == "//tools:hello" && !strings.Contains(r.Stdout, "hello a b") {
			t.Errorf("bob run %s -- a b: stdout %q does not contain %q (stdout is the program's)", c.label, r.Stdout, "hello a b")
		}
		if strings.Contains(r.Combined(), "BOB-E-") && c.exit != 0 {
			t.Errorf("bob run %s: bob printed an error of its own after the program started:\n%s", c.label, r.Combined())
		}
	}
	// before the program starts, bob's codes apply: an unknown label is a usage error.
	r := bob(t, dir, env, "run", "//tools:nosuch")
	if harness.Red(t, r) {
		return
	}
	if r.Exit != 2 || !strings.Contains(r.Combined(), "BOB-E-0003") && !strings.Contains(r.Combined(), "BOB-E-0001") {
		t.Errorf("bob run //tools:nosuch: exit %d, want 2 with a BOB-E-000x usage code\n%s", r.Exit, r.Combined())
	}
}
