package auth

import (
	"crypto/rand"
	"encoding/base32"
	"encoding/binary"
	"log/slog"
	"strings"
	"time"
)

// Secret is a password or token value. It redacts itself in every printing and logging path
// (SPEC §6.3.1: String, GoString, MarshalJSON, slog.LogValuer), so passing one to a logger
// or an error by mistake leaks nothing.
type Secret string

const redacted = "[redacted]"

func (Secret) String() string                 { return redacted }
func (Secret) GoString() string               { return redacted }
func (Secret) MarshalJSON() ([]byte, error)   { return []byte(`"` + redacted + `"`), nil }
func (Secret) LogValue() slog.Value           { return slog.StringValue(redacted) }
func (s Secret) reveal() string               { return string(s) }
func (s Secret) hasPrefix(prefix string) bool { return strings.HasPrefix(string(s), prefix) }

// Token prefixes of SPEC §6.3.1.
const (
	PrefixAccess   = "bob_at_"
	PrefixRefresh  = "bob_rt_"
	PrefixSession  = "bob_st_"
	PrefixDownload = "bob_dt_"
	PrefixSA       = "bob_sa_"
)

var tokenEncoding = base32.NewEncoding("abcdefghijklmnopqrstuvwxyz234567").WithPadding(base32.NoPadding)

// newToken returns prefix + base32(32 random bytes) (52 characters after the prefix).
func newToken(prefix string) Secret {
	b := make([]byte, 32)
	_, _ = rand.Read(b) // crypto/rand.Read never fails (Go ≥ 1.24)
	return Secret(prefix + tokenEncoding.EncodeToString(b))
}

// crockford is the lower-case Crockford base32 alphabet of bob's ids (SPEC §6.1).
const crockford = "0123456789abcdefghjkmnpqrstvwxyz"

// newID returns a 26-character time-sortable id: 48-bit milliseconds + 80 random bits.
func newID(now time.Time) string {
	var b [16]byte
	binary.BigEndian.PutUint64(b[0:8], uint64(now.UnixMilli())<<16)
	_, _ = rand.Read(b[6:])
	// 128 bits → 26 base32 digits (the top 2 bits of the first digit are zero).
	hi, lo := binary.BigEndian.Uint64(b[0:8]), binary.BigEndian.Uint64(b[8:16])
	out := make([]byte, 26)
	for i := 25; i >= 0; i-- {
		out[i] = crockford[lo&31]
		lo = lo>>5 | hi<<59
		hi >>= 5
	}
	return string(out)
}

// randomString returns n characters from alphabet, uniformly (rejection sampling).
func randomString(alphabet string, n int) string {
	out := make([]byte, 0, n)
	limit := 256 - 256%len(alphabet)
	buf := make([]byte, 2*n)
	for len(out) < n {
		_, _ = rand.Read(buf)
		for _, c := range buf {
			if int(c) < limit && len(out) < n {
				out = append(out, alphabet[int(c)%len(alphabet)])
			}
		}
	}
	return string(out)
}
