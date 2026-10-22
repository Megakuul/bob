package fetch

import (
	"context"

	"github.com/megakuul/bob/pkg/bob/diag"
	"github.com/megakuul/bob/pkg/engine/store"
)

// Kind is the shape of a fetched input.
type Kind string

const (
	File    Kind = "file"    // one flat file
	Archive Kind = "archive" // an unpacked tree
)

// Request is one external input (SPEC R-NIX-014).
type Request struct {
	URLs     []string // mirrors in order
	Kind     Kind     // File (flat) or Archive (unpacked tree)
	Hash     string   // SRI of the downloaded bytes (required for File; checked if set for Archive)
	NarHash  string   // SRI of the unpacked tree (required for Archive)
	Name     string   // store name
	Strip    int      // leading path components to drop (Archive)
	MaxBytes int64    // 0 = 2 GiB
	Label    string   // for error messages
}

// Fetcher gets every external input.
type Fetcher interface {
	// Ensure makes the store path valid and returns it: already valid, then substituters
	// (nix-store --realise of the path), then origin URLs. It never runs a Nix FOD.
	Ensure(ctx context.Context, r Request) (storePath string, err error)
	// Resolve downloads without a known NarHash (bob lock) and returns the hashes to record.
	Resolve(ctx context.Context, r Request) (hash, narHash string, err error)
}

// Policy is the egress policy set by the caller (developers: no restriction).
type Policy struct {
	DenyPrivate bool // refuse private, loopback and link-local addresses
}

// New returns a Fetcher that imports into st.
func New(st *store.Store, p Policy) Fetcher { return stub{} }

type stub struct{}

func (stub) Ensure(context.Context, Request) (string, error) {
	return "", diag.NotImplemented("fetch.Ensure")
}

func (stub) Resolve(context.Context, Request) (string, string, error) {
	return "", "", diag.NotImplemented("fetch.Resolve")
}
