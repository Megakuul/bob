// Package ui serves the embedded web UI (SPEC §6.10, R-UI-001): the static SvelteKit build of
// web/ (adapter-static, web/build) on the main listener at "/" with the SPA fallback 200.html.
//
// Go embedding cannot reach outside a package directory, so the web build mirrors web/build into
// dist/ (web/svelte.config.js, after adapter-static has written it) and dist/ is embedded here.
// dist/ holds only its .gitignore in a checkout that has not built web/ yet: such a binary
// answers every UI path with 503 and a hint (ErrNotBuilt), the API and all other routes work.
package ui

import (
	"bytes"
	"crypto/sha256"
	"embed"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"path"
	"strings"
	"time"
)

//go:embed all:dist
var dist embed.FS

// Fallback is the SPA entry page adapter-static writes for every route it did not prerender.
const Fallback = "200.html"

// immutablePrefix holds content-hashed assets: their names change with their content.
const immutablePrefix = "_app/immutable/"

// ErrNotBuilt means the embedded build has no 200.html: web/ was not built before go build.
var ErrNotBuilt = errors.New("this bob-server was built without its web UI: build web/ (vite build) before go build")

// UI serves one static build. Files are read and their headers computed once at New.
type UI struct {
	files map[string]*file // slash path relative to the build root ("_app/version.json")
}

type file struct {
	body    []byte
	headers map[string]string
}

// Embedded is the UI of the build embedded in this binary.
func Embedded() (*UI, error) {
	sub, err := fs.Sub(dist, "dist")
	if err != nil {
		return nil, fmt.Errorf("embedded web UI: %w", err)
	}
	return New(sub)
}

// New loads a static build. It fails with ErrNotBuilt when the build has no 200.html.
func New(build fs.FS) (*UI, error) {
	u := &UI{files: map[string]*file{}}
	err := fs.WalkDir(build, ".", func(name string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || strings.HasPrefix(path.Base(name), ".") {
			return nil // directories are not served; .gitignore is not part of the build
		}
		body, err := fs.ReadFile(build, name)
		if err != nil {
			return err
		}
		u.files[name] = newFile(name, body)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("web UI build: %w", err)
	}
	if u.files[Fallback] == nil {
		return nil, ErrNotBuilt
	}
	return u, nil
}

func newFile(name string, body []byte) *file {
	sum := sha256.Sum256(body)
	h := map[string]string{
		"Content-Type":           contentType(name),
		"ETag":                   `"` + hex.EncodeToString(sum[:16]) + `"`,
		"X-Content-Type-Options": "nosniff",
		"Referrer-Policy":        "same-origin",
		// HTML and the unhashed files (version.json) are revalidated on every load, so a new
		// server version is picked up at once; hashed assets never change under their name.
		"Cache-Control": "no-cache",
	}
	if strings.HasPrefix(name, immutablePrefix) {
		h["Cache-Control"] = "public, max-age=31536000, immutable"
	}
	if path.Ext(name) == ".html" {
		h["Content-Security-Policy"] = csp(body)
	}
	return &file{body: body, headers: h}
}

// csp is the policy of one HTML page: scripts only from this origin plus the page's own inline
// scripts by hash (SvelteKit's start script, the theme script of app.html), no plugins, no
// framing, no <base> rewriting. Inline styles stay allowed: markup and components set style
// attributes for dynamic values (AGENTS.md §8). form-action is not restricted because the
// OAuth consent form's answer redirects to the CLI's loopback redirect_uri (SPEC §6.3.2).
func csp(html []byte) string {
	script := []string{"'self'"}
	for _, s := range inlineScripts(html) {
		sum := sha256.Sum256(s)
		script = append(script, "'sha256-"+base64.StdEncoding.EncodeToString(sum[:])+"'")
	}
	return strings.Join([]string{
		"default-src 'self'",
		"script-src " + strings.Join(script, " "),
		"style-src 'self' 'unsafe-inline'",
		"img-src 'self' data:",
		"object-src 'none'",
		"base-uri 'none'",
		"frame-ancestors 'none'",
	}, "; ")
}

// inlineScripts returns the bodies of the <script> elements without a src attribute. The input
// is bob's own build output (lowercase tags, no "</script" inside scripts), not user content.
func inlineScripts(html []byte) [][]byte {
	var out [][]byte
	for {
		i := bytes.Index(html, []byte("<script"))
		if i < 0 {
			return out
		}
		html = html[i+len("<script"):]
		end := bytes.IndexByte(html, '>')
		if end < 0 {
			return out
		}
		attrs := html[:end]
		html = html[end+1:]
		stop := bytes.Index(html, []byte("</script"))
		if stop < 0 {
			return out
		}
		if !bytes.Contains(attrs, []byte("src=")) {
			out = append(out, html[:stop])
		}
		html = html[stop:]
	}
}

// contentType is fixed per extension instead of mime.TypeByExtension, which also reads the
// host's mime.types and could differ between machines.
func contentType(name string) string {
	switch path.Ext(name) {
	case ".html":
		return "text/html; charset=utf-8"
	case ".js", ".mjs":
		return "text/javascript; charset=utf-8"
	case ".css":
		return "text/css; charset=utf-8"
	case ".json", ".map":
		return "application/json"
	case ".svg":
		return "image/svg+xml"
	case ".png":
		return "image/png"
	case ".ico":
		return "image/x-icon"
	case ".woff2":
		return "font/woff2"
	case ".txt":
		return "text/plain; charset=utf-8"
	case ".webmanifest":
		return "application/manifest+json"
	}
	return "application/octet-stream"
}

// lookup maps a request path to a file: the file itself, a prerendered page (/login →
// login.html), or the SPA fallback. Missing files under /_app/ are 404: answering a script
// or stylesheet request with HTML would only produce a confusing MIME error in the browser.
func (u *UI) lookup(p string) *file {
	name := strings.TrimPrefix(path.Clean("/"+p), "/")
	if name == "" {
		name = "index.html"
	}
	if f := u.files[name]; f != nil {
		return f
	}
	if f := u.files[name+".html"]; f != nil {
		return f
	}
	if name == "_app" || strings.HasPrefix(name, "_app/") {
		return nil
	}
	return u.files[Fallback]
}

// ServeHTTP answers GET and HEAD; conditional requests (If-None-Match) and ranges are handled
// by http.ServeContent.
func (u *UI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	f := u.lookup(r.URL.Path)
	if f == nil {
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	for k, v := range f.headers {
		w.Header().Set(k, v)
	}
	http.ServeContent(w, r, "", time.Time{}, bytes.NewReader(f.body))
}

// NotBuilt answers every request with 503 and the ErrNotBuilt hint.
func NotBuilt() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		http.Error(w, ErrNotBuilt.Error(), http.StatusServiceUnavailable)
	})
}
