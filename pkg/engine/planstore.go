package engine

import (
	"context"
	"errors"

	"github.com/megakuul/bob/pkg/engine/drv"
	"github.com/megakuul/bob/pkg/engine/fetch"
	"github.com/megakuul/bob/pkg/engine/srcs"
)

// PlanStore is the store access an ecosystem may use while planning (PlanRequest.Store). The
// build fills it from its store and fetcher. Ecosystems that decide their own source sets
// while planning need it: Go reads the files of each package and imports one source path per
// workspace package (R-GO-002a), and reads third-party module trees before planning them
// (two phases, S2-2).
type PlanStore interface {
	// HostPath returns where bob can read the store path p: p itself inside bob's namespace,
	// else the same path below the store's home.
	HostPath(p string) string
	// ImportSources imports trees (files of the working tree or of readable store paths) as
	// content-addressed source paths (R-NIX-009, srcs.Import) and returns their store paths
	// in order. Trees that are already valid cost one batched validity query.
	ImportSources(ctx context.Context, trees []srcs.Source) ([]string, error)
	// Ensure makes a pinned external input valid and returns its store path (the fetcher's
	// Ensure: store, substituters, origin). It never runs a Nix fixed-output build.
	Ensure(ctx context.Context, r fetch.Request) (string, error)
}

// PlanRealiser is an optional extension of PlanStore (a type assertion on PlanRequest.Store):
// building derivations while planning, for the early-cutoff modes (§03.6.3 codegen, R-GO-005
// Go export data). An ecosystem builds what it planned so far, adds a part of the outputs to
// the store by content (ImportSources) and plans dependents against that path, so a
// byte-identical part rebuilds nothing downstream.
type PlanRealiser interface {
	// RealiseNow makes the derivations valid (with the bob derivations they need, planned by
	// this or earlier calls) and builds those whose outputs are not valid yet. It returns, per
	// derivation path, output name → path bob can read (HostPath). A failed build is an error;
	// the caller then plans without the cutoff and the build stage reports the failure.
	RealiseNow(ctx context.Context, drvs map[string]*drv.Derivation) (map[string]map[string]string, error)
}

// PlanOutput is one store output an ecosystem needs on disk while planning: Path is the output
// of the derivation Drv (its output Name). Drv is "" for a plain store path (a source).
type PlanOutput struct {
	Drv, Name, Path string
}

// PlanCloser is an optional extension of PlanStore (a type assertion on PlanRequest.Store) for
// outputs that are built from closures (SPEC §04): an OCI image has one layer derivation per
// store path of its programs' closures (R-OUT-011), a static tarball must reject a program with
// run-time dependencies before anything is built (R-OUT-004), and pruning is checked against
// the ELF files of the closure (R-OUT-007).
type PlanCloser interface {
	// Closure makes outs valid (bob derivations planned so far are built as RealiseNow does,
	// other derivations are substituted or built by Nix) and returns every path of their
	// closure with its references (without the path itself), sorted.
	Closure(ctx context.Context, outs []PlanOutput) (map[string][]string, error)
}

// ErrClosureBuild wraps a Closure failure caused by a build (a program that does not compile):
// the planner then plans a derivation that depends on the failing inputs, so the build stage
// reports the real failure with its log.
var ErrClosureBuild = errors.New("building a closure while planning failed")
