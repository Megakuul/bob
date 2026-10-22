package cli

import (
	"os/exec"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/megakuul/bob/tests/golden"
	"github.com/megakuul/bob/tests/harness"
	"github.com/megakuul/bob/tests/parity"
)

// TestRCLI004CompleteLabels: after one `bob build`, `bob __complete build //li` prints the labels
// that start with //li, one per line, in ≤ 50 ms p95 [2c/4G] (budget stated by the SPEC
// acceptance text itself); without .bob/labels it prints no labels and exits 0.
func TestRCLI004CompleteLabels(t *testing.T) {
	parity.Covers(t, "R-CLI-004")
	dir := harness.Fixture(t, "polyglot")
	env := home(t)

	// no .bob/labels yet: no labels, exit 0, and completion never evaluates.
	r := bob(t, dir, env, "__complete", "build", "//li")
	if harness.Red(t, r) {
		return
	}
	if r.Exit != 0 || strings.TrimSpace(r.Stdout) != "" {
		t.Errorf("without .bob/labels: exit %d, stdout %q; want exit 0 and no labels", r.Exit, r.Stdout)
	}

	if b := bob(t, dir, env, "build"); b.Exit != 0 {
		t.Fatalf("bob build: exit %d\n%s", b.Exit, b.Combined())
	}
	r = bob(t, dir, env, "__complete", "build", "//li")
	if r.Exit != 0 {
		t.Fatalf("bob __complete build //li: exit %d\n%s", r.Exit, r.Combined())
	}
	got := lines(r.Stdout)
	want := []string{"//libs/foo:foo"}
	for _, w := range want {
		found := false
		for _, g := range got {
			found = found || g == w
		}
		if !found {
			t.Errorf("label %s not offered; got %v", w, got)
		}
	}
	for _, g := range got {
		if !strings.HasPrefix(g, "//li") {
			t.Errorf("candidate %q does not start with //li", g)
		}
	}
	seen := map[string]bool{}
	for _, g := range got {
		if seen[g] {
			t.Errorf("duplicate candidate %q", g)
		}
		seen[g] = true
	}
	// commands and flag values are offered too.
	c := bob(t, dir, env, "__complete", "ch")
	if !strings.Contains(c.Stdout, "check") {
		t.Errorf("`__complete ch` does not offer `check`: %q", c.Stdout)
	}
	e := bob(t, dir, env, "__complete", "env", "--format", "")
	for _, f := range []string{"sh", "fish", "json"} {
		if !strings.Contains(e.Stdout, f) {
			t.Errorf("`__complete env --format` does not offer %q: %q", f, e.Stdout)
		}
	}

	// p95 ≤ 50 ms over 40 runs (process start included; [2c/4G] [proposal]).
	var d []time.Duration
	for range 40 {
		t0 := time.Now()
		bob(t, dir, env, "__complete", "build", "//li")
		d = append(d, time.Since(t0))
	}
	sort.Slice(d, func(i, j int) bool { return d[i] < d[j] })
	if p95 := d[len(d)*95/100-1]; p95 > 50*time.Millisecond {
		t.Errorf("bob __complete p95 = %v, budget 50 ms", p95)
	}
}

// TestRCLI004Scripts: the bash, zsh and fish scripts are golden-tested and sourced without
// errors in each shell.
func TestRCLI004Scripts(t *testing.T) {
	parity.Covers(t, "R-CLI-004")
	env := home(t)
	for _, sh := range []string{"bash", "zsh", "fish"} {
		t.Run(sh, func(t *testing.T) {
			r := bob(t, t.TempDir(), env, "completion", sh)
			if harness.Red(t, r) {
				return
			}
			if r.Exit != 0 || r.Stdout == "" {
				t.Fatalf("bob completion %s: exit %d\n%s", sh, r.Exit, r.Combined())
			}
			if !strings.Contains(r.Stdout, "__complete") {
				t.Errorf("the %s script does not call `bob __complete`", sh)
			}
			golden.Assert(t, "tests/goldens/cli/completion/"+sh+".txt", []byte(r.Stdout), golden.Reqs("R-CLI-004"))
			path, err := exec.LookPath(sh)
			if err != nil {
				t.Fatalf("harness error: %s is not on PATH (the pinned shells of HARNESS §1.4 are required): %v", sh, err)
			}
			script := t.TempDir() + "/bob-completion." + sh
			if err := writeFile(script, r.Stdout); err != nil {
				t.Fatal(err)
			}
			var cmd *exec.Cmd
			switch sh {
			case "bash":
				cmd = exec.Command(path, "--norc", "--noprofile", "-c", "set -e; source "+script)
			case "zsh":
				cmd = exec.Command(path, zshSourceArgs(script)...)
			default:
				cmd = exec.Command(path, "--no-config", "-c", "source "+script)
			}
			cmd.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + homeDir(env), "TERM=dumb"}
			if out, err := cmd.CombinedOutput(); err != nil || len(out) != 0 {
				t.Errorf("sourcing the %s script: err %v, output %q; want silence and success", sh, err, out)
			}
		})
	}
}
