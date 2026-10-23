package ui

import (
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

const page = `<!doctype html><html><head><script>
	theme();
</script><link href="/_app/immutable/entry/start.abc.js" rel="modulepreload"><script src="/x.js"></script></head>
<body><div style="display: contents"><script>
	start();
</script></div></body></html>`

func build() fstest.MapFS {
	return fstest.MapFS{
		".gitignore":                         {Data: []byte("*\n")},
		"200.html":                           {Data: []byte(page)},
		"login.html":                         {Data: []byte("<!doctype html><title>login</title>")},
		"_app/version.json":                  {Data: []byte(`{"version":"v"}`)},
		"_app/immutable/entry/start.abc.js":  {Data: []byte("export const a = 1;")},
		"_app/immutable/assets/0.def.css":    {Data: []byte("body{}")},
		"_app/immutable/nodes/0.ghi.js":      {Data: []byte("export {};")},
		"_app/immutable/assets/font.x.woff2": {Data: []byte("wOF2")},
	}
}

func serve(t *testing.T, u *UI, method, target string, hdr map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, target, nil)
	for k, v := range hdr {
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	u.ServeHTTP(w, r)
	return w
}

func hash(s string) string {
	sum := sha256.Sum256([]byte(s))
	return "'sha256-" + base64.StdEncoding.EncodeToString(sum[:]) + "'"
}

func TestRouting(t *testing.T) {
	u, err := New(build())
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		path, body, cache, ctype string
		code                     int
	}{
		// R-UI-001: unknown routes get 200.html, no-cache; hashed assets are immutable.
		{"/p/x/builds/y", page, "no-cache", "text/html; charset=utf-8", 200},
		{"/", page, "no-cache", "text/html; charset=utf-8", 200},
		{"/fleet/workers/w-1?x=1", page, "no-cache", "text/html; charset=utf-8", 200},
		{"/login", "<!doctype html><title>login</title>", "no-cache", "text/html; charset=utf-8", 200},
		{"/_app/immutable/entry/start.abc.js", "export const a = 1;", "public, max-age=31536000, immutable", "text/javascript; charset=utf-8", 200},
		{"/_app/immutable/assets/0.def.css", "body{}", "public, max-age=31536000, immutable", "text/css; charset=utf-8", 200},
		{"/_app/immutable/assets/font.x.woff2", "wOF2", "public, max-age=31536000, immutable", "font/woff2", 200},
		{"/_app/version.json", `{"version":"v"}`, "no-cache", "application/json", 200},
		// A missing asset is not answered with HTML; dot files of dist/ are not served.
		{"/_app/immutable/entry/gone.js", "not found\n", "no-cache", "text/plain; charset=utf-8", 404},
		{"/_app", "not found\n", "no-cache", "text/plain; charset=utf-8", 404},
		{"/.gitignore", page, "no-cache", "text/html; charset=utf-8", 200},
		// Traversal stays inside the build.
		{"/../../_app/version.json", `{"version":"v"}`, "no-cache", "application/json", 200},
	} {
		w := serve(t, u, http.MethodGet, c.path, nil)
		if w.Code != c.code || w.Body.String() != c.body {
			t.Errorf("GET %s: %d %q, want %d %q", c.path, w.Code, w.Body.String(), c.code, c.body)
		}
		if got := w.Header().Get("Cache-Control"); got != c.cache {
			t.Errorf("GET %s: Cache-Control %q, want %q", c.path, got, c.cache)
		}
		if got := w.Header().Get("Content-Type"); got != c.ctype {
			t.Errorf("GET %s: Content-Type %q, want %q", c.path, got, c.ctype)
		}
		if w.Header().Get("X-Content-Type-Options") != "nosniff" {
			t.Errorf("GET %s: no nosniff", c.path)
		}
	}
}

func TestCSP(t *testing.T) {
	u, err := New(build())
	if err != nil {
		t.Fatal(err)
	}
	got := serve(t, u, http.MethodGet, "/p/x", nil).Header().Get("Content-Security-Policy")
	want := "default-src 'self'; script-src 'self' " + hash("\n\ttheme();\n") + " " + hash("\n\tstart();\n") +
		"; style-src 'self' 'unsafe-inline'; img-src 'self' data:; object-src 'none'; base-uri 'none'; frame-ancestors 'none'"
	if got != want {
		t.Errorf("CSP\n got %s\nwant %s", got, want)
	}
	if h := serve(t, u, http.MethodGet, "/_app/immutable/entry/start.abc.js", nil).Header().Get("Content-Security-Policy"); h != "" {
		t.Errorf("a script carries a CSP: %q", h)
	}
}

func TestConditionalAndMethods(t *testing.T) {
	u, err := New(build())
	if err != nil {
		t.Fatal(err)
	}
	w := serve(t, u, http.MethodGet, "/p/x", nil)
	etag := w.Header().Get("ETag")
	if etag == "" {
		t.Fatal("no ETag")
	}
	if w := serve(t, u, http.MethodGet, "/other", map[string]string{"If-None-Match": etag}); w.Code != http.StatusNotModified {
		t.Errorf("revalidation with the fallback's ETag: %d, want 304", w.Code)
	}
	if w := serve(t, u, http.MethodHead, "/p/x", nil); w.Code != 200 || w.Body.Len() != 0 {
		t.Errorf("HEAD: %d with %d body bytes", w.Code, w.Body.Len())
	}
	if w := serve(t, u, http.MethodPost, "/p/x", nil); w.Code != http.StatusMethodNotAllowed || w.Header().Get("Allow") != "GET, HEAD" {
		t.Errorf("POST: %d Allow %q", w.Code, w.Header().Get("Allow"))
	}
}

func TestNotBuilt(t *testing.T) {
	if _, err := New(fstest.MapFS{".gitignore": {Data: []byte("*\n")}}); !errors.Is(err, ErrNotBuilt) {
		t.Errorf("empty build: %v, want ErrNotBuilt", err)
	}
	w := httptest.NewRecorder()
	NotBuilt().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/p/x", nil))
	if w.Code != http.StatusServiceUnavailable || !strings.Contains(w.Body.String(), "web UI") {
		t.Errorf("NotBuilt: %d %q", w.Code, w.Body.String())
	}
}

// TestEmbedded checks the embedded dist/: either a full build or only its .gitignore.
func TestEmbedded(t *testing.T) {
	u, err := Embedded()
	if errors.Is(err, ErrNotBuilt) {
		entries, _ := fs.ReadDir(dist, "dist")
		if len(entries) != 1 || entries[0].Name() != ".gitignore" {
			t.Errorf("dist/ has files but no %s", Fallback)
		}
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	if w := serve(t, u, http.MethodGet, "/p/x/builds/y", nil); w.Code != 200 || !strings.Contains(w.Body.String(), "/_app/immutable/") {
		t.Errorf("embedded fallback: %d", w.Code)
	}
}
