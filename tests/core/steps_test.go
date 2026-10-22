package core

// SPEC §1.4.2–§1.4.3 and §1.16: R-FW-023 bob.Run, R-FW-024 tests are packages, R-FW-025
// loopback services, R-FW-020 chain of steps.

import (
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/megakuul/bob/tests/harness"
	"github.com/megakuul/bob/tests/parity"
)

// outputPaths builds label with --print-paths --json and returns the host paths of its
// outputs (package.finish paths[]).
func outputPaths(t *testing.T, dir string, env []string, label string, extra ...string) ([]string, harness.Result, bool) {
	t.Helper()
	args := append([]string{"build", label, "--print-paths", "--json"}, extra...)
	r := bobRunEnv(t, dir, env, args...)
	if harness.Red(t, r) {
		return nil, r, false
	}
	if r.Exit != 0 {
		return nil, r, true
	}
	for _, e := range eventsNamed(events(t, r), "package.finish") {
		if e.str("label") != label {
			continue
		}
		var paths []string
		if ps, ok := e["paths"].([]any); ok {
			for _, p := range ps {
				if s, ok := p.(string); ok {
					paths = append(paths, s)
				}
			}
		}
		return paths, r, true
	}
	t.Errorf("no package.finish event for %s:\n%s", label, r.Stdout)
	return nil, r, false
}

// R-FW-023 (a): a step that runs curl fails with the network error template (BOB-E-2009).
func TestRFW023StepCannotUseNetwork(t *testing.T) {
	parity.Covers(t, "R-FW-023")
	r := bobRun(t, repo(t, "steps"), "build", "//steps:curl", "--json")
	if harness.Red(t, r) || !exitIs(t, r, 1) {
		return
	}
	e, ok := errorWithCode(t, r, "BOB-E-2009")
	if ok && e.Label != "//steps:curl" {
		t.Errorf("error label %q, want //steps:curl", e.Label)
	}
}

// R-FW-023 (b) and the Statement: the step sees an empty /home and exactly the documented
// environment, with PATH made of the declared tools' bin/ only.
func TestRFW023StepEnvironment(t *testing.T) {
	parity.Covers(t, "R-FW-023")
	dir := repo(t, "steps")
	home, r, ok := outputPaths(t, dir, nil, "//steps:home")
	if !ok || !exitIs(t, r, 0) {
		return
	}
	if len(home) == 0 {
		t.Fatalf("no output path for //steps:home")
	}
	if got := strings.TrimSpace(readFile(t, filepath.Join(home[0], "home-count.txt"))); got != "0" {
		t.Errorf("the step sees %s entries in /home, want 0:\n%s", got, readFile(t, filepath.Join(home[0], "listing.txt")))
	}
	paths, r, ok := outputPaths(t, dir, nil, "//steps:env")
	if !ok || !exitIs(t, r, 0) || len(paths) == 0 {
		return
	}
	env := map[string]string{}
	for _, l := range lines(readFile(t, filepath.Join(paths[0], "env.txt"))) {
		k, v, _ := strings.Cut(l, "=")
		env[k] = v
	}
	for k, want := range map[string]string{
		"HOME": "/homeless-shelter", "TMPDIR": "/build/tmp", "BOB_SRC": "/build/src",
		"SOURCE_DATE_EPOCH": "1", "TZ": "UTC", "LC_ALL": "C",
	} {
		if env[k] != want {
			t.Errorf("%s=%q in the step, want %q", k, env[k], want)
		}
	}
	if !regexp.MustCompile(`^/nix/store/[0-9a-z]{32}-.*steps.*env`).MatchString(env["BOB_OUT"]) && !strings.HasPrefix(env["BOB_OUT"], "/nix/store/") {
		t.Errorf("BOB_OUT=%q is not the output store path", env["BOB_OUT"])
	}
	if strings.TrimSpace(readFile(t, filepath.Join(paths[0], "pwd.txt"))) != "/build/src" {
		t.Errorf("the working directory of the step is not /build/src")
	}
	for _, d := range strings.Split(env["PATH"], ":") {
		if !strings.HasPrefix(d, "/nix/store/") || !strings.HasSuffix(d, "/bin") {
			t.Errorf("PATH entry %q is not the bin/ of a declared tool", d)
		}
	}
	for _, k := range []string{"http_proxy", "https_proxy", "SSL_CERT_FILE", "NIX_BUILD_TOP", "USER", "SHELL"} {
		if v, found := env[k]; found {
			t.Errorf("the step sees %s=%q (not part of the step environment)", k, v)
		}
	}
}

// R-FW-023 Statement: a declared output the step did not create is a build error; undeclared
// files in $out are kept.
func TestRFW023DeclaredOutputs(t *testing.T) {
	parity.Covers(t, "R-FW-023")
	dir := repo(t, "steps")
	r := bobRun(t, dir, "build", "//steps:missing")
	if harness.Red(t, r) || !exitIs(t, r, 1) {
		return
	}
	if !strings.Contains(r.Stderr, "//steps:missing: step finished but did not create promised.txt") {
		t.Errorf("stderr lacks \"//steps:missing: step finished but did not create promised.txt\":\n%s", r.Stderr)
	}
	paths, r, ok := outputPaths(t, dir, nil, "//steps:extra")
	if !ok || !exitIs(t, r, 0) || len(paths) == 0 {
		return
	}
	for _, f := range []string{"declared.txt", "undeclared.txt"} {
		if _, err := os.Stat(filepath.Join(paths[0], f)); err != nil {
			t.Errorf("the output lacks %s: %v", f, err)
		}
	}
}

// R-FW-023 (c): the same step on two machines (two separate bob homes) gives the same output
// path and the same content (NAR hash: path, mode class and bytes of every entry).
func TestRFW023SameOutputOnTwoMachines(t *testing.T) {
	parity.Covers(t, "R-FW-023")
	dir := repo(t, "steps")
	var paths, trees []string
	for i := 0; i < 2; i++ {
		env := []string{"BOB_HOME=" + t.TempDir()}
		p, r, ok := outputPaths(t, dir, env, "//steps:stable")
		if !ok || !exitIs(t, r, 0) || len(p) == 0 {
			return
		}
		paths = append(paths, filepath.Base(p[0]))
		trees = append(trees, treeHash(t, p[0]))
	}
	if paths[0] != paths[1] {
		t.Errorf("output store paths differ: %s vs %s", paths[0], paths[1])
	}
	if trees[0] != trees[1] {
		t.Errorf("output contents differ between the two machines")
	}
}

// R-FW-023 (d): no exported parameter, field or type of the public API (L0–L3) is named or
// typed Nix…Expr: there is no API that accepts Nix code.
func TestRFW023NoNixExprInAPI(t *testing.T) {
	parity.Covers(t, "R-FW-023")
	root := harness.Root(t)
	re := regexp.MustCompile(`Nix\w*Expr`)
	fset := token.NewFileSet()
	checked := 0
	for _, p := range apiPackages {
		_ = filepath.WalkDir(filepath.Join(root, filepath.FromSlash(p)), func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if d.IsDir() && (d.Name() == "gen" || d.Name() == "bop" || d.Name() == "testdata") {
				return filepath.SkipDir
			}
			if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			f, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
			if err != nil {
				t.Errorf("%s: %v", path, err)
				return nil
			}
			checked++
			ast.Inspect(f, func(n ast.Node) bool {
				if id, ok := n.(*ast.Ident); ok && re.MatchString(id.Name) {
					t.Errorf("%s: identifier %s accepts or names Nix code", fset.Position(id.Pos()), id.Name)
				}
				return true
			})
			return nil
		})
	}
	if checked == 0 {
		t.Fatalf("no API source files found")
	}
}

// testRun runs bob test --json in dir; ok is false when the test is red (feature missing).
func testRun(t *testing.T, dir string, args ...string) (harness.Result, []event, bool) {
	t.Helper()
	r := bobRun(t, dir, append([]string{"test", "--json"}, args...)...)
	if harness.Red(t, r) {
		return r, nil, false
	}
	return r, events(t, r), true
}

func counts(t *testing.T, evs []event) map[string]float64 {
	t.Helper()
	fin := eventsNamed(evs, "run.finish")
	if len(fin) != 1 {
		t.Fatalf("want one run.finish event, got %d", len(fin))
	}
	out := map[string]float64{}
	if c, ok := fin[0]["counts"].(map[string]any); ok {
		for k, v := range c {
			out[k], _ = v.(float64)
		}
	}
	return out
}

// R-FW-024 (a): the second bob test //... with no change runs no builder: every test result is
// cached and nothing is built. (The no-op time budget is B-016/B-021 in tests/budgets.)
func TestRFW024PassingTestsAreCached(t *testing.T) {
	parity.Covers(t, "R-FW-024")
	dir := harness.Fixture(t, "acme")
	r, first, ok := testRun(t, dir, "//cmd/api/...", "//internal/greet/...")
	if !ok || !exitIs(t, r, 0) {
		return
	}
	results := eventsNamed(first, "test.result")
	if len(results) == 0 {
		t.Fatalf("the first run reported no test.result:\n%s", r.Stdout)
	}
	r, second, ok := testRun(t, dir, "//cmd/api/...", "//internal/greet/...")
	if !ok || !exitIs(t, r, 0) {
		return
	}
	if n := len(eventsNamed(second, "test.result")); n != len(results) {
		t.Errorf("second run has %d test.result events, first had %d", n, len(results))
	}
	for _, e := range eventsNamed(second, "test.result") {
		if e.str("status") != "cached" {
			t.Errorf("second run: %s is %s, want cached", e.str("label"), e.str("status"))
		}
	}
	if c := counts(t, second); c["built"] != 0 || c["failed"] != 0 {
		t.Errorf("second run built %v derivations, want 0 (counts %v)", c["built"], c)
	}
}

// R-FW-024 (b): a failing test reruns on every invocation (a failure is never cached).
func TestRFW024FailingTestReruns(t *testing.T) {
	parity.Covers(t, "R-FW-024")
	dir := harness.Fixture(t, "acme")
	matches, _ := filepath.Glob(filepath.Join(dir, "internal", "greet", "*_test.go"))
	if len(matches) == 0 {
		t.Fatal("harness: acme has no internal/greet test")
	}
	writeFile(t, matches[0], readFile(t, matches[0])+"\nfunc TestAlwaysFails(t *testing.T) { t.Fatal(\"fails on purpose\") }\n")
	for run := 1; run <= 2; run++ {
		r, evs, ok := testRun(t, dir, "//internal/greet/...")
		if !ok {
			return
		}
		if r.Exit == 0 {
			t.Fatalf("run %d: bob test passed with a failing test", run)
		}
		started := 0
		for _, e := range eventsNamed(evs, "package.start") {
			if e.str("action") == "test" || e.str("action") == "build" {
				started++
			}
		}
		if started == 0 {
			t.Errorf("run %d: no test derivation started: the failure was served from a cache", run)
		}
		for _, e := range eventsNamed(evs, "test.result") {
			if e.str("status") == "cached" {
				t.Errorf("run %d: failing test %s reported cached", run, e.str("label"))
			}
		}
	}
}

// R-FW-024 Statement: a passing test's output holds test-result.json (v1) and test.log;
// bob test --rerun forces a rebuild of the selected tests.
func TestRFW024TestOutputAndRerun(t *testing.T) {
	parity.Covers(t, "R-FW-024")
	dir := harness.Fixture(t, "acme")
	r := bobRun(t, dir, "test", "//internal/greet/...", "--print-paths", "--json")
	if harness.Red(t, r) || !exitIs(t, r, 0) {
		return
	}
	checked := false
	for _, e := range eventsNamed(events(t, r), "package.finish") {
		ps, _ := e["paths"].([]any)
		for _, p := range ps {
			dirp, _ := p.(string)
			b, err := os.ReadFile(filepath.Join(dirp, "test-result.json"))
			if err != nil {
				continue
			}
			checked = true
			var tr struct {
				Version int    `json:"version"`
				Label   string `json:"label"`
				Passed  bool   `json:"passed"`
			}
			if err := json.Unmarshal(b, &tr); err != nil || tr.Version != 1 || !tr.Passed || tr.Label == "" {
				t.Errorf("test-result.json of %s is not a passing v1 result: %s", e.str("label"), b)
			}
			if _, err := os.Stat(filepath.Join(dirp, "test.log")); err != nil {
				t.Errorf("%s has no test.log: %v", dirp, err)
			}
		}
	}
	if !checked {
		t.Errorf("no test output with test-result.json among the printed paths:\n%s", r.Stdout)
	}
	rr, evs, ok := testRun(t, dir, "//internal/greet/...", "--rerun")
	if !ok || !exitIs(t, rr, 0) {
		return
	}
	if c := counts(t, evs); c["built"] == 0 {
		t.Errorf("bob test --rerun built nothing (counts %v)", c)
	}
	for _, e := range eventsNamed(evs, "test.result") {
		if e.str("status") == "cached" {
			t.Errorf("--rerun reported %s as cached", e.str("label"))
		}
	}
}

// procsNamed counts running processes whose command name is one of names.
func procsNamed(names ...string) int {
	ents, _ := os.ReadDir("/proc")
	n := 0
	for _, e := range ents {
		b, err := os.ReadFile(filepath.Join("/proc", e.Name(), "comm"))
		if err != nil {
			continue
		}
		c := strings.TrimSpace(string(b))
		for _, want := range names {
			if c == want {
				n++
			}
		}
	}
	return n
}

// R-FW-025 (a): a Postgres test passes in ≤ 3 s, two such tests in parallel both pass, 0
// service processes remain. (Budget row: S13 measured 1.3–2.0 s.)
func TestRFW025PostgresServices(t *testing.T) {
	parity.Covers(t, "R-FW-025")
	dir := repo(t, "svc")
	// Warm the store (toolchains, postgres) so the timed run measures the services.
	if r := bobRun(t, dir, "build", "//..."); harness.Red(t, r) || !exitIs(t, r, 0) {
		return
	}
	r := bobRun(t, dir, "test", "//it:it", "//it:it2", "--json", "--rerun")
	if !exitIs(t, r, 0) {
		return
	}
	results := eventsNamed(events(t, r), "test.result")
	if len(results) != 2 {
		t.Fatalf("want 2 test.result events (both tests in parallel), got %d:\n%s", len(results), r.Stdout)
	}
	for _, e := range results {
		if e.str("status") != "pass" {
			t.Errorf("%s: %s, want pass", e.str("label"), e.str("status"))
		}
		if ms, _ := e["durationMs"].(float64); ms > 3000 {
			t.Errorf("%s took %.0f ms, want ≤ 3000 ms", e.str("label"), ms)
		}
	}
	if n := procsNamed("postgres", "pg_ctl", "sleeper"); n != 0 {
		t.Errorf("%d service processes remain after the tests", n)
	}
}

// R-FW-025 (b): a never-ready service fails with "<label>: service <name> did not become ready
// within 30s" followed by the last 20 log lines of the service.
func TestRFW025NeverReadyService(t *testing.T) {
	parity.Covers(t, "R-FW-025")
	dir := repo(t, "svc")
	start := time.Now()
	r := bobRun(t, dir, "test", "//it:never")
	if harness.Red(t, r) || !exitIs(t, r, 1) {
		return
	}
	if !regexp.MustCompile(`//it:never: service \S+ did not become ready within 30s`).MatchString(r.Stderr) {
		t.Errorf("stderr lacks \"//it:never: service <name> did not become ready within 30s\":\n%s", r.Stderr)
	}
	for i := 11; i <= 30; i++ {
		if !strings.Contains(r.Stderr, fmt.Sprintf("sleeper line %d\n", i)) {
			t.Errorf("the last 20 log lines miss \"sleeper line %d\"", i)
		}
	}
	if strings.Contains(r.Stderr, "sleeper line 10\n") {
		t.Errorf("more than the last 20 log lines are shown")
	}
	if d := time.Since(start); d < 25*time.Second {
		t.Errorf("failed after %s, before the 30 s readiness timeout", d)
	}
	if n := procsNamed("sleeper"); n != 0 {
		t.Errorf("%d sleeper processes remain", n)
	}
}

// R-FW-020: a synthetic chain of N=100 trivial bob.Run steps builds cold in ≤ 12 s at both
// profiles (SPEC acceptance text; the budget stage does not duplicate it).
func TestRFW020ChainOf100Steps(t *testing.T) {
	parity.Covers(t, "R-FW-020")
	for _, variant := range []string{"release", "debug"} {
		t.Run(variant, func(t *testing.T) {
			dir := chainRepo(t, 100)
			// The store is cold for the chain's own derivations; tool closures may be warm.
			warm := bobRun(t, dir, "build", "//chain:s000", "--variant", variant)
			if harness.Red(t, warm) || !exitIs(t, warm, 0) {
				return
			}
			start := time.Now()
			r := bobRun(t, dir, "build", "//chain:s099", "--variant", variant)
			if !exitIs(t, r, 0) {
				return
			}
			if d := time.Since(start); d > 12*time.Second {
				t.Errorf("cold chain of 100 steps took %s, want ≤ 12 s", d.Round(time.Millisecond))
			}
		})
	}
}

// chainRepo writes a repository with n steps s000…s(n-1), each depending on the previous one.
func chainRepo(t *testing.T, n int) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "chain")
	var b strings.Builder
	b.WriteString("//go:build bob\n\npackage chain\n\nimport \"github.com/megakuul/bob/pkg/bob\"\n\n")
	for i := 0; i < n; i++ {
		in := "nil"
		if i > 0 {
			in = fmt.Sprintf("map[string]bob.Tree{\"prev\": s%03d(p)}", i-1)
		}
		fmt.Fprintf(&b, "var s%03d = bob.Define(func(p *bob.P) *bob.Step {\n\treturn bob.Run(p, \"s%03d\", bob.RunOpts{Cmd: []string{\"true\"}, Inputs: %s, Env: map[string]string{\"N\": \"%d\"}})\n})\n\n", i, i, in, i)
	}
	writeFile(t, filepath.Join(dir, "chain", "bob.go"), b.String())
	writeFile(t, filepath.Join(dir, "bob.go"), "//go:build bob\n\npackage chain\n\nimport \"github.com/megakuul/bob/pkg/bob\"\n\nvar _ = bob.Repo(bob.RepoOpts{Nixpkgs: \"nixos-26.05\"})\n")
	writeFile(t, filepath.Join(dir, "go.mod"), "module example.com/chain\n\ngo 1.26.0\n")
	writeFile(t, filepath.Join(dir, "bob.lock"), "{\n  \"lockVersion\": 1\n}\n")
	gitInit(t, dir)
	return dir
}
