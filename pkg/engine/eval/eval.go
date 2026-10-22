package eval

import (
	"context"
	"fmt"
	"time"
)

// Request is one evaluation of a repository's bob.go files (SPEC §1.2, R-FW-043).
type Request struct {
	Root     string   // repository root: the directory holding bob.lock
	Patterns []string // label patterns; they become the IR roots
	Limits   Limits   // zero fields take the local defaults (R-FW-045)
}

// Limits bound one evaluation inside the evaluator sandbox (R-FW-045, S13 §5.3). Wall clock,
// IR output and stderr caps always apply; Pids and MemoryBytes need a delegated cgroup.
type Limits struct {
	WallClock   time.Duration // default 120 s
	IROutput    int64         // bytes written to fd 3, default 64 MiB
	StderrKept  int64         // default 1 MiB
	Pids        int           // pids.max, default 512
	MemoryBytes int64         // memory.max incl. the /work tmpfs, default 2 GiB
	WorkBytes   int64         // /work tmpfs size, default 1 GiB
}

// Result is a finished evaluation.
type Result struct {
	IR     []byte // the IR JSON the evaluator wrote to fd 3, canonical (SPEC §1.6)
	Cached bool   // the eval key matched .bob/eval/key, so go build did not run
	Stderr []byte // evaluator diagnostics, capped at 1 MiB
}

// Evaluate generates (or reuses) the evaluator of r.Root and runs it in the evaluator sandbox
// (R-FW-045).
func Evaluate(ctx context.Context, r Request) (*Result, error) {
	return nil, notImplemented("eval.Evaluate")
}

// notImplemented is BOB-E-9008. Layer E3 may not import pkg/bob/diag (R-ARCH-003 table), so
// the stub carries the code in its text.
func notImplemented(feature string) error {
	return fmt.Errorf("error[BOB-E-9008]: %s is not implemented yet", feature)
}
