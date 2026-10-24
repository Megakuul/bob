package regmirror

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func TestCratesRoutes(t *testing.T) {
	man, blobs := t.TempDir(), t.TempDir()
	write(t, filepath.Join(man, "crates", "index", "it", "oa", "itoa"), `{"name":"itoa","vers":"1.0.15"}`+"\n")
	write(t, filepath.Join(man, "crates", "index", "3", "l", "log"), `{"name":"log","vers":"0.4.0"}`+"\n")
	write(t, filepath.Join(blobs, "crates", "itoa", "itoa-1.0.15.crate"), "CRATE")
	srv := httptest.NewServer(New(Config{Manifests: man, Blobs: blobs}))
	defer srv.Close()
	tests := []struct {
		name, path string
		status     int
		body       string
	}{
		{"config", "/crates/index/config.json", 200, `"dl":"` + srv.URL + `/crates/dl"`},
		{"index file", "/crates/index/it/oa/itoa", 200, `"vers":"1.0.15"`},
		{"three letters", "/crates/index/3/l/log", 200, `"log"`},
		{"wrong prefix", "/crates/index/ab/cd/itoa", 404, ""},
		{"unknown crate", "/crates/index/se/rd/serde", 404, ""},
		{"download", "/crates/dl/itoa/1.0.15/download", 200, "CRATE"},
		{"bad version", "/crates/dl/itoa/..%2f..%2fx/download", 404, ""},
		{"no download suffix", "/crates/dl/itoa/1.0.15", 404, ""},
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			status, body, _ := get(t, srv, c.path)
			if status != c.status || !strings.Contains(body, c.body) {
				t.Fatalf("GET %s = %d %q, want %d containing %q", c.path, status, body, c.status, c.body)
			}
		})
	}
}

func TestCratesRecord(t *testing.T) {
	var hits atomic.Int32
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if r.URL.Path == "/se/rd/serde" {
			_, _ = w.Write([]byte(`{"name":"serde"}` + "\n"))
			return
		}
		http.NotFound(w, r)
	}))
	defer up.Close()
	man := t.TempDir()
	m := &mirror{c: Config{Manifests: man, Blobs: t.TempDir(), RecordCrates: true}}
	req := httptest.NewRequest(http.MethodGet, "/crates/index/se/rd/serde", nil)
	// Point the recorder at the fake upstream.
	old := cratesIndexUpstreamURL
	cratesIndexUpstreamURL = up.URL + "/"
	defer func() { cratesIndexUpstreamURL = old }()
	for i := 0; i < 2; i++ {
		rec := httptest.NewRecorder()
		m.ServeHTTP(rec, req)
		if rec.Code != 200 || !strings.Contains(rec.Body.String(), "serde") {
			t.Fatalf("recorded GET = %d %q", rec.Code, rec.Body.String())
		}
	}
	if hits.Load() != 1 {
		t.Errorf("upstream hit %d times, want once (the second answer comes from the recording)", hits.Load())
	}
	rec := httptest.NewRecorder()
	m.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/crates/index/no/pe/nope", nil))
	if rec.Code != 404 {
		t.Errorf("a crate upstream does not have = %d, want 404", rec.Code)
	}
}
