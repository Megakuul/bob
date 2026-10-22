package store

import (
	"errors"

	"github.com/megakuul/bob/pkg/bob/diag"
)

// ErrKeySegment is returned for a key segment that does not match [A-Za-z0-9@._-]{1,100}.
var ErrKeySegment = errors.New("store: key segment invalid: must match [A-Za-z0-9@._-]{1,100}")

// Key builders (R-STORE-104). Each returns a valid, collision-free key segment or
// ErrKeySegment. The names are the harness's choice: SPEC fixes the encodings, not the API.

// Num10 is %010d.
func Num10(n uint64) (string, error) { return "", diag.NotImplemented("store.Num10") }

// Seq20 is %020d.
func Seq20(n uint64) (string, error) { return "", diag.NotImplemented("store.Seq20") }

// Enc is b+base32(s) for len(s) <= 60, else h+first 40 hex chars of SHA-256(s).
func Enc(s string) (string, error) { return "", diag.NotImplemented("store.Enc") }

// Hash64 is the hex SHA-256 of s.
func Hash64(s string) (string, error) { return "", diag.NotImplemented("store.Hash64") }

// Sh2 is %02d of fnv32a(id) mod shards.
func Sh2(id string, shards int) (string, error) { return "", diag.NotImplemented("store.Sh2") }

// Segment validates one raw key segment.
func Segment(s string) (string, error) { return "", diag.NotImplemented("store.Segment") }

// Key joins <pk>/<pkval> or <pk>/<pkval>/<sk>/<skval>, validating every segment.
func Key(pk, pkval string, sk ...string) (string, error) {
	return "", diag.NotImplemented("store.Key")
}
