package cli

import (
	"regexp"
	"strings"
	"testing"

	"github.com/megakuul/bob/tests/golden"
	"github.com/megakuul/bob/tests/harness"
	"github.com/megakuul/bob/tests/parity"
)

// TestRCLI002JSONStream: every --json line of every run parses as JSON, has v=1, a known event
// and validates against schema v1; run.finish is last and its exitCode equals the exit status;
// the query commands emit their `result` event with the documented data shape.
func TestRCLI002JSONStream(t *testing.T) {
	parity.Covers(t, "R-CLI-002")
	checkSchemaFile(t)
	acme := harness.Fixture(t, "acme")
	exits := repo(t, "exits")
	env := home(t)
	type run struct {
		dir  string
		args []string
		exit int      // -1: any
		data []string // keys of result.data, "" no result event expected
		cmd  string
	}
	runs := []run{
		{acme, []string{"build", "//...", "--json"}, 0, nil, ""},
		{acme, []string{"build", "//...", "--json", "--json-log"}, 0, nil, ""},
		{acme, []string{"build", "//...", "--json", "--print-paths"}, 0, nil, ""},
		{acme, []string{"test", "//...", "--json"}, 0, nil, ""},
		{acme, []string{"graph", "--json"}, 0, []string{"nodes", "edges", "why"}, "graph"},
		{acme, []string{"check", "--json"}, 0, []string{"findings"}, "check"},
		{acme, []string{"lock", "--json"}, 0, []string{"changes"}, "lock"},
		{acme, []string{"version", "--json"}, 0, []string{"bob", "nix", "nixpkgs"}, "version"},
		{acme, []string{"list", "--json"}, 0, []string{"installed"}, "list"},
		{acme, []string{"env", "--json"}, 0, []string{"root", "shell", "outside", "insideOnly", "tools"}, "env"},
		{exits, []string{"build", "//tools:broken", "--json"}, 1, nil, ""},
		{exits, []string{"build", "//tools:nosuch", "--json"}, 2, nil, ""},
	}
	for _, c := range runs {
		t.Run(strings.Join(c.args, " "), func(t *testing.T) {
			r := bob(t, c.dir, env, c.args...)
			if harness.Red(t, r) {
				return
			}
			if c.exit >= 0 && r.Exit != c.exit {
				t.Errorf("exit %d, want %d\n%s", r.Exit, c.exit, r.Combined())
			}
			evs := checkStream(t, r, false)
			if c.cmd != "" {
				found := false
				for _, e := range evs {
					if e["event"] != "result" {
						continue
					}
					found = true
					if e["command"] != c.cmd {
						t.Errorf("result.command = %v, want %s", e["command"], c.cmd)
					}
					data, _ := e["data"].(map[string]any)
					for _, k := range c.data {
						if _, ok := data[k]; !ok {
							t.Errorf("result.data of %s lacks %q: %v", c.cmd, k, data)
						}
					}
				}
				if !found {
					t.Errorf("no result event for %s", c.cmd)
				}
			}
			if c.exit == 1 || c.exit == 2 {
				n := 0
				for _, e := range evs {
					if e["event"] == "error" {
						n++
					}
				}
				if n == 0 {
					t.Errorf("failed run (exit %d) has no error event", c.exit)
				}
			}
			if strings.Contains(strings.Join(c.args, " "), "--json-log") {
				n := 0
				for _, e := range evs {
					if e["event"] == "log" {
						n++
					}
				}
				if n == 0 {
					t.Errorf("--json-log produced no log event")
				}
			}
		})
	}
	// without --json-log there are no log events.
	r := bob(t, acme, env, "build", "//...", "--json")
	if !harness.Red(t, r) {
		for _, e := range events(t, r.Stdout) {
			if e["event"] == "log" {
				t.Errorf("log event without --json-log")
				break
			}
		}
	}
}

// TestRCLI002NoopBuild: the default output of a successful no-op build has at most 2 lines
// (stdout is not a TTY): only the summary line, no package line for `cached` packages.
func TestRCLI002NoopBuild(t *testing.T) {
	parity.Covers(t, "R-CLI-002")
	dir := harness.Fixture(t, "acme")
	env := home(t)
	first := bob(t, dir, env, "build", "//...")
	if harness.Red(t, first) {
		return
	}
	if first.Exit != 0 {
		t.Fatalf("first build: exit %d\n%s", first.Exit, first.Combined())
	}
	second := bob(t, dir, env, "build", "//...")
	if second.Exit != 0 {
		t.Fatalf("second build: exit %d\n%s", second.Exit, second.Combined())
	}
	out := lines(second.Stdout + second.Stderr)
	if len(out) > 2 {
		t.Errorf("no-op build printed %d lines, want ≤ 2:\n%s", len(out), second.Combined())
	}
	if len(out) == 0 || !strings.HasPrefix(out[len(out)-1], "bob: ") {
		t.Errorf("no-op build: last line is not the summary `bob: …`:\n%s", second.Combined())
	}
	for _, l := range out {
		if strings.HasPrefix(l, "cached ") {
			t.Errorf("no-op build prints a package line for a cached package: %q", l)
		}
	}
}

var durationRE = regexp.MustCompile(`\(\d+(\.\d+)?m?s\)|\b\d+(\.\d+)?m?s\b`)

// TestRCLI002VerboseAddsLines: the run with -v differs from the default only by added lines (the
// default lines are a subsequence of the -v lines; durations aside), for a failing and a
// succeeding run.
func TestRCLI002VerboseAddsLines(t *testing.T) {
	parity.Covers(t, "R-CLI-002")
	exits := repo(t, "exits")
	acme := harness.Fixture(t, "acme")
	for _, c := range []struct {
		dir  string
		args []string
	}{
		{exits, []string{"build", "//tools:broken"}},
		{exits, []string{"build", "//tools:hello"}},
		{acme, []string{"test", "//..."}},
	} {
		env := home(t)
		def := bob(t, c.dir, env, c.args...)
		if harness.Red(t, def) {
			continue
		}
		verb := bob(t, c.dir, env, append(append([]string{}, c.args...), "-v")...)
		if def.Exit != verb.Exit {
			t.Errorf("bob %v: exit %d without -v, %d with -v", c.args, def.Exit, verb.Exit)
		}
		want := lines(durationRE.ReplaceAllString(def.Stdout+def.Stderr, "<d>"))
		got := lines(durationRE.ReplaceAllString(verb.Stdout+verb.Stderr, "<d>"))
		if len(want) == 0 {
			t.Errorf("bob %v: the default run printed nothing (no summary line), nothing to compare", c.args)
			continue
		}
		i := 0
		for _, l := range got {
			if i < len(want) && l == want[i] {
				i++
			}
		}
		if i != len(want) {
			t.Errorf("bob %v: the -v output is not the default output plus added lines; first default line missing: %q", c.args, want[i])
		}
	}
}

// TestRCLI007HumanOutput: the structure of the human output (SPEC §02.5 rules 1, 2, 4) for a
// failing and a succeeding build without a TTY, with TERM=dumb. The byte-exact S15 golden set is
// replayed by the errors suite (R-NIX-012, R-CLI-007); this test pins the rules on a fixture of
// its own, and writes a golden of it.
func TestRCLI007HumanOutput(t *testing.T) {
	parity.Covers(t, "R-CLI-007")
	exits := repo(t, "exits")
	env := append(home(t), "TERM=dumb")
	r := bob(t, exits, env, "build", "//tools:broken", "//tools:hello")
	if harness.Red(t, r) {
		return
	}
	if r.Exit != 1 {
		t.Errorf("exit %d, want 1\n%s", r.Exit, r.Combined())
	}
	out := r.Stdout + r.Stderr
	noLeak(t, r)
	if strings.Contains(out, "\x1b[") || strings.Contains(out, "\r") {
		t.Errorf("escape sequences or CR without a TTY:\n%q", out)
	}
	ls := lines(out)
	failed, built := -1, -1
	for i, l := range ls {
		switch {
		case strings.HasPrefix(l, "FAILED  //tools:broken"):
			failed = i
		case strings.HasPrefix(l, "built   //tools:hello"):
			built = i
		}
	}
	if failed < 0 || built < 0 || failed > built {
		t.Errorf("want package lines `FAILED  //tools:broken` before `built   //tools:hello` (failed first):\n%s", out)
	}
	if !regexp.MustCompile(`(?m)^error\[BOB-E-2001\]: //tools:broken: compile error in `).MatchString(out) {
		t.Errorf("no error block `error[BOB-E-2001]: //tools:broken: compile error in …`:\n%s", out)
	}
	if !strings.Contains(out, "  package defined at tools/bob.go:") || !strings.Contains(out, "  full log: bob log //tools:broken") {
		t.Errorf("error block lacks `package defined at` or `full log:` line:\n%s", out)
	}
	if !regexp.MustCompile(`(?m)^bob: 1 failed, 0 skipped, 1 built, 0 cached \(exit 1\)$`).MatchString(out) {
		t.Errorf("summary line is not `bob: 1 failed, 0 skipped, 1 built, 0 cached (exit 1)`:\n%s", out)
	}
	if ls[len(ls)-1] != "bob: 1 failed, 0 skipped, 1 built, 0 cached (exit 1)" {
		t.Errorf("the summary is not the last line:\n%s", out)
	}
	// rule 3: no excerpt line contains a raw store path.
	for _, l := range ls {
		if strings.HasPrefix(strings.TrimSpace(l), "|") && regexp.MustCompile(`/nix/store/[0-9a-z]{32}`).MatchString(l) {
			t.Errorf("excerpt line with a raw store path: %q", l)
		}
	}
	golden.Assert(t, "tests/goldens/cli/human/build-broken.txt", []byte(r.Stdout+r.Stderr), golden.Human(), golden.Reqs("R-CLI-007"))

	// success: `bob: N built, N cached` without failures.
	ok := bob(t, exits, env, "build", "//tools:hello")
	if ok.Exit != 0 || !regexp.MustCompile(`(?m)^bob: 1 built, 0 cached$`).MatchString(ok.Stdout+ok.Stderr) {
		t.Errorf("success summary is not `bob: 1 built, 0 cached` (exit %d):\n%s", ok.Exit, ok.Combined())
	}
	// test summary form `bob: N passed, N failed, N cached (exit E)`.
	pipe := repo(t, "pipe")
	tr := bob(t, pipe, env, "test", "//...")
	if !regexp.MustCompile(`(?m)^bob: 1 passed, 0 failed, 0 cached$|^bob: 1 passed, 0 failed, 0 cached \(exit 0\)$`).MatchString(tr.Stdout + tr.Stderr) {
		t.Errorf("test summary is not `bob: N passed, N failed, N cached`:\n%s", tr.Combined())
	}
}

// TestRCLI007Colour: under a pseudo-terminal, NO_COLOR=1 gives no `\x1b[` bytes; BOB_COLOR=always
// (no NO_COLOR) gives colour sequences, also without a TTY; NO_COLOR still wins over
// BOB_COLOR=always and --color=always; TERM=dumb with a pty gives no `\r` redraws.
func TestRCLI007Colour(t *testing.T) {
	parity.Covers(t, "R-CLI-007")
	exits := repo(t, "exits")
	args := []string{"build", "//tools:broken", "//tools:hello"}
	base := home(t)
	with := func(extra ...string) []string { return append(append([]string{}, base...), extra...) }

	probe := bob(t, exits, base, "version")
	if harness.Red(t, probe) {
		// the feature behind the build is the same; mark the whole test red once.
		r := bob(t, exits, base, args...)
		harness.Red(t, r)
		return
	}
	out, exit := runPTY(t, exits, with("TERM=xterm-256color", "NO_COLOR=1"), args...)
	if exit != 1 {
		t.Errorf("pty NO_COLOR=1: exit %d, want 1\n%s", exit, out)
	}
	if strings.Contains(out, "\x1b[") {
		t.Errorf("pty with NO_COLOR=1 contains colour/escape sequences:\n%q", out)
	}
	out, _ = runPTY(t, exits, with("TERM=xterm-256color", "BOB_COLOR=always", "NO_COLOR=1"), args...)
	if strings.Contains(out, "\x1b[") {
		t.Errorf("NO_COLOR must win over BOB_COLOR=always:\n%q", out)
	}
	out, _ = runPTY(t, exits, with("TERM=xterm-256color", "BOB_COLOR=always"), args...)
	if !strings.Contains(out, "\x1b[") {
		t.Errorf("pty with BOB_COLOR=always has no colour sequences:\n%q", out)
	}
	if !strings.Contains(out, "\x1b[1;31m") && !strings.Contains(out, "\x1b[31;1m") {
		t.Errorf("error is not red bold (ESC[1;31m): %q", out)
	}
	out, _ = runPTY(t, exits, with("TERM=xterm-256color"), args...)
	if !strings.Contains(out, "\x1b[") {
		t.Errorf("pty with TERM=xterm-256color and no NO_COLOR: no colour:\n%q", out)
	}
	out, _ = runPTY(t, exits, with("TERM=dumb"), args...)
	if strings.Contains(out, "\x1b[") {
		t.Errorf("TERM=dumb must not colour:\n%q", out)
	}
	if strings.Contains(strings.ReplaceAll(out, "\r\n", "\n"), "\r") {
		t.Errorf("TERM=dumb with a pty: `\\r` redraws in output:\n%q", out)
	}
	// forced colour without a TTY, by flag and by variable.
	r := bob(t, exits, with("TERM=dumb"), append([]string{"--color=always"}, args...)...)
	if !strings.Contains(r.Combined(), "\x1b[") {
		t.Errorf("--color=always without a TTY: no colour sequences:\n%q", r.Combined())
	}
	r = bob(t, exits, with("TERM=dumb", "BOB_COLOR=always"), args...)
	if !strings.Contains(r.Combined(), "\x1b[") {
		t.Errorf("BOB_COLOR=always without a TTY: no colour sequences:\n%q", r.Combined())
	}
	r = bob(t, exits, with("TERM=dumb", "NO_COLOR=1"), append([]string{"--color=always"}, args...)...)
	if strings.Contains(r.Combined(), "\x1b[") {
		t.Errorf("NO_COLOR must win over --color=always:\n%q", r.Combined())
	}
}
