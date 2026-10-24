package cache

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/megakuul/bob/internal/store/s3client"
	"github.com/megakuul/bob/tests/serverenv/rustfs"
)

// narBuilder writes NAR tokens.
type narBuilder struct{ bytes.Buffer }

func (b *narBuilder) str(s string) *narBuilder {
	var n [8]byte
	binary.LittleEndian.PutUint64(n[:], uint64(len(s)))
	b.Write(n[:])
	b.WriteString(s)
	if p := (8 - len(s)%8) % 8; p > 0 {
		b.Write(make([]byte, p))
	}
	return b
}

func (b *narBuilder) strs(ss ...string) *narBuilder {
	for _, s := range ss {
		b.str(s)
	}
	return b
}

func file(b *narBuilder, contents string) {
	b.strs("(", "type", "regular", "contents", contents, ")")
}

func TestWalkNARAndListing(t *testing.T) {
	var b narBuilder
	b.strs("nix-archive-1", "(", "type", "directory")
	b.strs("entry", "(", "name", "a", "node")
	file(&b, "hello")
	b.str(")")
	b.strs("entry", "(", "name", "d", "node", "(", "type", "directory")
	b.strs("entry", "(", "name", "l", "node", "(", "type", "symlink", "target", "../a", ")", ")")
	b.strs(")", ")")
	b.strs("entry", "(", "name", "x", "node", "(", "type", "regular", "executable", "", "contents", "#!", ")", ")")
	b.str(")")
	var ls bytes.Buffer
	l := NewListing(&ls)
	if err := WalkNAR(bytes.NewReader(b.Bytes()), l.Add); err != nil {
		t.Fatal(err)
	}
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Version int `json:"version"`
		Root    struct {
			Type    string `json:"type"`
			Entries map[string]struct {
				Type       string          `json:"type"`
				Size       int             `json:"size"`
				Executable bool            `json:"executable"`
				NarOffset  int             `json:"narOffset"`
				Entries    json.RawMessage `json:"entries"`
			} `json:"entries"`
		} `json:"root"`
	}
	if err := json.Unmarshal(ls.Bytes(), &doc); err != nil {
		t.Fatalf("listing is not JSON: %v: %s", err, ls.Bytes())
	}
	a := doc.Root.Entries["a"]
	if doc.Version != 1 || doc.Root.Type != "directory" || a.Size != 5 || !doc.Root.Entries["x"].Executable ||
		string(b.Bytes()[a.NarOffset:a.NarOffset+5]) != "hello" || !strings.Contains(string(doc.Root.Entries["d"].Entries), `"l":{"type":"symlink","target":"../a"}`) {
		t.Fatalf("listing: %s", ls.Bytes())
	}
}

func TestWalkNARRejects(t *testing.T) {
	head := func() *narBuilder {
		var b narBuilder
		b.strs("nix-archive-1", "(", "type", "directory")
		return &b
	}
	cases := map[string][]byte{}
	b := head()
	b.strs("entry", "(", "name", "b", "node")
	file(b, "")
	b.strs(")", "entry", "(", "name", "a", "node")
	file(b, "")
	b.strs(")", ")")
	cases["unsorted"] = b.Bytes()
	for _, bad := range []string{"..", ".", "a/b", "a\x00"} {
		b = head()
		b.strs("entry", "(", "name", bad, "node")
		file(b, "")
		b.strs(")", ")")
		cases["name "+bad] = b.Bytes()
	}
	b = head()
	b.str(")")
	cases["trailing"] = append(b.Bytes(), 1, 2, 3)
	cases["truncated"] = head().Bytes()
	var m narBuilder
	m.strs("nix-archive-2")
	cases["magic"] = m.Bytes()
	var p narBuilder
	p.strs("nix-archive-1", "(", "type", "regular", "contents")
	p.Write([]byte{1, 0, 0, 0, 0, 0, 0, 0, 'x', 1, 0, 0, 0, 0, 0, 0})
	cases["padding"] = p.Bytes()
	for name, nar := range cases {
		if err := WalkNAR(bytes.NewReader(nar), func(NarEntry) error { return nil }); !errors.Is(err, ErrBadNAR) {
			t.Errorf("%s: %v, want ErrBadNAR", name, err)
		}
	}
}

func TestRouteAndRange(t *testing.T) {
	h := strings.Repeat("a", 32)
	for p, want := range map[string]request{
		"/nix/nix-cache-info":        {kind: "info"},
		"/nix/" + h + ".narinfo":     {kind: "narinfo", hash: h},
		"/nix/nar/" + h + ".nar.zst": {kind: "nar", hash: h},
		"/nix/" + h + ".ls":          {kind: "ls", hash: h},
	} {
		if got, ok := route(p); !ok || got != want {
			t.Errorf("route(%s) = %+v %v", p, got, ok)
		}
	}
	for _, p := range []string{"/nix/" + strings.Repeat("e", 32) + ".narinfo", "/nix/nar/" + h + ".nar.xz", "/nix/x", "/nix/../" + h + ".narinfo"} {
		if _, ok := route(p); ok {
			t.Errorf("route(%s) accepted", p)
		}
	}
	for in, want := range map[string]string{"bytes=0-99": "bytes=0-99", "bytes=5-": "bytes=5-", "bytes=-10": "bytes=-10",
		"bytes=0-1,5-9": "", "bytes=9-1": "", "items=0-1": "", "": "", "bytes=x-": ""} {
		if got := singleRange(in); got != want {
			t.Errorf("singleRange(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestNarInfoRoundTrip(t *testing.T) {
	in := "StorePath: /nix/store/" + strings.Repeat("a", 32) + "-x\nURL: nar/" + strings.Repeat("a", 32) + ".nar.zst\nCompression: zstd\n" +
		"FileHash: sha256:" + strings.Repeat("1", 52) + "\nFileSize: 10\nNarHash: sha256:" + strings.Repeat("2", 52) +
		"\nNarSize: 20\nReferences: " + strings.Repeat("b", 32) + "-y " + strings.Repeat("a", 32) + "-x\nDeriver: z.drv\nSig: k-1:abc\n"
	ni, err := ParseNarInfo([]byte(in))
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Replace(strings.Replace(in, "Deriver: z.drv\n", "", 1),
		strings.Repeat("b", 32)+"-y "+strings.Repeat("a", 32)+"-x", strings.Repeat("a", 32)+"-x "+strings.Repeat("b", 32)+"-y", 1)
	if got := string(ni.Format()); got != want {
		t.Errorf("format:\n%s\nwant:\n%s", got, want)
	}
	for _, bad := range []string{"StorePath: /nix/store/x\nNarHash: sha256:" + strings.Repeat("2", 52), in + "StorePath: again\n", strings.Replace(in, "NarHash: sha256", "NarHash: md5", 1)} {
		if _, err := ParseNarInfo([]byte(bad)); err == nil {
			t.Errorf("accepted %.60q", bad)
		}
	}
}

// TestVisibilityGETsBounded: 1,000 concurrent lookups of 10 documents cost at most one GET
// per document (R-REG-150 allows 20), and a write is visible to the writer at once.
func TestVisibilityGETsBounded(t *testing.T) {
	if testing.Short() {
		t.Skip("starts RustFS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	s, err := rustfs.Start(ctx, rustfs.Options{Dir: t.TempDir(), Offline: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Stop() })
	target, _ := url.Parse(s.URL)
	var gets atomic.Int64
	rp := httputil.NewSingleHostReverseProxy(target)
	px := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/bob-artifacts/nix/vis/") {
			gets.Add(1)
		}
		rp.ServeHTTP(w, r)
	}))
	defer px.Close()
	o, err := OpenObjects(ctx, s3client.Options{Endpoint: px.URL, Region: "us-east-1",
		AccessKey: rustfs.RootAccessKey, SecretKey: rustfs.RootSecretKey}, "bob-artifacts")
	if err != nil {
		t.Fatal(err)
	}
	writer := NewVisStore(o, NixVisPrefix, nil)
	var ids []string
	for i := 0; i < 10; i++ {
		id := strings.Repeat(string("abcdfghijk"[i]), 32)
		ids = append(ids, id)
		if err := writer.AddProject(ctx, id, "p1"); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.AddProject(ctx, ids[0], "p1"); err != nil { // idempotent
		t.Fatal(err)
	}
	if err := writer.SetPublic(ctx, ids[0], "p1.0000000001", true); err != nil {
		t.Fatal(err)
	}
	if d, _ := writer.Get(ctx, ids[0]); d == nil || len(d.Projects) != 1 || len(d.Public) != 1 {
		t.Fatalf("doc %+v", d)
	}
	reader := NewVisStore(o, NixVisPrefix, nil)
	gets.Store(0)
	var wg sync.WaitGroup
	for w := 0; w < 20; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 50; i++ {
				if d, err := reader.Get(ctx, ids[(w*50+i)%10]); err != nil || d == nil {
					t.Errorf("get: %v %v", d, err)
				}
			}
		}()
	}
	wg.Wait()
	if n := gets.Load(); n > 10 {
		t.Errorf("1,000 lookups of 10 documents: %d GETs, want <= 10", n)
	}
	if d, err := reader.Get(ctx, strings.Repeat("z", 32)); d != nil || err != nil {
		t.Errorf("absent document: %v %v", d, err)
	}
}
