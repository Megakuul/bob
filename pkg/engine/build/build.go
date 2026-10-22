package build

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"github.com/megakuul/bob/pkg/bob/diag"
	"github.com/megakuul/bob/pkg/engine/eval"
	"github.com/megakuul/bob/pkg/engine/lock"
	"github.com/megakuul/bob/pkg/engine/store"
	"github.com/megakuul/bob/pkg/engine/translate"
)

// Command selects what Run does after planning.
type Command string

const (
	CmdBuild Command = "build" // realise the requested packages
	CmdTest  Command = "test"  // realise the test units among the requested packages
	// CmdGen realises the working-tree outputs (engine.Materialisation) of the requested
	// packages and materialises them (bob gen); nothing else is built.
	CmdGen Command = "gen"
)

// nixVersion is the embedded Nix (run.start "nix").
const nixVersion = store.NixVersion

// Options is one run of the pipeline (R-FW-049). Zero values are the defaults.
type Options struct {
	Command  Command  // "" = CmdBuild
	Root     string   // repository root (the directory holding bob.lock)
	Patterns []string // label patterns; empty = "//..."
	Platform string   // --platform ("" = host); appended to every pattern as "@<p>[+v…]"
	Variants []string // --variant

	Store  *store.Store // opened by the caller (after store.Enter); nil: Run opens one with Home
	Home   string       // state home when Store is nil ("" = default)
	GoRoot string       // pinned Go for the evaluator ("" = go on PATH)
	// Caches are bob's own binary caches (not cache.nixos.org). Their narinfos are checked in
	// one batch before Nix runs and hits are substituted by bob; bob derivations carry
	// allowSubstitutes = false, so Nix never asks a cache about them itself (A71).
	Caches []store.Substituter
	// Untrusted and Strict are the worker's evaluation mode (A98, S13-3): replace targets
	// outside the repository are refused and the evaluator sandbox refuses weaker isolation.
	Untrusted, Strict bool

	Lock      bool  // --lock: re-resolve stale lock entries instead of failing with BOB-E-1012
	Offline   bool  // --offline: no substituters, no downloads
	KeepGoing *bool // nil = true
	MaxJobs   int   // -j (0 = Nix's default: the number of CPUs)
	Limits    translate.Limits
	Rerun     bool // bob test --rerun
	// RequireAllTests makes a test that cannot run on this machine (skipped, R-FW-039) fail the
	// run with exit 1 (bob test --require-all-tests).
	RequireAllTests bool
	TestArgs        []string // bob test -- args (engine.PlanRequest.TestArgs)
	PrintPaths      bool     // fill Package.HostPaths and package.finish Paths
	JSONLog         bool     // --json-log: emit log events with build log lines
	Replay          string   // BOB_TEST_NIX_REPLAY: translate this capture instead of running Nix
	// NoMaterialise keeps the working tree as it is (remote workers and pipeline runs never
	// materialise, SPEC §03.6.6); Untrusted implies it.
	NoMaterialise bool
	// Force moves conflicting files of owned directories to .bob/gen/backup/ (bob gen --force).
	Force bool
	// DryRun stops after planning (bob build --dry-run, R-RS-005, ruling G24): nothing is
	// fetched, imported or built; Result.Planned lists the derivations.
	DryRun bool
	Events func(Event)
}

// Event is one typed event of a run. Names: the SPEC §02.4 events (run.start, package.start,
// package.wait, upstream.start, log, error, warning, test.result, package.finish, run.finish),
// the R-FW-049 stage events (Stage* constants) and "drv.imported". The caller adds the --json
// envelope and decides what to print.
type Event struct {
	Event      string
	Label      string           // package.*, log, test.result
	Action     string           // package.start: build | download | test
	Cache      string           // package.start: substituter name, "" for builds
	Name       string           // upstream.start
	Reason     string           // package.wait
	Line       string           // log (store paths rewritten)
	Status     string           // package.finish, test.result
	Error      *diag.Error      // error, warning
	Hint       string           // error, warning: the hint to print
	Requested  []string         // run.start
	Nix        string           // run.start: the embedded Nix version
	Paths      []string         // package.finish with PrintPaths: host paths of the outputs
	Failed     []string         // test.result
	DurationMs int64            // stage events, test.result, run.finish
	Count      int              // drv.imported
	Cached     bool             // eval stage: the evaluator was reused
	ExitCode   int              // run.finish
	Counts     translate.Counts // run.finish
	Drift      []string         // run.finish
	Note       *Note            // note (R-FW-036, R-FW-038): a "note:" line, never an error
	Runner     string           // test.result: the emulator a foreign test ran under ("qemu-x86_64")
}

// Stage event names in the order Run emits them. StageNamespace is the caller's (the CLI
// enters the namespace before Run, store.Entered).
const (
	StageNamespace = "namespace"
	StageScan      = "scan"
	StageEval      = "eval"
	StageIR        = "ir"
	StageLock      = "lock"
	StageIndex     = "index"
	StageSources   = "sources"
	StagePlan      = "plan"
	StageFetch     = "fetch"
	StageDrv       = "drv"
	StageNix       = "nix"
	StageValidity  = "validity"
	StageTranslate = "translate"
)

// Result is a finished run.
type Result struct {
	// Report is never nil: errors raised before Nix ran are in Report.Errors too. Render it
	// with translate.Human.
	Report    *translate.Report
	Requested []string     // labels after pattern expansion
	Packages  []Package    // requested packages, sorted by label
	Tests     []TestResult // CmdTest only, sorted by label
	ExitCode  int
	// Generated are the not-committed generated native files this run wrote or kept (for bob's
	// block in .git/info/exclude), as Gen returns them.
	Generated []string
	// Planned are the derivation paths of the plan, sorted (Options.DryRun only).
	Planned []string
}

// Package is one requested package after the run.
type Package struct {
	Label     string
	Status    string            // built | cached | failed | skipped | cancelled
	Outputs   map[string]string // output name → store path
	Drv       string            // its derivation ("" when it has none): bob log reads its build log
	HostPaths []string          // PrintPaths: <home>/nix/store/… of the outputs, by output name
}

// TestResult is the outcome of one test unit.
type TestResult struct {
	Label      string
	Status     string // pass | fail | cached | skipped
	Runner     string // the emulator the unit ran under ("qemu-x86_64"), "" natively
	Reason     string // skipped: why ("not run: needs a native linux-amd64 machine")
	DurationMs int64
	Failed     []string
}

// Run runs the pipeline of R-FW-049 (scan, eval, ir, lock, index, sources, plan, fetch, drv,
// nix, validity, translate) and emits each stage's event with its duration, then run.finish.
// Every failure that is a bob diagnostic is in Result.Report; an error is returned for
// anything else (for example an *evalbox.Error, a damaged store or a cancelled context), and
// Result is still returned with the errors known so far.
func Run(ctx context.Context, o Options) (*Result, error) {
	start := time.Now()
	r := &runner{o: o, start: start}
	res, err := r.run(ctx)
	if res == nil {
		res = &Result{}
	}
	if res.Report == nil {
		res.Report = &translate.Report{}
	}
	if !r.started {
		r.emit(Event{Event: "run.start", Requested: res.Requested, Nix: nixVersion})
	}
	if err != nil {
		if errs, ok := diagErrors(err); ok {
			res.Report.Errors = append(res.Report.Errors, errs...)
			for _, e := range errs {
				r.emit(Event{Event: "error", Error: e, Hint: e.Hint()})
			}
			err = nil
		}
	}
	// The translator's exit wins (it weighs Nix's outcome); errors raised outside it decide
	// otherwise.
	res.ExitCode = res.Report.ExitCode
	if res.ExitCode == 0 {
		res.ExitCode = diag.ExitCode(res.Report.Errors)
	}
	for _, t := range res.Tests {
		if (t.Status == "fail" || t.Status == "skipped" && o.RequireAllTests) && res.ExitCode == 0 {
			res.ExitCode = 1
		}
	}
	res.Report.ExitCode = res.ExitCode
	if err != nil && res.ExitCode == 0 {
		res.ExitCode = 70 // not a diagnostic: the caller renders err (its own exit, if any, wins)
	}
	for _, p := range res.Report.Packages {
		e := Event{Event: "package.finish", Label: p.Label, Status: string(p.Status)}
		if o.PrintPaths {
			e.Paths = r.hostPaths[p.Label]
		}
		r.emit(e)
	}
	r.emit(Event{Event: "run.finish", ExitCode: res.ExitCode, Counts: res.Report.Counts,
		Drift: res.Report.Drift, DurationMs: time.Since(start).Milliseconds()})
	return res, err
}

// runner carries one run's state between the stages.
type runner struct {
	o         Options
	start     time.Time
	started   bool                // run.start was emitted
	hostPaths map[string][]string // label → host paths of its outputs (sorted by output name)
	skipped   []TestResult        // test units that cannot run on this machine (R-FW-039)
	// transientPin is the nixpkgs pin resolved for the evaluator's Go before bob.lock had one;
	// the lock stage records it when bob.go declares the same channel.
	transientPin lock.NixpkgsPin
}

func (r *runner) emit(e Event) {
	if e.Event == "run.start" {
		r.started = true
	}
	if r.o.Events != nil {
		r.o.Events(e)
	}
}

// stage runs f and emits the stage event with its duration (also when f fails).
func (r *runner) stage(name string, f func() error) error {
	t := time.Now()
	err := f()
	r.emit(Event{Event: name, DurationMs: time.Since(t).Milliseconds()})
	return err
}

// openStore returns the caller's store or opens one.
func openStore(ctx context.Context, st *store.Store, home string, caches []store.Substituter) (*store.Store, error) {
	if st != nil {
		return st, nil
	}
	return store.Open(ctx, store.Config{Home: home, Substituters: caches})
}

// targets are the evaluator's targets: the patterns with the platform/variant suffix.
func targets(o Options) []string {
	pats := o.Patterns
	if len(pats) == 0 {
		pats = []string{"//..."}
	}
	suffix := ""
	if o.Platform != "" || len(o.Variants) > 0 {
		suffix = "@" + o.Platform
		for _, v := range o.Variants {
			suffix += "+" + v
		}
	}
	out := make([]string, len(pats))
	for i, p := range pats {
		out[i] = p + suffix
	}
	return out
}

// diagErrors flattens err into bob diagnostics; ok is false when any part is not one.
func diagErrors(err error) ([]*diag.Error, bool) {
	var d *diag.Error
	if u, ok := err.(interface{ Unwrap() []error }); ok {
		var out []*diag.Error
		for _, e := range u.Unwrap() {
			list, ok := diagErrors(e)
			if !ok {
				return nil, false
			}
			out = append(out, list...)
		}
		return out, len(out) > 0
	}
	if errors.As(err, &d) {
		return []*diag.Error{d}, true
	}
	return nil, false
}

// internalf is BOB-E-9004 for label: bob produced something invalid on its own.
func internalf(label, format string, args ...any) *diag.Error {
	return diag.New(9004, "label", label, "reason", fmt.Sprintf(format, args...))
}

// hostPath is where a store path is readable from the host (below the bob home).
func hostPath(st *store.Store, p string) string { return filepath.Join(st.Home(), p) }

// evalRequest is the evaluator request of o with the evaluator's Go toolchain.
func evalRequest(o Options, goRoot string) eval.Request {
	return eval.Request{Root: o.Root, Patterns: targets(o), GoRoot: goRoot, Untrusted: o.Untrusted, Strict: o.Strict}
}
