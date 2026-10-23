package errorsuite

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/megakuul/bob/tests/errors/leakcheck"
)

// EnvReplay names the directory of a recorded capture (meta.json, stream.log). bob reads bob's
// context and Nix's internal-json stream from it instead of evaluating and running Nix, so the
// translator sees exactly the captured stream (HARNESS.md §5.3 replay mode). It is a test hook:
// bob honours it only in builds made for the harness.
const EnvReplay = "BOB_TEST_NIX_REPLAY"

// notImplemented is the code every not-yet-built feature returns (diag.CodeNotImplemented).
const notImplemented = "BOB-E-9008"

// Status is the result of one entry.
type Status string

const (
	Pass Status = "pass"
	// Red: bob does not implement the feature yet (it answered BOB-E-9008). Expected until the
	// feature lands; reported in the parity catalogue as failing, but not a suite failure.
	Red  Status = "red"
	Fail Status = "fail"
)

// Runner runs catalogue entries against bob binaries.
type Runner struct {
	Root    string            // repository root
	Bins    map[string]string // "bob", "bob-server" → binary path
	Live    bool              // also run live captures for entries that have a replay
	Scratch string            // parent of per-run working directories
	Timeout time.Duration     // per bob invocation
}

// Output is what one bob invocation produced.
type Output struct {
	Args           []string
	Stdout, Stderr []byte
	Exit           int // -1: killed or did not start
}

// Combined is stdout followed by stderr.
func (o Output) Combined() []byte { return append(append([]byte{}, o.Stdout...), o.Stderr...) }

// GoldenCheck is an output the caller compares with an approved golden (tests/golden).
type GoldenCheck struct {
	Path string // relative to the repository root
	Got  []byte
}

// Result is the outcome of one entry over all its modes.
type Result struct {
	ID      string        `json:"id"`
	Status  Status        `json:"status"`
	Modes   []string      `json:"modes"`
	Red     []string      `json:"red,omitempty"`     // why the entry is red
	Reasons []string      `json:"reasons,omitempty"` // why it failed
	Notes   []string      `json:"notes,omitempty"`   // harness gaps (missing fixture, injection)
	Golden  []GoldenCheck `json:"-"`
}

func (r *Result) failf(format string, a ...any) {
	r.Reasons = append(r.Reasons, fmt.Sprintf(format, a...))
}

// Run runs e in every mode it has: replay for each recorded Nix version, live when the entry has
// no recording or r.Live is set.
func (r *Runner) Run(ctx context.Context, e Entry) Result {
	res := Result{ID: e.ID}
	replayed := false
	for _, nv := range e.NixVer {
		if d := e.ReplayDir(nv); d != "" {
			replayed = true
			r.runMode(ctx, e, "replay "+nv, d, &res)
		}
	}
	if !replayed || r.Live {
		r.runMode(ctx, e, "live", "", &res)
	}
	switch {
	case len(res.Reasons) > 0:
		res.Status = Fail
	case len(res.Red) > 0:
		res.Status = Red
	default:
		res.Status = Pass
	}
	return res
}

func (r *Runner) runMode(ctx context.Context, e Entry, mode, replay string, res *Result) {
	res.Modes = append(res.Modes, mode)
	work, err := os.MkdirTemp(r.Scratch, e.ID+"-")
	if err != nil {
		res.failf("%s: %v", mode, err)
		return
	}
	defer os.RemoveAll(work)
	home := filepath.Join(work, ".home")
	repo := filepath.Join(work, "repo")
	if err := os.MkdirAll(home, 0o755); err != nil {
		res.failf("%s: %v", mode, err)
		return
	}
	fixture := false
	if replay == "" {
		fixture = r.prepareLive(ctx, e, repo, res)
	} else if err := os.MkdirAll(repo, 0o755); err != nil {
		res.failf("%s: %v", mode, err)
		return
	}
	env := append(os.Environ(), "HOME="+home, "TERM=dumb", "NO_COLOR=1")
	if replay != "" {
		env = append(env, EnvReplay+"="+replay)
	}
	run := func(flag string) (Output, bool) {
		o, err := r.exec(ctx, e.Fault, repo, env, flag)
		if err != nil {
			res.failf("%s: %v", mode, err)
			return o, false
		}
		return o, true
	}
	plain, ok1 := run("")
	jsonOut, ok2 := run("--json")
	verbose, ok3 := run("-v")
	if !ok1 || !ok2 || !ok3 {
		return
	}
	// Leaks and crashes fail an entry even while the feature is not implemented.
	for _, o := range []Output{plain, jsonOut} {
		for _, l := range leakcheck.Check(o.Combined(), e.Forbid...) {
			res.failf("%s: raw Nix text in `%s`: %s", mode, strings.Join(o.Args, " "), l)
		}
	}
	for _, o := range []Output{plain, jsonOut, verbose} {
		if msg := crashed(o); msg != "" {
			res.failf("%s: `%s` %s", mode, strings.Join(o.Args, " "), msg)
		}
	}
	if !slices.ContainsFunc(e.Expect.Outcomes(), func(o Outcome) bool { return slices.Contains(o.Codes, notImplemented) }) {
		if line := codeLine(plain.Combined(), notImplemented); line != "" {
			res.Red = append(res.Red, fmt.Sprintf("%s: %s", mode, line))
			return
		}
	}
	r.check(e, mode, replay, repo, fixture, plain, jsonOut, verbose, res)
}

// prepareLive copies the fixture repository and applies the fault's patch. It reports false
// when the fixture is missing; the run still happens (in an empty directory) so that every
// entry runs, and the gap is noted.
func (r *Runner) prepareLive(ctx context.Context, e Entry, repo string, res *Result) bool {
	src := filepath.Join(r.Root, "tests/fixtures/repos", e.Fixture)
	if _, err := os.Stat(src); err != nil {
		res.Notes = append(res.Notes, "fixture repo tests/fixtures/repos/"+e.Fixture+" is missing")
		if err := os.MkdirAll(repo, 0o755); err != nil {
			res.failf("live: %v", err)
		}
		return false
	}
	if err := os.CopyFS(repo, os.DirFS(src)); err != nil {
		res.failf("live: copy fixture: %v", err)
		return false
	}
	switch e.Inject.Kind {
	case "none":
	case "patch":
		patch := filepath.Join(e.Dir, "patch.diff")
		if _, err := os.Stat(patch); err != nil {
			res.Notes = append(res.Notes, "patch.diff is missing (written when the fault is captured on fixture "+e.Fixture+")")
			break
		}
		cmd := exec.CommandContext(ctx, "git", "apply", patch)
		cmd.Dir = repo
		if out, err := cmd.CombinedOutput(); err != nil {
			res.failf("live: git apply %s: %v: %s", patch, err, out)
		}
	default:
		res.Notes = append(res.Notes, "inject kind "+e.Inject.Kind+" is not supported by the live runner yet")
	}
	return true
}

func (r *Runner) exec(ctx context.Context, f Fault, dir string, env []string, flag string) (Output, error) {
	bin := r.Bins[f.Binary]
	if bin == "" {
		return Output{Exit: -1}, fmt.Errorf("no binary for %q", f.Binary)
	}
	args := slices.Clone(f.Command)
	if flag != "" {
		i := slices.Index(args, "--")
		if i < 0 {
			i = len(args)
		}
		args = slices.Insert(args, i, flag)
	}
	timeout := r.Timeout
	if timeout == 0 {
		// A live run starts with an empty HOME (no bob store, no nixpkgs source): a cold
		// evaluation of fixture acme took 2m13s on the 8-core harness host (I-B34), so the
		// earlier 2 minutes killed every live entry that evaluates. A hang still fails.
		timeout = 10 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Dir = dir
	cmd.Env = env
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	o := Output{Args: append([]string{f.Binary}, args...)}
	err := cmd.Run()
	o.Stdout, o.Stderr = stdout.Bytes(), stderr.Bytes()
	var ee *exec.ExitError
	switch {
	case err == nil:
		o.Exit = 0
	case errors.As(err, &ee):
		o.Exit = ee.ExitCode()
	default:
		return Output{Exit: -1}, fmt.Errorf("run %s: %w", strings.Join(o.Args, " "), err)
	}
	return o, nil
}

var panicRE = regexp.MustCompile(`(?m)^(panic: |fatal error: |goroutine \d+ \[)`)

// crashed describes how bob crashed, or returns "".
func crashed(o Output) string {
	switch {
	case o.Exit < 0:
		return "was killed or timed out"
	case panicRE.Match(o.Stderr) || panicRE.Match(o.Stdout):
		return "panicked"
	}
	return ""
}

// codeLine returns the first line that reports code as an error, or "".
func codeLine(out []byte, code string) string {
	for _, l := range strings.Split(string(out), "\n") {
		if strings.Contains(l, "error["+code+"]") {
			return strings.TrimSpace(l)
		}
	}
	return ""
}

func (r *Runner) check(e Entry, mode, replay, repo string, fixture bool, plain, jsonOut, verbose Output, res *Result) {
	out := string(plain.Combined())
	// 1. Code and exit.
	var why []string
	matched := false
	for _, oc := range e.Expect.Outcomes() {
		if msg := outcomeMismatch(oc, out, plain.Exit); msg == "" {
			matched = true
			break
		} else {
			why = append(why, msg)
		}
	}
	if !matched {
		res.failf("%s: %s", mode, strings.Join(why, "; or "))
	}
	for _, w := range e.Expect.Warnings {
		if !strings.Contains(out, "warning["+w+"]") {
			res.failf("%s: warning[%s] missing", mode, w)
		}
	}
	for _, m := range e.Expect.Messages {
		if !strings.Contains(out, m) {
			res.failf("%s: message %q missing", mode, m)
		}
	}
	// 2. Go source position.
	switch {
	case replay != "" && e.ExpectPosition != nil:
		if p := e.ExpectPosition.DefinedAt; p != "" && !strings.Contains(out, "package defined at "+p+"\n") {
			res.failf("%s: want `package defined at %s`", mode, p)
		}
		if p := e.ExpectPosition.Site; p != "" && !strings.Contains(out, "--> "+p+"\n") {
			res.failf("%s: want `--> %s`", mode, p)
		}
	case replay == "" && e.Position == "marker" && fixture:
		p, err := MarkerPos(repo, e.ID)
		if err != nil {
			res.failf("%s: %v", mode, err)
		} else if !strings.Contains(out, "package defined at "+p+"\n") {
			res.failf("%s: want `package defined at %s`", mode, p)
		}
		if e.Inject.File != "" {
			if s, err := SitePos(repo, e.Inject.File); err != nil {
				res.failf("%s: %v", mode, err)
			} else if !strings.Contains(out, "--> "+s+":") {
				res.failf("%s: want `--> %s:<col>`", mode, s)
			}
		}
	}
	if e.Excerpt && !strings.Contains(out, "\n   | ") {
		res.failf("%s: no log excerpt", mode)
	}
	if e.Hint && !strings.Contains(out, "\n  hint: ") {
		res.failf("%s: no hint", mode)
	}
	// 3. Leaks were checked by the caller. 4. -v is a superset of the default output.
	if l := missingLine(out, string(verbose.Combined())); l != "" {
		res.failf("%s: -v output lacks the default line %q", mode, l)
	}
	if replay != "" && strings.Contains(string(verbose.Combined()), "@nix") {
		res.failf("%s: -v shows raw @nix lines instead of nix: lines", mode)
	}
	// 5. Golden (compared by the caller).
	if replay != "" {
		rel, err := filepath.Rel(r.Root, filepath.Join(replay, "golden.txt"))
		if err == nil {
			res.Golden = append(res.Golden, GoldenCheck{Path: filepath.ToSlash(rel), Got: plain.Combined()})
		}
	}
	// 6. JSON.
	for _, msg := range checkJSON(jsonOut.Stdout) {
		res.failf("%s: --json: %s", mode, msg)
	}
	for _, a := range e.Absent {
		if _, err := os.Stat(filepath.Join(repo, a)); !errors.Is(err, fs.ErrNotExist) {
			res.failf("%s: %s exists after the run", mode, a)
		}
	}
}

// outcomeMismatch returns "" when out has every code of oc (as often as listed) and the exit
// matches.
func outcomeMismatch(oc Outcome, out string, exit int) string {
	var miss []string
	need := map[string]int{}
	for _, c := range oc.Codes {
		need[c]++
	}
	for c, n := range need {
		if got := strings.Count(out, "error["+c+"]"); got < n {
			miss = append(miss, fmt.Sprintf("error[%s] ×%d (got %d)", c, n, got))
		}
	}
	slices.Sort(miss)
	switch {
	case oc.Exit != nil && exit != *oc.Exit:
		miss = append(miss, fmt.Sprintf("exit %d (got %d)", *oc.Exit, exit))
	case oc.Exit == nil && exit == 0:
		miss = append(miss, "a non-zero exit (got 0)")
	}
	if len(miss) == 0 {
		return ""
	}
	return "want " + strings.Join(miss, ", ")
}

// missingLine returns the first non-empty line of def that is not found, in order, in verbose.
func missingLine(def, verbose string) string {
	vl := strings.Split(verbose, "\n")
	i := 0
	for _, l := range strings.Split(def, "\n") {
		if l == "" {
			continue
		}
		for i < len(vl) && vl[i] != l {
			i++
		}
		if i == len(vl) {
			return l
		}
		i++
	}
	return ""
}

// events is the --json event set of schema v1 (SPEC §02.4).
var events = map[string]bool{
	"run.start": true, "package.start": true, "package.wait": true, "upstream.start": true,
	"log": true, "error": true, "warning": true, "test.result": true, "package.finish": true,
	"remote.submitted": true, "remote.state": true, "result": true, "run.finish": true,
	"note": true, // SPEC §02.4 (new); same as tests/cli knownEvents (correction 57)
	// R-FW-049 stage events and drv.imported (schema/events-v1.json ev.stage, ev.drv.imported).
	"namespace": true, "scan": true, "eval": true, "ir": true, "lock": true, "index": true,
	"sources": true, "plan": true, "fetch": true, "drv": true, "nix": true, "validity": true,
	"translate": true, "drv.imported": true,
}

// checkJSON checks that every line parses, has "v":1 and a known "event".
func checkJSON(stdout []byte) []string {
	var bad []string
	for i, l := range bytes.Split(stdout, []byte("\n")) {
		if len(bytes.TrimSpace(l)) == 0 {
			continue
		}
		var ev struct {
			V     *int   `json:"v"`
			Event string `json:"event"`
		}
		if err := json.Unmarshal(l, &ev); err != nil {
			bad = append(bad, fmt.Sprintf("line %d does not parse: %v", i+1, err))
			continue
		}
		if ev.V == nil || *ev.V != 1 {
			bad = append(bad, fmt.Sprintf("line %d: v is not 1", i+1))
		}
		if !events[ev.Event] {
			bad = append(bad, fmt.Sprintf("line %d: unknown event %q", i+1, ev.Event))
		}
	}
	return bad
}
