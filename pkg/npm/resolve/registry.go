package resolve

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// DefaultRegistry is the public npm registry.
const DefaultRegistry = "https://registry.npmjs.org/"

// MaxDocBytes caps one registry document (SPEC §03.4.2: 128 MB). Documents are hostile input.
const MaxDocBytes = 128 << 20

const (
	acceptAbbreviated = "application/vnd.npm.install-v1+json; q=1.0, application/json; q=0.8, */*"
	acceptFull        = "application/json"
)

// ErrNotFound reports a package or version the registry does not have.
var ErrNotFound = errors.New("not found in the registry")

// Registry reads package metadata from an npm registry: the abbreviated packument per
// package, and the per-version document for packages with os/cpu (only it carries libc).
// Documents are cached on disk with their ETag and revalidated (If-None-Match). A Registry is
// safe for concurrent use.
type Registry struct {
	URL      string        // base URL ending in "/"; default DefaultRegistry
	Client   *http.Client  // default: 2 min timeout
	CacheDir string        // ETag cache ($XDG_CACHE_HOME/bob/npm-meta); "" disables it
	MaxAge   time.Duration // cached documents younger than this are used without a request
	MaxBytes int64         // per document; default MaxDocBytes
	Parallel int           // concurrent requests; default 16

	once  sync.Once
	sem   chan struct{}
	mu    sync.Mutex
	calls map[string]*docCall
}

type docCall struct {
	done chan struct{}
	p    *packument
	m    *manifest
	err  error
}

func (r *Registry) init() {
	r.once.Do(func() {
		if r.URL == "" {
			r.URL = DefaultRegistry
		}
		if !strings.HasSuffix(r.URL, "/") {
			r.URL += "/"
		}
		if r.Client == nil {
			r.Client = &http.Client{Timeout: 2 * time.Minute}
		}
		if r.MaxBytes <= 0 {
			r.MaxBytes = MaxDocBytes
		}
		if r.Parallel <= 0 {
			r.Parallel = 16
		}
		r.sem = make(chan struct{}, r.Parallel)
		r.calls = map[string]*docCall{}
	})
}

// base returns the normalised registry URL.
func (r *Registry) base() string {
	r.init()
	return r.URL
}

// escapedName is npm's escaped package name for registry URLs ("@scope%2fname").
func escapedName(name string) string { return strings.Replace(name, "/", "%2f", 1) }

// DefaultTarball is the tarball URL the registry uses by convention; bob.lock records a
// tarball only when it differs.
func (r *Registry) DefaultTarball(name, version string) string {
	return r.base() + name + "/-/" + name[strings.LastIndex(name, "/")+1:] + "-" + version + ".tgz"
}

// packument returns the abbreviated packument of name. Concurrent calls share one request.
func (r *Registry) packument(ctx context.Context, name string) (*packument, error) {
	c := r.call(ctx, "p\x00"+name, func() *docCall {
		var p packument
		if err := r.getJSON(ctx, r.base()+escapedName(name), acceptAbbreviated, name, &p); err != nil {
			return &docCall{err: err}
		}
		if err := p.prepare(name); err != nil {
			return &docCall{err: err}
		}
		return &docCall{p: &p}
	})
	return c.p, c.err
}

// versionDoc returns the per-version document of name@version.
func (r *Registry) versionDoc(ctx context.Context, name, version string) (*manifest, error) {
	c := r.call(ctx, "v\x00"+name+"\x00"+version, func() *docCall {
		var m manifest
		u := r.base() + escapedName(name) + "/" + url.PathEscape(version)
		if err := r.getJSON(ctx, u, acceptFull, name+"@"+version, &m); err != nil {
			return &docCall{err: err}
		}
		return &docCall{m: &m}
	})
	return c.m, c.err
}

// prefetch starts fetching the packuments of names in the background.
func (r *Registry) prefetch(ctx context.Context, names []string) {
	for _, n := range names {
		go func() {
			defer func() { _ = recover() }() // a prefetch never takes the process down; the real call reports
			_, _ = r.packument(ctx, n)
		}()
	}
}

func (r *Registry) call(ctx context.Context, key string, f func() *docCall) *docCall {
	r.init()
	for {
		r.mu.Lock()
		c, ok := r.calls[key]
		if !ok {
			c = &docCall{done: make(chan struct{})}
			r.calls[key] = c
			r.mu.Unlock()
			r.run(ctx, key, c, f)
			return c
		}
		r.mu.Unlock()
		select {
		case <-c.done:
		case <-ctx.Done():
			return &docCall{err: ctx.Err()}
		}
		// A call started under another, now cancelled context is not our result: retry.
		if c.err != nil && (errors.Is(c.err, context.Canceled) || errors.Is(c.err, context.DeadlineExceeded)) && ctx.Err() == nil {
			continue
		}
		return c
	}
}

func (r *Registry) run(ctx context.Context, key string, c *docCall, f func() *docCall) {
	res := func() (res *docCall) {
		defer func() {
			if p := recover(); p != nil {
				res = &docCall{err: fmt.Errorf("registry: internal error: %v", p)}
			}
		}()
		select {
		case r.sem <- struct{}{}:
		case <-ctx.Done():
			return &docCall{err: ctx.Err()}
		}
		defer func() { <-r.sem }()
		return f()
	}()
	c.p, c.m, c.err = res.p, res.m, res.err
	if c.err != nil && ctx.Err() != nil {
		// do not keep a cancellation for later callers with a live context
		r.mu.Lock()
		if r.calls[key] == c {
			delete(r.calls, key)
		}
		r.mu.Unlock()
	}
	close(c.done)
}

func (r *Registry) cachePath(u, accept string) string {
	if r.CacheDir == "" {
		return ""
	}
	h := sha256.Sum256([]byte(accept + "\x00" + u))
	return filepath.Join(r.CacheDir, hex.EncodeToString(h[:16]))
}

// getJSON fetches u (with the ETag cache) and decodes it into v. what names the document in
// errors.
func (r *Registry) getJSON(ctx context.Context, u, accept, what string, v any) error {
	body, err := r.get(ctx, u, accept, what)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(body, v); err != nil {
		return fmt.Errorf("registry document for %s is not valid JSON: %w", what, err)
	}
	return nil
}

func (r *Registry) readCapped(rd io.Reader, what string) ([]byte, error) {
	b, err := io.ReadAll(io.LimitReader(rd, r.MaxBytes+1))
	if err != nil {
		return nil, fmt.Errorf("registry document for %s: %w", what, err)
	}
	if int64(len(b)) > r.MaxBytes {
		return nil, fmt.Errorf("registry document for %s is larger than %d bytes", what, r.MaxBytes)
	}
	return b, nil
}

func (r *Registry) get(ctx context.Context, u, accept, what string) ([]byte, error) {
	cp := r.cachePath(u, accept)
	var cached []byte
	var etag string
	if cp != "" {
		if fi, err := os.Stat(cp + ".json"); err == nil && fi.Size() <= r.MaxBytes {
			if b, err := os.ReadFile(cp + ".json"); err == nil {
				cached = b
				if r.MaxAge > 0 && time.Since(fi.ModTime()) < r.MaxAge {
					return cached, nil
				}
				if e, err := os.ReadFile(cp + ".etag"); err == nil && len(e) < 1024 {
					etag = string(e)
				}
			}
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, fmt.Errorf("registry request for %s: %w", what, err)
	}
	req.Header.Set("Accept", accept)
	if etag != "" {
		req.Header.Set("If-None-Match", etag)
	}
	resp, err := r.Client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("registry request for %s: %w", what, err)
	}
	defer func() { _ = resp.Body.Close() }()
	switch {
	case resp.StatusCode == http.StatusNotModified && cached != nil:
		now := time.Now()
		_ = os.Chtimes(cp+".json", now, now)
		return cached, nil
	case resp.StatusCode == http.StatusNotFound:
		return nil, fmt.Errorf("%s: %w", what, ErrNotFound)
	case resp.StatusCode != http.StatusOK:
		return nil, fmt.Errorf("registry answered HTTP %d for %s", resp.StatusCode, what)
	}
	if resp.ContentLength > r.MaxBytes {
		return nil, fmt.Errorf("registry document for %s is larger than %d bytes", what, r.MaxBytes)
	}
	body, err := r.readCapped(resp.Body, what)
	if err != nil {
		return nil, err
	}
	if cp != "" {
		writeCache(cp, body, resp.Header.Get("ETag"))
	}
	return body, nil
}

// writeCache stores a document atomically; failures only cost a later refetch.
func writeCache(cp string, body []byte, etag string) {
	if err := os.MkdirAll(filepath.Dir(cp), 0o755); err != nil {
		return
	}
	tmp, err := os.CreateTemp(filepath.Dir(cp), ".npm-meta-*")
	if err != nil {
		return
	}
	_, werr := tmp.Write(body)
	cerr := tmp.Close()
	if werr != nil || cerr != nil || os.Rename(tmp.Name(), cp+".json") != nil {
		_ = os.Remove(tmp.Name())
		return
	}
	if etag != "" && len(etag) < 1024 {
		_ = os.WriteFile(cp+".etag", []byte(etag), 0o644)
	} else {
		_ = os.Remove(cp + ".etag")
	}
}
