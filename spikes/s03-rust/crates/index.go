package crates

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// IndexDep is one dependency line of a crates.io index entry.
type IndexDep struct {
	Name            string   `json:"name"` // name as used in code (rename key)
	Req             string   `json:"req"`
	Features        []string `json:"features"`
	Optional        bool     `json:"optional"`
	DefaultFeatures bool     `json:"default_features"`
	Target          string   `json:"target"` // cfg(...) or target triple, "" = all
	Kind            string   `json:"kind"`   // normal | build | dev
	Registry        string   `json:"registry"`
	Package         string   `json:"package"` // real crate name if renamed
}

// IndexEntry is one published version of a crate (one JSON line in the sparse index).
type IndexEntry struct {
	Name        string              `json:"name"`
	Vers        string              `json:"vers"`
	Deps        []IndexDep          `json:"deps"`
	Cksum       string              `json:"cksum"`
	Features    map[string][]string `json:"features"`
	Features2   map[string][]string `json:"features2"`
	Yanked      bool                `json:"yanked"`
	Links       string              `json:"links"`
	RustVersion string              `json:"rust_version"`

	version Version
}

// AllFeatures merges features and features2 (index v2 format).
func (e *IndexEntry) AllFeatures() map[string][]string {
	if len(e.Features2) == 0 {
		return e.Features
	}
	m := map[string][]string{}
	for k, v := range e.Features {
		m[k] = v
	}
	for k, v := range e.Features2 {
		m[k] = v
	}
	return m
}

// Index is a crates.io sparse-protocol client with an on-disk cache (ETag revalidation).
type Index struct {
	Base    string // https://index.crates.io/
	Dir     string // cache directory
	Offline bool   // only use the cache
	HTTP    *http.Client

	mu      sync.Mutex
	entries map[string][]*IndexEntry
	Fetched int // HTTP requests made (stats)
}

func NewIndex(cacheDir string) *Index {
	return &Index{Base: "https://index.crates.io/", Dir: filepath.Join(cacheDir, "index"),
		HTTP: &http.Client{Timeout: 30 * time.Second}, entries: map[string][]*IndexEntry{}}
}

// IndexPath is the sparse index path for a crate name (Cargo reference, "Index format").
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

func validName(n string) bool {
	if n == "" || len(n) > 64 {
		return false
	}
	for _, r := range n {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
			return false
		}
	}
	return true
}

const maxIndexFile = 32 << 20 // hostile input: cap one index file

// Get returns all versions of a crate (cached in memory for the run).
func (ix *Index) Get(ctx context.Context, name string) ([]*IndexEntry, error) {
	if !validName(name) {
		return nil, fmt.Errorf("invalid crate name %q", name)
	}
	key := strings.ToLower(name)
	ix.mu.Lock()
	if e, ok := ix.entries[key]; ok {
		ix.mu.Unlock()
		return e, nil
	}
	ix.mu.Unlock()
	body, err := ix.fetch(ctx, IndexPath(key))
	if err != nil {
		return nil, fmt.Errorf("crates.io index for %s: %w", name, err)
	}
	var out []*IndexEntry
	sc := bufio.NewScanner(bytes.NewReader(body))
	sc.Buffer(make([]byte, 1<<20), 8<<20)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		var e IndexEntry
		if err := json.Unmarshal(line, &e); err != nil {
			return nil, fmt.Errorf("crates.io index for %s: bad line: %w", name, err)
		}
		v, err := ParseVersion(e.Vers)
		if err != nil {
			continue // skip unparsable versions rather than fail the whole crate
		}
		e.version = v
		out = append(out, &e)
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	ix.mu.Lock()
	ix.entries[key] = out
	ix.mu.Unlock()
	return out, nil
}

func (ix *Index) fetch(ctx context.Context, rel string) ([]byte, error) {
	file := filepath.Join(ix.Dir, rel)
	cached, cerr := os.ReadFile(file)
	if ix.Offline {
		if cerr != nil {
			return nil, fmt.Errorf("not in the offline index cache")
		}
		return cached, nil
	}
	etag, _ := os.ReadFile(file + ".etag")
	req, err := http.NewRequestWithContext(ctx, "GET", ix.Base+rel, nil)
	if err != nil {
		return nil, err
	}
	if cerr == nil && len(etag) > 0 {
		req.Header.Set("If-None-Match", string(etag))
	}
	ix.mu.Lock()
	ix.Fetched++
	ix.mu.Unlock()
	resp, err := ix.HTTP.Do(req)
	if err != nil {
		if cerr == nil {
			return cached, nil
		}
		return nil, err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusNotModified:
		return cached, nil
	case http.StatusOK:
	case http.StatusNotFound, http.StatusGone, 451:
		return nil, fmt.Errorf("no such crate on crates.io")
	default:
		return nil, fmt.Errorf("HTTP %s", resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxIndexFile+1))
	if err != nil {
		return nil, err
	}
	if len(body) > maxIndexFile {
		return nil, fmt.Errorf("index file larger than %d bytes", maxIndexFile)
	}
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err == nil {
		tmp := file + ".tmp"
		if os.WriteFile(tmp, body, 0o644) == nil {
			_ = os.Rename(tmp, file)
			if et := resp.Header.Get("ETag"); et != "" {
				_ = os.WriteFile(file+".etag", []byte(et), 0o644)
			}
		}
	}
	return body, nil
}

// Prefetch loads several crates in parallel (n workers).
func (ix *Index) Prefetch(ctx context.Context, names []string, n int) {
	ch := make(chan string)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for name := range ch {
				_, _ = ix.Get(ctx, name)
			}
		}()
	}
	for _, nm := range names {
		ix.mu.Lock()
		_, have := ix.entries[strings.ToLower(nm)]
		ix.mu.Unlock()
		if !have {
			ch <- nm
		}
	}
	close(ch)
	wg.Wait()
}
