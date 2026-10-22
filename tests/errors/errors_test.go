package errorsuite

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/megakuul/bob/pkg/bob/diag"
	"github.com/megakuul/bob/tests/errors/leakcheck"
	"github.com/megakuul/bob/tests/golden"
	"github.com/megakuul/bob/tests/parity"
	"github.com/megakuul/bob/tests/serverenv"
)

// EnvSummary names a file TestFaults writes its per-entry results to (tools/ci prints it).
const EnvSummary = "BOB_ERRORS_SUMMARY"

// EnvLive makes TestFaults run live captures for every entry (tools/ci -slow).
const EnvLive = "BOB_ERRORS_LIVE"

func root(t testing.TB) string {
	t.Helper()
	r, err := golden.Root()
	if err != nil {
		t.Fatal(err)
	}
	return r
}

var (
	binsOnce sync.Once
	bins     map[string]string
	binsErr  error
)

// buildBins builds bob and bob-server once per test binary into a temporary directory, with the
// bobtest build tag merged with the caller's GOFLAGS -tags (serverenv.BobtestTagsFlag, decision
// A109) so that BOB_TEST_NIX_REPLAY replay is compiled in, as the harness builds them.
func buildBins(t testing.TB) map[string]string {
	t.Helper()
	binsOnce.Do(func() {
		dir, err := os.MkdirTemp("", "bob-errors-bin-")
		if err != nil {
			binsErr = err
			return
		}
		bins = map[string]string{}
		for _, name := range []string{"bob", "bob-server"} {
			out := filepath.Join(dir, name)
			cmd := exec.Command("go", "build", serverenv.BobtestTagsFlag(), "-o", out, "./cmd/"+name)
			cmd.Dir = root(t)
			if b, err := cmd.CombinedOutput(); err != nil {
				binsErr = fmt.Errorf("go build ./cmd/%s: %w: %s", name, err, b)
				return
			}
			bins[name] = out
		}
	})
	if binsErr != nil {
		t.Fatal(binsErr)
	}
	return bins
}

// redTB reports a test with red entries as failed to the parity catalogue only.
type redTB struct {
	testing.TB
	red *bool
}

func (r redTB) Failed() bool { return r.TB.Failed() || *r.red }

// TestFaults runs every catalogue entry. An entry whose feature bob does not implement yet is
// red: logged, counted as failing in the parity catalogue, but not a test failure. A crash, a raw
// Nix leak or a wrong answer from an implemented feature fails the test.
func TestFaults(t *testing.T) {
	red := false
	parity.Covers(redTB{t, &red}, "R-NIX-010", "R-NIX-012", "R-CLI-006")
	r := root(t)
	entries, err := Load(r)
	if err != nil {
		t.Fatal(err)
	}
	runner := &Runner{Root: r, Bins: buildBins(t), Scratch: t.TempDir(), Live: os.Getenv(EnvLive) == "1"}
	var results []Result
	for _, e := range entries {
		t.Run(e.ID, func(t *testing.T) {
			res := runner.Run(context.Background(), e)
			results = append(results, res)
			for _, n := range res.Notes {
				t.Logf("note: %s", n)
			}
			switch res.Status {
			case Fail:
				for _, m := range res.Reasons {
					t.Error(m)
				}
			case Red:
				red = true
				t.Logf("expected red: %s", strings.Join(res.Red, "; "))
			case Pass:
				for _, g := range res.Golden {
					golden.Assert(t, g.Path, g.Got, golden.Human(), golden.Reqs("R-NIX-012"))
				}
			}
		})
	}
	counts := map[Status]int{}
	for _, res := range results {
		counts[res.Status]++
	}
	t.Logf("fault catalogue: %d entries: %d pass, %d red, %d fail", len(results), counts[Pass], counts[Red], counts[Fail])
	if p := os.Getenv(EnvSummary); p != "" {
		b, err := json.MarshalIndent(results, "", " ")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// TestCatalogue checks that the catalogue is complete and consistent with the code table.
func TestCatalogue(t *testing.T) {
	entries, err := Load(root(t))
	if err != nil {
		t.Fatal(err)
	}
	ids := map[string]Entry{}
	codes := map[string]bool{}
	for _, e := range entries {
		ids[e.ID] = e
		for _, oc := range e.Expect.Outcomes() {
			for _, c := range oc.Codes {
				codes[c] = true
			}
		}
	}
	// Faults 1–48 of S15 §3.2 keep their numbers.
	for n := 1; n <= 48; n++ {
		prefix := fmt.Sprintf("%02d-", n)
		found := false
		for id, e := range ids {
			if strings.HasPrefix(id, prefix) {
				found = strings.HasPrefix(e.Source, "S15 fault "+strconv.Itoa(n)+" ")
			}
		}
		if !found {
			t.Errorf("S15 fault %d is missing", n)
		}
	}
	// The additions of HARNESS.md §5.1.
	for _, c := range []string{"1011", "1012", "1019", "2013", "2014", "2015", "3004", "1024", "1025",
		"1018", "1021", "1016", "7005", "7007", "7008", "9999", "9998", "4009", "4008"} {
		if !codes["BOB-E-"+c] {
			t.Errorf("no entry expects BOB-E-%s (HARNESS.md §5.1)", c)
		}
	}
	kinds := []string{"patch", "env", "cache-fault", "limit", "signal", "disk", "ir-script", "none"}
	for _, e := range entries {
		t.Run(e.ID, func(t *testing.T) {
			if !slices.Contains(kinds, e.Inject.Kind) {
				t.Errorf("inject kind %q", e.Inject.Kind)
			}
			if len(e.Command) == 0 || e.Fixture == "" || e.Title == "" {
				t.Errorf("command, fixture and title are required")
			}
			if e.Code == "" && len(e.Expect.Codes) > 0 || e.Code != "" && !slices.Contains(e.Expect.Codes, e.Code) {
				t.Errorf("fault.json code %q is not in expect.json %v", e.Code, e.Expect.Codes)
			}
			if (e.Exit == nil) != (e.Expect.Exit == nil) || e.Exit != nil && *e.Exit != *e.Expect.Exit {
				t.Errorf("fault.json exit differs from expect.json")
			}
			for _, oc := range e.Expect.Outcomes() {
				var errs []*diag.Error
				for _, c := range oc.Codes {
					n, err := strconv.Atoi(strings.TrimPrefix(c, "BOB-E-"))
					if err != nil || !strings.HasPrefix(c, "BOB-E-") {
						t.Fatalf("code %q", c)
					}
					if _, ok := diag.Lookup(diag.Code(n)); !ok {
						t.Errorf("%s is not in the code table", c)
					}
					errs = append(errs, &diag.Error{Code: diag.Code(n), Severity: diag.SeverityError})
				}
				if oc.Exit != nil && diag.ExitCode(errs) != *oc.Exit {
					t.Errorf("exit %d, but the code table gives %d for %v", *oc.Exit, diag.ExitCode(errs), oc.Codes)
				}
			}
			if strings.HasPrefix(e.Source, "S15") {
				for _, nv := range e.NixVer {
					if e.ReplayDir(nv) == "" {
						t.Errorf("no capture for Nix %s", nv)
					}
				}
			}
			// A fault's own forbidden texts are real Nix text from its stream.
			stream := streamText(t, e)
			for _, f := range e.Forbid {
				if !strings.Contains(stream, f) {
					t.Errorf("forbidden text %q is not in the recorded stream", f)
				}
			}
		})
	}
}

func streamText(t *testing.T, e Entry) string {
	var b strings.Builder
	for _, nv := range e.NixVer {
		p := filepath.Join(e.Dir, nv, "stream.log")
		if s, err := os.ReadFile(p); err == nil {
			b.Write(s)
		} else if f, err := os.Open(p + ".gz"); err == nil {
			zr, err := gzip.NewReader(f)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := io.Copy(&b, zr); err != nil {
				t.Fatal(err)
			}
			f.Close()
		}
	}
	// Messages are JSON-encoded in the stream; decode them so escaped text is found too.
	var dec strings.Builder
	for _, l := range strings.Split(b.String(), "\n") {
		var ev struct{ Msg string }
		if rest, ok := strings.CutPrefix(l, "@nix "); ok && json.Unmarshal([]byte(rest), &ev) == nil {
			dec.WriteString(ansi.ReplaceAllString(ev.Msg, ""))
			dec.WriteByte('\n')
		}
	}
	return b.String() + dec.String()
}

var ansi = regexp.MustCompile(`\x1b\[[0-9;]*m`)

// TestCapturedGoldensDoNotLeak runs the leak check over the approved S15 outputs: the patterns
// must accept bob's real output, or the suite could never pass.
func TestCapturedGoldensDoNotLeak(t *testing.T) {
	entries, err := Load(root(t))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		for _, nv := range e.NixVer {
			for _, g := range []string{"golden.txt", "golden.tty.txt", "golden.jsonl"} {
				b, err := os.ReadFile(filepath.Join(e.Dir, nv, g))
				if err != nil {
					continue
				}
				for _, l := range leakCheck(b, e.Forbid) {
					t.Errorf("%s/%s/%s: %s", e.ID, nv, g, l)
				}
			}
		}
	}
}

// TestRunnerClassifies runs the runner against stand-in binaries for each kind of answer.
func TestRunnerClassifies(t *testing.T) {
	one := 1
	entry := Entry{
		Fault:  Fault{ID: "x", Fixture: "none", Inject: Inject{Kind: "none"}, Binary: "bob", Command: []string{"build", "//a:a"}, Hint: true, Forbid: []string{"timed out after"}},
		Expect: Expect{Outcome: Outcome{Codes: []string{"BOB-E-2001", "BOB-E-2001"}, Exit: &one}},
	}
	tests := []struct {
		name, script string
		want         Status
	}{
		{"not implemented", `echo 'error[BOB-E-9008]: bob build is not implemented yet' >&2; exit 70`, Red},
		{"pass", `printf 'error[BOB-E-2001]: a\nerror[BOB-E-2001]: b\n  hint: fix it\n' >&2; exit 1`, Pass},
		{"only one of two", `printf 'error[BOB-E-2001]: a\n  hint: x\n' >&2; exit 1`, Fail},
		{"wrong exit", `printf 'error[BOB-E-2001]: a\nerror[BOB-E-2001]: b\n  hint: x\n' >&2; exit 4`, Fail},
		{"store path leak", `echo 'error[BOB-E-9008]: /nix/store/0123456789abcdfghijklmnpqrsvwxyz-x' >&2; exit 70`, Fail},
		{"fault's own text", `echo 'error[BOB-E-9008]: timed out after 3 seconds' >&2; exit 70`, Fail},
		{"raw error line", `echo 'error: builder failed' >&2; exit 1`, Fail},
		{"panic", `echo 'panic: boom' >&2; echo 'goroutine 1 [running]:' >&2; exit 2`, Fail},
		{"json garbage", `case " $* " in *" --json "*) echo 'not json';; esac; printf 'error[BOB-E-2001]: a\nerror[BOB-E-2001]: b\n  hint: x\n' >&2; exit 1`, Fail},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			bin := filepath.Join(t.TempDir(), "bob")
			if err := os.WriteFile(bin, []byte("#!/bin/sh\n"+tt.script+"\n"), 0o755); err != nil {
				t.Fatal(err)
			}
			r := &Runner{Root: t.TempDir(), Bins: map[string]string{"bob": bin}, Scratch: t.TempDir()}
			res := r.Run(context.Background(), entry)
			if res.Status != tt.want {
				t.Fatalf("status %s, want %s: red=%v reasons=%v", res.Status, tt.want, res.Red, res.Reasons)
			}
		})
	}
}

func TestMarkerPos(t *testing.T) {
	dir := t.TempDir()
	src := "//go:build bob\n\npackage foo\n\nimport \"github.com/megakuul/bob/pkg/cc\"\n\n//bob:fault 06-cxx-compile\nvar Foo = cc.SharedLibrary(\"foo\", cc.Opts{})\n"
	if err := os.MkdirAll(filepath.Join(dir, "libs/foo"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "libs/foo/bob.go"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := MarkerPos(dir, "06-cxx-compile")
	if err != nil || got != "libs/foo/bob.go:8:11" {
		t.Fatalf("MarkerPos = %q, %v", got, err)
	}
	if _, err := MarkerPos(dir, "07-c-compile"); err == nil {
		t.Fatal("missing marker not reported")
	}
	if err := os.WriteFile(filepath.Join(dir, "libs/foo/foo.cpp"), []byte("int f() {\n  return 0 // bob:fault-site\n}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got, err := SitePos(dir, "libs/foo/foo.cpp"); err != nil || got != "libs/foo/foo.cpp:2" {
		t.Fatalf("SitePos = %q, %v", got, err)
	}
}

func TestLeakCheck(t *testing.T) {
	tests := []struct {
		in   string
		leak bool
	}{
		{"error[BOB-E-2001]: //libfoo:foo: compile error in libfoo/src/foo.cpp", false},
		{"  --> libfoo/src/foo.cpp:13:13", false},
		{"error: Cannot build '/nix/store/r9fk9k889kinxn3nbb8hypvbmcmz5vbg-api-api.drv'.", true},
		{"       Reason: builder failed with exit code 1.", true},
		{"builder for 'x' failed", true},
		{"@nix {\"action\":\"msg\"}", true},
		{"… while evaluating the attribute", true},
		{"at /nix/store/abc-source/pkgs/top-level/all-packages.nix:12:3", true},
		{"local builds are disabled (max-jobs = 0)", true},
		{"bob build --keep-going=true failed", false},
		{"cannot use the substituter", true},
	}
	for _, tt := range tests {
		if got := len(leakCheck([]byte(tt.in), nil)) > 0; got != tt.leak {
			t.Errorf("%q: leak=%v, want %v", tt.in, got, tt.leak)
		}
	}
}

func TestMissingLine(t *testing.T) {
	tests := []struct{ def, verbose, want string }{
		{"a\nb\n", "a\nnix: x\nb\n", ""},
		{"a\nb\n", "b\na\n", "b"},
		{"a\nc\n", "a\nb\n", "c"},
	}
	for _, tt := range tests {
		if got := missingLine(tt.def, tt.verbose); got != tt.want {
			t.Errorf("missingLine(%q, %q) = %q, want %q", tt.def, tt.verbose, got, tt.want)
		}
	}
}

func TestCheckJSON(t *testing.T) {
	tests := []struct {
		in  string
		bad int
	}{
		{`{"v":1,"event":"run.start"}` + "\n" + `{"v":1,"event":"run.finish"}` + "\n", 0},
		{`{"v":2,"event":"run.start"}`, 1},
		{`{"v":1,"event":"nix.msg"}`, 1},
		{`plain text`, 1},
	}
	for _, tt := range tests {
		if got := checkJSON([]byte(tt.in)); len(got) != tt.bad {
			t.Errorf("%q: %v", tt.in, got)
		}
	}
}

func leakCheck(b []byte, extra []string) []leakcheck.Leak { return leakcheck.Check(b, extra...) }
