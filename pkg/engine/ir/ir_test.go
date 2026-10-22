package ir

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/megakuul/bob/pkg/bob/diag"
)

const doc = `{"bobVersion":"1","configs":[],"irVersion":1,"module":"m","nodes":[` +
	`{"definedAt":{"file":"a/bob.go","line":3},"deps":[],"id":"//a:a@linux-arm64","kind":"bob.run","label":"//a:a","proj":{"platform":"linux-arm64"},"spec":{"x":"<&>"},"srcs":[]},` +
	`{"definedAt":{"file":"b/bob.go","line":4},"deps":["//a:a@linux-arm64"],"id":"//b:b","kind":"bob.fetch","label":"//b:b","proj":{},"spec":{},"srcs":[{"dir":"b","patterns":["*.c"]}]}` +
	`],"pipelines":[],"repo":{"nixpkgs":"nixos-26.05","variants":[]},"roots":["//b:b"]}`

func TestParseAndCanonical(t *testing.T) {
	g, err := Parse([]byte(doc), "1")
	if err != nil {
		t.Fatal(err)
	}
	b, err := g.Canonical()
	if err != nil || string(b) != doc {
		t.Errorf("Canonical() differs from the canonical input (%v):\n%s\n%s", err, b, doc)
	}
}

func TestParseRejects(t *testing.T) {
	cases := map[string]string{
		"unknown field":   strings.Replace(doc, `"module":"m"`, `"module":"m","extra":1`, 1),
		"trailing data":   doc + "{}",
		"duplicate id":    strings.Replace(doc, `"id":"//b:b"`, `"id":"//a:a@linux-arm64"`, 1),
		"bad kind":        strings.Replace(doc, `"kind":"bob.fetch"`, `"kind":"Bob.Fetch"`, 1),
		"unknown root":    strings.Replace(doc, `"roots":["//b:b"]`, `"roots":["//c:c"]`, 1),
		"deep nesting":    strings.Replace(doc, `"spec":{}`, `"spec":{"a":`+strings.Repeat("[", 200)+strings.Repeat("]", 200)+`}`, 1),
		"self cycle":      strings.Replace(doc, `"deps":[],"id":"//a:a@linux-arm64"`, `"deps":["//a:a@linux-arm64"],"id":"//a:a@linux-arm64"`, 1),
		"other version":   strings.Replace(doc, `"bobVersion":"1"`, `"bobVersion":"2"`, 1),
		"unknown dep":     strings.Replace(doc, `"deps":["//a:a@linux-arm64"]`, `"deps":["//z:z"]`, 1),
		"no id":           strings.Replace(doc, `"id":"//b:b"`, `"id":""`, 1),
		"node not object": strings.Replace(doc, `"nodes":[`, `"nodes":[1,`, 1),
	}
	for name, in := range cases {
		_, err := Parse([]byte(in), "1")
		var d *diag.Error
		if !errors.As(err, &d) {
			t.Errorf("%s: error %v is not a *diag.Error", name, err)
		}
	}
	_, err := Parse([]byte(strings.Replace(doc, `"deps":[],"id":"//a:a@linux-arm64"`, `"deps":["//b:b"],"id":"//a:a@linux-arm64"`, 1)), "1")
	var d *diag.Error
	if !errors.As(err, &d) || d.Code != 1006 || !strings.Contains(err.Error(), "//a:a (a/bob.go:3) → //b:b (b/bob.go:4) → //a:a (a/bob.go:3)") {
		t.Errorf("cycle: %v", err)
	}
}

// Hostile shapes stay fast (the scan stops at the depth and size bounds).
func TestParseHostileIsFast(t *testing.T) {
	var chain strings.Builder
	chain.WriteString(`{"bobVersion":"1","configs":[],"irVersion":1,"module":"m","nodes":[`)
	for i := 0; i < 50000; i++ {
		if i > 0 {
			chain.WriteByte(',')
		}
		dep := ""
		if i > 0 {
			dep = fmt.Sprintf(`"n%d"`, i-1)
		}
		fmt.Fprintf(&chain, `{"definedAt":{"file":"","line":0},"deps":[%s],"id":"n%d","kind":"bob.run","label":"","proj":{},"spec":{},"srcs":[]}`, dep, i)
	}
	chain.WriteString(`],"pipelines":[],"repo":{"nixpkgs":"","variants":[]},"roots":[]}`)
	start := time.Now()
	if _, err := Parse([]byte(chain.String()), "1"); err != nil {
		t.Errorf("a chain of 50,000 nodes: %v", err)
	}
	t.Logf("chain of 50,000 nodes: %v", time.Since(start))
	for _, in := range [][]byte{
		bytes.Repeat([]byte("["), 1<<20),
		bytes.Repeat([]byte(`{"a":`), 1<<18),
		append([]byte(`"`), bytes.Repeat([]byte("x"), 1<<20)...),
		[]byte(`{"nodes":[` + strings.Repeat(`{},`, 100000) + `{}]}`),
	} {
		start := time.Now()
		if _, err := Parse(in, "1"); err == nil {
			t.Errorf("accepted %.20q", in)
		}
		if d := time.Since(start); d > 2*time.Second {
			t.Errorf("%.20q took %v", in, d)
		}
	}
}
