package worker

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/megakuul/bob/internal/safe"
	"github.com/megakuul/bob/pkg/bob/diag"
	"github.com/megakuul/bob/pkg/engine/eval"
)

// Job is what the runner inside a job container gets on its stdin (JSON).
type Job struct {
	ID      string `json:"id"`
	Build   string `json:"build"`
	Epoch   int64  `json:"epoch"`
	Attempt int64  `json:"attempt"`
	Spec    []byte `json:"spec"` // the signed canonical CBOR JobSpec
	Slot    int    `json:"slot"` // the worker slot (evalbox maps its uid from it)
}

// PackageEnd is the end of one package's build, for its package_run row.
type PackageEnd struct {
	Drv    string    `json:"drv,omitempty"`
	Status string    `json:"status"` // ok | failed | cached | cancelled
	Start  time.Time `json:"start"`
	End    time.Time `json:"end"`
	Lines  int64     `json:"lines,omitempty"`
}

// Output is where a Runner reports the build output of its packages. The worker turns it into
// the job's log (lake, live subscribers).
type Output interface {
	// Line is one output line of package pkg (stream 1 stdout, 2 stderr, 3 bob).
	Line(pkg string, stream int32, text string)
	// EndPackage marks the end of pkg's output.
	EndPackage(pkg string, e PackageEnd)
}

// Runner does a job's work inside its job container (SPEC §05.8): evaluate bob.go in evalbox
// (isolation.Evaluate, strict, with EvalRequest), generate derivations, build with the
// embedded Nix as root (non-root builds), and stage the outputs, reporting package output to
// out. It never sees the worker's credentials.
type Runner interface {
	Run(ctx context.Context, j Job, out Output) Outcome
}

// EvalRequest is the evaluation request a Runner must use on a worker: the repository is
// untrusted (A98: go.mod replace targets outside the repository are refused). The evaluator
// sandbox must also run with evalbox Strict: true (isolation.Evaluate does; eval.Request has no
// pass-through for it yet, see plan/progress/I-B6.md NEEDS).
func EvalRequest(r eval.Request) eval.Request {
	r.Untrusted = true
	return r
}

// NotBuilt is the Runner until job execution through pkg/engine/build lands with the build
// integration milestones (M16/M17): every job fails with BOB-E-9008. The job container
// around it is real.
type NotBuilt struct{}

// Run implements Runner.
func (NotBuilt) Run(context.Context, Job, Output) Outcome {
	return Outcome{Status: "failed", Error: diag.NotImplemented("worker job execution (pkg/engine/build)").Error()}
}

// The job container's stdout is line-delimited JSON: output lines ("l"), package ends ("e")
// and, last, the outcome ("o").
type (
	jobEvent struct {
		Line    *lineEvent  `json:"l,omitempty"`
		End     *endEvent   `json:"e,omitempty"`
		Outcome *jobOutcome `json:"o,omitempty"`
	}
	lineEvent struct {
		Pkg    string `json:"p"`
		Stream int32  `json:"s"`
		Text   string `json:"t"`
	}
	endEvent struct {
		Pkg string     `json:"p"`
		End PackageEnd `json:"r"`
	}
	jobOutcome struct {
		Status string `json:"status"`
		Error  string `json:"error,omitempty"`
	}
)

// maxJobInput bounds the job document a runner accepts (a JobSpec is at most 64 KiB).
const maxJobInput = 1 << 20

// maxEventLine bounds one event line of the job container (log lines are cut at 4 KiB).
const maxEventLine = 1 << 20

// eventWriter is the Output of `bob-server job-run`: events on stdout.
type eventWriter struct {
	mu  sync.Mutex
	enc *json.Encoder
	err error
}

func (w *eventWriter) emit(e jobEvent) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.err == nil {
		w.err = w.enc.Encode(e)
	}
}

func (w *eventWriter) Line(pkg string, stream int32, text string) {
	w.emit(jobEvent{Line: &lineEvent{Pkg: pkg, Stream: stream, Text: text}})
}

func (w *eventWriter) EndPackage(pkg string, e PackageEnd) {
	w.emit(jobEvent{End: &endEvent{Pkg: pkg, End: e}})
}

// RunJob is `bob-server job-run` inside a job container: it reads the Job from in, runs r and
// writes its output events and, last, the outcome to out.
func RunJob(ctx context.Context, in io.Reader, out io.Writer, r Runner) error {
	var j Job
	if err := json.NewDecoder(io.LimitReader(in, maxJobInput)).Decode(&j); err != nil {
		return fmt.Errorf("job-run: read the job: %w", err)
	}
	w := &eventWriter{enc: json.NewEncoder(out)}
	var o Outcome
	if err := safe.Run(func() error { o = r.Run(ctx, j, w); return nil }); err != nil {
		o = Outcome{Status: "infra", Error: "job runner crashed"}
	}
	w.emit(jobEvent{Outcome: &jobOutcome{Status: o.Status, Error: o.Error}})
	return w.err
}

// readEvents feeds the job container's events from r to out until EOF and returns the
// outcome (nil when the container ended without one). The container is not trusted: event
// lines are size-capped and a malformed one ends the reading.
func readEvents(r io.Reader, out Output) (*jobOutcome, error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64<<10), maxEventLine)
	var o *jobOutcome
	for sc.Scan() {
		var e jobEvent
		if err := json.Unmarshal(sc.Bytes(), &e); err != nil {
			return o, fmt.Errorf("job container: malformed output event: %w", err)
		}
		switch {
		case e.Line != nil && out != nil:
			out.Line(e.Line.Pkg, e.Line.Stream, e.Line.Text)
		case e.End != nil && out != nil:
			out.EndPackage(e.End.Pkg, e.End.End)
		case e.Outcome != nil:
			o = e.Outcome
		}
	}
	if err := sc.Err(); err != nil {
		return o, fmt.Errorf("job container output: %w", err)
	}
	return o, nil
}
