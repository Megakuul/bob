package fetch

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/megakuul/bob/pkg/bob/diag"
	"github.com/megakuul/bob/pkg/engine/drv"
	"github.com/megakuul/bob/pkg/engine/nar"
	"github.com/megakuul/bob/pkg/engine/store"
)

// memStore is a store that keeps imported NARs in memory.
type memStore struct {
	home string
	mu   sync.Mutex
	nars map[string][]byte
}

func (m *memStore) Home() string { return m.home }

func (m *memStore) Invalid(_ context.Context, paths []string) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []string
	for _, p := range paths {
		if _, ok := m.nars[p]; !ok {
			out = append(out, p)
		}
	}
	return out, nil
}

func (m *memStore) Import(_ context.Context, items []store.ExportItem) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, it := range items {
		m.nars[it.Path] = it.NAR
	}
	return nil
}

type env struct {
	st   *memStore
	f    *fetcher
	srv  *httptest.Server
	mux  *http.ServeMux
	hits atomic.Int64
}

func newEnv(t *testing.T) *env {
	e := &env{st: &memStore{home: t.TempDir(), nars: map[string][]byte{}}, mux: http.NewServeMux()}
	e.f = newWithStore(e.st, newClient(Policy{}))
	e.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		e.hits.Add(1)
		e.mux.ServeHTTP(w, r)
	}))
	t.Cleanup(e.srv.Close)
	return e
}

func (e *env) serve(path string, status int, body []byte) string {
	e.mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
		_, _ = w.Write(body)
	})
	return e.srv.URL + path
}

func sriOf(b []byte) string {
	h := sha256.Sum256(b)
	return "sha256-" + base64.StdEncoding.EncodeToString(h[:])
}

func code(t *testing.T, err error, want diag.Code) string {
	t.Helper()
	var de *diag.Error
	if !errors.As(err, &de) || de.Code != want {
		t.Fatalf("error %v, want %s", err, want.ID())
	}
	text := de.Error() + "\n" + de.Cause
	for _, v := range de.Args {
		text += "\n" + v
	}
	return text
}

func TestFileFlow(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	good := []byte("payload\n")
	bad := e.serve("/bad", 200, []byte("payloaD\n"))
	_, err := e.f.Ensure(ctx, Request{URLs: []string{bad}, Kind: File, Hash: sriOf(good), Name: "p", Label: "demo"})
	text := code(t, err, 3001)
	for _, w := range []string{bad, sriOf(good), sriOf([]byte("payloaD\n"))} {
		if !strings.Contains(text, w) {
			t.Errorf("3001 lacks %q", w)
		}
	}
	missing := e.serve("/missing", 404, nil)
	_, err = e.f.Ensure(ctx, Request{URLs: []string{missing}, Kind: File, Hash: sriOf(good), Name: "p", Label: "demo"})
	code(t, err, 3003)

	ok := e.serve("/ok", 200, good)
	p, err := e.f.Ensure(ctx, Request{URLs: []string{missing, ok}, Kind: File, Hash: sriOf(good), Name: "p.txt", Label: "demo"})
	if err != nil {
		t.Fatal(err)
	}
	h := sha256.Sum256(good)
	want, _ := drv.FixedOutputPath("sha256", hex.EncodeToString(h[:]), false, "p.txt")
	if p != want {
		t.Errorf("path %s, want %s", p, want)
	}
	// Valid now: no network.
	e.hits.Store(0)
	if again, err := e.f.Ensure(ctx, Request{URLs: []string{"http://127.0.0.1:1/x"}, Kind: File, Hash: sriOf(good), Name: "p.txt"}); err != nil || again != p {
		t.Errorf("valid path: %s %v", again, err)
	}
	if e.hits.Load() != 0 {
		t.Errorf("a valid path contacted the origin")
	}
	// Plain http without a pin is refused before any request; the size cap holds.
	if _, _, err := e.f.Resolve(ctx, Request{URLs: []string{ok}, Kind: File, Name: "p"}); err == nil || e.hits.Load() != 0 {
		t.Errorf("plain http without a pin: %v, %d hits", err, e.hits.Load())
	}
	if _, err := e.f.Ensure(ctx, Request{URLs: []string{ok}, Kind: File, Hash: sriOf(good), Name: "q", MaxBytes: 4}); err == nil {
		t.Errorf("size cap ignored")
	}
}

func TestDenyPrivate(t *testing.T) {
	e := newEnv(t)
	e.f.client = newClient(Policy{DenyPrivate: true})
	u := e.serve("/x", 200, []byte("x"))
	_, err := e.f.Ensure(context.Background(), Request{URLs: []string{u}, Kind: File, Hash: sriOf([]byte("x")), Name: "x"})
	if err == nil || e.hits.Load() != 0 {
		t.Errorf("loopback allowed under DenyPrivate: %v", err)
	}
}

func gzTar(t *testing.T, hdrs []tar.Header) []byte {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, h := range hdrs {
		if h.Mode == 0 {
			h.Mode = 0o644
		}
		body := []byte(nil)
		if h.Typeflag == tar.TypeReg {
			body = []byte("x")
			h.Size = 1
		}
		if err := tw.WriteHeader(&h); err != nil {
			t.Fatal(err)
		}
		_, _ = tw.Write(body)
	}
	_ = tw.Close()
	_ = gz.Close()
	return buf.Bytes()
}

func TestUnsafeArchives(t *testing.T) {
	e := newEnv(t)
	dir := func(n string) tar.Header { return tar.Header{Name: n, Typeflag: tar.TypeDir, Mode: 0o755} }
	for _, c := range []struct {
		name, path string
		hdrs       []tar.Header
	}{
		{"dotdot", "../x", []tar.Header{{Name: "../x", Typeflag: tar.TypeReg}}},
		{"abs", "/a", []tar.Header{{Name: "/a", Typeflag: tar.TypeReg}}},
		{"symlink", "p/l", []tar.Header{dir("p"), {Name: "p/l", Typeflag: tar.TypeSymlink, Linkname: "../../o"}}},
		{"through", "p/d/f", []tar.Header{dir("p"), dir("p/s"), {Name: "p/d", Typeflag: tar.TypeSymlink, Linkname: "s"}, {Name: "p/d/f", Typeflag: tar.TypeReg}}},
		{"fifo", "p/f", []tar.Header{dir("p"), {Name: "p/f", Typeflag: tar.TypeFifo}}},
		{"hardlink", "p/h", []tar.Header{dir("p"), {Name: "p/h", Typeflag: tar.TypeLink, Linkname: "../../x"}}},
	} {
		data := gzTar(t, c.hdrs)
		u := e.serve("/"+c.name+".tar.gz", 200, data)
		_, _, err := e.f.Resolve(context.Background(), Request{URLs: []string{u}, Kind: Archive, Hash: sriOf(data), Name: "u"})
		if text := code(t, err, 3004); !strings.Contains(text, c.path) || !strings.Contains(text, u) {
			t.Errorf("%s: %s", c.name, text)
		}
	}
}

// TestArchiveFormats unpacks the acceptance archives (tests/nix/testdata/archives) and checks
// the tree's NAR hash against the host Nix's hash of the tree they must produce.
func TestArchiveFormats(t *testing.T) {
	if _, err := exec.LookPath("nix-store"); err != nil {
		t.Skip("no host nix-store")
	}
	e := newEnv(t)
	for _, ext := range []string{"tar", "tar.gz", "tgz", "tar.bz2", "tar.xz", "tar.zst", "zip", "crate"} {
		data, err := os.ReadFile(filepath.Join("..", "..", "..", "tests", "nix", "testdata", "archives", "tree."+ext))
		if err != nil {
			t.Fatal(err)
		}
		u := e.serve("/tree."+ext, 200, data)
		req := Request{URLs: []string{u}, Kind: Archive, Hash: sriOf(data), Name: "tree", Strip: 1, Label: "demo"}
		_, narHash, err := e.f.Resolve(context.Background(), req)
		if err != nil {
			t.Fatalf("%s: %v", ext, err)
		}
		tree := t.TempDir()
		writeTree(t, tree, ext != "zip")
		out, err := exec.Command("nix-store", "--dump", tree).Output()
		if err != nil {
			t.Fatal(err)
		}
		if want := sriOf(out); narHash != want {
			t.Errorf("%s: narHash %s, nix says %s", ext, narHash, want)
		}
		req.NarHash = narHash
		p, err := e.f.Ensure(context.Background(), req)
		sum, _, _ := nar.Hash(tree, nil)
		want, _ := drv.FixedOutputPath("sha256", hex.EncodeToString(sum[:]), true, "tree")
		if err != nil || p != want {
			t.Errorf("%s: Ensure = %s, %v; want %s", ext, p, err, want)
		}
	}
}

func writeTree(t *testing.T, dir string, hard bool) {
	lib := []byte("pub fn f() -> u32 { 42 }\n")
	blob := make([]byte, 256*8)
	for i := range blob {
		blob[i] = byte(i % 256)
	}
	files := []struct {
		p    string
		data []byte
		mode os.FileMode
	}{
		{"Cargo.toml", []byte("[package]\nname = \"pkg\"\nversion = \"1.0.0\"\n"), 0o644},
		{"src/lib.rs", lib, 0o644},
		{"bin/run.sh", []byte("#!/bin/sh\necho hi\n"), 0o755},
		{"data/blob.bin", blob, 0o755},
	}
	if hard {
		files = append(files, struct {
			p    string
			data []byte
			mode os.FileMode
		}{"src/hard.rs", lib, 0o644})
	}
	for _, f := range files {
		p := filepath.Join(dir, filepath.FromSlash(f.p))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, f.data, f.mode); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(p, f.mode); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink("lib.rs", filepath.Join(dir, "src", "alias.rs")); err != nil {
		t.Fatal(err)
	}
}
