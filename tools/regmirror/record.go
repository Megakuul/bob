package regmirror

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

// Upstream npm and opam locations used by RecordNpm and RecordOpam (variables so tests can point
// them at a fake upstream).
var (
	npmUpstreamURL       = "https://registry.npmjs.org/"
	opamRepoUpstreamURL  = "https://github.com/ocaml/opam-repository/archive/"
	opamCacheUpstreamURL = "https://opam.ocaml.org/cache/"
)

// npmAbbreviated is the Accept header of an abbreviated packument (the "short form" npm itself
// asks for; it carries os, cpu, engines, dist and the dependency maps of every version).
const npmAbbreviated = "application/vnd.npm.install-v1+json; q=1.0, application/json; q=0.8, */*"

// maxBlob caps a recorded tarball or archive.
const maxBlob = 2 << 30

// tarballURL matches the upstream prefix of a dist.tarball in an npm document.
var tarballURL = regexp.MustCompile(`("tarball"\s*:\s*")https?://registry\.npmjs\.org/`)

// fileLocks serialises recordings of the same file (two importers asking for one packument).
var fileLocks sync.Map

// recordCrate fetches a missing crates.io file from upstream into file (RecordCrates).
func (m *mirror) recordCrate(r *http.Request, rest, file string) error {
	var url string
	if idx, ok := strings.CutPrefix(rest, "index/"); ok {
		url = cratesIndexUpstreamURL + idx
	} else {
		parts := strings.Split(rest, "/") // dl/<name>/<version>/download
		url = cratesDLUpstreamURL + parts[1] + "/" + parts[1] + "-" + parts[2] + ".crate"
	}
	return m.fetch(r, url, "", file, maxDoc, nil)
}

// recordNpm fetches a missing npm document or tarball from registry.npmjs.org (RecordNpm).
// Packuments are fetched in the abbreviated form; documents get their dist.tarball URLs
// rewritten to BaseToken so the recording does not depend on the mirror's address.
func (m *mirror) recordNpm(r *http.Request, rest, file string, doc bool) error {
	url := npmUpstreamURL + rest
	if !doc {
		return m.fetch(r, url, "", file, maxBlob, nil)
	}
	nameParts := 1
	if strings.HasPrefix(rest, "@") {
		nameParts = 2
	}
	accept := "application/json" // per-version document
	if strings.Count(rest, "/")+1 == nameParts {
		accept = npmAbbreviated // packument
	}
	return m.fetch(r, url, accept, file, maxDoc, rewriteNpmDoc)
}

// rewriteNpmDoc checks that b is JSON and replaces the registry.npmjs.org prefix of every
// dist.tarball by BaseToken. A tarball on another host stays as it is (logged): the mirror cannot
// serve it.
func rewriteNpmDoc(url string, b []byte) ([]byte, error) {
	if !json.Valid(b) {
		return nil, fmt.Errorf("regmirror: %s: not a JSON document", url)
	}
	out := tarballURL.ReplaceAll(b, []byte("${1}"+BaseToken+"/"))
	if n, k := strings.Count(string(out), `"tarball"`), strings.Count(string(out), `"tarball":"`+BaseToken+"/"); n != k {
		log.Printf("regmirror: %s: %d of %d dist.tarball URLs are not on registry.npmjs.org; the mirror cannot serve them", url, n-k, n)
	}
	return out, nil
}

// recordOpam fetches a missing opam-repository snapshot or opam archive cache entry (RecordOpam).
func (m *mirror) recordOpam(r *http.Request, rest, file string) error {
	var url string
	if c, ok := strings.CutPrefix(rest, "repository/"); ok {
		url = opamRepoUpstreamURL + c // <commit>.tar.gz
	} else {
		url = opamCacheUpstreamURL + strings.TrimPrefix(rest, "cache/")
	}
	return m.fetch(r, url, "", file, maxBlob, nil)
}

// fetch downloads url into file unless file exists. An upstream 404/403/410 leaves the file
// missing (the request then gets 404). transform, if set, rewrites the body before it is kept.
func (m *mirror) fetch(r *http.Request, url, accept, file string, limit int64, transform func(string, []byte) ([]byte, error)) error {
	mu, _ := fileLocks.LoadOrStore(file, &sync.Mutex{})
	mu.(*sync.Mutex).Lock()
	defer mu.(*sync.Mutex).Unlock()
	if _, err := os.Stat(file); err == nil {
		return nil
	}
	c := m.c.Client
	if c == nil {
		c = &http.Client{Timeout: 10 * time.Minute}
	}
	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	resp, err := c.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	switch {
	case resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusGone:
		return nil
	case resp.StatusCode != http.StatusOK:
		return fmt.Errorf("regmirror: %s: %s", url, resp.Status)
	}
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(file), ".rec-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	var n int64
	if transform == nil {
		n, err = io.Copy(tmp, io.LimitReader(resp.Body, limit+1))
	} else {
		var b []byte
		if b, err = io.ReadAll(io.LimitReader(resp.Body, limit+1)); err == nil {
			n = int64(len(b))
			if n <= limit {
				if b, err = transform(url, b); err == nil {
					_, err = tmp.Write(b)
				}
			}
		}
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	if n > limit {
		return fmt.Errorf("regmirror: %s is larger than %d bytes", url, limit)
	}
	if err := os.Chmod(tmp.Name(), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), file)
}

// Index is tests/fixtures/registry/index.json (README there).
type Index struct {
	V        int    `json:"v"`
	Captured string `json:"captured"`
	Npm      struct {
		Snapshot string `json:"snapshot,omitempty"`
	} `json:"npm"`
	Opam struct {
		Commit string `json:"commit,omitempty"`
	} `json:"opam"`
}

var commitHex = regexp.MustCompile(`^[0-9a-f]{40}$`)

// UpdateIndex creates or updates index.json in the manifest directory for a recording run: the
// capture date and the npm snapshot date are set when missing (npm), the opam commit is set when
// opamCommit is not empty. Existing values are kept, so resuming a recording does not move the
// snapshot date.
func UpdateIndex(manifests string, now time.Time, npm bool, opamCommit string) error {
	if opamCommit != "" && !commitHex.MatchString(opamCommit) {
		return fmt.Errorf("regmirror: opam commit %q is not 40 lowercase hex digits", opamCommit)
	}
	p := filepath.Join(manifests, "index.json")
	var idx Index
	if b, err := os.ReadFile(p); err == nil {
		if err := json.Unmarshal(b, &idx); err != nil {
			return fmt.Errorf("regmirror: %s: %w", p, err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	date := now.UTC().Format("2006-01-02")
	idx.V = 1
	if idx.Captured == "" {
		idx.Captured = date
	}
	if npm && idx.Npm.Snapshot == "" {
		idx.Npm.Snapshot = date
	}
	if opamCommit != "" {
		idx.Opam.Commit = opamCommit
	}
	b, err := json.MarshalIndent(idx, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(manifests, 0o755); err != nil {
		return err
	}
	return os.WriteFile(p, append(b, '\n'), 0o644)
}
