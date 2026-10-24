package core

// SPEC §1.2 bob.go convention (ADR 0003): R-FW-001, R-FW-002, R-FW-043, R-FW-044, R-FW-008.
// R-FW-045 (evaluator sandbox) is suite tests/sandbox.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/megakuul/bob/pkg/bob/diag"
	"github.com/megakuul/bob/pkg/engine/ir"
	"github.com/megakuul/bob/tests/determinism"
	"github.com/megakuul/bob/tests/harness"
	"github.com/megakuul/bob/tests/parity"
	"github.com/megakuul/bob/tests/serverenv"
)

// runAs runs path as uid/gid (when the test runs as root) with HOME=home in dir.
func runAs(t *testing.T, uid, gid uint32, home, dir string, env []string, path string, args ...string) harness.Result {
	t.Helper()
	cmd := exec.Command(path, args...)
	cmd.Dir = dir
	cmd.Env = append([]string{"PATH=/usr/bin:/bin", "HOME=" + home, "TZ=UTC", "LC_ALL=C"}, env...)
	if os.Geteuid() == 0 {
		cmd.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: uid, Gid: gid}}
		// The harness builds its binaries into a 0700 temp directory; bobtest must reach them.
		if info, err := os.Stat(filepath.Dir(path)); err == nil && info.Mode().Perm()&0o055 != 0o055 {
			_ = os.Chmod(filepath.Dir(path), 0o755)
		}
	}
	r := runCmd(t, cmd, args)
	noLeak(t, r)
	return r
}

// pinnedGopls realises gopls from the nixpkgs pin (HARNESS.md §1.4).
func pinnedGopls(t *testing.T) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	p, err := serverenv.NixTool(ctx, "gopls")
	if err != nil {
		t.Fatalf("gopls from the nixpkgs pin: %v", err)
	}
	return filepath.Join(p, "bin", "gopls")
}

// R-FW-001: the S1 LSP script passes against examples/acme with gopls from the pin, directly
// and through the .bob/bin/gopls shim, as a non-root user, in bind and pivot mode.
func TestRFW001GoplsSeesBobGo(t *testing.T) {
	parity.Covers(t, "R-FW-001")
	uid, gid := unprivileged(t)
	dir := harness.Fixture(t, "acme")
	home := filepath.Join(t.TempDir(), "home")
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatal(err)
	}
	chownTree(t, dir, uid, gid)
	chownTree(t, home, uid, gid)
	bobBin := harness.Bin(t, "bob")
	// bob gen writes the generated go.mod (framework require); bob env writes .bob/env.json and
	// the shims (§02.7.2, §02.7.3).
	if r := runAs(t, uid, gid, home, dir, nil, bobBin, "gen"); harness.Red(t, r) || !exitIs(t, r, 0) {
		return
	}
	r := runAs(t, uid, gid, home, dir, nil, bobBin, "env", "--format", "json")
	if harness.Red(t, r) || !exitIs(t, r, 0) {
		return
	}
	var env struct {
		Outside struct {
			Set     map[string]string   `json:"set"`
			Prepend map[string][]string `json:"prepend"`
		} `json:"outside"`
	}
	if err := json.Unmarshal([]byte(r.Stdout), &env); err != nil {
		t.Fatalf("bob env --format json: %v\n%s", err, r.Stdout)
	}
	shim := filepath.Join(dir, ".bob", "bin", "gopls")
	if _, err := os.Stat(shim); err != nil {
		t.Fatalf("bob env wrote no gopls shim: %v", err)
	}
	gopls := pinnedGopls(t)
	steps, err := parseSteps(readFile(t, filepath.Join(dir, "bobtest", "lsp", "acme.steps")))
	if err != nil {
		t.Fatal(err)
	}
	direct := []string{"PATH=" + strings.Join(append(env.Outside.Prepend["PATH"], "/usr/bin", "/bin"), ":")}
	for k, v := range env.Outside.Set {
		direct = append(direct, k+"="+v)
	}
	settings := map[string]any{"build.buildFlags": []string{"-tags=bob"}}
	modes := []struct {
		name string
		argv []string
		env  []string
	}{
		{"direct", []string{gopls}, direct},
		{"shim-bind", []string{shim}, nil},
		// Pivot mode: the host /nix is hidden, so bob must build its tmpfs root (R-NIX-021).
		{"shim-pivot", []string{"/usr/bin/unshare", "--user", "--map-current-user", "--mount", "--",
			"/bin/sh", "-c", `mount -t tmpfs tmpfs /nix && exec "$0" "$@"`, shim}, nil},
	}
	for _, m := range modes {
		t.Run(m.name, func(t *testing.T) {
			cmd := exec.Command(m.argv[0], m.argv[1:]...)
			cmd.Dir = dir
			cmd.Env = append([]string{"PATH=/usr/bin:/bin", "HOME=" + home, "TZ=UTC", "LC_ALL=C"}, m.env...)
			if os.Geteuid() == 0 {
				cmd.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: uid, Gid: gid}}
			}
			c, err := startLSP(cmd, dir, settings)
			if err != nil {
				t.Fatalf("start gopls (%s): %v", m.name, err)
			}
			defer c.close()
			runAcmeSteps(t, c, steps)
			if t.Failed() {
				t.Logf("gopls stderr:\n%s", c.stderr.String())
			}
		})
	}
}

// runAcmeSteps runs bobtest/lsp/acme.steps and checks the S1 expectations: no diagnostics on
// the unchanged files, completion foo. → Lib, definition foo.Lib → libs/foo/bob.go:12, a type
// error for the wrong handle type and none after the change is undone.
func runAcmeSteps(t *testing.T, c *lspClient, steps []lspStep) {
	t.Helper()
	changed := false
	for i, st := range steps {
		where := fmt.Sprintf("step %d (%s %s %q)", i+1, st.Op, st.File, st.Args)
		switch st.Op {
		case "open":
			if err := c.open(st.File); err != nil {
				t.Fatalf("%s: %v", where, err)
			}
		case "change":
			if err := c.change(st.File, st.Args[0], st.Args[1]); err != nil {
				t.Fatalf("%s: %v", where, err)
			}
			changed = !changed
		case "diag":
			var errs []string
			for _, d := range c.diagnostics(st.File) {
				if d.Severity == 1 {
					errs = append(errs, fmt.Sprintf("%d:%d: %s", d.Range.Start.Line+1, d.Range.Start.Character+1, d.Message))
				}
			}
			switch {
			case changed && len(errs) == 0:
				t.Errorf("%s: passing a *golang.Binary as a cc.Linkable gave no type error", where)
			case changed && !strings.Contains(strings.Join(errs, "\n"), "LinkInfo"):
				t.Errorf("%s: the type error does not name the missing method LinkInfo: %v", where, errs)
			case !changed && len(errs) > 0:
				t.Errorf("%s: unexpected errors: %v", where, errs)
			}
		case "complete":
			res, err := c.at("textDocument/completion", st.File, st.Args[0], 0)
			if err != nil {
				t.Errorf("%s: %v", where, err)
				continue
			}
			want := map[string]string{"foo.": "Lib", "greet.": "Hello"}[st.Args[0]]
			if want == "" {
				want = st.Args[0][len(st.Args[0])-1:] // "…{\n\t\tX" → the field X
			}
			if !strings.Contains(string(res), `"label":"`+want) {
				t.Errorf("%s: completion does not offer %s: %.400s", where, want, res)
			}
		case "def":
			res, err := c.at("textDocument/definition", st.File, st.Args[0], 1)
			if err != nil {
				t.Errorf("%s: %v", where, err)
				continue
			}
			var locs []struct {
				URI   string `json:"uri"`
				Range struct {
					Start struct{ Line int } `json:"start"`
				} `json:"range"`
			}
			if err := json.Unmarshal(res, &locs); err != nil || len(locs) == 0 {
				t.Errorf("%s: no definition: %s", where, res)
				continue
			}
			got := fmt.Sprintf("%s:%d", strings.TrimPrefix(locs[0].URI, "file://"+c.root+"/"), locs[0].Range.Start.Line+1)
			switch st.Args[0] {
			case "foo.Lib":
				if got != "libs/foo/bob.go:12" {
					t.Errorf("%s: definition is %s, want libs/foo/bob.go:12", where, got)
				}
			case "greet.Hello":
				if !strings.HasPrefix(got, "internal/greet/greet.go:") {
					t.Errorf("%s: definition is %s, want internal/greet/greet.go", where, got)
				}
			case "cc.SharedLibrary":
				if !regexp.MustCompile(`github\.com/megakuul/bob@[^/]+/pkg/cc/[^/]+\.go:\d+$`).MatchString(got) {
					t.Errorf("%s: definition is %s, want a file of the framework module's pkg/cc", where, got)
				}
			}
		case "hover":
			res, err := c.at("textDocument/hover", st.File, st.Args[0], 1)
			if err != nil || len(res) == 0 || string(res) == "null" {
				t.Errorf("%s: no hover: %v %s", where, err, res)
			}
		default:
			t.Fatalf("%s: unknown step", where)
		}
	}
}

// R-FW-002: one sub-repo per rule; bob build //... exits 3 and stderr contains exactly the
// rule's text with the paths filled in; R5 produces the stub and gopls (CGO_ENABLED=1)
// reports no diagnostics. All violations are reported together, sorted by path.
func TestRFW002BobGoRules(t *testing.T) {
	parity.Covers(t, "R-FW-002")
	cases := []struct {
		rule string
		want []string
	}{
		{"r1", []string{`lib/bob.go: the first line must be "//go:build bob" so that go build, go vet and go test ignore it`}},
		{"r2", []string{`lib/bob.go: package other, but the Go code in this directory is package lib; bob.go must use package lib`}},
		{"r3", []string{`lib/bob.go: imports the build definitions of cmd/app/, which is a main package and cannot be imported; define the shared package in a bob.go of a non-main directory`}},
		{"r4", []string{`lib/bob.go:8: Files is also declared at lib/lib.go:5; bob.go shares its package with that code, rename one of them`}},
		{"r6", []string{`b/bob.go imports a, whose Go code imports b (cycle exists only for build definitions); move the definition`}},
		{"r7", []string{`lib/bob.go: imports github.com/megakuul/bob/pkg/engine/ir, which build definitions cannot use; bob.go may import the bob framework, the standard library and other bob.go packages`}},
		{"all", []string{
			`a/bob.go: the first line must be "//go:build bob" so that go build, go vet and go test ignore it`,
			`b/bob.go: imports github.com/megakuul/bob/pkg/engine/ir, which build definitions cannot use; bob.go may import the bob framework, the standard library and other bob.go packages`,
		}},
	}
	for _, c := range cases {
		t.Run(c.rule, func(t *testing.T) {
			dir := repo(t, "rules/"+c.rule)
			r := bobRun(t, dir, "build", "//...")
			if harness.Red(t, r) || !exitIs(t, r, 3) {
				return
			}
			if !strings.Contains(r.Stderr, "BOB-E-1024") {
				t.Errorf("rule violation is not reported as BOB-E-1024:\n%s", r.Stderr)
			}
			last := -1
			for _, w := range c.want {
				i := strings.Index(r.Stderr, w)
				if i < 0 {
					t.Errorf("stderr does not contain the %s text %q:\n%s", c.rule, w, r.Stderr)
					continue
				}
				if i < last {
					t.Errorf("violations are not sorted by path:\n%s", r.Stderr)
				}
				last = i
			}
		})
	}
	t.Run("r5", func(t *testing.T) {
		dir := repo(t, "rules/r5")
		r := bobRun(t, dir, "build", "//...")
		if harness.Red(t, r) || !exitIs(t, r, 0) {
			return
		}
		got := lines(readFile(t, filepath.Join(dir, "native", "zz_bob_cgo.go")))
		want := []string{"//go:build bob && cgo", "package native", `import "C"`}
		if strings.Join(got, "\n") != strings.Join(want, "\n") {
			t.Errorf("native/zz_bob_cgo.go = %q, want the lines %q", got, want)
		}
		if out, err := exec.Command("git", "-C", dir, "check-ignore", "-q", "native/zz_bob_cgo.go").CombinedOutput(); err != nil {
			t.Errorf("native/zz_bob_cgo.go is not ignored by git: %v %s", err, out)
		}
		// gopls runs the go shim of the dev environment (bob env writes it, §02.7.3).
		if r := bobRun(t, dir, "env"); !exitIs(t, r, 0) {
			return
		}
		// gopls check prints one line per diagnostic.
		check := exec.Command(pinnedGopls(t), "check", "native/bob.go", "native/native.go")
		check.Dir = dir
		check.Env = append(os.Environ(), "CGO_ENABLED=1", "GOFLAGS=-tags=bob",
			"PATH="+filepath.Join(dir, ".bob", "bin")+":/usr/bin:/bin")
		out, err := check.CombinedOutput()
		if err != nil || len(bytes.TrimSpace(out)) > 0 {
			t.Errorf("gopls check (CGO_ENABLED=1, -tags=bob) reports diagnostics: %v\n%s", err, out)
		}
	})
}

// R-FW-043 (a): two copies of examples/acme at different paths give byte-identical IR.
func TestRFW043EvaluatorIRIndependentOfPath(t *testing.T) {
	parity.Covers(t, "R-FW-043")
	a, ok := evaluate(t, harness.Fixture(t, "acme"))
	if !ok {
		return
	}
	b, ok := evaluate(t, harness.Fixture(t, "acme"))
	if !ok {
		return
	}
	if len(a.IR) == 0 {
		t.Errorf("evaluation produced no IR")
	}
	if !bytes.Equal(a.IR, b.IR) {
		t.Errorf("IR of two copies of acme differs (%d vs %d bytes)", len(a.IR), len(b.IR))
	}
}

// R-FW-043 (b): a real Go file in a bob.go directory that does not compile does not affect
// evaluation.
func TestRFW043EvaluatorHidesOtherGoFiles(t *testing.T) {
	parity.Covers(t, "R-FW-043")
	clean, ok := evaluate(t, harness.Fixture(t, "acme"))
	if !ok {
		return
	}
	dir := harness.Fixture(t, "acme")
	writeFile(t, filepath.Join(dir, "libs", "foo", "generated.go"), "package foo\n\nvar X = generatedByBob\n")
	dirty, ok := evaluate(t, dir)
	if !ok {
		return
	}
	if !bytes.Equal(clean.IR, dirty.IR) {
		t.Errorf("a non-compiling Go file next to libs/foo/bob.go changed the IR")
	}
}

// R-FW-043 (c): a type error in cmd/api/bob.go (main package) is reported as
// cmd/api/bob.go:<line>:<col>: ….
func TestRFW043TypeErrorInMainBobGo(t *testing.T) {
	parity.Covers(t, "R-FW-043")
	dir := harness.Fixture(t, "acme")
	path := filepath.Join(dir, "cmd", "api", "bob.go")
	src := readFile(t, path)
	line := strings.Count(src, "\n") + 2
	writeFile(t, path, src+"\nvar _ int = \"not an int\"\n")
	r := bobRun(t, dir, "build", "//...")
	if harness.Red(t, r) || !exitIs(t, r, 3) {
		return
	}
	pos := regexp.MustCompile(fmt.Sprintf(`(?m)(^|\s)cmd/api/bob\.go:%d:13: `, line))
	if !pos.MatchString(r.Stderr) {
		t.Errorf("the type error is not reported at cmd/api/bob.go:%d:13:\n%s", line, r.Stderr)
	}
	if strings.Contains(r.Combined(), ".bob/eval") || strings.Contains(r.Combined(), "bobmain") {
		t.Errorf("the compiler message names the evaluator's rewritten file:\n%s", r.Combined())
	}
}

// R-FW-043 (d): with the repo unchanged, the second evaluation does not run go build
// (--json event eval with cached=true).
func TestRFW043EvaluatorCached(t *testing.T) {
	parity.Covers(t, "R-FW-043")
	dir := harness.Fixture(t, "acme")
	first := bobRun(t, dir, "graph", "--json", "//...")
	if harness.Red(t, first) || !exitIs(t, first, 0) {
		return
	}
	for _, e := range eventsNamed(events(t, first), "eval") {
		if c, _ := e["cached"].(bool); c {
			t.Errorf("the first evaluation of a fresh copy reports cached=true")
		}
	}
	second := bobRun(t, dir, "graph", "--json", "//...")
	if !exitIs(t, second, 0) {
		return
	}
	evs := eventsNamed(events(t, second), "eval")
	if len(evs) == 0 {
		t.Fatalf("no eval event in the second run:\n%s", second.Stdout)
	}
	for _, e := range evs {
		if c, _ := e["cached"].(bool); !c {
			t.Errorf("the second evaluation of an unchanged repo is not cached: %v", e)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, ".bob", "eval", "key")); err != nil {
		t.Errorf(".bob/eval/key: %v", err)
	}
}

// minimalIR is a valid IR v1 document of bob version v.
func minimalIR(v string) []byte {
	return []byte(`{"bobVersion":"` + v + `","configs":[],"irVersion":1,"module":"example.com/x","nodes":[],"pipelines":[],"repo":{"nixpkgs":"nixos-26.05","variants":[]},"roots":[]}`)
}

// R-FW-044: IR with another bobVersion is rejected: error class internal, exit 70.
func TestRFW044IRVersionLock(t *testing.T) {
	parity.Covers(t, "R-FW-044")
	const own = "0.1.0"
	g, err := ir.Parse(minimalIR(own), own)
	if isNotImplemented(err) {
		harness.RedFeature(t, "ir.Parse")
		return
	}
	if err != nil || g == nil || g.BobVersion != own {
		t.Fatalf("ir.Parse of a valid document of the own version: %v, %+v", err, g)
	}
	_, err = ir.Parse(minimalIR("0.0.9"), own)
	var d *diag.Error
	if !errors.As(err, &d) {
		t.Fatalf("ir.Parse of another bobVersion = %v, want a *diag.Error", err)
	}
	if d.Code == diag.CodeNotImplemented {
		harness.RedFeature(t, "ir.Parse bobVersion check")
		return
	}
	if d.Code.Class() != diag.ClassInternal {
		t.Errorf("another bobVersion gives %s (class %s), want class internal", d.Code.ID(), d.Code.Class())
	}
	if got := diag.ExitCode([]*diag.Error{d}); got != 70 {
		t.Errorf("exit code %d, want 70", got)
	}
}

// R-FW-008 (a): bob check --determinism passes on every example repo.
func TestRFW008DeterminismCheckPasses(t *testing.T) {
	parity.Covers(t, "R-FW-008")
	names, err := determinism.Fixtures(filepath.Join(harness.Root(t), "tests", "fixtures", "repos"))
	if err != nil || len(names) == 0 {
		t.Fatalf("no fixture repositories: %v", err)
	}
	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			r := bobRun(t, harness.Fixture(t, name), "check", "--determinism")
			if harness.Red(t, r) {
				return
			}
			exitIs(t, r, 0)
		})
	}
}

// R-FW-008 (b): a bob.go that builds a deps slice by ranging over a map with ≥ 8 keys makes
// bob check --determinism fail within 5 runs with BOB-E-1018.
func TestRFW008NondeterministicBobGo(t *testing.T) {
	parity.Covers(t, "R-FW-008")
	dir := repo(t, "nondet")
	for run := 1; run <= 5; run++ {
		r := bobRun(t, dir, "check", "--determinism", "--json")
		if harness.Red(t, r) {
			return
		}
		if r.Exit == 0 {
			continue
		}
		exitIs(t, r, 3)
		e, ok := errorWithCode(t, r, "BOB-E-1018")
		if !ok {
			return
		}
		msg := regexp.MustCompile(`^evaluating bob\.go is not deterministic: //nondet:nondet differs between two runs \(field [^)]*deps[^)]*\)$`)
		if !msg.MatchString(e.Message) {
			t.Errorf("message %q, want %s", e.Message, msg)
		}
		if e.Hint != "do not range over maps when building lists; sort the keys" {
			t.Errorf("hint %q", e.Hint)
		}
		return
	}
	t.Errorf("bob check --determinism passed 5 times on a bob.go that ranges over a map")
}
