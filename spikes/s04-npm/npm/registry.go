package npm

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

// Manifest is the subset of a registry version document the resolver uses.
type Manifest struct {
	Name                 string                     `json:"name"`
	Version              string                     `json:"version"`
	Dependencies         map[string]string          `json:"dependencies"`
	OptionalDependencies map[string]string          `json:"optionalDependencies"`
	PeerDependencies     map[string]string          `json:"peerDependencies"`
	PeerDependenciesMeta map[string]struct{ Optional bool } `json:"peerDependenciesMeta"`
	BundleDependencies   json.RawMessage            `json:"bundleDependencies"`
	BundledDependencies  json.RawMessage            `json:"bundledDependencies"`
	OS                   []string                   `json:"os"`
	CPU                  []string                   `json:"cpu"`
	Libc                 []string                   `json:"libc"`
	HasInstallScript     bool                       `json:"hasInstallScript"`
	Deprecated           json.RawMessage            `json:"deprecated"`
	Bin                  json.RawMessage            `json:"bin"`
	Engines              json.RawMessage            `json:"engines"`
	Dist                 struct {
		Tarball   string `json:"tarball"`
		Integrity string `json:"integrity"`
		Shasum    string `json:"shasum"`
	} `json:"dist"`
}

// Bundled returns bundleDependencies as a list (true = all dependencies).
func (m *Manifest) Bundled() []string {
	raw := m.BundleDependencies
	if len(raw) == 0 {
		raw = m.BundledDependencies
	}
	var l []string
	if json.Unmarshal(raw, &l) == nil {
		return l
	}
	var all bool
	if json.Unmarshal(raw, &all) == nil && all {
		for k := range m.Dependencies {
			l = append(l, k)
		}
	}
	return l
}

func (m *Manifest) IsDeprecated() bool {
	return len(m.Deprecated) > 0 && string(m.Deprecated) != "false" && string(m.Deprecated) != `""`
}

type Packument struct {
	Name     string               `json:"name"`
	DistTags map[string]string    `json:"dist-tags"`
	Versions map[string]*Manifest `json:"versions"`
	parsed   []Version
}

type Registry struct {
	Base     string
	CacheDir string
	Offline  bool // use only the disk cache
	MaxBytes int64
	Client   *http.Client

	mu    sync.Mutex
	docs  map[string]*Packument
	calls map[string]*call
	Stats struct{ Fetched, Cached, Bytes int64 }
}

type call struct {
	done chan struct{}
	p    *Packument
	err  error
}

func NewRegistry(cacheDir string) *Registry {
	return &Registry{Base: "https://registry.npmjs.org", CacheDir: cacheDir, MaxBytes: 128 << 20,
		Client: &http.Client{Timeout: 120 * time.Second}, docs: map[string]*Packument{}, calls: map[string]*call{}}
}

var ErrNotFound = errors.New("package not found in registry")

func validName(n string) bool {
	if n == "" || len(n) > 214 || strings.Contains(n, "..") {
		return false
	}
	for _, r := range n {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || strings.ContainsRune("-._~@/", r) || r >= 'A' && r <= 'Z') {
			return false
		}
	}
	return true
}

// Get returns the abbreviated packument (cached on disk with ETag revalidation once per process).
func (r *Registry) Get(ctx context.Context, name string) (*Packument, error) {
	r.mu.Lock()
	if p := r.docs[name]; p != nil {
		r.mu.Unlock()
		return p, nil
	}
	if c := r.calls[name]; c != nil {
		r.mu.Unlock()
		<-c.done
		return c.p, c.err
	}
	c := &call{done: make(chan struct{})}
	r.calls[name] = c
	r.mu.Unlock()
	c.p, c.err = r.fetch(ctx, name, "", "application/vnd.npm.install-v1+json")
	r.mu.Lock()
	if c.err == nil {
		r.docs[name] = c.p
	}
	delete(r.calls, name)
	r.mu.Unlock()
	close(c.done)
	return c.p, c.err
}

// Full returns the full version document (needed only for libc, which the abbreviated form lacks).
func (r *Registry) Full(ctx context.Context, name, version string) (*Manifest, error) {
	p, err := r.fetch(ctx, name, version, "application/json")
	if err != nil {
		return nil, err
	}
	return p.Versions[version], nil
}

func (r *Registry) fetch(ctx context.Context, name, version, accept string) (*Packument, error) {
	if !validName(name) {
		return nil, fmt.Errorf("invalid package name %q", name)
	}
	key := name + "@" + version + "|" + accept
	h := sha256.Sum256([]byte(key))
	cf := filepath.Join(r.CacheDir, hex.EncodeToString(h[:8])+".json")
	ef := cf + ".etag"
	var body []byte
	cached, cerr := os.ReadFile(cf)
	if r.Offline {
		if cerr != nil {
			return nil, fmt.Errorf("%s: not in offline cache", name)
		}
		body = cached
	} else {
		u := r.Base + "/" + strings.Replace(url.PathEscape(name), "%40", "@", 1)
		if version != "" {
			u += "/" + url.PathEscape(version)
		}
		req, _ := http.NewRequestWithContext(ctx, "GET", u, nil)
		req.Header.Set("Accept", accept)
		if et, err := os.ReadFile(ef); err == nil && cerr == nil {
			req.Header.Set("If-None-Match", string(et))
		}
		resp, err := r.Client.Do(req)
		if err != nil {
			return nil, fmt.Errorf("registry: %w", err)
		}
		defer resp.Body.Close()
		switch resp.StatusCode {
		case 304:
			body = cached
			r.mu.Lock()
			r.Stats.Cached++
			r.mu.Unlock()
		case 200:
			body, err = io.ReadAll(io.LimitReader(resp.Body, r.MaxBytes+1))
			if err != nil {
				return nil, err
			}
			if int64(len(body)) > r.MaxBytes {
				return nil, fmt.Errorf("registry document for %s exceeds %d bytes", name, r.MaxBytes)
			}
			_ = os.MkdirAll(r.CacheDir, 0o755)
			_ = os.WriteFile(cf, body, 0o644)
			if et := resp.Header.Get("ETag"); et != "" {
				_ = os.WriteFile(ef, []byte(et), 0o644)
			}
			r.mu.Lock()
			r.Stats.Fetched++
			r.Stats.Bytes += int64(len(body))
			r.mu.Unlock()
		case 404:
			return nil, fmt.Errorf("%s: %w", name, ErrNotFound)
		default:
			return nil, fmt.Errorf("registry %s: HTTP %d", name, resp.StatusCode)
		}
	}
	if version != "" {
		var m Manifest
		if err := json.Unmarshal(body, &m); err != nil {
			return nil, fmt.Errorf("registry %s@%s: %w", name, version, err)
		}
		return &Packument{Name: name, Versions: map[string]*Manifest{version: &m}}, nil
	}
	var p Packument
	if err := json.Unmarshal(body, &p); err != nil {
		return nil, fmt.Errorf("registry %s: %w", name, err)
	}
	for v, m := range p.Versions {
		if m == nil || m.Version != v {
			delete(p.Versions, v)
			continue
		}
		if pv, err := ParseVersion(v); err == nil {
			p.parsed = append(p.parsed, pv)
		}
	}
	return &p, nil
}
