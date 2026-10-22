package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"io"

	"github.com/megakuul/bob/internal/safe"
	"github.com/megakuul/bob/pkg/bob/diag"
	"github.com/megakuul/bob/pkg/engine/eval"
)

// Job is what the runner inside a job container gets on its stdin (JSON).
type Job struct {
	ID      string `json:"id"`
	Epoch   int64  `json:"epoch"`
	Attempt int64  `json:"attempt"`
	Spec    []byte `json:"spec"` // the signed canonical CBOR JobSpec
	Slot    int    `json:"slot"` // the worker slot (evalbox maps its uid from it)
}

// Runner does a job's work inside its job container (SPEC §05.8): evaluate bob.go in evalbox
// (isolation.Evaluate, strict), generate derivations, build with the embedded Nix as root
// (non-root builds), and stage the outputs. It never sees the worker's credentials.
type Runner interface {
	Run(ctx context.Context, j Job) Outcome
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
func (NotBuilt) Run(context.Context, Job) Outcome {
	return Outcome{Status: "failed", Error: diag.NotImplemented("worker job execution (pkg/engine/build)").Error()}
}

// jobOutcome is the runner's answer on stdout.
type jobOutcome struct {
	Status string `json:"status"`
	Error  string `json:"error,omitempty"`
}

// maxJobInput bounds the job document a runner accepts (a JobSpec is at most 64 KiB).
const maxJobInput = 1 << 20

// RunJob is `bob-server job-run` inside a job container: it reads the Job from in, runs r and
// writes the Outcome to out.
func RunJob(ctx context.Context, in io.Reader, out io.Writer, r Runner) error {
	var j Job
	if err := json.NewDecoder(io.LimitReader(in, maxJobInput)).Decode(&j); err != nil {
		return fmt.Errorf("job-run: read the job: %w", err)
	}
	var o Outcome
	if err := safe.Run(func() error { o = r.Run(ctx, j); return nil }); err != nil {
		o = Outcome{Status: "infra", Error: "job runner crashed"}
	}
	return json.NewEncoder(out).Encode(jobOutcome{Status: o.Status, Error: o.Error})
}
