package remote

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// fakeCache serves signed narinfos and NARs for paths (path → NAR bytes, references).
type fakeCache struct {
	priv  ed25519.PrivateKey
	key   string
	paths map[string]struct {
		nar  []byte
		refs []string
	}
	mu   sync.Mutex
	auth []string
}

func newFakeCache(t *testing.T) *fakeCache {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return &fakeCache{priv: priv, key: "test-1:" + base64.StdEncoding.EncodeToString(pub), paths: map[string]struct {
		nar  []byte
		refs []string
	}{}}
}

func (f *fakeCache) add(path string, nar []byte, refs ...string) {
	f.paths[path] = struct {
		nar  []byte
		refs []string
	}{nar, refs}
}

func (f *fakeCache) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.auth = append(f.auth, r.Header.Get("Authorization"))
	f.mu.Unlock()
	name := strings.TrimPrefix(r.URL.Path, "/nix/")
	for p, e := range f.paths {
		h := hashPart(p)
		sum := sha256.Sum256(e.nar)
		switch name {
		case h + ".narinfo":
			var refs []string
			for _, x := range e.refs {
				refs = append(refs, strings.TrimPrefix(x, "/nix/store/"))
			}
			ni := &Narinfo{StorePath: p, NarHash: "sha256:" + Nix32(sum[:]), NarSize: int64(len(e.nar)), References: e.refs}
			sig := "test-1:" + base64.StdEncoding.EncodeToString(ed25519.Sign(f.priv, []byte(ni.Fingerprint())))
			fmt.Fprintf(w, "StorePath: %s\nURL: nar/%s.nar\nCompression: none\nNarHash: sha256:%s\nNarSize: %d\nReferences: %s\nSig: %s\n",
				p, h, hex.EncodeToString(sum[:]), len(e.nar), strings.Join(refs, " "), sig)
			return
		case "nar/" + h + ".nar":
			_, _ = w.Write(e.nar)
			return
		}
	}
	http.NotFound(w, r)
}

const (
	pathA = "/nix/store/0c3iw4j7r5pr3wzgk3zxx2nrqm8r7f6a-a-1.0"
	pathB = "/nix/store/1c3iw4j7r5pr3wzgk3zxx2nrqm8r7f6a-b-1.0"
	pathC = "/nix/store/2c3iw4j7r5pr3wzgk3zxx2nrqm8r7f6a-c-1.0"
)

func TestFetchClosure(t *testing.T) {
	f := newFakeCache(t)
	f.add(pathA, []byte("nar-a"), pathB, pathC)
	f.add(pathB, []byte("nar-b"))
	srv := httptest.NewServer(f)
	defer srv.Close()
	dir := t.TempDir()
	c := &Cache{URL: srv.URL + "/nix", Keys: []string{f.key}}
	missing, err := c.Fetch(context.Background(), []string{pathA}, nil, dir)
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(missing) != "["+pathC+"]" {
		t.Errorf("missing = %v, want [%s] (a reference the cache lacks)", missing, pathC)
	}
	for _, p := range []string{pathA, pathB} {
		if _, err := os.Stat(filepath.Join(dir, hashPart(p)+".narinfo")); err != nil {
			t.Errorf("narinfo of %s: %v", p, err)
		}
		if _, err := os.Stat(filepath.Join(dir, "nar", hashPart(p)+".nar")); err != nil {
			t.Errorf("NAR of %s: %v", p, err)
		}
	}
	for _, a := range f.auth {
		if a != "" {
			t.Errorf("anonymous fetch sent Authorization %q", a)
		}
	}

	// have() skips a path and its references.
	f.auth = nil
	if _, err := c.Fetch(context.Background(), []string{pathA}, func(p string) bool { return p == pathA }, t.TempDir()); err != nil {
		t.Fatal(err)
	}
	if len(f.auth) != 0 {
		t.Errorf("a present path was fetched: %d requests", len(f.auth))
	}

	// the token is sent as a bearer token.
	c.Token = "bob_at_x"
	f.auth = nil
	if _, err := c.Fetch(context.Background(), []string{pathB}, nil, t.TempDir()); err != nil {
		t.Fatal(err)
	}
	if len(f.auth) == 0 || f.auth[0] != "Bearer bob_at_x" {
		t.Errorf("Authorization = %v, want Bearer", f.auth)
	}
}

func TestFetchErrors(t *testing.T) {
	f := newFakeCache(t)
	f.add(pathA, []byte("nar-a"))
	srv := httptest.NewServer(f)
	defer srv.Close()
	other := newFakeCache(t)
	kind := func(err error) string {
		var ce *CacheError
		if errors.As(err, &ce) {
			return ce.Kind
		}
		return fmt.Sprint(err)
	}
	c := &Cache{URL: srv.URL + "/nix", Keys: []string{other.key}}
	if _, err := c.Fetch(context.Background(), []string{pathA}, nil, t.TempDir()); kind(err) != "untrusted" {
		t.Errorf("unpinned key: %v, want untrusted", err)
	}
	c.Keys = []string{f.key}
	if _, err := c.Fetch(context.Background(), []string{pathB}, nil, t.TempDir()); kind(err) != "missing" {
		t.Errorf("missing root: %v, want missing", err)
	}
	if _, err := c.Fetch(context.Background(), []string{"/nix/store/../etc"}, nil, t.TempDir()); kind(err) != "corrupt" {
		t.Errorf("bad root: %v, want corrupt", err)
	}
}

func TestParseNarinfoHostile(t *testing.T) {
	for _, bad := range []string{
		"StorePath: /etc/passwd\nNarHash: sha256:" + strings.Repeat("0", 52) + "\nNarSize: 1\nURL: nar/x.nar\n",
		"StorePath: " + pathA + "\nNarHash: sha256:" + strings.Repeat("0", 52) + "\nNarSize: 1\nURL: ../x.nar\n",
		"StorePath: " + pathA + "\nNarHash: sha256:" + strings.Repeat("0", 52) + "\nNarSize: 1\nURL: /abs.nar\n",
		"StorePath: " + pathA + "\nNarHash: md5:00\nNarSize: 1\nURL: nar/x.nar\n",
		"StorePath: " + pathA + "\nNarHash: sha256:" + strings.Repeat("0", 52) + "\nNarSize: 1\nURL: nar/x.nar\nReferences: ../../etc\n",
	} {
		if _, err := ParseNarinfo([]byte(bad)); err == nil {
			t.Errorf("accepted hostile narinfo:\n%s", bad)
		}
	}
	// SRI and base-16 hashes normalise to Nix base-32.
	sum := sha256.Sum256([]byte("x"))
	want := "sha256:" + Nix32(sum[:])
	for _, h := range []string{"sha256:" + hex.EncodeToString(sum[:]), "sha256-" + base64.StdEncoding.EncodeToString(sum[:]), want} {
		if got, err := normHash(h); err != nil || got != want {
			t.Errorf("normHash(%s) = %s, %v; want %s", h, got, err, want)
		}
	}
}
