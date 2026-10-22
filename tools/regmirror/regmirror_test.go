package regmirror

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func write(t *testing.T, p, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func get(t *testing.T, srv *httptest.Server, p string, hdr ...string) (int, string, http.Header) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, srv.URL+p, nil)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b), resp.Header
}

func TestMirror(t *testing.T) {
	man, blobs := t.TempDir(), t.TempDir()
	write(t, filepath.Join(man, "npm", "packuments", "left-pad.json"), `{"dist":{"tarball":"@REGISTRY@/left-pad/-/left-pad-1.3.0.tgz"}}`)
	write(t, filepath.Join(man, "npm", "packuments", "@scope%2fpkg.json"), `{"name":"@scope/pkg"}`)
	write(t, filepath.Join(man, "npm", "versions", "left-pad", "1.3.0.json"), `{"os":["linux"]}`)
	write(t, filepath.Join(blobs, "npm", "left-pad", "-", "left-pad-1.3.0.tgz"), "TGZ")
	commit := strings.Repeat("a", 40)
	write(t, filepath.Join(blobs, "opam", "repository", commit+".tar.gz"), "REPO")
	hash := "ab" + strings.Repeat("c", 62)
	write(t, filepath.Join(blobs, "opam", "cache", "sha256", "ab", hash), "SRC")
	srv := httptest.NewServer(New(Config{Manifests: man, Blobs: blobs}))
	defer srv.Close()

	tests := []struct {
		name, path string
		status     int
		body       string // substring
	}{
		{"packument with base replaced", "/npm/left-pad", 200, srv.URL + "/npm/left-pad/-/left-pad-1.3.0.tgz"},
		{"scoped packument", "/npm/@scope/pkg", 200, `"@scope/pkg"`},
		{"escaped scoped packument", "/npm/@scope%2fpkg", 200, `"@scope/pkg"`},
		{"version document", "/npm/left-pad/1.3.0", 200, `"linux"`},
		{"tarball", "/npm/left-pad/-/left-pad-1.3.0.tgz", 200, "TGZ"},
		{"unknown package", "/npm/nope", 404, ""},
		{"traversal in name", "/npm/..%2f..%2fetc%2fpasswd", 404, ""},
		{"traversal in tarball", "/npm/left-pad/-/..%2f..%2fx.tgz", 404, ""},
		{"opam snapshot", "/opam/repository/" + commit + ".tar.gz", 200, "REPO"},
		{"opam snapshot bad commit", "/opam/repository/zz.tar.gz", 404, ""},
		{"opam cache entry", "/opam/cache/sha256/ab/" + hash, 200, "SRC"},
		{"opam cache wrong prefix", "/opam/cache/sha256/cd/" + hash, 404, ""},
		{"opam cache bad algorithm", "/opam/cache/rot13/ab/" + hash, 404, ""},
		{"other route", "/crates/x", 404, ""},
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			status, body, _ := get(t, srv, c.path)
			if status != c.status || !strings.Contains(body, c.body) {
				t.Fatalf("GET %s = %d %q, want %d containing %q", c.path, status, body, c.status, c.body)
			}
		})
	}

	t.Run("etag", func(t *testing.T) {
		_, _, h := get(t, srv, "/npm/left-pad")
		etag := h.Get("ETag")
		if etag == "" {
			t.Fatal("no ETag")
		}
		if status, _, _ := get(t, srv, "/npm/left-pad", "If-None-Match", etag); status != http.StatusNotModified {
			t.Fatalf("conditional GET = %d, want 304", status)
		}
	})
	t.Run("method", func(t *testing.T) {
		resp, err := srv.Client().Post(srv.URL+"/npm/left-pad", "text/plain", strings.NewReader("x"))
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusMethodNotAllowed {
			t.Fatalf("POST = %d", resp.StatusCode)
		}
	})
}
