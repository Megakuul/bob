package lock

import (
	"encoding/json"

	"github.com/megakuul/bob/pkg/bob/diag"
)

// File is bob.lock in memory. Sections are raw JSON owned by their ecosystem (SPEC §1.8).
type File struct {
	LockVersion int
	Nixpkgs     NixpkgsPin
	Sources     map[string]Source
	Sections    map[string]json.RawMessage // "go", "rust", "npm", "opam", "cc"
}

// NixpkgsPin is the "nixpkgs" entry.
type NixpkgsPin struct {
	Channel string `json:"channel"`
	Inputs  string `json:"inputs"`
	NarHash string `json:"narHash"`
	Rev     string `json:"rev"`
	URL     string `json:"url"`
}

// Source is one "sources" entry (bob.Fetch).
type Source struct {
	Hash    string `json:"hash"`
	Kind    string `json:"kind"` // file | archive
	Name    string `json:"name"`
	NarHash string `json:"narHash,omitempty"`
	Strip   int    `json:"strip"`
	URL     string `json:"url"`
}

// Read reads and validates bob.lock (errors per R-FW-048: BOB-E-1019).
func Read(path string) (*File, error) { return nil, diag.NotImplemented("lock.Read") }

// Write writes f canonically and atomically, and only if the bytes change.
func (f *File) Write(path string) error { return diag.NotImplemented("lock.File.Write") }

// DecodeSection decodes and validates one ecosystem section ("go", "rust", "npm", "opam",
// "cc") into its §03.7 type. Names, versions and hashes are checked against the ecosystem's
// grammar; invalid input is an error, never a panic.
func DecodeSection(key string, raw json.RawMessage) (any, error) {
	return nil, diag.NotImplemented("lock.DecodeSection")
}

// EncodeSection encodes a section decoded by DecodeSection canonically (keys sorted, arrays in
// schema order).
func EncodeSection(key string, v any) (json.RawMessage, error) {
	return nil, diag.NotImplemented("lock.EncodeSection")
}
