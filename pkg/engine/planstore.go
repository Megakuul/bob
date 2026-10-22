package engine

import (
	"context"

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
