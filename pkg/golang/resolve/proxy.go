package resolve

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode"
)

// Size caps of what the proxy may send (hostile input, AGENTS §5.4): cmd/go's own limits
// for go.mod (16 MiB) and module zips (500 MiB).
const (
	MaxModBytes  = 16 << 20
	MaxZipBytes  = 500 << 20
	maxListBytes = 4 << 20
	maxInfoBytes = 64 << 10
)

// DefaultProxy is the module proxy when none is configured (§03.2.4).
const DefaultProxy = "https://proxy.golang.org"

// ErrNotFound marks a module or version the proxy does not have.
var ErrNotFound = errors.New("not found")

// Proxy reads the GOPROXY protocol from an http(s) server or a file:// directory.
type Proxy struct {
	base   string // without a trailing slash
	dir    string // file:// proxies: the local directory
	cache  string // local GOPROXY directory that keeps every .mod and .zip read ("" = none)
	client *http.Client
}

// WithCache keeps every go.mod and zip read from the proxy in dir, in the GOPROXY layout
// (§03.2.5: bob env points GOPROXY at it), and answers later reads from there: module versions
// are immutable, so a warm cache makes resolution need no network.
func (p *Proxy) WithCache(dir string) *Proxy {
	if p.dir == "" || filepath.Clean(dir) != p.dir {
		p.cache = dir
	}
	return p
}

// NewProxy checks a proxy URL: one http, https or file URL ("direct" and "off" are not
// proxies; lists are not supported in v1).
func NewProxy(raw string) (*Proxy, error) {
	raw = strings.TrimSuffix(strings.TrimSpace(raw), "/")
	if raw == "" || strings.ContainsAny(raw, ",|") || raw == "direct" || raw == "off" {
		return nil, fmt.Errorf("module proxy %q: bob needs exactly one proxy URL (http, https or file)", raw)
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("module proxy %q: %w", raw, err)
	}
	p := &Proxy{base: raw}
	switch u.Scheme {
	case "file":
		if u.Host != "" && u.Host != "localhost" || !filepath.IsAbs(u.Path) {
			return nil, fmt.Errorf("module proxy %q: a file URL names an absolute local directory", raw)
		}
		p.dir = filepath.Clean(u.Path)
	case "http", "https":
		if u.Host == "" {
			return nil, fmt.Errorf("module proxy %q has no host", raw)
		}
		d := &net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}
		p.client = &http.Client{Transport: &http.Transport{
			Proxy: http.ProxyFromEnvironment, DialContext: d.DialContext,
			TLSHandshakeTimeout: 30 * time.Second, ResponseHeaderTimeout: 60 * time.Second,
			MaxIdleConnsPerHost: 16, ForceAttemptHTTP2: true,
		}}
	default:
		return nil, fmt.Errorf("module proxy %q: scheme %q is not supported (http, https or file)", raw, u.Scheme)
	}
	return p, nil
}

// URL is the proxy's URL as written into bob.lock.
func (p *Proxy) URL() string { return p.base }

// Host names the proxy in messages ("proxy.golang.org", or the directory).
func (p *Proxy) Host() string {
	if p.dir != "" {
		return p.dir
	}
	u, err := url.Parse(p.base)
	if err != nil {
		return p.base
	}
	return u.Host
}

// EscapePath is the proxy's case encoding: an upper-case letter becomes "!" + lower case.
func EscapePath(s string) string {
	var b strings.Builder
	for _, r := range s {
		if unicode.IsUpper(r) {
			b.WriteByte('!')
			b.WriteRune(unicode.ToLower(r))
		} else {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// ZipURL is where the proxy serves the zip of a module version.
func (p *Proxy) ZipURL(m Module) string { return p.base + "/" + versionFile(m, ".zip") }

func versionFile(m Module, ext string) string {
	return EscapePath(m.Path) + "/@v/" + EscapePath(m.Version) + ext
}

// Mod returns the go.mod of a module version.
func (p *Proxy) Mod(ctx context.Context, m Module) ([]byte, error) {
	rel := versionFile(m, ".mod")
	if p.cache != "" {
		if b, err := readCapped(filepath.Join(p.cache, filepath.FromSlash(rel)), MaxModBytes); err == nil {
			return b, nil
		}
	}
	b, err := p.get(ctx, rel, MaxModBytes)
	if err != nil || p.cache == "" {
		return b, err
	}
	// A cache that cannot be written only costs a later download.
	_ = p.store(m, ".mod", func(f *os.File) error { _, err := f.Write(b); return err })
	return b, nil
}

// ZipFile returns a local file holding the zip of a module version: the proxy's own file
// (file://), the cached copy, or a download into tmp (temp reports that the caller removes it).
func (p *Proxy) ZipFile(ctx context.Context, m Module, tmp string) (file string, temp bool, err error) {
	rel := versionFile(m, ".zip")
	if p.cache != "" {
		file = filepath.Join(p.cache, filepath.FromSlash(rel))
		if _, err := os.Stat(file); err == nil {
			return file, false, nil
		}
		err := p.store(m, ".zip", func(f *os.File) error { return p.stream(ctx, rel, MaxZipBytes, f) })
		if err == nil {
			return file, false, nil
		}
		if errors.Is(err, ErrNotFound) || ctx.Err() != nil {
			return "", false, err
		}
		// The cache is not writable: read the proxy directly.
	}
	if p.dir != "" {
		file = filepath.Join(p.dir, filepath.FromSlash(rel))
		if _, err := os.Stat(file); err != nil {
			return "", false, fmt.Errorf("%s: %w", rel, ErrNotFound)
		}
		return file, false, nil
	}
	f, err := os.CreateTemp(tmp, "gozip-*.zip")
	if err != nil {
		return "", false, err
	}
	err = p.stream(ctx, rel, MaxZipBytes, f)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(f.Name())
		return "", false, err
	}
	return f.Name(), true, nil
}

// store writes one version file into the cache (temp file + rename, so readers never see a
// partial file) and keeps the GOPROXY layout complete: .info and the version list.
func (p *Proxy) store(m Module, ext string, write func(*os.File) error) error {
	dir := filepath.Join(p.cache, filepath.FromSlash(EscapePath(m.Path)), "@v")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return err
	}
	err = write(f)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(f.Name(), filepath.Join(dir, EscapePath(m.Version)+ext))
	}
	if err != nil {
		os.Remove(f.Name())
		return err
	}
	return WriteVersionInfo(p.cache, m)
}

// WriteVersionInfo adds a version to a GOPROXY directory: <v>.info and the list file.
func WriteVersionInfo(root string, m Module) error {
	dir := filepath.Join(root, filepath.FromSlash(EscapePath(m.Path)), "@v")
	info := filepath.Join(dir, EscapePath(m.Version)+".info")
	if _, err := os.Stat(info); err != nil {
		b, _ := json.Marshal(struct{ Version string }{m.Version})
		if err := writeAtomic(info, b); err != nil {
			return err
		}
	}
	list := filepath.Join(dir, "list")
	old, _ := readCapped(list, maxListBytes)
	vs := strings.Fields(string(old))
	for _, v := range vs {
		if v == m.Version {
			return nil
		}
	}
	vs = append(vs, m.Version)
	sort.Slice(vs, func(i, j int) bool { return Compare(vs[i], vs[j]) < 0 })
	return writeAtomic(list, []byte(strings.Join(vs, "\n")+"\n"))
}

func writeAtomic(path string, b []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	_, err = f.Write(b)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Chmod(f.Name(), 0o644)
	}
	if err == nil {
		err = os.Rename(f.Name(), path)
	}
	if err != nil {
		os.Remove(f.Name())
	}
	return err
}

func readCapped(path string, limit int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var b strings.Builder
	if err := copyCapped(&b, f, limit, path); err != nil {
		return nil, err
	}
	return []byte(b.String()), nil
}

// Latest returns the highest release version of a module (pre-releases only when there is
// no release), from @v/list or, when the list is empty, @latest.
func (p *Proxy) Latest(ctx context.Context, path string) (string, error) {
	b, err := p.get(ctx, EscapePath(path)+"/@v/list", maxListBytes)
	if err != nil && !errors.Is(err, ErrNotFound) {
		return "", err
	}
	best, bestPre := "", ""
	for _, v := range strings.Fields(string(b)) {
		switch {
		case !ValidVersion(v):
		case IsPrerelease(v):
			if Compare(v, bestPre) > 0 {
				bestPre = v
			}
		case Compare(v, best) > 0:
			best = v
		}
	}
	if best == "" {
		best = bestPre
	}
	if best != "" {
		return best, nil
	}
	info, err := p.get(ctx, EscapePath(path)+"/@latest", maxInfoBytes)
	if err != nil {
		return "", err
	}
	var v struct{ Version string }
	if err := json.Unmarshal(info, &v); err != nil || !ValidVersion(v.Version) {
		return "", fmt.Errorf("%s/@latest: invalid answer from %s", path, p.Host())
	}
	return v.Version, nil
}

func (p *Proxy) get(ctx context.Context, rel string, limit int64) ([]byte, error) {
	var b strings.Builder
	if err := p.stream(ctx, rel, limit, &b); err != nil {
		return nil, err
	}
	return []byte(b.String()), nil
}

// stream copies one proxy file to w, at most limit bytes; http(s) requests are retried on
// network errors and server errors (not after bytes were written).
func (p *Proxy) stream(ctx context.Context, rel string, limit int64, w io.Writer) error {
	if p.dir != "" {
		f, err := os.Open(filepath.Join(p.dir, filepath.FromSlash(rel)))
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("%s: %w", rel, ErrNotFound)
		}
		if err != nil {
			return err
		}
		defer f.Close()
		return copyCapped(w, f, limit, rel)
	}
	var last error
	for attempt := 0; attempt < 4; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Duration(attempt*attempt) * 500 * time.Millisecond):
			}
		}
		retry, wrote, err := p.httpGet(ctx, rel, limit, w)
		if err == nil || !retry || wrote {
			return err
		}
		last = err
	}
	return last
}

func (p *Proxy) httpGet(ctx context.Context, rel string, limit int64, w io.Writer) (retry, wrote bool, err error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.base+"/"+rel, nil)
	if err != nil {
		return false, false, err
	}
	req.Header.Set("User-Agent", "bob")
	resp, err := p.client.Do(req)
	if err != nil {
		return ctx.Err() == nil, false, fmt.Errorf("%s: %w", p.base+"/"+rel, err)
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusOK:
	case resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusGone:
		// The proxy protocol's "not found" may carry an explanation in the body.
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return false, false, fmt.Errorf("%s: %w%s", rel, ErrNotFound, bodyNote(msg))
	default:
		r := resp.StatusCode >= 500 || resp.StatusCode == http.StatusTooManyRequests
		return r, false, fmt.Errorf("%s: HTTP %d", p.base+"/"+rel, resp.StatusCode)
	}
	if resp.ContentLength > limit {
		return false, false, fmt.Errorf("%s is larger than the limit of %d bytes", rel, limit)
	}
	cw := &countWriter{w: w}
	if err := copyCapped(cw, resp.Body, limit, rel); err != nil {
		return cw.n == 0 && ctx.Err() == nil, cw.n > 0, err
	}
	return false, false, nil
}

func bodyNote(b []byte) string {
	s := strings.TrimSpace(strings.ToValidUTF8(string(b), ""))
	if s == "" || strings.ContainsAny(s, "\x1b") {
		return ""
	}
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return " (" + s + ")"
}

type countWriter struct {
	w io.Writer
	n int64
}

func (c *countWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.n += int64(n)
	return n, err
}

func copyCapped(w io.Writer, r io.Reader, limit int64, what string) error {
	n, err := io.Copy(w, io.LimitReader(r, limit+1))
	if err != nil {
		return fmt.Errorf("%s: %w", what, err)
	}
	if n > limit {
		return fmt.Errorf("%s is larger than the limit of %d bytes", what, limit)
	}
	return nil
}
