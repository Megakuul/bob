package isolation

import (
	"context"
	"errors"
	"strings"

	"github.com/megakuul/bob/pkg/engine/eval/evalbox"
)

// Evaluate runs a bob.go evaluation with evalbox in worker mode (ADR 0015 decision 1, S13-3):
// Strict, so a container without a delegated cgroup or Landlock ABI ≥ 4 refuses instead of
// evaluating with weaker limits. It is called inside the job container, where the job's
// cgroup leaf is the cgroup namespace root that evalbox delegates from.
func Evaluate(ctx context.Context, s evalbox.Spec) (*evalbox.Outcome, error) {
	s.Strict = true
	out, err := evalbox.Run(ctx, s)
	if errors.Is(err, evalbox.ErrNoIsolation) {
		reason, _, _ := strings.Cut(strings.TrimPrefix(err.Error(), evalbox.ErrNoIsolation.Error()+": "), "\n")
		return nil, &Refusal{Reason: "evaluation sandbox: " + reason, Err: err}
	}
	return out, err
}
