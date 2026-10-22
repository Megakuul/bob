package nixidx

import (
	"context"

	"github.com/megakuul/bob/pkg/bob/diag"
	"github.com/megakuul/bob/pkg/engine/store"
)

// Config selects the nixpkgs the index answers for.
type Config struct {
	Source            string // store path (or directory) of the nixpkgs source tree
	Rev               string
	System            string // Nix system, "aarch64-linux"
	AllowUnfree       bool
	PermittedInsecure []string
}

// Entry is the data resolve.nix returns for one attribute (SPEC R-NIX-027).
type Entry struct {
	Missing              bool
	Broken               bool
	Insecure             bool
	KnownVulnerabilities []string
	Unfree               bool
	Available            bool
	Version              string
	DrvPath              string
	Outputs              map[string]string // output name → store path
	EvalRefused          string
}

// Index is the nixpkgs lookup of one (rev, system).
type Index struct{}

// Open opens the index; results are cached in <home>/cache/nixpkgs/<rev>-<system>.json.
func Open(ctx context.Context, st *store.Store, c Config) (*Index, error) {
	return nil, diag.NotImplemented("nixidx.Open")
}

// Lookup resolves attrs. An attribute that cannot be evaluated is isolated by a per-attribute
// retry and reported as BOB-E-1017 in the returned error (a *diag.Error); the other attributes
// are still answered.
func (i *Index) Lookup(ctx context.Context, attrs []string) (map[string]Entry, error) {
	return nil, diag.NotImplemented("nixidx.Lookup")
}

// Names returns the top-level attribute names (for suggestions).
func (i *Index) Names(ctx context.Context) ([]string, error) {
	return nil, diag.NotImplemented("nixidx.Names")
}

// Cached reports whether outPath is available from a configured substituter.
func (i *Index) Cached(ctx context.Context, outPath string) (bool, error) {
	return false, diag.NotImplemented("nixidx.Cached")
}
