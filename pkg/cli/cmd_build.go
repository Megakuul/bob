package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/megakuul/bob/pkg/bob"
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
	if cmd == build.CmdBuild && e.a.dash {
		return e.out.fail(diag.New(4, "flag", "--", "what", "bob test, run, exec or dev"))
	}
	if e.a.bool("remote") {
		return runRemote(e, cmd == build.CmdTest)
	}
	if dir := replayDir(e.getenv); dir != "" {
		return runReplay(e, dir)
	}
	if err := checkPlatforms(e.a.str("platform"), e.a.pos); err != nil {
		return e.out.fail(err) // before the store opens: no Nix process for a platform bob rejects
	}
	root, err := e.root(false)
	if err != nil {
		return e.out.fail(err)
	}
	opts, err := e.buildOptions(root, cmd)
	if err != nil {
		return e.out.fail(err)
	}
	st, caches, warns, exit, done := e.openStore(true)
	if done {
		return exit
	}
	opts.Store, opts.Caches = st, caches
	exit, counts, drift := runStep(e, root, opts, warns)
	if e.out.json {
		return e.out.finish(exit, counts, drift)
	}
	return exit
}

// runStep runs one build.Run inside the store namespace and renders its human result; in
// --json mode the caller ends the run with the counts and drift it returns (a pipeline run
// emits one run.finish for all its steps).
func runStep(e *env, root string, opts build.Options, warns []*diag.Error) (int, translate.Counts, []string) {
	s := newSink(e.out)
	opts.Events = s.put
	s.put(build.Event{Event: build.StageNamespace, DurationMs: namespaceMs()})
	for _, w := range warns {
		if e.out.json { // after run.start, which Run emits later
			s.put(build.Event{Event: "warning", Error: w, Hint: w.Hint()})
		} else {
			e.out.diagnose(w, "")
		}
	}
	res, err := build.Run(e.ctx, opts)
	s.flush()
	s.clear()
	if res == nil {
		for _, d := range diagErrors(err) {
			e.out.diagnose(d, "")
		}
		return diag.ExitCode(e.out.errs), translate.Counts{}, nil
	}
	recordBuild(root, drvsOf(res))
	recordLabels(root, res.Requested)
	if len(res.Generated) > 0 {
		// Native files the run wrote stay out of git (best effort: bob check reports 1013).
		_ = updateExclude(root, res.Generated, false)
	}
	exit := res.ExitCode
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
		return exit, res.Report.Counts, res.Report.Drift
	}
	if opts.Command == build.CmdTest {
		renderTests(e.out, res)
	} else {
		renderBuild(e.out, res)
	}
	return exit, res.Report.Counts, res.Report.Drift
}

// checkPlatforms refuses an unknown platform in --platform or a pattern's "@<platform>" suffix
// (R-PLAT-001, usage error BOB-E-0010).
func checkPlatforms(flag string, patterns []string) error {
	ps := []string{flag}
	for _, p := range patterns {
		if _, cfg, ok := strings.Cut(p, "@"); ok {
			plat, _, _ := strings.Cut(cfg, "+")
			ps = append(ps, plat)
		}
	}
	for _, p := range ps {
		if p != "" && bob.Platform(p).NixSystem() == "" {
			return diag.New(10, "platform", p, "known", "linux-amd64, linux-arm64")
		}
	}
	return nil
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
	if e.out.verbose >= 2 { // -vv: bob's debug log (the raw Nix log) on stderr
		o.Debug = e.out.errw
	}
	o.RequireAllTests = cmd == build.CmdTest && e.a.bool("require-all-tests")
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
	if e.a.has("version") { // release version (R-OUT-022); checked before the store opens
		o.Version = e.a.str("version")
		if err := build.CheckVersion(o.Version); err != nil {
			return o, err
		}
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
	if ev.Event == "note" && ev.Note != nil && !s.o.json {
		// Notes go to stderr at once, before the build they announce (R-FW-036).
		s.clear()
		fmt.Fprintf(s.o.errw, "note: %s\n", ev.Note.Text)
		return
	}
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
			Runner     string   `json:"runner,omitempty"`
			Reason     string   `json:"reason,omitempty"`
		}{ev.Label, ev.Status, ev.DurationMs, failed, ev.Runner, ev.Line})
	case "note":
		if n := ev.Note; n != nil {
			o.event(ev.Event, struct {
				Type     string `json:"type"`
				Code     string `json:"code"`
				Message  string `json:"message"`
				Label    string `json:"label,omitempty"`
				Attr     string `json:"attr,omitempty"`
				Platform string `json:"platform,omitempty"`
				Field    string `json:"field,omitempty"`
			}{"note", n.Code, n.Text, n.Label, n.Attr, n.Platform, n.Field})
		}
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
	translate.HumanTo(o.w, o.errw, r, o.style)
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
	var pass, fail, cached, skipped int
	for _, t := range res.Tests {
		dur := fmt.Sprintf(" (%.1fs)", float64(t.DurationMs)/1000)
		if t.Runner != "" {
			dur = fmt.Sprintf(" (%.1fs, %s)", float64(t.DurationMs)/1000, t.Runner)
		}
		switch t.Status {
		case "skipped":
			skipped++
			fmt.Fprintf(o.w, "%s    %s: %s\n", paint(o.style, sgrYellow, "SKIP"), t.Label, t.Reason)
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
	// Blocks and the summary after them go to stderr (stdout followed by stderr reads in order).
	w := o.w
	blocks := append(append([]*diag.Error{}, r.Warnings...), r.Errors...)
	if len(blocks) > 0 {
		w = o.errw
	}
	first := len(res.Tests) == 0
	for _, d := range blocks {
		if !first {
			fmt.Fprintln(w)
		}
		first = false
		translate.Block(w, d, r.Hint(d), o.style)
	}
	fmt.Fprintln(w)
	line := fmt.Sprintf("bob: %d passed, %d failed, %d cached", pass, fail, cached)
	if skipped > 0 {
		line += fmt.Sprintf(", %d skipped", skipped)
	}
	if res.ExitCode != 0 {
		fmt.Fprintf(w, "%s (exit %d)\n", paint(o.style, sgrRedBold, line), res.ExitCode)
		return
	}
	fmt.Fprintln(w, paint(o.style, sgrGreen, line))
}

const (
	sgrGreen  = "\x1b[32m"
	sgrYellow = "\x1b[33m"
)

// runReplay translates a recorded Nix run instead of building (BOB_TEST_NIX_REPLAY, test
// builds only, A27): the same output as a live run of the capture.
func runReplay(e *env, dir string) int {
	r, evs, err := translate.Replay(dir)
	if err != nil {
		return e.out.fail(err)
	}
	if !e.out.json {
		translate.HumanTo(e.out.w, e.out.errw, r, e.out.style)
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
		return e.out.fail(diag.New(10, "platform", p, "known", "native"))
	}
	root, err := e.root(false)
	if err != nil {
		return e.out.fail(err)
	}
	arg := e.a.pos[0]
	if arg == "." {
		arg = "./" // bob run .: the package named like the current directory ("//dir:dir")
	}
	label := resolvePattern(e.relDir(root), arg)
	st, caches, warns, exit, done := e.openStore(true)
	if done {
		return exit
	}
	for _, w := range warns {
		e.out.diagnose(w, "")
	}
	res, err := build.Run(e.ctx, build.Options{Command: build.CmdBuild, Root: root, Patterns: []string{label}, Store: st, Caches: caches, Offline: e.a.bool("offline")})
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
	// The program's stderr passes through unchanged; its tail is kept so that a failure bob
	// can explain (an npm addon whose install script was denied, an undeclared import inside a
	// store package: SPEC §03.4.4) gets bob's error block after the program's own output.
	tail := &tailWriter{max: 64 << 10}
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, io.MultiWriter(os.Stderr, tail)
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
		if d := translate.Program(label, tail.lines()); d != nil {
			e.out.diagnose(d, "")
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

// tailWriter keeps the last max bytes written to it.
type tailWriter struct {
	max int
	buf []byte
}

func (w *tailWriter) Write(p []byte) (int, error) {
	w.buf = append(w.buf, p...)
	if over := len(w.buf) - w.max; over > 0 {
		w.buf = append(w.buf[:0], w.buf[over:]...)
	}
	return len(p), nil
}

// lines are the kept bytes as lines (the first one may be cut).
func (w *tailWriter) lines() []string {
	return strings.Split(strings.TrimRight(string(w.buf), "\n"), "\n")
}
