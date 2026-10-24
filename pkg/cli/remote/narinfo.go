package remote

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Bounds for hostile caches (AGENTS.md §5.4).
const (
	maxNarinfo    = 1 << 20
	maxReferences = 10000
	maxClosure    = 20000   // paths of one closure
	maxNARFile    = 1 << 36 // 64 GiB: a NAR without FileSize is cut off here
)

// CacheError is a failed download from a binary cache. Kind says what the CLI reports:
// "unauthenticated" (401), "denied" (403), "missing" (a root the cache does not have),
// "untrusted" (no signature by a pinned key), "corrupt" (hash or size mismatch, malformed
// narinfo) and "unreachable" (no answer, 5xx).
type CacheError struct {
	Kind   string
	Cache  string
	Path   string // the store path concerned
	Status int
	Err    error
}

func (e *CacheError) Error() string {
	s := fmt.Sprintf("cache %s: %s: %s", e.Cache, e.Path, e.Kind)
	if e.Status != 0 {
		s += fmt.Sprintf(" (HTTP %d)", e.Status)
	}
	if e.Err != nil {
		s += ": " + e.Err.Error()
	}
	return s
}

func (e *CacheError) Unwrap() error { return e.Err }

// Cache is a bob server's /nix binary cache as bob install reads it: every request carries
// the bearer token when there is one, and every narinfo must be signed by one of Keys (the
// registry's pinned keys, R-INST-004). bob downloads the closure itself and hands it to Nix
// as a local binary cache, so Nix never needs the server's credentials.
type Cache struct {
	URL       string // substituter URL without a trailing slash
	Token     string
	Keys      []string // "name:base64(ed25519 public key)"
	UserAgent string
	HTTP      *http.Client // nil: a client with a 10 min timeout
}

// Narinfo is a parsed .narinfo file.
type Narinfo struct {
	StorePath   string
	URL         string
	Compression string
	FileHash    string
	FileSize    int64
	NarHash     string // normalised: sha256:<nix32>
	NarSize     int64
	References  []string // full store paths
	Sigs        []string
	Raw         []byte
}

// ParseNarinfo parses a narinfo; it checks the shape of every field it uses.
func ParseNarinfo(b []byte) (*Narinfo, error) {
	n := &Narinfo{Raw: b}
	sc := bufio.NewScanner(bytes.NewReader(b))
	sc.Buffer(make([]byte, 0, 64<<10), maxNarinfo)
	for sc.Scan() {
		k, v, ok := strings.Cut(sc.Text(), ": ")
		if !ok {
			continue
		}
		var err error
		switch k {
		case "StorePath":
			n.StorePath = v
		case "URL":
			n.URL = v
		case "Compression":
			n.Compression = v
		case "FileHash":
			n.FileHash, err = normHash(v)
		case "FileSize":
			n.FileSize, err = strconv.ParseInt(v, 10, 64)
		case "NarHash":
			n.NarHash, err = normHash(v)
		case "NarSize":
			n.NarSize, err = strconv.ParseInt(v, 10, 64)
		case "References":
			for _, r := range strings.Fields(v) {
				p := "/nix/store/" + r
				if !IsStorePath(p) {
					return nil, fmt.Errorf("narinfo: invalid reference %q", r)
				}
				n.References = append(n.References, p)
			}
			if len(n.References) > maxReferences {
				return nil, errors.New("narinfo: too many references")
			}
		case "Sig":
			n.Sigs = append(n.Sigs, v)
		}
		if err != nil {
			return nil, fmt.Errorf("narinfo: %s: %w", k, err)
		}
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("narinfo: %w", err)
	}
	switch {
	case !IsStorePath(n.StorePath):
		return nil, fmt.Errorf("narinfo: invalid StorePath %q", n.StorePath)
	case n.NarHash == "" || n.NarSize <= 0:
		return nil, errors.New("narinfo: NarHash or NarSize missing")
	case !safeRel(n.URL):
		return nil, fmt.Errorf("narinfo: unsafe URL %q", n.URL)
	case n.FileSize < 0:
		return nil, errors.New("narinfo: negative FileSize")
	}
	return n, nil
}

// Fingerprint is the signed text of a narinfo (Nix: "1;path;narHash;narSize;refs").
func (n *Narinfo) Fingerprint() string {
	return "1;" + n.StorePath + ";" + n.NarHash + ";" + strconv.FormatInt(n.NarSize, 10) + ";" + strings.Join(n.References, ",")
}

// Trusted reports whether one of the signatures verifies with one of keys.
func (n *Narinfo) Trusted(keys []string) bool {
	fp := []byte(n.Fingerprint())
	for _, k := range keys {
		kn, kb, ok := strings.Cut(k, ":")
		pub, err := base64.StdEncoding.DecodeString(kb)
		if !ok || err != nil || len(pub) != ed25519.PublicKeySize {
			continue
		}
		for _, s := range n.Sigs {
			sn, sb, ok := strings.Cut(s, ":")
			sig, err := base64.StdEncoding.DecodeString(sb)
			if !ok || sn != kn || err != nil || len(sig) != ed25519.SignatureSize {
				continue
			}
			if ed25519.Verify(ed25519.PublicKey(pub), fp, sig) {
				return true
			}
		}
	}
	return false
}

// Fetch downloads the closure of roots into dir, laid out as a local binary cache (narinfo
// files next to nix-cache-info, NARs at their URL), checking every signature against the
// pinned keys and every file against its hash. Paths for which have reports true are
// skipped with their references (they are valid in the local store already). A reference
// the cache does not have is returned in missing (another substituter must provide it); a
// root it does not have is a CacheError of kind "missing".
func (c *Cache) Fetch(ctx context.Context, roots []string, have func(string) bool, dir string) (missing []string, err error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(dir, "nix-cache-info"), []byte("StoreDir: /nix/store\n"), 0o644); err != nil {
		return nil, err
	}
	isRoot := map[string]bool{}
	seen := map[string]bool{}
	queue := []string{}
	for _, r := range roots {
		if !IsStorePath(r) {
			return nil, &CacheError{Kind: "corrupt", Cache: c.URL, Path: r, Err: errors.New("not a store path")}
		}
		isRoot[r] = true
		if !seen[r] {
			seen[r] = true
			queue = append(queue, r)
		}
	}
	for len(queue) > 0 {
		p := queue[0]
		queue = queue[1:]
		if have != nil && have(p) {
			continue
		}
		ni, err := c.fetchNarinfo(ctx, p)
		var ce *CacheError
		if errors.As(err, &ce) && ce.Kind == "missing" && !isRoot[p] {
			missing = append(missing, p)
			continue
		}
		if err != nil {
			return nil, err
		}
		if err := c.fetchNAR(ctx, ni, dir); err != nil {
			return nil, err
		}
		if err := os.WriteFile(filepath.Join(dir, hashPart(p)+".narinfo"), ni.Raw, 0o644); err != nil {
			return nil, err
		}
		for _, r := range ni.References {
			if !seen[r] {
				if len(seen) >= maxClosure {
					return nil, &CacheError{Kind: "corrupt", Cache: c.URL, Path: p, Err: errors.New("closure too large")}
				}
				seen[r] = true
				queue = append(queue, r)
			}
		}
	}
	return missing, nil
}

func (c *Cache) client() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{Timeout: 10 * time.Minute}
}

// get issues one GET below the cache URL; the caller closes the body of a 200 answer.
func (c *Cache) get(ctx context.Context, rel, path string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.URL+"/"+rel, nil)
	if err != nil {
		return nil, &CacheError{Kind: "unreachable", Cache: c.URL, Path: path, Err: err}
	}
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	if c.UserAgent != "" {
		req.Header.Set("User-Agent", c.UserAgent)
	}
	resp, err := c.client().Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, &CacheError{Kind: "unreachable", Cache: c.URL, Path: path, Err: err}
	}
	if resp.StatusCode == http.StatusOK {
		return resp, nil
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	resp.Body.Close()
	kind := "unreachable"
	switch resp.StatusCode {
	case http.StatusUnauthorized:
		kind = "unauthenticated"
	case http.StatusForbidden:
		kind = "denied"
	case http.StatusNotFound, http.StatusGone:
		kind = "missing"
	}
	return nil, &CacheError{Kind: kind, Cache: c.URL, Path: path, Status: resp.StatusCode}
}

func (c *Cache) fetchNarinfo(ctx context.Context, p string) (*Narinfo, error) {
	resp, err := c.get(ctx, hashPart(p)+".narinfo", p)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxNarinfo+1))
	if err != nil {
		return nil, &CacheError{Kind: "unreachable", Cache: c.URL, Path: p, Err: err}
	}
	if len(b) > maxNarinfo {
		return nil, &CacheError{Kind: "corrupt", Cache: c.URL, Path: p, Err: errors.New("narinfo too large")}
	}
	ni, err := ParseNarinfo(b)
	if err != nil {
		return nil, &CacheError{Kind: "corrupt", Cache: c.URL, Path: p, Err: err}
	}
	if ni.StorePath != p {
		return nil, &CacheError{Kind: "corrupt", Cache: c.URL, Path: p, Err: fmt.Errorf("narinfo describes %s", ni.StorePath)}
	}
	if !ni.Trusted(c.Keys) {
		return nil, &CacheError{Kind: "untrusted", Cache: c.URL, Path: p}
	}
	return ni, nil
}

// fetchNAR downloads the NAR file of ni to dir/<URL> (temp file + rename), checking FileSize
// and FileHash (or NarSize/NarHash when it is uncompressed).
func (c *Cache) fetchNAR(ctx context.Context, ni *Narinfo, dir string) error {
	dst := filepath.Join(dir, filepath.FromSlash(ni.URL))
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	resp, err := c.get(ctx, ni.URL, ni.StorePath)
	if err != nil {
		var ce *CacheError
		if errors.As(err, &ce) && ce.Kind == "missing" {
			ce.Kind = "corrupt" // listed but not delivered
		}
		return err
	}
	defer resp.Body.Close()
	tmp, err := os.CreateTemp(filepath.Dir(dst), ".nar-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	limit := int64(maxNARFile)
	if ni.FileSize > 0 {
		limit = ni.FileSize
	}
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(tmp, h), io.LimitReader(resp.Body, limit+1))
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return &CacheError{Kind: "unreachable", Cache: c.URL, Path: ni.StorePath, Err: err}
	}
	sum := "sha256:" + Nix32(h.Sum(nil))
	bad := n > limit || (ni.FileSize > 0 && n != ni.FileSize) || (ni.FileHash != "" && ni.FileHash != sum)
	if ni.Compression == "none" || ni.Compression == "" {
		bad = bad || n != ni.NarSize || sum != ni.NarHash
	}
	if bad {
		return &CacheError{Kind: "corrupt", Cache: c.URL, Path: ni.StorePath, Err: errors.New("NAR does not match its narinfo")}
	}
	return os.Rename(tmp.Name(), dst)
}

// safeRel accepts a relative slash path without "..", empty or dot segments.
func safeRel(p string) bool {
	if p == "" || len(p) > 512 || strings.HasPrefix(p, "/") {
		return false
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return false
		}
		for _, r := range seg {
			if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("._+-", r)) {
				return false
			}
		}
	}
	return true
}

// normHash brings a sha256 hash in Nix base-32, base-16 or SRI form to "sha256:<nix32>".
func normHash(v string) (string, error) {
	var raw []byte
	switch {
	case strings.HasPrefix(v, "sha256:"):
		h := strings.TrimPrefix(v, "sha256:")
		switch len(h) {
		case 52:
			for _, c := range h {
				if !strings.ContainsRune(nix32Alphabet, c) {
					return "", fmt.Errorf("invalid hash %q", v)
				}
			}
			return v, nil
		case 64:
			b, err := hex.DecodeString(h)
			if err != nil {
				return "", fmt.Errorf("invalid hash %q", v)
			}
			raw = b
		default:
			return "", fmt.Errorf("invalid hash %q", v)
		}
	case strings.HasPrefix(v, "sha256-"):
		b, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(v, "sha256-"))
		if err != nil || len(b) != sha256.Size {
			return "", fmt.Errorf("invalid hash %q", v)
		}
		raw = b
	default:
		return "", fmt.Errorf("unsupported hash %q", v)
	}
	return "sha256:" + Nix32(raw), nil
}

const nix32Alphabet = "0123456789abcdfghijklmnpqrsvwxyz"

// Nix32 is Nix's base-32 encoding of a hash.
func Nix32(h []byte) string {
	n := (len(h)*8-1)/5 + 1
	out := make([]byte, 0, n)
	for i := n - 1; i >= 0; i-- {
		b := i * 5
		j, k := b/8, b%8
		c := uint(h[j]) >> k
		if j+1 < len(h) {
			c |= uint(h[j+1]) << (8 - k)
		}
		out = append(out, nix32Alphabet[c&0x1f])
	}
	return string(out)
}

// IsStorePath checks the shape /nix/store/<32 base-32 chars>-<name>.
func IsStorePath(p string) bool {
	b, ok := strings.CutPrefix(p, "/nix/store/")
	if !ok || len(b) < 34 || len(b) > 211 || b[32] != '-' {
		return false
	}
	for _, c := range b[:32] {
		if !strings.ContainsRune(nix32Alphabet, c) {
			return false
		}
	}
	for _, c := range b[33:] {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune("+-._?=", c)) {
			return false
		}
	}
	return true
}

func hashPart(p string) string { return strings.TrimPrefix(p, "/nix/store/")[:32] }
