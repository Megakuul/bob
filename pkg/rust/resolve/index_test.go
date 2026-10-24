package resolve

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

const sumA = "0000000000000000000000000000000000000000000000000000000000000001"

func TestParseIndexFile(t *testing.T) {
	body := `{"name":"foo","vers":"1.0.0","deps":[{"name":"bar2","package":"bar","req":"^0.3","features":["x"],"optional":true,"default_features":false,"target":"cfg(unix)","kind":"normal"}],"cksum":"` + sumA + `","features":{"a":["bar2/x"]},"features2":{"b":["dep:bar2"]},"yanked":false,"links":null,"v":2}
{"name":"foo","vers":"not-a-version","deps":[],"cksum":"` + sumA + `","features":{},"yanked":false}

{"name":"foo","vers":"9.0.0","deps":[],"cksum":"` + sumA + `","features":{},"yanked":false,"v":3}
{"name":"foo","vers":"1.1.0","deps":[{"name":"q","req":"1","features":[],"optional":false,"default_features":true,"target":null,"kind":"dev","registry":"https://example.com/other-index"}],"cksum":"` + sumA + `","features":{},"yanked":true}
{"name":"foo","vers":"1.2.0","deps":[],"cksum":"bad","features":{},"yanked":false}`
	es, err := ParseIndexFile("foo", []byte(body))
	if err != nil {
		t.Fatal(err)
	}
	if len(es) != 1 {
		t.Fatalf("got %d entries, want 1 (bad version, newer schema, foreign registry and bad checksum skipped)", len(es))
	}
	e := es[0]
	d := e.Deps[0]
	if d.Name != "bar2" || d.Package != "bar" || d.Default || !d.Optional || d.Target != "cfg(unix)" || d.Kind != "" || d.Req.String() != "^0.3" {
		t.Errorf("dep = %+v", d)
	}
	if len(e.Table) != 2 || e.Features["b"][0] != "dep:bar2" {
		t.Errorf("features = %v", e.Features)
	}
	if _, ok := e.Features["bar2"]; ok {
		t.Errorf("dep:bar2 must hide the implicit feature bar2")
	}
	if _, err := ParseIndexFile("foo", []byte("{not json")); err == nil {
		t.Error("malformed line accepted")
	}
	if _, err := ParseIndexFile("foo", make([]byte, MaxIndexFile+1)); err == nil {
		t.Error("oversized file accepted")
	}
}

func TestIndexPath(t *testing.T) {
	for name, want := range map[string]string{"a": "1/a", "ab": "2/ab", "abc": "3/a/abc", "Serde_json": "se/rd/serde_json"} {
		if got := IndexPath(name); got != want {
			t.Errorf("IndexPath(%s) = %s, want %s", name, got, want)
		}
	}
	for _, bad := range []string{"", "../x", "a/b", "a b", strings.Repeat("a", 65)} {
		if ValidName(bad) {
			t.Errorf("ValidName(%q)", bad)
		}
	}
}

func TestSparseIndexCache(t *testing.T) {
	line := `{"name":"serde","vers":"1.0.0","deps":[],"cksum":"` + sumA + `","features":{},"yanked":false}` + "\n"
	var requests, conditional atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		switch r.URL.Path {
		case "/se/rd/serde":
			if r.Header.Get("If-None-Match") == `"v1"` {
				conditional.Add(1)
				w.WriteHeader(http.StatusNotModified)
				return
			}
			w.Header().Set("ETag", `"v1"`)
			_, _ = w.Write([]byte(line))
		case "/hu/ge/huge":
			_, _ = w.Write(make([]byte, MaxIndexFile+10))
		case "/br/ok/broken":
			http.Error(w, "boom", http.StatusInternalServerError)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	dir := t.TempDir()
	ctx := context.Background()

	ix := NewIndex(srv.URL, dir)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ { // concurrent callers share one request
		wg.Add(1)
		go func() {
			defer wg.Done()
			if es, err := ix.Versions(ctx, "serde"); err != nil || len(es) != 1 {
				t.Errorf("Versions = %v, %v", es, err)
			}
		}()
	}
	wg.Wait()
	if n := ix.Requests(); n != 1 {
		t.Errorf("%d requests for one crate, want 1", n)
	}
	// a new client revalidates with the stored ETag
	if es, err := NewIndex(srv.URL, dir).Versions(ctx, "serde"); err != nil || len(es) != 1 || conditional.Load() != 1 {
		t.Errorf("revalidation: %v %v, conditional requests %d", es, err, conditional.Load())
	}
	// offline answers from the cache without requests
	before := requests.Load()
	off := NewIndex(srv.URL, dir)
	off.Offline = true
	if es, err := off.Versions(ctx, "serde"); err != nil || len(es) != 1 || requests.Load() != before {
		t.Errorf("offline: %v %v", es, err)
	}
	if _, err := off.Versions(ctx, "tokio"); err == nil {
		t.Error("offline lookup of an uncached crate succeeded")
	}
	if _, err := ix.Versions(ctx, "nope"); !errors.Is(err, ErrNoCrate) {
		t.Errorf("missing crate: %v, want ErrNoCrate", err)
	}
	if _, err := ix.Versions(ctx, "huge"); err == nil || !strings.Contains(err.Error(), "larger") {
		t.Errorf("oversized index file: %v", err)
	}
	if _, err := ix.Versions(ctx, "broken"); err == nil || errors.Is(err, ErrNoCrate) {
		t.Errorf("server error: %v", err)
	}
	if _, err := ix.Versions(ctx, "../etc"); err == nil {
		t.Error("invalid name accepted")
	}
	ix.Prefetch(ctx, []string{"serde", "nope", "broken"})
}
