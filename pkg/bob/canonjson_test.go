package bob

import (
	"bytes"
	"encoding/json"
	"math/rand"
	"testing"
)

// canonicalJSON equals its definition, decodeEncode, on hand-written documents and on 20,000
// random values (A318).
func TestCanonicalJSONMatchesDecodeEncode(t *testing.T) {
	docs := []string{
		`{}`, `[]`, `""`, `0`, `-0`, `1E5`, `1.50`, `true`, `null`,
		` { "b" : 1 , "a" : [ 1 , 2 , { "d" : null , "c" : "x" } ] } `,
		`{"a":1,"a":2}`, `{"b":{"z":1,"a":2},"a":{"y":[],"x":{}}}`,
		`"<>& <>&    😀 \ud800 \/ \b\f\n\r\t \u0001 é"`,
		`{"b":1,"a\u0000":2,"a":3,"é":4,"é":5}`,
		`[1,"a",[[[]]],{"k":[{"k":[]}]}]`,
		`{"a":1} trailing`, `{"a":}`, `[1,]`, ``,
	}
	for _, d := range docs {
		check(t, []byte(d))
	}
	r := rand.New(rand.NewSource(1))
	for range 20000 {
		b, err := json.Marshal(randomValue(r, 4))
		if err != nil {
			t.Fatal(err)
		}
		check(t, b)
	}
}

func check(t *testing.T, b []byte) {
	t.Helper()
	got, gerr := canonicalJSON(b)
	want, werr := decodeEncode(b)
	if (gerr == nil) != (werr == nil) || !bytes.Equal(got, want) {
		t.Fatalf("canonicalJSON(%s) = %s, %v; want %s, %v", b, got, gerr, want, werr)
	}
}

var pieces = []string{"a", "b", "Z", "<", ">", "&", "\"", "\\", "/", "\n", "\t", "\x00", "\x1f", "\x7f", "é", "€",
	" ", " ", "😀", "\xff", "\xc3", " ", "0", "key", " "}

func randomString(r *rand.Rand) string {
	var b bytes.Buffer
	for range r.Intn(6) {
		b.WriteString(pieces[r.Intn(len(pieces))])
	}
	return b.String()
}

func randomValue(r *rand.Rand, depth int) any {
	k := r.Intn(9)
	if depth == 0 {
		k = r.Intn(5)
	}
	switch k {
	case 0:
		return nil
	case 1:
		return r.Intn(2) == 0
	case 2:
		return r.Int63n(1<<40) - 1<<39
	case 3:
		return r.NormFloat64() * 1e6
	case 4:
		return randomString(r)
	case 5, 6:
		m := map[string]any{}
		for range r.Intn(5) {
			m[randomString(r)] = randomValue(r, depth-1)
		}
		return m
	case 7:
		s := make([]any, r.Intn(4))
		for i := range s {
			s[i] = randomValue(r, depth-1)
		}
		return s
	default:
		return struct {
			Z string `json:"z"`
			A any    `json:"a,omitempty"`
			M string `json:"<m>"`
		}{randomString(r), randomValue(r, depth-1), randomString(r)}
	}
}
