package srcs

import (
	"context"

	"github.com/megakuul/bob/pkg/bob/diag"
	"github.com/megakuul/bob/pkg/engine/store"
)

// Tree is a filtered source tree: Files are slash-separated paths relative to Root.
type Tree struct {
	Root  string
	Files []string
}

// Source is one package source set to import.
type Source struct {
	Name string
	Tree Tree
}

// Path computes makeStorePath("source", sha256(NAR), name) without touching the store.
func Path(name string, t Tree) (string, error) { return "", diag.NotImplemented("srcs.Path") }

// Import serialises each source as a NAR in Go, checks validity in one batch and imports only
// the missing paths through one `nix-store --import` stream. It returns the store paths in the
// order of sources.
func Import(ctx context.Context, st *store.Store, sources []Source) ([]string, error) {
	return nil, diag.NotImplemented("srcs.Import")
}
