// Package regmirror serves the offline registry mirror of the harness (HARNESS.md §4.3): npm
// package documents and tarballs, and the opam repository snapshot with its archive cache. It
// answers from manifests committed under tests/fixtures/registry plus a blob cache that is not
// committed, and never touches the network (a manifest refresh is a manual, online step).
//
// The manifest format is documented in tests/fixtures/registry/README.md. The Go module proxy is
// served by the same layout in a later addition; this package covers the ecosystems of SPEC
// §03.3 (crates.io sparse index and .crate files), §03.4 (npm) and §03.5 (opam).
package regmirror

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"time"
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
	// RecordCrates, when set, makes the mirror fetch crates.io files it does not have from the
	// network and keep them (manifests: index files, blobs: .crate files): the manual, online
	// capture step of HARNESS.md §4.3. Gate runs never set it.
	RecordCrates bool
	// Client is used by RecordCrates; nil = a client with a 60 s timeout.
	Client *http.Client
}

// Upstream crates.io locations used by RecordCrates (variables so tests can point them at a
// fake upstream).
var (
	cratesIndexUpstreamURL = "https://index.crates.io/"
	cratesDLUpstreamURL    = "https://static.crates.io/crates/"
)

// New returns the mirror handler. Routes:
//
//	GET /npm/<name>                               abbreviated packument (name may be @scope/name)
//	GET /npm/<name>/<version>                     per-version document (os, cpu, libc)
//	GET /npm/<name>/-/<file>.tgz                  tarball from the blob cache
//	GET /opam/repository/<commit>.tar.gz          opam-repository snapshot from the blob cache
//	GET /opam/cache/<alg>/<xx>/<hash>             opam archive cache entry from the blob cache
//	GET /crates/index/config.json                 sparse index configuration (dl = this mirror)
//	GET /crates/index/<index path of a crate>     sparse index file (1/a, 2/ab, 3/a/abc, ab/cd/abcd…)
//	GET /crates/dl/<name>/<version>/download      .crate file from the blob cache
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
	case "crates":
		if rest == "index/config.json" {
			m.cratesConfig(w, r)
			return
		}
		file, doc = m.crateFile(rest)
		if file != "" && m.c.RecordCrates {
			if err := m.record(r, rest, file); err != nil {
				http.Error(w, err.Error(), http.StatusBadGateway)
				return
			}
		}
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
	if route == "npm" {
		base := "http://" + r.Host + "/npm"
		b = []byte(strings.ReplaceAll(string(b), BaseToken, base))
	}
	serveDoc(w, r, b, "application/json")
}

// serveDoc answers with a document, its ETag and 304 for a matching If-None-Match.
func serveDoc(w http.ResponseWriter, r *http.Request, b []byte, contentType string) {
	sum := sha256.Sum256(b)
	etag := `"` + hex.EncodeToString(sum[:8]) + `"`
	w.Header().Set("ETag", etag)
	if r.Header.Get("If-None-Match") == etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.Header().Set("Content-Type", contentType)
	_, _ = w.Write(b)
}

// cratesConfig serves the sparse index's config.json: crate downloads come from this mirror.
func (m *mirror) cratesConfig(w http.ResponseWriter, r *http.Request) {
	base := "http://" + r.Host + "/crates"
	serveDoc(w, r, []byte(`{"dl":"`+base+`/dl","api":"`+base+`"}`), "application/json")
}

var (
	crateSeg     = regexp.MustCompile(`^[a-z0-9_-]{1,64}$`)
	crateName    = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
	crateVersion = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+([-+][0-9A-Za-z.+-]{1,128})?$`)
)

// IndexPath is the sparse-index path of a crate name (Cargo reference, "Index files").
func IndexPath(name string) string {
	n := strings.ToLower(name)
	switch len(n) {
	case 1:
		return "1/" + n
	case 2:
		return "2/" + n
	case 3:
		return "3/" + n[:1] + "/" + n
	}
	return n[:2] + "/" + n[2:4] + "/" + n
}

// crateFile maps the part after /crates/ to a file; doc says the file is an index document.
func (m *mirror) crateFile(rest string) (file string, doc bool) {
	parts := strings.Split(rest, "/")
	switch {
	case len(parts) >= 3 && parts[0] == "index":
		name := parts[len(parts)-1]
		if !crateSeg.MatchString(name) || "index/"+IndexPath(name) != rest {
			return "", false
		}
		return filepath.Join(m.c.Manifests, "crates", "index", filepath.FromSlash(IndexPath(name))), true
	case len(parts) == 4 && parts[0] == "dl" && parts[3] == "download":
		name, version := parts[1], parts[2]
		if !crateName.MatchString(name) || !crateVersion.MatchString(version) || strings.Contains(version, "..") {
			return "", false
		}
		return filepath.Join(m.c.Blobs, "crates", strings.ToLower(name), strings.ToLower(name)+"-"+version+".crate"), false
	}
	return "", false
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

// record fetches a missing crates.io file from upstream into file (RecordCrates). A file the
// upstream does not have stays missing (the request then gets 404).
func (m *mirror) record(r *http.Request, rest, file string) error {
	if _, err := os.Stat(file); err == nil {
		return nil
	}
	var url string
	if idx, ok := strings.CutPrefix(rest, "index/"); ok {
		url = cratesIndexUpstreamURL + idx
	} else {
		parts := strings.Split(rest, "/") // dl/<name>/<version>/download
		url = cratesDLUpstreamURL + parts[1] + "/" + parts[1] + "-" + parts[2] + ".crate"
	}
	c := m.c.Client
	if c == nil {
		c = &http.Client{Timeout: 60 * time.Second}
	}
	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := c.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	switch {
	case resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusForbidden:
		return nil
	case resp.StatusCode != http.StatusOK:
		return fmt.Errorf("regmirror: %s: %s", url, resp.Status)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxDoc+1))
	if err != nil {
		return err
	}
	if len(b) > maxDoc {
		return fmt.Errorf("regmirror: %s is larger than %d bytes", url, maxDoc)
	}
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		return err
	}
	tmp := file + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, file)
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
