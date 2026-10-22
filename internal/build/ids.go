package buildctl

import (
	"crypto/rand"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// crockford is the lower-case Crockford base32 alphabet of build ids (SPEC §6.1).
const crockford = "0123456789abcdefghjkmnpqrstvwxyz"

// NewBuildID returns a fresh build id (SPEC §6.1, ULID layout): 26 lower-case Crockford
// characters, the first ten the 48-bit creation time in milliseconds (so ids sort by time;
// logstore.BuildTime reads it back), the other sixteen 80 random bits.
func NewBuildID(now time.Time) (string, error) {
	var rnd [10]byte
	if _, err := rand.Read(rnd[:]); err != nil {
		return "", fmt.Errorf("build id: %w", err)
	}
	ms := uint64(now.UnixMilli()) & (1<<48 - 1)
	var b [26]byte
	for i := range 10 {
		b[i] = crockford[(ms>>(5*(9-i)))&31]
	}
	var acc uint64
	bits, j := 0, 10
	for _, x := range rnd {
		acc = acc<<8 | uint64(x)
		bits += 8
		for bits >= 5 {
			bits -= 5
			b[j] = crockford[(acc>>bits)&31]
			j++
		}
	}
	return string(b[:]), nil
}

// JobID is the id of the n-th job (1-based) of a build: "<buildID>.j<nnnn>". The build id
// is part of the job id, so a job resolves to its build without an index (the job → build
// gap of decision A96).
func JobID(buildID string, n int) string { return fmt.Sprintf("%s.j%04d", buildID, n) }

// BuildOfJob returns the build id of a job id made by JobID.
func BuildOfJob(jobID string) (string, bool) {
	b, n, ok := strings.Cut(jobID, ".j")
	if !ok || len(b) != 26 || len(n) != 4 {
		return "", false
	}
	if _, err := strconv.ParseUint(n, 10, 16); err != nil {
		return "", false
	}
	return b, true
}
