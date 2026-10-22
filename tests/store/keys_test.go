package store

import (
	"crypto/sha256"
	"encoding/base32"
	"encoding/hex"
	"errors"
	"fmt"
	"hash/fnv"
	"math/rand/v2"
	"regexp"
	"strings"
	"testing"

	"github.com/megakuul/bob/internal/store"
	"github.com/megakuul/bob/tests/parity"
	"github.com/megakuul/bob/tests/store/env"
)

var segmentRE = regexp.MustCompile(`^[A-Za-z0-9@._-]{1,100}$`)

var sh2RE = regexp.MustCompile(`^[0-9]{2}$`)

const segmentMsg = "store: key segment invalid: must match [A-Za-z0-9@._-]{1,100}"

// Reference encodings, written from the SPEC text and never from the code under test.
func refEnc(s string) string {
	if len(s) <= 60 {
		return "b" + strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString([]byte(s)))
	}
	h := sha256.Sum256([]byte(s))
	return "h" + hex.EncodeToString(h[:])[:40]
}

func refHash64(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

func refSh2(id string, shards int) string {
	h := fnv.New32a()
	h.Write([]byte(id))
	return fmt.Sprintf("%02d", int(h.Sum32())%shards)
}

// randomInputs yields n strings that include '/', the empty string and 1 KiB inputs.
func randomInputs(n int, seed uint64) []string {
	r := rand.New(rand.NewPCG(seed, 0x9e3779b97f4a7c15))
	alphabet := []rune("abcXYZ019@._-/ \x00\né世\U0001F600%")
	out := make([]string, 0, n+4)
	out = append(out, "", "/", strings.Repeat("a", 1024), strings.Repeat("/", 1024))
	for len(out) < n {
		var l int
		switch r.IntN(10) {
		case 0:
			l = 1024
		case 1:
			l = 55 + r.IntN(12) // straddles the 60-byte enc boundary
		default:
			l = r.IntN(24)
		}
		b := make([]rune, l)
		for i := range b {
			b[i] = alphabet[r.IntN(len(alphabet))]
		}
		out = append(out, string(b))
	}
	return out
}

func TestRSTORE104KeySegmentBuilders(t *testing.T) {
	parity.Covers(t, "R-STORE-104")

	// Fixed vectors from the SPEC encodings.
	vec := []struct {
		name string
		got  func() (string, error)
		want string
	}{
		{"num10", func() (string, error) { return store.Num10(42) }, "0000000042"},
		{"seq20", func() (string, error) { return store.Seq20(42) }, "00000000000000000042"},
		{"enc short", func() (string, error) { return store.Enc("main") }, refEnc("main")},
		{"enc 60", func() (string, error) { return store.Enc(strings.Repeat("x", 60)) }, refEnc(strings.Repeat("x", 60))},
		{"enc 61", func() (string, error) { return store.Enc(strings.Repeat("x", 61)) }, refEnc(strings.Repeat("x", 61))},
		{"enc slash", func() (string, error) { return store.Enc("feature/a b") }, refEnc("feature/a b")},
		{"hash64", func() (string, error) { return store.Hash64("abc") }, refHash64("abc")},
		{"sh2", func() (string, error) { return store.Sh2("01JABCDEF", 16) }, refSh2("01JABCDEF", 16)},
	}
	for _, v := range vec {
		got, err := v.got()
		if env.Red(t, err) {
			return
		}
		if err != nil || got != v.want {
			t.Errorf("%s = %q, %v; want %q", v.name, got, err, v.want)
		}
	}
	if enc, _ := store.Enc(strings.Repeat("x", 61)); !strings.HasPrefix(enc, "h") || len(enc) != 41 {
		t.Errorf("enc of 61 bytes = %q, want h + 40 hex chars", enc)
	}

	// Raw segments: valid ones pass unchanged, everything else is the exact error.
	for _, ok := range []string{"a", "A9@._-", strings.Repeat("a", 100)} {
		if got, err := store.Segment(ok); err != nil || got != ok {
			t.Errorf("Segment(%q) = %q, %v; want it unchanged", ok, got, err)
		}
	}
	for _, bad := range []string{"", "a/b", "a b", "ä", strings.Repeat("a", 101), strings.Repeat("a", 1024), "a\x00", "a\n"} {
		_, err := store.Segment(bad)
		if !errors.Is(err, store.ErrKeySegment) || err.Error() != segmentMsg {
			t.Errorf("Segment(%.20q) error = %v, want exactly %q", bad, err, segmentMsg)
		}
		if _, err := store.Key("job", bad); !errors.Is(err, store.ErrKeySegment) {
			t.Errorf("Key(job, %.20q) error = %v, want ErrKeySegment", bad, err)
		}
		if _, err := store.Key("qs", "c1", "s", bad); !errors.Is(err, store.ErrKeySegment) {
			t.Errorf("Key(qs, c1, s, %.20q) error = %v, want ErrKeySegment", bad, err)
		}
	}
	if k, err := store.Key("job", "01J0"); err != nil || k != "job/01J0" {
		t.Errorf("Key(job, 01J0) = %q, %v", k, err)
	}
	if k, err := store.Key("qs", "c1", "s", "00000000000000000001"); err != nil || k != "qs/c1/s/00000000000000000001" {
		t.Errorf("Key(qs, c1, s, seq) = %q, %v", k, err)
	}

	// 10^6 random strings: no input yields an invalid or colliding segment.
	n := 1_000_000
	if testing.Short() {
		n = 50_000
	}
	inputs := randomInputs(n, 104)
	seenEnc := map[string]string{}
	seenHash := map[string]string{}
	for _, in := range inputs {
		enc, err := store.Enc(in)
		if err != nil || !segmentRE.MatchString(enc) || enc != refEnc(in) {
			t.Fatalf("Enc(%.30q) = %q, %v; want valid %q", in, enc, err, refEnc(in))
		}
		if prev, dup := seenEnc[enc]; dup && prev != in {
			t.Fatalf("Enc collision: %.30q and %.30q both give %q", prev, in, enc)
		}
		seenEnc[enc] = in
		h, err := store.Hash64(in)
		if err != nil || len(h) != 64 || !segmentRE.MatchString(h) || h != refHash64(in) {
			t.Fatalf("Hash64(%.30q) = %q, %v", in, h, err)
		}
		if prev, dup := seenHash[h]; dup && prev != in {
			t.Fatalf("Hash64 collision: %.30q and %.30q", prev, in)
		}
		seenHash[h] = in
		sh, err := store.Sh2(in, 16)
		if err != nil || !sh2RE.MatchString(sh) || sh != refSh2(in, 16) {
			if in == "" { // an empty id has no shard: an error is the right answer
				if !errors.Is(err, store.ErrKeySegment) {
					t.Fatalf("Sh2(\"\") = %q, %v; want ErrKeySegment", sh, err)
				}
				continue
			}
			t.Fatalf("Sh2(%.30q) = %q, %v; want %q", in, sh, err, refSh2(in, 16))
		}
		seg, err := store.Segment(in)
		if segmentRE.MatchString(in) {
			if err != nil || seg != in {
				t.Fatalf("Segment(%.30q) = %q, %v; want it unchanged", in, seg, err)
			}
		} else if !errors.Is(err, store.ErrKeySegment) {
			t.Fatalf("Segment(%.30q) accepted an invalid segment: %q, %v", in, seg, err)
		}
	}
	// sh2 of a 26-char Crockford ULID (the SPEC's id type) is always a valid shard equal to the formula
	const crockford = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"
	ur := rand.New(rand.NewPCG(1041, 7))
	for i := 0; i < 5000; i++ {
		b := make([]byte, 26)
		for j := range b {
			b[j] = crockford[ur.IntN(len(crockford))]
		}
		for _, shards := range []int{1, 16, 64} {
			sh, err := store.Sh2(string(b), shards)
			if err != nil || !sh2RE.MatchString(sh) || sh != refSh2(string(b), shards) {
				t.Fatalf("Sh2(ULID %s, %d) = %q, %v; want %q", b, shards, sh, err, refSh2(string(b), shards))
			}
		}
	}
	for i := uint64(0); i < 1000; i++ {
		a, err1 := store.Num10(i * 7919)
		b, err2 := store.Seq20(i * 7919)
		if err1 != nil || err2 != nil || len(a) != 10 || len(b) != 20 || !segmentRE.MatchString(a) || !segmentRE.MatchString(b) {
			t.Fatalf("Num10/Seq20(%d) = %q %q, %v %v", i*7919, a, b, err1, err2)
		}
	}
}

// FuzzKeySegmentBuilders is the Fuzz target the fuzz-long suite runs (HARNESS.md §14 #37).
func FuzzKeySegmentBuilders(f *testing.F) {
	for _, s := range []string{"", "/", "main", "feature/x", strings.Repeat("a", 1024), "a\x00b"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		enc, err := store.Enc(s)
		if env.Red(t, err) {
			return
		}
		if err != nil || !segmentRE.MatchString(enc) || enc != refEnc(s) {
			t.Fatalf("Enc(%.30q) = %q, %v", s, enc, err)
		}
		seg, err := store.Segment(s)
		if segmentRE.MatchString(s) != (err == nil) || (err == nil && seg != s) {
			t.Fatalf("Segment(%.30q) = %q, %v", s, seg, err)
		}
	})
}
