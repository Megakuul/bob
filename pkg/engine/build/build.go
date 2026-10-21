package build

import (
	"context"
	"fmt"
)

// Request is one run of the pipeline of R-FW-049.
type Request struct {
	Root     string   // repository root: the directory holding bob.lock
	Patterns []string // label patterns
}

// SourcePaths runs the pipeline up to source import (R-FW-049) and returns, per IR node id,
// the content-addressed source store path the ecosystems receive in PlanRequest.Sources
// (R-FW-021, R-NIX-009).
func SourcePaths(ctx context.Context, r Request) (map[string]string, error) {
	return nil, fmt.Errorf("error[BOB-E-9008]: build.SourcePaths is not implemented yet")
}
