package fetch

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"os"
	"path/filepath"
	"strings"

	"github.com/megakuul/bob/pkg/bob/diag"
	"github.com/megakuul/bob/pkg/engine/drv"
	"github.com/megakuul/bob/pkg/engine/nar"
	"github.com/megakuul/bob/pkg/engine/store"
)

// Kind is the shape of a fetched input.
type Kind string

const (
	File    Kind = "file"    // one flat file
	Archive Kind = "archive" // an unpacked tree
)

// Request is one external input (SPEC R-NIX-014).
type Request struct {
	URLs     []string // mirrors in order
	Kind     Kind     // File (flat) or Archive (unpacked tree)
	Hash     string   // SRI of the downloaded bytes (required for File; checked if set for Archive)
	NarHash  string   // SRI of the unpacked tree (required for Archive)
	Name     string   // store name
	Strip    int      // leading path components to drop (Archive)
	MaxBytes int64    // 0 = 2 GiB
	Label    string   // for error messages
}

// Fetcher gets every external input.
type Fetcher interface {
	// Ensure makes the store path valid and returns it: already valid, then substituters
	// (nix-store --realise of the path), then origin URLs. It never runs a Nix FOD.
	Ensure(ctx context.Context, r Request) (storePath string, err error)
	// Resolve downloads without a known NarHash (bob lock) and returns the hashes to record.
	Resolve(ctx context.Context, r Request) (hash, narHash string, err error)
}

// Policy is the egress policy set by the caller (developers: no restriction).
type Policy struct {
	DenyPrivate bool // refuse private, loopback and link-local addresses
}

// defaultMaxBytes is the download size cap when a request sets none.
const defaultMaxBytes = 2 << 30

// storeAPI is what the fetcher needs of the store (an interface so tests can fake it).
type storeAPI interface {
	Home() string
	Invalid(ctx context.Context, paths []string) ([]string, error)
	Import(ctx context.Context, items []store.ExportItem) error
}

// realiser is implemented by a store that can substitute plain store paths from its binary
// caches.
type realiser interface {
	Realise(ctx context.Context, paths []string) error
}

type fetcher struct {
	st     storeAPI
	client *client
}

// New returns a Fetcher that imports into st.
func New(st *store.Store, p Policy) Fetcher {
	f := &fetcher{client: newClient(p)}
	if st != nil {
		f.st = substituting{st}
	}
	return f
}

// substituting is the store with substitution of plain paths from its binary caches.
type substituting struct{ *store.Store }

// Realise is `nix-store --realise` of paths: it succeeds only when a substituter has them.
// Its output is discarded; a miss is not an error the user sees.
func (s substituting) Realise(ctx context.Context, paths []string) error {
	return s.Command(ctx, "nix-store", append([]string{"--realise"}, paths...)...).Run()
}

// newWithStore is New for any storeAPI (tests).
func newWithStore(st storeAPI, c *client) *fetcher { return &fetcher{st: st, client: c} }

// sri is a parsed SRI hash.
type sri struct {
	algo   string
	digest []byte
}

func (s sri) String() string { return s.algo + "-" + base64.StdEncoding.EncodeToString(s.digest) }

func (s sri) hex() string { return hex.EncodeToString(s.digest) }

func parseSRI(s string) (sri, error) {
	algo, b64, ok := strings.Cut(s, "-")
	want := map[string]int{"sha256": sha256.Size, "sha512": sha512.Size}[algo]
	if !ok || want == 0 {
		return sri{}, fmt.Errorf("hash %q is not an SRI sha256 or sha512 hash", s)
	}
	d, err := base64.StdEncoding.DecodeString(b64)
	if err != nil || len(d) != want {
		return sri{}, fmt.Errorf("hash %q is not an SRI %s hash", s, algo)
	}
	return sri{algo: algo, digest: d}, nil
}

func newHash(algo string) hash.Hash {
	if algo == "sha512" {
		return sha512.New()
	}
	return sha256.New()
}

// prepared is a validated request.
type prepared struct {
	Request
	hash    *sri // download hash, if pinned
	narHash *sri // tree hash, if pinned
}

func prepare(r Request) (*prepared, error) {
	p := &prepared{Request: r}
	if p.Label == "" {
		p.Label = r.Name
	}
	if len(r.URLs) == 0 {
		return nil, fmt.Errorf("fetch %s: no URL", p.Label)
	}
	if r.Kind != File && r.Kind != Archive {
		return nil, fmt.Errorf("fetch %s: unknown kind %q", p.Label, r.Kind)
	}
	if r.Strip < 0 {
		return nil, fmt.Errorf("fetch %s: negative strip", p.Label)
	}
	if r.Hash != "" {
		h, err := parseSRI(r.Hash)
		if err != nil {
			return nil, fmt.Errorf("fetch %s: %w", p.Label, err)
		}
		p.hash = &h
	}
	if r.NarHash != "" {
		h, err := parseSRI(r.NarHash)
		if err != nil || h.algo != "sha256" {
			return nil, fmt.Errorf("fetch %s: narHash %q is not an SRI sha256 hash", p.Label, r.NarHash)
		}
		p.narHash = &h
	}
	if p.MaxBytes <= 0 {
		p.MaxBytes = defaultMaxBytes
	}
	return p, nil
}

// path is the store path of the request, known only when the relevant hash is pinned.
func (p *prepared) path() (string, error) {
	switch {
	case p.Kind == File && p.hash != nil:
		return drv.FixedOutputPath(p.hash.algo, p.hash.hex(), false, p.Name)
	case p.Kind == Archive && p.narHash != nil:
		return drv.FixedOutputPath("sha256", p.narHash.hex(), true, p.Name)
	}
	return "", fmt.Errorf("fetch %s: no pinned hash", p.Label)
}

// Ensure makes the store path of r valid and returns it.
func (f *fetcher) Ensure(ctx context.Context, r Request) (string, error) {
	p, err := prepare(r)
	if err != nil {
		return "", err
	}
	if p.Kind == File && p.hash == nil {
		return "", fmt.Errorf("fetch %s: a file needs a pinned hash", p.Label)
	}
	if p.Kind == Archive && p.narHash == nil {
		return "", fmt.Errorf("fetch %s: an archive needs a pinned narHash", p.Label)
	}
	sp, err := p.path()
	if err != nil {
		return "", fmt.Errorf("fetch %s: %w", p.Label, err)
	}
	if f.st == nil {
		return "", fmt.Errorf("fetch %s: no store", p.Label)
	}
	if ok, err := f.valid(ctx, sp); err != nil || ok {
		return sp, err
	}
	if rs, ok := f.st.(realiser); ok {
		// A cache miss is normal; the origin is next.
		if rs.Realise(ctx, []string{sp}) == nil {
			if ok, err := f.valid(ctx, sp); err != nil || ok {
				return sp, err
			}
		}
	}
	got, err := f.fetch(ctx, p)
	if err != nil {
		return "", err
	}
	defer got.cleanup()
	if got.path != sp {
		// Unreachable: fetch verified both hashes, and the path follows from them.
		return "", fmt.Errorf("fetch %s: computed path %s, want %s", p.Label, got.path, sp)
	}
	return sp, nil
}

// Resolve downloads r and returns the hashes to record; the result is imported as well, so a
// following Ensure finds the path valid.
func (f *fetcher) Resolve(ctx context.Context, r Request) (string, string, error) {
	p, err := prepare(r)
	if err != nil {
		return "", "", err
	}
	got, err := f.fetch(ctx, p)
	if err != nil {
		return "", "", err
	}
	defer got.cleanup()
	return got.hash.String(), got.narHash, nil
}

// StorePath is the store path Ensure makes valid for r (r must be pinned).
func StorePath(r Request) (string, error) {
	p, err := prepare(r)
	if err != nil {
		return "", err
	}
	return p.path()
}

// CheckValid asks f's store about the store paths of all pinned requests in one batched
// validity query. With a store that remembers valid paths (store.WithValidityMemo), the
// following Ensure of an input that is valid already then runs no Nix command (A312). It only
// saves work: requests without a pinned hash are left to Ensure, and an error changes nothing
// Ensure would do.
func CheckValid(ctx context.Context, f Fetcher, reqs []Request) error {
	ff, ok := f.(*fetcher)
	if !ok || ff.st == nil {
		return nil
	}
	var paths []string
	for _, r := range reqs {
		if sp, err := StorePath(r); err == nil {
			paths = append(paths, sp)
		}
	}
	if len(paths) < 2 {
		return nil // Ensure asks about a single path itself
	}
	_, err := ff.st.Invalid(ctx, paths)
	return err
}

func (f *fetcher) valid(ctx context.Context, sp string) (bool, error) {
	inv, err := f.st.Invalid(ctx, []string{sp})
	if err != nil {
		return false, fmt.Errorf("fetch: check validity of %s: %w", sp, err)
	}
	return len(inv) == 0, nil
}

// fetched is a downloaded and verified input.
type fetched struct {
	path    string
	hash    sri    // of the download
	narHash string // SRI of the tree ("" for files)
	work    string
}

func (g *fetched) cleanup() { _ = os.RemoveAll(g.work) }

// fetch downloads p from its mirrors, verifies, unpacks, and imports the result when there is
// a store.
func (f *fetcher) fetch(ctx context.Context, p *prepared) (*fetched, error) {
	work, err := f.workDir()
	if err != nil {
		return nil, fmt.Errorf("fetch %s: %w", p.Label, err)
	}
	g := &fetched{work: work}
	ok := false
	defer func() {
		if !ok {
			g.cleanup()
		}
	}()
	algo := "sha256"
	if p.hash != nil {
		algo = p.hash.algo
	}
	file := filepath.Join(work, "download")
	url, digest, err := f.client.download(ctx, p, file, algo)
	if err != nil {
		return nil, err
	}
	g.hash = sri{algo: algo, digest: digest}
	var root string
	var narSum *[32]byte // the tree's NAR hash, checked again while it is imported
	if p.Kind == File {
		root = file
		if g.path, err = drv.FixedOutputPath(algo, g.hash.hex(), false, p.Name); err != nil {
			return nil, fmt.Errorf("fetch %s: %w", p.Label, err)
		}
	} else {
		root = filepath.Join(work, "tree")
		if err := unpack(ctx, file, root, p.Strip, p.Label, url); err != nil {
			return nil, err
		}
		sum, _, err := nar.Hash(root, nil)
		if err != nil {
			return nil, fmt.Errorf("fetch %s: %w", p.Label, err)
		}
		narSum = &sum
		got := sri{algo: "sha256", digest: sum[:]}
		g.narHash = got.String()
		if p.narHash != nil && !bytes.Equal(p.narHash.digest, sum[:]) {
			return nil, mismatch(p.Label, url, p.narHash.String(), g.narHash)
		}
		if g.path, err = drv.FixedOutputPath("sha256", got.hex(), true, p.Name); err != nil {
			return nil, fmt.Errorf("fetch %s: %w", p.Label, err)
		}
	}
	if f.st != nil {
		if err := f.importPath(ctx, g.path, root, narSum); err != nil {
			return nil, fmt.Errorf("fetch %s: %w", p.Label, err)
		}
	}
	ok = true
	return g, nil
}

// importPath imports the tree or file at root as store path sp (R-NIX-009's import route). The
// NAR is streamed into the store, never held in memory (F5): archives may be 8 GiB.
func (f *fetcher) importPath(ctx context.Context, sp, root string, narSum *[32]byte) error {
	if ok, err := f.valid(ctx, sp); err != nil || ok {
		return err
	}
	if err := f.st.Import(ctx, []store.ExportItem{store.TreeItem(sp, root, nil, narSum)}); err != nil {
		return fmt.Errorf("import %s: %w", sp, err)
	}
	return nil
}

// workDir is a fresh scratch directory: below the store home (the large disk), else TMPDIR.
func (f *fetcher) workDir() (string, error) {
	base := os.TempDir()
	if f.st != nil && f.st.Home() != "" {
		base = filepath.Join(f.st.Home(), "cache", "fetch")
		if err := os.MkdirAll(base, 0o755); err != nil {
			return "", err
		}
	}
	return os.MkdirTemp(base, "fetch-")
}

// mismatch is BOB-E-3001 with the URL, the recorded hash and the hash bob got, in the
// arguments and in the excerpt (the layout of the S15 golden of fault 16-wrong-hash).
func mismatch(label, url, want, got string) error {
	e := diag.New(3001, "label", label)
	e.Args["url"], e.Args["expected"], e.Args["got"] = url, want, got
	e.Cause = "the downloaded file has a different hash than bob.lock pins"
	e.Excerpt = []string{"url:        " + url, "bob.lock:   " + want, "downloaded: " + got}
	return e
}

// unsafeEntry is BOB-E-3004.
func unsafeEntry(label, url, path, reason string) error {
	return diag.New(3004, "label", label, "path", path, "reason", reason, "url", url)
}

// isDiag reports whether err already is a bob diagnostic.
func isDiag(err error) bool {
	var de *diag.Error
	return errors.As(err, &de)
}
