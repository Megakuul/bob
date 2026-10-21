package eval

import (
	"context"
	"fmt"
)

// Request is one evaluation of a repository's bob.go files (SPEC §1.2, R-FW-043).
type Request struct {
	Root     string   // repository root: the directory holding bob.lock
	Patterns []string // label patterns; they become the IR roots
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
