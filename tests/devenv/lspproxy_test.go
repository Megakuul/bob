package devenv

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/megakuul/bob/pkg/bob/diag"
	"github.com/megakuul/bob/pkg/cli/devenv"
	"github.com/megakuul/bob/tests/harness"
	"github.com/megakuul/bob/tests/parity"
)

const (
	store = "/nix/store"
	state = "/home/u/.local/share/bob"
)

// stubbed reports whether err is the stub answer of devenv and fails t as red if so.
func stubbed(t testing.TB, err error, feature string) bool {
	t.Helper()
	var de *diag.Error
	if errors.As(err, &de) && de.Code == diag.CodeNotImplemented {
		harness.RedFeature(t, feature)
		return true
	}
	return false
}

// semEqual compares two JSON documents by value (numbers kept exact), not by byte.
func semEqual(t testing.TB, a, b []byte) bool {
	t.Helper()
	dec := func(x []byte) any {
		d := json.NewDecoder(bytes.NewReader(x))
		d.UseNumber()
		var v any
		if err := d.Decode(&v); err != nil {
			t.Errorf("not valid JSON: %v: %s", err, x)
		}
		return v
	}
	return reflect.DeepEqual(dec(a), dec(b))
}

// TestRDEV010ProxyRewrite: the rewriting rules of SPEC §02.7.4 (rules 3 to 6): URI form, bare
// path, boundary, percent-encoding, object keys, numbers above 2^53, pass-through byte equality,
// invalid JSON pass-through.
func TestRDEV010ProxyRewrite(t *testing.T) {
	parity.Covers(t, "R-DEV-010")
	toHost := state + store // server → client
	cases := []struct {
		name     string
		from, to string
		in       string
		want     string // semantic equality; "" = byte-identical pass-through
	}{
		{"uri", store, toHost,
			`{"uri":"file:///nix/store/x1qs-go-1.26.8/share/go/src/fmt/print.go"}`,
			`{"uri":"file://` + toHost + `/x1qs-go-1.26.8/share/go/src/fmt/print.go"}`},
		{"uri client to server", toHost, store,
			`{"uri":"file://` + toHost + `/x1qs-go-1.26.8/src/a.go"}`,
			`{"uri":"file:///nix/store/x1qs-go-1.26.8/src/a.go"}`},
		{"bare path", store, toHost,
			`{"cmd":"/nix/store/abc-gcc-15/bin/gcc","args":["-I/nix/store/abc-glibc-dev/include","--sysroot=/nix/store/abc-glibc"]}`,
			`{"cmd":"` + toHost + `/abc-gcc-15/bin/gcc","args":["-I/nix/store/abc-glibc-dev/include","--sysroot=` + toHost + `/abc-glibc"]}`},
		{"bare path after boundary bytes", store, toHost,
			`{"a":"x=/nix/store/p","b":"(/nix/store/q)","c":"a /nix/store/r","d":"a:/nix/store/s"}`,
			`{"a":"x=` + toHost + `/p","b":"(` + toHost + `/q)","c":"a ` + toHost + `/r","d":"a:` + toHost + `/s"}`},
		{"host path is not rewritten twice", store, toHost,
			`{"uri":"file://` + toHost + `/x/print.go","p":"` + toHost + `/y"}`, ""},
		{"foo/nix/store untouched", store, toHost,
			`{"p":"/foo/nix/store/x","q":"/opt/nix/store/y","r":"a-/nix/store/z"}`, ""},
		{"store-like name untouched", store, toHost,
			`{"p":"/nix/storefront/x","q":"/nix/store"}`, ""},
		{"one pass", store, "/home/u/nix/store",
			`{"p":"/nix/store/a /nix/store/b"}`,
			`{"p":"/home/u/nix/store/a /home/u/nix/store/b"}`},
		{"client to server restores the store", toHost, store,
			`{"p":"` + toHost + `/a","q":"/foo` + toHost + `/b"}`,
			`{"p":"/nix/store/a","q":"/foo` + toHost + `/b"}`},
		{"percent-encoding", store, "/home/a b/.local/share/bob/nix/store",
			`{"uri":"file:///nix/store/x/a.go","p":"/nix/store/y"}`,
			`{"uri":"file:///home/a%20b/.local/share/bob/nix/store/x/a.go","p":"/home/a b/.local/share/bob/nix/store/y"}`},
		{"percent-encoded client to server", "/home/a b/.local/share/bob/nix/store", store,
			`{"uri":"file:///home/a%20b/.local/share/bob/nix/store/x/a.go"}`,
			`{"uri":"file:///nix/store/x/a.go"}`},
		{"object keys", store, toHost,
			`{"changes":{"file:///nix/store/x/a.go":[{"newText":"/nix/store/y"}]}}`,
			`{"changes":{"file://` + toHost + `/x/a.go":[{"newText":"` + toHost + `/y"}]}}`},
		{"numbers above 2^53", store, toHost,
			`{"id":9007199254740993,"v":1.0,"big":123456789012345678901234567890,"uri":"file:///nix/store/x"}`,
			`{"id":9007199254740993,"v":1.0,"big":123456789012345678901234567890,"uri":"file://` + toHost + `/x"}`},
		{"escaped slashes", store, toHost,
			`{"uri":"file:\/\/\/nix\/store\/x"}`,
			`{"uri":"file://` + toHost + `/x"}`},
		{"no HTML escaping", store, toHost,
			`{"t":"a<b>&c","uri":"file:///nix/store/x"}`,
			`{"t":"a<b>&c","uri":"file://` + toHost + `/x"}`},
		{"pass-through is byte for byte", store, toHost,
			"{ \"id\" :  1 ,\n \"a\":\"x\\u003c\" , \"n\": 1.50 }", ""},
		{"invalid JSON", store, toHost, `not json, /nix/store/x`, ""},
		{"truncated JSON", store, toHost, `{"uri":"file:///nix/store/x"`, ""},
		{"empty body", store, toHost, ``, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := devenv.Rewrite([]byte(c.in), c.from, c.to)
			if stubbed(t, err, "LSP proxy rewrite") {
				return
			}
			if err != nil {
				t.Fatalf("Rewrite: %v", err)
			}
			if c.want == "" {
				if string(got) != c.in {
					t.Errorf("pass-through changed the body:\n got %q\nwant %q", got, c.in)
				}
				return
			}
			if !semEqual(t, got, []byte(c.want)) {
				t.Errorf("rewrite:\n got %s\nwant %s", got, c.want)
			}
			for _, bad := range []string{`\u003c`, `\u0026`, `\u003e`} {
				if strings.Contains(string(got), bad) && !strings.Contains(c.in, bad) {
					t.Errorf("output is HTML-escaped (%s): %s", bad, got)
				}
			}
			if strings.Contains(c.name, "numbers") {
				for _, n := range []string{"9007199254740993", "123456789012345678901234567890"} {
					if !strings.Contains(string(got), n) {
						t.Errorf("number %s was not kept exactly: %s", n, got)
					}
				}
			}
		})
	}
}

// frame encodes one LSP frame; extra headers are added before Content-Length is read.
func frame(body string, extra ...string) string {
	return strings.Join(extra, "") + fmt.Sprintf("Content-Length: %d\r\n\r\n%s", len(body), body)
}

// TestRDEV010ProxyFraming: the pump keeps order, drops other headers, writes only Content-Length
// with the new length, and forwards bodies without a pattern byte for byte.
func TestRDEV010ProxyFraming(t *testing.T) {
	parity.Covers(t, "R-DEV-010")
	bodies := []string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`,
		`{ "jsonrpc" : "2.0", "id":2,"result":[{"uri":"file:///nix/store/x1qs-go/src/fmt/print.go","range":{}}] }`,
		`{"jsonrpc":"2.0","method":"window/logMessage","params":{"message":"héllo ✓ no pattern"}}`,
		`{"jsonrpc":"2.0","id":3,"result":{"changes":{"file:///nix/store/k/a.go":[]}}}`,
	}
	in := frame(bodies[0]) +
		frame(bodies[1], "Content-Type: application/vscode-jsonrpc; charset=utf-8\r\n") +
		frame(bodies[2], "X-Other: 1\r\n") +
		frame(bodies[3])
	var out bytes.Buffer
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	err := devenv.Pump(ctx, strings.NewReader(in), &out, store, state+store)
	if stubbed(t, err, "LSP proxy") {
		return
	}
	if err != nil && !errors.Is(err, io.EOF) {
		t.Fatalf("Pump: %v", err)
	}
	rest := out.String()
	for i, b := range bodies {
		head, tail, ok := strings.Cut(rest, "\r\n\r\n")
		if !ok {
			t.Fatalf("frame %d: no header terminator in %q", i, rest)
		}
		var n int
		if _, err := fmt.Sscanf(head, "Content-Length: %d", &n); err != nil || strings.Contains(head, "\r\n") || head != fmt.Sprintf("Content-Length: %d", n) {
			t.Fatalf("frame %d: header %q, want only Content-Length", i, head)
		}
		if len(tail) < n {
			t.Fatalf("frame %d: body shorter than Content-Length %d: %q", i, n, tail)
		}
		got := tail[:n]
		rest = tail[n:]
		if strings.Contains(b, "/nix/store/") {
			if strings.Contains(got, `"file:///nix/store/`) || !strings.Contains(got, "file://"+state+store+"/") {
				t.Errorf("frame %d not rewritten: %s", i, got)
			}
			if !semEqual(t, []byte(got), []byte(strings.ReplaceAll(b, "file:///nix/store/", "file://"+state+store+"/"))) {
				t.Errorf("frame %d: %s", i, got)
			}
		} else if got != b {
			t.Errorf("frame %d changed although it has no store path:\n got %q\nwant %q", i, got, b)
		}
	}
	if rest != "" {
		t.Errorf("extra output after the last frame: %q", rest)
	}
}
