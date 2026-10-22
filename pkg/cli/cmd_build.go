package cli

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/megakuul/bob/pkg/bob/diag"
	"github.com/megakuul/bob/pkg/engine/build"
	"github.com/megakuul/bob/pkg/engine/translate"
)

// runBuild is `bob build` (SPEC §02.6).
func runBuild(e *env) int { return runPipeline(e, build.CmdBuild) }

// runTest is `bob test`.
func runTest(e *env) int { return runPipeline(e, build.CmdTest) }

// runPipeline runs build.Run for build and test and renders the result.
func runPipeline(e *env, cmd build.Command) int {
	if e.a.bool("remote") {
		return needsServer(e)
	}
	if cmd == build.CmdBuild && e.a.dash {
		return e.out.fail(diag.New(4, "flag", "--", "what", "bob test, run, exec or dev"))
	}
	if dir := replayDir(e.getenv); dir != "" {
		return runReplay(e, dir)
	}
	root, err := e.root(false)
	if err != nil {
		return e.out.fail(err)
	}
	opts, err := e.buildOptions(root, cmd)
	if err != nil {
		return e.out.fail(err)
	}
	st, exit, done := e.enterStore()
	if done {
		return exit
	}
	opts.Store = st
	s := newSink(e.out)
	opts.Events = s.put
	s.put(build.Event{Event: build.StageNamespace, DurationMs: namespaceMs()})
	res, err := build.Run(e.ctx, opts)
	s.flush()
	s.clear()
	if res == nil {
		return e.out.fail(err)
	}
	recordBuild(root, drvsOf(res))
	recordLabels(root, res.Requested)
	if len(res.Generated) > 0 {
		// Native files the run wrote stay out of git (best effort: bob check reports 1013).
		_ = updateExclude(root, res.Generated, false)
	}
	exit = res.ExitCode
	if err != nil {
		// a failure that is not a diagnostic: report it with the run's own errors
		res.Report.Errors = append(res.Report.Errors, diagErrors(err)...)
		exit = diag.ExitCode(append(append([]*diag.Error{}, res.Report.Errors...), e.out.errs...))
		res.Report.ExitCode, res.ExitCode = exit, exit
		if e.out.json {
			for _, d := range diagErrors(err) {
				e.out.diagnose(d, "")
			}
		}
	}
	if e.out.json {
		return e.out.finish(exit, res.Report.Counts, res.Report.Drift)
	}
	if cmd == build.CmdTest {
		renderTests(e.out, res)
	} else {
		renderBuild(e.out, res)
	}
	return exit
}

// buildOptions turns the command line into build.Options.
func (e *env) buildOptions(root string, cmd build.Command) (build.Options, error) {
	o := build.Options{
		Command:    cmd,
		Root:       root,
		Patterns:   e.patterns(root, e.a.pos),
		Platform:   e.a.str("platform"),
		Lock:       e.a.bool("lock"),
		Offline:    e.a.bool("offline"),
		Rerun:      e.a.bool("rerun"),
		PrintPaths: e.a.bool("print-paths"),
		JSONLog:    e.out.jsonLog,
	}
	if v := e.a.str("variant"); v != "" {
		o.Variants = strings.Split(v, "+")
	}
	if e.a.has("keep-going") {
		k := e.a.bool("keep-going")
		o.KeepGoing = &k
	}
	if j := e.a.str("j"); j != "" {
		n, err := strconv.Atoi(j)
		if err != nil || n < 1 {
			return o, diag.New(4, "flag", "-j", "what", "a number of at least 1")
		}
		o.MaxJobs = n
	}
	if cmd == build.CmdTest {
		o.TestArgs = e.a.pass
	}
	return o, nil
}

// sink turns the events of build.Run into --json lines. run.start must be the first line
// (SPEC §02.4) but Run emits it only after evaluation, so earlier events wait for it.
type sink struct {
	o       *out
	pending []build.Event
	final   *build.Event // run.finish, written by the caller after Run returned

	// live status line (SPEC §02.5 rule 7): TTY, not TERM=dumb, not -q, not --json
	live    bool
	total   int
	done    int
	current string
	drawn   time.Time
}

func newSink(o *out) *sink {
	return &sink{o: o, live: !o.json && !o.quiet && isTerminal(o.errw) && os.Getenv("TERM") != "dumb"}
}

// status redraws the live status line on stderr, at most 10 times a second.
func (s *sink) status(ev build.Event) {
	switch ev.Event {
	case "run.start":
		s.total = len(ev.Requested)
	case "package.start":
		s.current = ev.Label
	case "package.finish":
		s.done++
	default:
		return
	}
	if time.Since(s.drawn) < 100*time.Millisecond {
		return
	}
	s.drawn = time.Now()
	fmt.Fprintf(s.o.errw, "\r\x1b[K[%d/%d] building %s", s.done, max(s.total, s.done), s.current)
}

// clear removes the live status line before other output.
func (s *sink) clear() {
	if s.live && !s.drawn.IsZero() {
		fmt.Fprint(s.o.errw, "\r\x1b[K")
	}
}

func (s *sink) put(ev build.Event) {
	if !s.o.json {
		if ev.Event == "run.start" {
			s.o.started = true
		}
		if s.live {
			s.status(ev)
		}
		return
	}
	if !s.o.started && ev.Event != "run.start" {
		s.pending = append(s.pending, ev)
		return
	}
	if ev.Event == "run.start" {
		s.o.begin(ev.Requested)
		for _, p := range s.pending {
			s.write(p)
		}
		s.pending = nil
		return
	}
	s.write(ev)
}

// flush writes events still waiting for run.start (a run that failed before evaluation).
func (s *sink) flush() {
	if !s.o.json || len(s.pending) == 0 {
		return
	}
	s.o.begin(nil)
	p := s.pending
	s.pending = nil
	for _, ev := range p {
		s.write(ev)
	}
}

func (s *sink) write(ev build.Event) {
	o := s.o
	switch ev.Event {
	case "package.start":
		o.event(ev.Event, struct {
			Label  string `json:"label"`
			Action string `json:"action"`
			Cache  string `json:"cache"`
		}{ev.Label, ev.Action, ev.Cache})
	case "package.wait":
		o.event(ev.Event, struct {
			Label  string `json:"label"`
			Reason string `json:"reason"`
		}{ev.Label, ev.Reason})
	case "upstream.start":
		o.event(ev.Event, struct {
			Name string `json:"name"`
		}{ev.Name})
	case "log":
		if o.jsonLog { // Run forwards log events only with Options.JSONLog
			o.event(ev.Event, struct {
				Label string `json:"label"`
				Line  string `json:"line"`
			}{ev.Label, ev.Line})
		}
	case "error", "warning":
		if ev.Error != nil {
			hint := ev.Hint
			o.errs = append(o.errs, ev.Error)
			o.event(eventName(ev.Error), errorEvent(ev.Error, hint, o.verbose > 0))
		}
	case "test.result":
		failed := ev.Failed
		if failed == nil {
			failed = []string{}
		}
		o.event(ev.Event, struct {
			Label      string   `json:"label"`
			Status     string   `json:"status"`
			DurationMs int64    `json:"durationMs"`
			Failed     []string `json:"failed"`
		}{ev.Label, ev.Status, ev.DurationMs, failed})
	case "package.finish":
		o.event(ev.Event, struct {
			Label  string   `json:"label"`
			Status string   `json:"status"`
			Paths  []string `json:"paths,omitempty"`
		}{ev.Label, ev.Status, ev.Paths})
	case "run.finish":
		f := ev
		s.final = &f
	case "drv.imported":
		o.event(ev.Event, struct {
			Count int `json:"count"`
		}{ev.Count})
	default: // R-FW-049 stage events: the stage name with its duration
		o.event(ev.Event, struct {
			DurationMs int64 `json:"durationMs"`
			Cached     bool  `json:"cached,omitempty"`
		}{ev.DurationMs, ev.Cached})
	}
}

// renderBuild prints the human result of a build (SPEC §02.5). A successful run whose
// packages were all cached prints only the summary when stdout is not a terminal (R-CLI-002).
func renderBuild(o *out, res *build.Result) {
	r := res.Report
	if r.ExitCode == 0 && !o.isTTY && allCached(r) {
		q := *r
		q.Packages = nil
		r = &q
	}
	if o.quiet {
		q := *r
		q.Packages = nil
		r = &q
	}
	translate.Human(o.w, r, o.style)
	if len(res.Packages) > 0 && hasPaths(res) {
		for _, p := range res.Packages {
			for _, hp := range p.HostPaths {
				fmt.Fprintf(o.w, "%s %s\n", p.Label, hp)
			}
		}
	}
}

// drvsOf maps each requested package to the store path bob log reads the log of: its
// derivation (also for a failed build, whose outputs are not valid), else its main output.
func drvsOf(res *build.Result) map[string]string {
	m := map[string]string{}
	for _, p := range res.Packages {
		switch {
		case p.Drv != "":
			m[p.Label] = p.Drv
		case p.Outputs["out"] != "":
			m[p.Label] = p.Outputs["out"]
		}
	}
	return m
}

func allCached(r *translate.Report) bool {
	for _, p := range r.Packages {
		if p.Status != translate.StCached {
			return false
		}
	}
	return true
}

func hasPaths(res *build.Result) bool {
	for _, p := range res.Packages {
		if len(p.HostPaths) > 0 {
			return true
		}
	}
	return false
}

// renderTests prints the human result of bob test (SPEC §02.5 sample): one line per test
// unit, the warning and error blocks, and `bob: N passed, N failed, N cached (exit E)`.
func renderTests(o *out, res *build.Result) {
	r := res.Report
	var pass, fail, cached int
	for _, t := range res.Tests {
		dur := fmt.Sprintf(" (%.1fs)", float64(t.DurationMs)/1000)
		switch t.Status {
		case "pass":
			pass++
			fmt.Fprintf(o.w, "%s    %s%s\n", paint(o.style, sgrGreen, "PASS"), t.Label, dur)
		case "fail":
			fail++
			fmt.Fprintf(o.w, "%s    %s%s\n", paint(o.style, sgrRedBold, "FAIL"), t.Label, dur)
		case "cached":
			cached++
			if o.isTTY || !o.quiet {
				fmt.Fprintf(o.w, "%s  %s\n", paint(o.style, sgrGreen, "cached"), t.Label)
			}
		}
	}
	for _, p := range r.Packages {
		if p.Status == translate.StFailed {
			fmt.Fprintf(o.w, "%s  %s\n", paint(o.style, sgrRedBold, "FAILED"), p.Label)
		}
	}
	first := len(res.Tests) == 0
	for _, d := range append(append([]*diag.Error{}, r.Warnings...), r.Errors...) {
		if !first {
			fmt.Fprintln(o.w)
		}
		first = false
		translate.Block(o.w, d, r.Hint(d), o.style)
	}
	fmt.Fprintln(o.w)
	line := fmt.Sprintf("bob: %d passed, %d failed, %d cached", pass, fail, cached)
	if res.ExitCode != 0 {
		fmt.Fprintf(o.w, "%s (exit %d)\n", paint(o.style, sgrRedBold, line), res.ExitCode)
		return
	}
	fmt.Fprintln(o.w, paint(o.style, sgrGreen, line))
}

const sgrGreen = "\x1b[32m"

// runReplay translates a recorded Nix run instead of building (BOB_TEST_NIX_REPLAY, test
// builds only, A27): the same output as a live run of the capture.
func runReplay(e *env, dir string) int {
	r, evs, err := translate.Replay(dir)
	if err != nil {
		return e.out.fail(err)
	}
	if !e.out.json {
		translate.Human(e.out.w, r, e.out.style)
		return r.ExitCode
	}
	s := newSink(e.out)
	for _, ev := range evs {
		s.put(build.Event{Event: ev.Event, Label: ev.Label, Action: ev.Action, Cache: ev.Cache, Name: ev.Name,
			Reason: ev.Reason, Line: ev.Line, Status: string(ev.Status), Error: ev.Error, Hint: ev.Hint,
			Requested: ev.Requested, ExitCode: ev.ExitCode, Counts: ev.Counts, Drift: ev.Drift})
	}
	s.flush()
	if s.final != nil {
		return e.out.finish(s.final.ExitCode, s.final.Counts, s.final.Drift)
	}
	return e.out.finish(r.ExitCode, r.Counts, r.Drift)
}

// runRun is `bob run <label> [-- args]` (SPEC §02.6): build the label for this machine, then
// run its binary inside the namespace with the caller's directory, streams and environment.
// Once the program started, its exit status is bob's (signal N → 128+N, R-CLI-006).
func runRun(e *env) int {
	if len(e.a.pos) != 1 {
		return e.out.fail(diag.New(4, "flag", "bob run", "what", "one label"))
	}
	if p := e.a.str("platform"); p != "" && p != "native" {
		return e.out.fail(diag.New(1, "kind", "platform", "name", p, "suggest", "native"))
	}
	label := e.a.pos[0]
	root, err := e.root(false)
	if err != nil {
		return e.out.fail(err)
	}
	st, exit, done := e.enterStore()
	if done {
		return exit
	}
	res, err := build.Run(e.ctx, build.Options{Command: build.CmdBuild, Root: root, Patterns: []string{label}, Store: st, Offline: e.a.bool("offline")})
	if err != nil {
		return e.out.fail(err)
	}
	recordBuild(root, drvsOf(res))
	if res.ExitCode != 0 {
		renderBuild(e.out, res)
		return res.ExitCode
	}
	var pkg *build.Package
	for i := range res.Packages {
		if res.Packages[i].Label == label {
			pkg = &res.Packages[i]
		}
	}
	if pkg == nil {
		return e.out.fail(diag.New(3, "pattern", label))
	}
	bin, err := mainBinary(pkg, e.a.str("bin"))
	if err != nil {
		return e.out.fail(err)
	}
	cmd := exec.CommandContext(e.ctx, bin, e.a.pass...)
	cmd.Args[0] = filepath.Base(bin)
	cmd.Dir = e.cwd
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	cmd.Env = os.Environ()
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
	signal.Ignore(syscall.SIGINT) // the program gets ^C from the terminal itself
	err = cmd.Run()
	var ee *exec.ExitError
	switch {
	case err == nil:
		return 0
	case errors.As(err, &ee):
		if ws, ok := ee.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
			return 128 + int(ws.Signal())
		}
		return ee.ExitCode()
	}
	return e.out.fail(diag.New(5, "label", label))
}

// mainBinary picks the program of a built package: <out>/bin/<name> with --bin, else the only
// file in <out>/bin (BOB-E-0004 when there are several, 0005 when there is none).
func mainBinary(p *build.Package, name string) (string, error) {
	out := p.Outputs["out"]
	if out == "" {
		return "", diag.New(5, "label", p.Label)
	}
	dir := filepath.Join(out, "bin")
	if name != "" {
		b := filepath.Join(dir, filepath.Base(name))
		if _, err := os.Stat(b); err != nil {
			return "", diag.New(5, "label", p.Label+" --bin "+name)
		}
		return b, nil
	}
	ents, err := os.ReadDir(dir)
	if err != nil || len(ents) == 0 {
		return "", diag.New(5, "label", p.Label)
	}
	if len(ents) > 1 {
		return "", diag.New(4, "flag", "bob run "+p.Label, "what", "--bin <name> (it has several binaries)")
	}
	return filepath.Join(dir, ents[0].Name()), nil
}
