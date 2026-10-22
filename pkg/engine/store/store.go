package store

import (
	"context"
	"io"

	"github.com/megakuul/bob/pkg/bob/diag"
	"github.com/megakuul/bob/pkg/engine/drv"
)

// Substituter is a binary cache bob passes to Nix.
type Substituter struct{ URL, PublicKey, NetrcMachine, NetrcPassword string }

// Config configures bob's private Nix store (SPEC R-NIX-024).
type Config struct {
	Home         string // state home; empty: $BOB_HOME, else ${XDG_DATA_HOME:-$HOME/.local/share}/bob
	Substituters []Substituter
}

// Store is the embedded rootless Nix of one bob command.
type Store struct{}

// Open extracts the embedded Nix on first use, enters the namespace and returns the store.
func Open(ctx context.Context, c Config) (*Store, error) {
	return nil, diag.NotImplemented("store.Open")
}

// Home is the state home in use.
func (s *Store) Home() string { return "" }

// Invalid returns those of paths that are not valid in the store, in one batched
// `nix-store --check-validity --print-invalid`.
func (s *Store) Invalid(ctx context.Context, paths []string) ([]string, error) {
	return nil, diag.NotImplemented("store.Invalid")
}

// Import imports items in one `nix-store --import` stream (export format v1, no signatures).
func (s *Store) Import(ctx context.Context, items []ExportItem) error {
	return diag.NotImplemented("store.Import")
}

// ExportItem is one path of an export stream: the NAR of the path, its references and deriver.
type ExportItem struct {
	Path       string
	NAR        []byte
	References []string
	Deriver    string
}

// WriteExport writes items as an export stream of format v1; items must be topologically
// ordered (references first).
func WriteExport(w io.Writer, items []ExportItem) error {
	return diag.NotImplemented("store.WriteExport")
}

// DrvExport is the export item of a finalised derivation: the .drv file with the input
// derivations and sources as references.
func DrvExport(d *drv.Derivation) (ExportItem, error) {
	return ExportItem{}, diag.NotImplemented("store.DrvExport")
}

// BuilderPath returns the recorded store path of the embedded bob-builder for a Nix system
// ("aarch64-linux", "x86_64-linux"), from the generated table builders.go (R-NIX-015).
func BuilderPath(system string) (string, error) {
	return "", diag.NotImplemented("store.BuilderPath")
}
