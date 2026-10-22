// Package regmirror serves the offline registry mirror of the harness (HARNESS.md §4.3): npm
// package documents and tarballs, and the opam repository snapshot with its archive cache. It
// answers from manifests committed under tests/fixtures/registry plus a blob cache that is not
// committed, and never touches the network (a manifest refresh is a manual, online step).
//
// The manifest format is documented in tests/fixtures/registry/README.md. Crates.io and the Go
// module proxy are served by the same layout in later additions; this package covers the
// ecosystems of SPEC §03.4 (npm) and §03.5 (opam).
package regmirror

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
)

// BaseToken is replaced in every served npm document by the URL of the npm route of the
// request, so a manifest does not depend on the port the mirror listens on.
const BaseToken = "@REGISTRY@"

// maxDoc caps a manifest document the mirror reads into memory (hostile-input rule, AGENTS §5.4).
const maxDoc = 64 << 20

var (
	segment  = regexp.MustCompile(`^[A-Za-z0-9@][A-Za-z0-9._~+-]*$`)
	hexDigit = regexp.MustCompile(`^[0-9a-f]{40,128}$`)
	algName  = regexp.MustCompile(`^(md5|sha1|sha256|sha512)$`)
)

// Config locates the manifests and the blob cache.
type Config struct {
	Manifests string // tests/fixtures/registry
	Blobs     string // blob cache directory (tarballs, archives); may be missing
}

// New returns the mirror handler. Routes:
//
//	GET /npm/<name>                               abbreviated packument (name may be @scope/name)
//	GET /npm/<name>/<version>                     per-version document (os, cpu, libc)
//	GET /npm/<name>/-/<file>.tgz                  tarball from the blob cache
//	GET /opam/repository/<commit>.tar.gz          opam-repository snapshot from the blob cache
//	GET /opam/cache/<alg>/<xx>/<hash>             opam archive cache entry from the blob cache
//
// Anything else, a path that does not validate, or a missing file is 404. Documents carry an
// ETag and answer If-None-Match with 304 (bob's metadata cache relies on it).
func New(c Config) http.Handler {
	return &mirror{c: c}
}

type mirror struct{ c Config }

func (m *mirror) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	p := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
	route, rest, _ := strings.Cut(p, "/")
	var file string
	var doc bool
	switch route {
	case "npm":
		file, doc = m.npmFile(rest)
	case "opam":
		file = m.opamFile(rest)
	}
	if file == "" {
		http.NotFound(w, r)
		return
	}
	if !doc {
		http.ServeFile(w, r, file)
		return
	}
	b, err := readCapped(file)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	base := "http://" + r.Host + "/npm"
	b = []byte(strings.ReplaceAll(string(b), BaseToken, base))
	sum := sha256.Sum256(b)
	etag := `"` + hex.EncodeToString(sum[:8]) + `"`
	w.Header().Set("ETag", etag)
	if r.Header.Get("If-None-Match") == etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(b)
}

// npmFile maps the part after /npm/ to a file; doc says the file is a JSON manifest document.
func (m *mirror) npmFile(rest string) (file string, doc bool) {
	parts := strings.Split(rest, "/")
	name := 1
	if strings.HasPrefix(rest, "@") {
		name = 2
	}
	if len(parts) < name || !validSegments(parts) {
		return "", false
	}
	pkg := strings.Join(parts[:name], "/")
	esc := strings.ReplaceAll(pkg, "/", "%2f")
	switch tail := parts[name:]; {
	case len(tail) == 0:
		return filepath.Join(m.c.Manifests, "npm", "packuments", esc+".json"), true
	case len(tail) == 1:
		return filepath.Join(m.c.Manifests, "npm", "versions", esc, tail[0]+".json"), true
	case len(tail) == 2 && tail[0] == "-" && strings.HasSuffix(tail[1], ".tgz"):
		return filepath.Join(m.c.Blobs, "npm", filepath.FromSlash(pkg), "-", tail[1]), false
	}
	return "", false
}

func (m *mirror) opamFile(rest string) string {
	parts := strings.Split(rest, "/")
	switch {
	case len(parts) == 2 && parts[0] == "repository" && strings.HasSuffix(parts[1], ".tar.gz"):
		if !hexDigit.MatchString(strings.TrimSuffix(parts[1], ".tar.gz")) {
			return ""
		}
		return filepath.Join(m.c.Blobs, "opam", "repository", parts[1])
	case len(parts) == 4 && parts[0] == "cache":
		if !algName.MatchString(parts[1]) || !hexDigit.MatchString(parts[3]) || len(parts[2]) != 2 || !strings.HasPrefix(parts[3], parts[2]) {
			return ""
		}
		return filepath.Join(m.c.Blobs, "opam", "cache", parts[1], parts[2], parts[3])
	}
	return ""
}

func validSegments(parts []string) bool {
	for _, s := range parts {
		if s == "-" {
			continue
		}
		if !segment.MatchString(s) || strings.Contains(s, "..") {
			return false
		}
	}
	return true
}

func readCapped(file string) ([]byte, error) {
	f, err := os.Open(file)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	b, err := io.ReadAll(io.LimitReader(f, maxDoc+1))
	if err != nil {
		return nil, err
	}
	if len(b) > maxDoc {
		return nil, errors.New("regmirror: manifest document too large")
	}
	return b, nil
}
