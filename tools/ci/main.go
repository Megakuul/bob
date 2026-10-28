// Command ci is bob's CI command (HARNESS.md §1). It runs the registered
// stages in order, prints a summary, writes out/ci/<run-id>/report.json and
// exits 0 (green), 1 (a stage failed) or 2 (the harness itself broke).
package main

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// errHarness marks a failure of the harness rather than of the code under
// test (a pinned tool would not start, the tree has no go.work, ...). Stages
// wrap it with harnessErr; the run then exits 2.
var errHarness = errors.New("harness error")

func harnessErr(err error) error { return fmt.Errorf("%w: %w", errHarness, err) }

// fastSkipped are the stages -fast leaves out (HARNESS.md §1.2: stages 1–12
// and 15 only).
var fastSkipped = map[int]bool{13: true, 14: true}

type options struct {
	fast, slow, json bool
	only             map[string]bool
	runID, profile   string
}

type stageResult struct {
	Name       string `json:"name"`
	Order      int    `json:"order"`
	Status     string `json:"status"` // ok | fail | harness
	DurationMS int64  `json:"duration_ms"`
	Error      string `json:"error,omitempty"`
}

type report struct {
	V      int           `json:"v"`
	RunID  string        `json:"run_id"`
	Fast   bool          `json:"fast"`
	Slow   bool          `json:"slow"`
	Stages []stageResult `json:"stages"`
	Exit   int           `json:"exit"`
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	code := run(ctx, os.Args[1:], os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) > 0 && args[0] == "protect" {
		return runProtect(ctx, args[1:], stderr)
	}
	opts, err := parseFlags(args, stderr)
	if err != nil {
		return 2
	}
	root, err := repoRoot()
	if err != nil {
		fmt.Fprintln(stderr, "ci:", err)
		return 2
	}
	selected, err := selectStages(stages, opts)
	if err != nil {
		fmt.Fprintln(stderr, "ci:", err)
		return 2
	}

	ev := events{json: opts.json, w: stdout}
	human := stderr
	if opts.json {
		human = io.Discard
	}
	env := &Env{Root: root, Fast: opts.fast}
	rep := report{V: 1, RunID: opts.runID, Fast: opts.fast, Slow: opts.slow}
	for _, s := range selected {
		ev.emit(map[string]any{"event": "stage.start", "stage": s.Name})
		fmt.Fprintf(human, "==> %s\n", s.Name)
		start := time.Now()
		err := runStage(ctx, s, env)
		res := stageResult{Name: s.Name, Order: s.Order, Status: "ok", DurationMS: time.Since(start).Milliseconds()}
		if err != nil {
			res.Status, res.Error = "fail", err.Error()
			if errors.Is(err, errHarness) {
				res.Status = "harness"
			}
			fmt.Fprintf(human, "%s\nFAIL %s (%s)\n", strings.TrimRight(res.Error, "\n"), s.Name, time.Since(start).Round(time.Millisecond))
		} else {
			fmt.Fprintf(human, "ok   %s (%s)\n", s.Name, time.Since(start).Round(time.Millisecond))
		}
		ev.emit(map[string]any{"event": "stage.end", "stage": s.Name, "status": res.Status, "duration_ms": res.DurationMS, "error": res.Error})
		rep.Stages = append(rep.Stages, res)
	}

	rep.Exit = exitCode(rep.Stages)
	if err := writeReport(root, rep); err != nil {
		fmt.Fprintln(stderr, "ci:", err)
		rep.Exit = max(rep.Exit, 2)
	}
	printSummary(human, rep)
	ev.emit(map[string]any{"event": "summary", "exit": rep.Exit, "stages": rep.Stages})
	return rep.Exit
}

// runProtect is `go run ./tools/ci protect -write`: it rewrites the
// protected-path manifest (HARNESS.md §2.1).
func runProtect(ctx context.Context, args []string, stderr io.Writer) int {
	fs := flag.NewFlagSet("protect", flag.ContinueOnError)
	fs.SetOutput(stderr)
	write := fs.Bool("write", false, "rewrite the protected-path manifest")
	if err := fs.Parse(args); err != nil || !*write || fs.NArg() > 0 {
		fmt.Fprintln(stderr, "usage: ci protect -write")
		return 2
	}
	root, err := repoRoot()
	if err == nil {
		err = protectWrite(ctx, root)
	}
	if err != nil {
		fmt.Fprintln(stderr, "ci protect:", err)
		return 2
	}
	return 0
}

func parseFlags(args []string, stderr io.Writer) (options, error) {
	// The global flag set, because stages read flags they share with
	// the runner through flag.Lookup (the golden stage reads -update).
	fs := flag.CommandLine
	fs.SetOutput(stderr)
	var o options
	var only string
	fs.BoolVar(&o.fast, "fast", false, "inner loop: stages 1–12 and 15, go test -short; never counts as CI passed")
	fs.BoolVar(&o.slow, "slow", false, "gate run plus all slow stages")
	fs.BoolVar(&o.json, "json", false, "NDJSON events on stdout instead of human output")
	fs.StringVar(&only, "only", "", "comma-separated stage names to run")
	fs.StringVar(&o.profile, "profile", "", "budget profile: 8c15g (default) or 2c4g")
	fs.Bool("update", false, "write golden candidates to out/golden-candidates/ (HARNESS.md §3)")
	fs.StringVar(&o.runID, "run-id", "", "output directory name under out/ci (default: timestamp)")
	if err := fs.Parse(args); err != nil {
		return o, err
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "ci: unexpected arguments %q\n", fs.Args())
		return o, errors.New("unexpected arguments")
	}
	if o.fast && o.slow {
		fmt.Fprintln(stderr, "ci: -fast and -slow exclude each other")
		return o, errors.New("conflicting flags")
	}
	switch o.profile {
	case "":
	case "8c15g", "2c4g":
		// The budgets stage reads the profile from the environment, so it
		// does not depend on this file's flag parsing.
		if err := os.Setenv("BOB_CI_PROFILE", o.profile); err != nil {
			return o, err
		}
	default:
		fmt.Fprintf(stderr, "ci: -profile must be 8c15g or 2c4g, not %q\n", o.profile)
		return o, errors.New("bad profile")
	}
	if only != "" {
		o.only = map[string]bool{}
		for _, n := range strings.Split(only, ",") {
			o.only[strings.TrimSpace(n)] = true
		}
	}
	if o.runID == "" {
		o.runID = time.Now().UTC().Format("20060102T150405Z")
	}
	return o, nil
}

// selectStages orders the registry and applies -fast, -slow and -only. The
// protected stage (order 1) always runs (HARNESS.md §1.3).
func selectStages(all []Stage, o options) ([]Stage, error) {
	known := map[string]bool{}
	for _, s := range all {
		if known[s.Name] {
			return nil, fmt.Errorf("stage %q registered twice", s.Name)
		}
		known[s.Name] = true
	}
	for n := range o.only {
		if !known[n] {
			return nil, fmt.Errorf("-only: unknown stage %q", n)
		}
	}
	var out []Stage
	for _, s := range all {
		switch {
		case s.Order == 1:
		case o.only != nil:
			if !o.only[s.Name] {
				continue
			}
		case s.Slow && !o.slow, o.fast && fastSkipped[s.Order]:
			continue
		}
		out = append(out, s)
	}
	slices.SortStableFunc(out, func(a, b Stage) int {
		return cmp.Or(cmp.Compare(a.Order, b.Order), strings.Compare(a.Name, b.Name))
	})
	return out, nil
}

// runStage converts a panic inside a stage into a harness error, so one
// broken stage never hides the results of the others.
func runStage(ctx context.Context, s Stage, env *Env) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = harnessErr(fmt.Errorf("stage %s panicked: %v", s.Name, r))
		}
	}()
	return s.Run(ctx, env)
}

func exitCode(rs []stageResult) int {
	code := 0
	for _, r := range rs {
		switch r.Status {
		case "harness":
			code = max(code, 2)
		case "fail":
			code = max(code, 1)
		}
	}
	return code
}

func printSummary(w io.Writer, rep report) {
	var failed []string
	for _, r := range rep.Stages {
		if r.Status != "ok" {
			failed = append(failed, r.Name)
		}
	}
	fmt.Fprintf(w, "\n%d stages, %d failed", len(rep.Stages), len(failed))
	if len(failed) > 0 {
		fmt.Fprintf(w, ": %s", strings.Join(failed, ", "))
	}
	fmt.Fprintf(w, "\nreport: out/ci/%s/report.json\nexit %d\n", rep.RunID, rep.Exit)
}

func writeReport(root string, rep report) error {
	dir := filepath.Join(root, "out", "ci", rep.RunID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("write report: %w", err)
	}
	b, err := json.MarshalIndent(rep, "", "  ")
	if err != nil {
		return fmt.Errorf("write report: %w", err)
	}
	return os.WriteFile(filepath.Join(dir, "report.json"), append(b, '\n'), 0o644)
}

// events writes the -json NDJSON stream (HARNESS.md §1.2).
type events struct {
	json bool
	w    io.Writer
}

func (e events) emit(fields map[string]any) {
	if !e.json {
		return
	}
	fields["v"] = 1
	b, err := json.Marshal(fields)
	if err != nil {
		return
	}
	fmt.Fprintf(e.w, "%s\n", b)
}

// repoRoot finds the directory holding go.work, starting at the working
// directory, so `go run ./tools/ci` works from anywhere in the tree.
func repoRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("find repo root: %w", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.work")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", errors.New("find repo root: no go.work above the working directory")
		}
		dir = parent
	}
}

// command runs a tool in dir and returns its combined output. A non-zero
// exit becomes an error carrying that output; a tool that cannot start at
// all is a harness error. extraEnv entries are KEY=VALUE.
func command(ctx context.Context, dir string, extraEnv []string, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), extraEnv...)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	var exitErr *exec.ExitError
	switch {
	case err == nil:
		return out.String(), nil
	case errors.As(err, &exitErr):
		return out.String(), fmt.Errorf("%s %s: %w\n%s", name, strings.Join(args, " "), err, out.String())
	default:
		return out.String(), harnessErr(fmt.Errorf("%s: %w", name, err))
	}
}

// goTool runs a tool pinned by a `tool` directive in tools/go.mod
// (HARNESS.md §1.4). The root go.work includes ./tools, so `go tool` from
// the repository root sees those directives.
func goTool(ctx context.Context, dir, tool string, args ...string) (string, error) {
	return goCmd(ctx, dir, nil, append([]string{"tool", tool}, args...)...)
}

// goCmd runs the go command. cgo is off unless the caller's environment
// sets CGO_ENABLED: the harness host has no C compiler on PATH, and neither
// the pinned tools nor the static checks need one.
func goCmd(ctx context.Context, dir string, extraEnv []string, args ...string) (string, error) {
	if _, ok := os.LookupEnv("CGO_ENABLED"); !ok {
		extraEnv = append([]string{"CGO_ENABLED=0"}, extraEnv...)
	}
	out, err := command(ctx, dir, extraEnv, "go", args...)
	return dropGoProgress(out), err
}

// dropGoProgress removes the "go: downloading ..." lines the go command prints to stderr when
// the module cache is cold (a fresh CI runner). command merges stderr into the output, and
// the stages treat any output of `go list` or `buf format -d` as a finding.
func dropGoProgress(out string) string {
	if !strings.Contains(out, "go: ") {
		return out
	}
	var keep []string
	for _, l := range strings.SplitAfter(out, "\n") {
		if strings.HasPrefix(l, "go: downloading ") || strings.HasPrefix(l, "go: finding ") || strings.HasPrefix(l, "go: extracting ") {
			continue
		}
		keep = append(keep, l)
	}
	return strings.Join(keep, "")
}
