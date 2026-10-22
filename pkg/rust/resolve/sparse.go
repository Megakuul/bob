package resolve

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// CratesIO is the crates.io sparse index.
const CratesIO = "https://index.crates.io/"

// Index is a sparse-protocol registry client (Cargo reference, "Registry index") with an
// on-disk cache revalidated by ETag / Last-Modified. Safe for concurrent use.
type Index struct {
	URL      string       // index base URL with trailing slash (CratesIO)
	CacheDir string       // e.g. $XDG_CACHE_HOME/bob/crates-index; "" = no disk cache
	Client   *http.Client // nil = a client with a 60 s timeout
	Offline  bool         // answer from the cache only
	Parallel int          // concurrent requests of Prefetch; 0 = 16

	mu    sync.Mutex
	files map[string]*indexCall
	reqs  int
}

type indexCall struct {
	done    chan struct{}
	entries []*Entry
	err     error
}

// NewIndex returns a client of the index at url caching in dir.
func NewIndex(url, dir string) *Index {
	if !strings.HasSuffix(url, "/") {
		url += "/"
	}
	return &Index{URL: url, CacheDir: dir}
}

// Requests is the number of HTTP requests made so far.
func (ix *Index) Requests() int {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	return ix.reqs
}

// Versions implements Registry: every version of the crate (fetched once per Index).
func (ix *Index) Versions(ctx context.Context, name string) ([]*Entry, error) {
	if !ValidName(name) {
		return nil, fmt.Errorf("invalid crate name %q", name)
	}
	key := strings.ToLower(name)
	ix.mu.Lock()
	if ix.files == nil {
		ix.files = map[string]*indexCall{}
	}
	c, ok := ix.files[key]
	if !ok {
		c = &indexCall{done: make(chan struct{})}
		ix.files[key] = c
	}
	ix.mu.Unlock()
	if !ok {
		func() {
			defer close(c.done)
			defer func() {
				if p := recover(); p != nil {
					c.err = fmt.Errorf("crates index for %s: internal error: %v", name, p)
				}
			}()
			c.entries, c.err = ix.load(ctx, key)
		}()
	}
	select {
	case <-c.done:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return c.entries, c.err
}

// Prefetch implements Prefetcher: loads names with bounded parallelism.
func (ix *Index) Prefetch(ctx context.Context, names []string) {
	n := ix.Parallel
	if n <= 0 {
		n = 16
	}
	ch := make(chan string)
	var wg sync.WaitGroup
	for i := 0; i < n && i < len(names); i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { _ = recover() }() // Versions records its own errors; a panic must not escape
			for name := range ch {
				_, _ = ix.Versions(ctx, name)
			}
		}()
	}
	for _, name := range names {
		select {
		case ch <- name:
		case <-ctx.Done():
		}
	}
	close(ch)
	wg.Wait()
}

func (ix *Index) load(ctx context.Context, key string) ([]*Entry, error) {
	body, err := ix.fetch(ctx, IndexPath(key))
	if err != nil {
		return nil, fmt.Errorf("crates index for %s: %w", key, err)
	}
	return ParseIndexFile(key, body)
}

func (ix *Index) cachePath(rel string) string {
	if ix.CacheDir == "" {
		return ""
	}
	return filepath.Join(ix.CacheDir, filepath.FromSlash(rel))
}

func (ix *Index) fetch(ctx context.Context, rel string) ([]byte, error) {
	file := ix.cachePath(rel)
	var cached []byte
	var cerr error = os.ErrNotExist
	if file != "" {
		cached, cerr = readCapped(file)
	}
	if ix.Offline {
		if cerr != nil {
			return nil, fmt.Errorf("not in the offline index cache: %w", cerr)
		}
		return cached, nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, ix.URL+rel, nil)
	if err != nil {
		return nil, err
	}
	if cerr == nil {
		if et, err := os.ReadFile(file + ".etag"); err == nil && len(et) > 0 && len(et) < 1024 {
			req.Header.Set("If-None-Match", string(et))
		} else if lm, err := os.ReadFile(file + ".lm"); err == nil && len(lm) > 0 && len(lm) < 1024 {
			req.Header.Set("If-Modified-Since", string(lm))
		}
	}
	ix.mu.Lock()
	ix.reqs++
	ix.mu.Unlock()
	client := ix.Client
	if client == nil {
		client = &http.Client{Timeout: 60 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusNotModified:
		if cerr != nil {
			return nil, fmt.Errorf("index answered 304 without a cached copy")
		}
		return cached, nil
	case http.StatusNotFound, http.StatusGone, http.StatusForbidden, http.StatusUnavailableForLegalReasons:
		return nil, ErrNoCrate
	default:
		return nil, fmt.Errorf("index answered %s", resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, MaxIndexFile+1))
	if err != nil {
		return nil, err
	}
	if len(body) > MaxIndexFile {
		return nil, fmt.Errorf("index file larger than %d bytes", MaxIndexFile)
	}
	if file != "" {
		ix.store(file, body, resp.Header.Get("ETag"), resp.Header.Get("Last-Modified"))
	}
	return body, nil
}

// store writes the cache atomically; a failure only costs a refetch next time.
func (ix *Index) store(file string, body []byte, etag, lastMod string) {
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		return
	}
	_ = os.Remove(file + ".etag")
	_ = os.Remove(file + ".lm")
	tmp, err := os.CreateTemp(filepath.Dir(file), ".tmp-*")
	if err != nil {
		return
	}
	_, werr := tmp.Write(body)
	cerr := tmp.Close()
	if werr != nil || cerr != nil || os.Rename(tmp.Name(), file) != nil {
		_ = os.Remove(tmp.Name())
		return
	}
	if etag != "" {
		_ = os.WriteFile(file+".etag", []byte(etag), 0o644)
	} else if lastMod != "" {
		_ = os.WriteFile(file+".lm", []byte(lastMod), 0o644)
	}
}

func readCapped(file string) ([]byte, error) {
	f, err := os.Open(file)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	b, err := io.ReadAll(io.LimitReader(f, MaxIndexFile+1))
	if err != nil {
		return nil, err
	}
	if len(b) > MaxIndexFile {
		return nil, errors.New("cached index file is too large")
	}
	return b, nil
}
