package buildctl

import (
	"crypto/rand"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// crockford is the upper-case Crockford base32 alphabet of build ids (ULID).
const crockford = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

// buildPrefix starts every build id (SPEC §02.6 shows `b-01JB7Q8V3K`).
const buildPrefix = "b-"

// NewBuildID returns a fresh build id: "b-" and a 26-character upper-case ULID, the first ten
// characters the 48-bit creation time in milliseconds (so ids sort by time; logstore.BuildTime
// reads it back), the other sixteen 80 random bits.
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
	return buildPrefix + string(b[:]), nil
}

// buildTime is the creation time in a build id. Besides "b-<ULID>" it accepts the 26
// lower-case characters of the ids made before the prefix (stores of earlier versions).
func buildTime(id string) (time.Time, error) {
	ulid, alphabet := id, strings.ToLower(crockford)
	if rest, ok := strings.CutPrefix(id, buildPrefix); ok {
		ulid, alphabet = rest, crockford
	}
	if len(ulid) != 26 {
		return time.Time{}, fmt.Errorf("build id %q", id)
	}
	var ms uint64
	for i := range len(ulid) {
		v := strings.IndexByte(alphabet, ulid[i])
		if v < 0 {
			return time.Time{}, fmt.Errorf("build id %q", id)
		}
		if i < 10 {
			ms = ms<<5 | uint64(v)
		}
	}
	if ms >= 1<<48 {
		return time.Time{}, fmt.Errorf("build id %q", id)
	}
	return time.UnixMilli(int64(ms)).UTC(), nil
}

// validBuildID reports whether id has the syntax of a build id.
func validBuildID(id string) bool {
	_, err := buildTime(id)
	return err == nil
}

// JobID is the id of the n-th job (1-based) of a build: "<buildID>.j<nnnn>". The build id
// is part of the job id, so a job resolves to its build without an index (the job → build
// gap of decision A96).
func JobID(buildID string, n int) string { return fmt.Sprintf("%s.j%04d", buildID, n) }

// BuildOfJob returns the build id of a job id made by JobID.
func BuildOfJob(jobID string) (string, bool) {
	b, n, ok := strings.Cut(jobID, ".j")
	if !ok || !validBuildID(b) || len(n) != 4 {
		return "", false
	}
	if _, err := strconv.ParseUint(n, 10, 16); err != nil {
		return "", false
	}
	return b, true
}
