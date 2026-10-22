package store

import (
	"crypto/sha256"
	"encoding/base32"
	"encoding/hex"
	"errors"
	"fmt"
	"hash/fnv"
	"strings"
)

// ErrKeySegment is returned for a key segment that does not match [A-Za-z0-9@._-]{1,100}.
var ErrKeySegment = errors.New("store: key segment invalid: must match [A-Za-z0-9@._-]{1,100}")

// Key builders (R-STORE-104). Each returns a valid, collision-free key segment or
// ErrKeySegment. The names are the harness's choice: SPEC fixes the encodings, not the API.

// maxSegment is the longest key segment dynamitedb's key sanitizer accepts.
const maxSegment = 100

var b32 = base32.StdEncoding.WithPadding(base32.NoPadding)

// Num10 is %010d.
func Num10(n uint64) (string, error) { return fmt.Sprintf("%010d", n), nil }

// Seq20 is %020d.
func Seq20(n uint64) (string, error) { return fmt.Sprintf("%020d", n), nil }

// Enc is b+base32(s) for len(s) <= 60, else h+first 40 hex chars of SHA-256(s).
// The prefixes keep the two forms apart, so Enc is collision-free up to SHA-256.
func Enc(s string) (string, error) {
	if len(s) <= 60 {
		return "b" + strings.ToLower(b32.EncodeToString([]byte(s))), nil
	}
	h := sha256.Sum256([]byte(s))
	return "h" + hex.EncodeToString(h[:])[:40], nil
}

// Hash64 is the hex SHA-256 of s.
func Hash64(s string) (string, error) {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:]), nil
}

// Sh2 is %02d of fnv32a(id) mod shards. It is total over non-empty ids (ruling G17: ids are
// validated at the API boundary, not here); shards must be 1..100 so the result has 2 digits.
func Sh2(id string, shards int) (string, error) {
	if id == "" {
		return "", ErrKeySegment
	}
	if shards < 1 || shards > 100 {
		return "", fmt.Errorf("store: shard count %d out of range 1..100", shards)
	}
	h := fnv.New32a()
	h.Write([]byte(id))
	return fmt.Sprintf("%02d", h.Sum32()%uint32(shards)), nil
}

// Segment validates one raw key segment.
func Segment(s string) (string, error) {
	if len(s) == 0 || len(s) > maxSegment {
		return "", ErrKeySegment
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		ok := c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' ||
			c == '@' || c == '.' || c == '_' || c == '-'
		if !ok {
			return "", ErrKeySegment
		}
	}
	return s, nil
}

// Key joins <pk>/<pkval> or <pk>/<pkval>/<sk>/<skval>, validating every segment.
func Key(pk, pkval string, sk ...string) (string, error) {
	if len(sk) != 0 && len(sk) != 2 {
		return "", fmt.Errorf("store: key needs a sort key name and value, got %d parts", len(sk))
	}
	parts := append([]string{pk, pkval}, sk...)
	for _, p := range parts {
		if _, err := Segment(p); err != nil {
			return "", err
		}
	}
	return strings.Join(parts, "/"), nil
}

// Scoped is the segment "<scope>.<enc(s)>" for keys that combine an id with an encoded name
// (e.g. bbr/<projectID.enc(branch)>). When that would exceed 100 characters, the name part
// is the hashed form "h"+40 hex chars, which Enc's prefixes keep apart from the short form.
func Scoped(scope, s string) (string, error) {
	if _, err := Segment(scope); err != nil {
		return "", err
	}
	e, _ := Enc(s)
	if len(scope)+1+len(e) > maxSegment {
		h := sha256.Sum256([]byte(s))
		e = "h" + hex.EncodeToString(h[:])[:40]
	}
	return Segment(scope + "." + e)
}
