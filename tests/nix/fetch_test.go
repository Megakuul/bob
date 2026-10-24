package nix

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
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
	"sync/atomic"
	"testing"
	"time"

	"github.com/megakuul/bob/pkg/bob/diag"
	"github.com/megakuul/bob/pkg/engine/drv"
	"github.com/megakuul/bob/pkg/engine/fetch"
	"github.com/megakuul/bob/pkg/engine/store"
	"github.com/megakuul/bob/tests/parity"
)

func sri(sum []byte) string { return "sha256-" + base64.StdEncoding.EncodeToString(sum) }

func sriOf(b []byte) string { h := sha256.Sum256(b); return sri(h[:]) }

func sriFromHex(t testing.TB, h string) string {
	t.Helper()
	b, err := hex.DecodeString(h)
	if err != nil {
		t.Fatalf("harness: %v", err)
	}
	return sri(b)
}

func hexFromSRI(t testing.TB, s string) string {
	t.Helper()
	b, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(s, "sha256-"))
	if err != nil {
		t.Fatalf("harness: bad SRI %q: %v", s, err)
	}
	return hex.EncodeToString(b)
}

// fetchEnv is a store, a Fetcher and a local HTTP origin.
type fetchEnv struct {
	ctx  context.Context
	st   *store.Store
	f    fetch.Fetcher
	srv  *httptest.Server
	hits atomic.Int64
	mux  *http.ServeMux
}

// newFetchEnv opens a store; ok is false (and t is failed as red) while the store is a stub.
func newFetchEnv(t *testing.T) (*fetchEnv, bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	t.Cleanup(cancel)
	st, err := store.Open(ctx, store.Config{Home: filepath.Join(t.TempDir(), "bobhome")})
	if notImplemented(t, err, "store.Open") {
		return nil, false
	}
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	e := &fetchEnv{ctx: ctx, st: st, f: fetch.New(st, fetch.Policy{}), mux: http.NewServeMux()}
	e.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		e.hits.Add(1)
		e.mux.ServeHTTP(w, r)
	}))
	t.Cleanup(e.srv.Close)
	return e, true
}

func (e *fetchEnv) serve(path string, status int, body []byte) string {
	e.mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
		_, _ = w.Write(body)
	})
	return e.srv.URL + path
}

// diagOf asserts err is a *diag.Error with code and returns all text it carries.
func diagOf(t testing.TB, err error, code diag.Code) (string, bool) {
	t.Helper()
	var de *diag.Error
	if !errors.As(err, &de) {
		t.Errorf("error %v is not a *diag.Error", err)
		return "", false
	}
	if de.Code != code {
		t.Errorf("error code = %s (%v), want %s", de.Code.ID(), err, code.ID())
		return "", false
	}
	// Raw holds the Nix text shown only with -v (SPEC R-NIX-010, R-NIX-027); it is not user output.
	text := de.Error() + "\n" + de.Cause + "\n" + strings.Join(de.Excerpt, "\n")
	for _, v := range de.Args {
		text += "\n" + v
	}
	return text, true
}

// TestRNIX014TamperedByte: a changed byte in a downloaded file gives 3001 with URL, lock hash
// and got hash.
func TestRNIX014TamperedByte(t *testing.T) {
	parity.Covers(t, "R-NIX-014")
	e, ok := newFetchEnv(t)
	if !ok {
		return
	}
	good := []byte("the genuine release payload\n")
	bad := bytes.Replace(good, []byte("genuine"), []byte("genuinE"), 1)
	url := e.serve("/pay.txt", 200, bad)
	_, err := e.f.Ensure(e.ctx, fetch.Request{URLs: []string{url}, Kind: fetch.File, Hash: sriOf(good), Name: "pay.txt", Label: "demo"})
	text, ok := diagOf(t, err, 3001)
	if !ok {
		return
	}
	for _, want := range []string{url, sriOf(good), sriOf(bad)} {
		if !strings.Contains(text, want) {
			t.Errorf("3001 does not print %q:\n%s", want, text)
		}
	}
}

// TestRNIX014NotFoundAndFailures: 404 gives 3003; a server that always answers 500 gives 3002
// after three attempts; the second mirror serves when the first is gone.
func TestRNIX014NotFoundAndFailures(t *testing.T) {
	parity.Covers(t, "R-NIX-014")
	e, ok := newFetchEnv(t)
	if !ok {
		return
	}
	payload := []byte("payload\n")
	missing := e.serve("/missing", 404, []byte("nope"))
	_, err := e.f.Ensure(e.ctx, fetch.Request{URLs: []string{missing}, Kind: fetch.File, Hash: sriOf(payload), Name: "m", Label: "demo"})
	diagOf(t, err, 3003)

	e.hits.Store(0)
	broken := e.serve("/broken", 500, []byte("boom"))
	start := time.Now()
	_, err = e.f.Ensure(e.ctx, fetch.Request{URLs: []string{broken}, Kind: fetch.File, Hash: sriOf(payload), Name: "b", Label: "demo"})
	diagOf(t, err, 3002)
	if n := e.hits.Load(); n != 3 {
		t.Errorf("a failing origin was asked %d times, want 3 attempts", n)
	}
	if d := time.Since(start); d < 4*time.Second {
		t.Errorf("3 attempts took %v; the backoff is 1 s then 4 s", d)
	}

	good := e.serve("/good", 200, payload)
	p, err := e.f.Ensure(e.ctx, fetch.Request{URLs: []string{missing, good}, Kind: fetch.File, Hash: sriOf(payload), Name: "g.txt", Label: "demo"})
	if err != nil {
		t.Fatalf("second mirror: %v", err)
	}
	h := sha256.Sum256(payload)
	want, _ := drv.FixedOutputPath("sha256", hex.EncodeToString(h[:]), false, "g.txt")
	if p != want {
		t.Errorf("flat path = %s, want %s", p, want)
	}
}

// TestRNIX008FetchedPathNeedsNoNetwork: once a path is valid, Ensure returns it without touching
// the origin: a build with an unreachable source URL succeeds when the path is already valid.
func TestRNIX008FetchedPathNeedsNoNetwork(t *testing.T) {
	parity.Covers(t, "R-NIX-008")
	e, ok := newFetchEnv(t)
	if !ok {
		return
	}
	payload := []byte("kept in the store\n")
	url := e.serve("/kept", 200, payload)
	req := fetch.Request{URLs: []string{url}, Kind: fetch.File, Hash: sriOf(payload), Name: "kept.txt", Label: "demo"}
	first, err := e.f.Ensure(e.ctx, req)
	if err != nil {
		t.Fatalf("first Ensure: %v", err)
	}
	e.srv.Close()
	req.URLs = []string{url, "http://127.0.0.1:1/unreachable"}
	again, err := e.f.Ensure(e.ctx, req)
	if err != nil || again != first {
		t.Errorf("Ensure of a valid path with a dead origin = %q, %v; want %q", again, err, first)
	}
	if inv, err := e.st.Invalid(e.ctx, []string{first}); err != nil || len(inv) != 0 {
		t.Errorf("the fetched path is not valid in the store: %v %v", inv, err)
	}
}

// TestRNIX014Policy: http needs a pinned hash; the size cap stops oversized bodies.
func TestRNIX014HTTPAndSizeCap(t *testing.T) {
	parity.Covers(t, "R-NIX-014")
	e, ok := newFetchEnv(t)
	if !ok {
		return
	}
	body := bytes.Repeat([]byte("x"), 4096)
	url := e.serve("/big", 200, body)
	e.hits.Store(0)
	if _, _, err := e.f.Resolve(e.ctx, fetch.Request{URLs: []string{url}, Kind: fetch.File, Name: "big", Label: "demo"}); err == nil {
		t.Errorf("Resolve over plain http without a pinned hash succeeded")
	}
	if n := e.hits.Load(); n != 0 {
		t.Errorf("plain http without a pinned hash still contacted the origin %d times", n)
	}
	_, err := e.f.Ensure(e.ctx, fetch.Request{URLs: []string{url}, Kind: fetch.File, Hash: sriOf(body), Name: "big", MaxBytes: 1024, Label: "demo"})
	if err == nil {
		t.Errorf("a 4096-byte body with MaxBytes 1024 was accepted")
	}
}

type tarEntry struct {
	hdr  tar.Header
	body []byte
}

func gzTar(t testing.TB, entries []tarEntry) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz, _ := gzip.NewWriterLevel(&buf, gzip.BestSpeed)
	tw := tar.NewWriter(gz)
	for _, e := range entries {
		h := e.hdr
		h.Size = int64(len(e.body))
		if h.Mode == 0 {
			h.Mode = 0o644
		}
		if err := tw.WriteHeader(&h); err != nil {
			t.Fatalf("harness: %v", err)
		}
		if _, err := tw.Write(e.body); err != nil {
			t.Fatalf("harness: %v", err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("harness: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("harness: %v", err)
	}
	return buf.Bytes()
}

func reg(name string) tarEntry {
	return tarEntry{hdr: tar.Header{Name: name, Typeflag: tar.TypeReg}, body: []byte("x")}
}

// TestRNIX014UnsafeArchives: tars with ../x, an absolute path, a path through a symlink, symlink
// escapes, device nodes, fifos, escaping hard links and 10^6+1 entries are each refused with 3004.
func TestRNIX014UnsafeArchives(t *testing.T) {
	parity.Covers(t, "R-NIX-014")
	e, ok := newFetchEnv(t)
	if !ok {
		return
	}
	dir := func(n string) tarEntry { return tarEntry{hdr: tar.Header{Name: n, Typeflag: tar.TypeDir, Mode: 0o755}} }
	link := func(n, to string) tarEntry {
		return tarEntry{hdr: tar.Header{Name: n, Typeflag: tar.TypeSymlink, Linkname: to}}
	}
	cases := []struct {
		name    string
		path    string // entry the error must name
		entries []tarEntry
	}{
		{"dotdot", "../x", []tarEntry{reg("../x")}},
		{"dotdot-inside", "pkg/../../escape", []tarEntry{reg("pkg/../../escape")}},
		{"absolute", "/abs/file", []tarEntry{reg("/abs/file")}},
		{"symlink-escape", "pkg/l", []tarEntry{dir("pkg"), link("pkg/l", "../../outside")}},
		{"symlink-absolute", "pkg/l", []tarEntry{dir("pkg"), link("pkg/l", "/etc/passwd")}},
		{"through-symlink", "pkg/d/f", []tarEntry{dir("pkg"), dir("pkg/sub"), link("pkg/d", "sub"), reg("pkg/d/f")}},
		{"chardev", "pkg/dev", []tarEntry{dir("pkg"), {hdr: tar.Header{Name: "pkg/dev", Typeflag: tar.TypeChar, Devmajor: 1, Devminor: 3}}}},
		{"blockdev", "pkg/blk", []tarEntry{dir("pkg"), {hdr: tar.Header{Name: "pkg/blk", Typeflag: tar.TypeBlock, Devmajor: 7, Devminor: 0}}}},
		{"fifo", "pkg/fifo", []tarEntry{dir("pkg"), {hdr: tar.Header{Name: "pkg/fifo", Typeflag: tar.TypeFifo}}}},
		{"hardlink-escape", "pkg/h", []tarEntry{dir("pkg"), {hdr: tar.Header{Name: "pkg/h", Typeflag: tar.TypeLink, Linkname: "../../x"}}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			data := gzTar(t, c.entries)
			url := e.serve("/unsafe-"+c.name+".tar.gz", 200, data)
			req := fetch.Request{URLs: []string{url}, Kind: fetch.Archive, Hash: sriOf(data), NarHash: sriOf([]byte("unknown")), Name: "unsafe", Label: "demo"}
			for _, op := range []string{"Resolve", "Ensure"} {
				var err error
				if op == "Resolve" {
					_, _, err = e.f.Resolve(e.ctx, req)
				} else {
					_, err = e.f.Ensure(e.ctx, req)
				}
				text, ok := diagOf(t, err, 3004)
				if !ok {
					continue
				}
				if !strings.Contains(text, c.path) || !strings.Contains(text, url) {
					t.Errorf("%s: 3004 must name the entry %q and %s:\n%s", op, c.path, url, text)
				}
			}
		})
	}
	t.Run("too-many-entries", func(t *testing.T) {
		var buf bytes.Buffer
		gz, _ := gzip.NewWriterLevel(&buf, gzip.BestSpeed)
		tw := tar.NewWriter(gz)
		for i := 0; i <= 1_000_000; i++ { // 10^6+1 entries
			if err := tw.WriteHeader(&tar.Header{Name: fmt.Sprintf("p/d%d/f%d", i%1000, i), Typeflag: tar.TypeReg, Mode: 0o644}); err != nil {
				t.Fatalf("harness: %v", err)
			}
		}
		_ = tw.Close()
		_ = gz.Close()
		url := e.serve("/many.tar.gz", 200, buf.Bytes())
		_, _, err := e.f.Resolve(e.ctx, fetch.Request{URLs: []string{url}, Kind: fetch.Archive, Hash: sriOf(buf.Bytes()), Name: "many", Label: "demo"})
		diagOf(t, err, 3004)
	})
}

// normalisedTree writes what unpacking testdata/archives/tree.* with Strip 1 must produce:
// modes 0644/0755, symlink kept, hard link turned into a copy (tar formats only).
func normalisedTree(t testing.TB, dir string, withHardlink bool) {
	t.Helper()
	lib := []byte("pub fn f() -> u32 { 42 }\n")
	mustWrite(t, filepath.Join(dir, "Cargo.toml"), []byte("[package]\nname = \"pkg\"\nversion = \"1.0.0\"\n"), 0o644)
	mustWrite(t, filepath.Join(dir, "src", "lib.rs"), lib, 0o644)
	mustWrite(t, filepath.Join(dir, "bin", "run.sh"), []byte("#!/bin/sh\necho hi\n"), 0o755)
	mustWrite(t, filepath.Join(dir, "data", "blob.bin"), bytes.Repeat(func() []byte {
		b := make([]byte, 256)
		for i := range b {
			b[i] = byte(i)
		}
		return b
	}(), 8), 0o755)
	for _, f := range []string{"Cargo.toml", "src/lib.rs", "bin/run.sh", "data/blob.bin"} {
		m := os.FileMode(0o644)
		if f == "bin/run.sh" || f == "data/blob.bin" {
			m = 0o755
		}
		if err := os.Chmod(filepath.Join(dir, filepath.FromSlash(f)), m); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink("lib.rs", filepath.Join(dir, "src", "alias.rs")); err != nil {
		t.Fatal(err)
	}
	if withHardlink {
		mustWrite(t, filepath.Join(dir, "src", "hard.rs"), lib, 0o644)
	}
}

// TestRNIX014ArchiveFormats: every supported archive format unpacks (Strip 1) to the tree with
// normalised modes, its NAR hash is what Resolve reports, Ensure verifies it, and the result
// equals the recursive fixed-output path of that narHash (also what `nix store add` makes).
func TestRNIX014ArchiveFormats(t *testing.T) {
	parity.Covers(t, "R-NIX-014")
	e, ok := newFetchEnv(t)
	if !ok {
		return
	}
	o := newOracle(t)
	for _, ext := range []string{"tar", "tar.gz", "tgz", "tar.bz2", "tar.xz", "tar.zst", "zip", "crate"} {
		t.Run(ext, func(t *testing.T) {
			data := mustRead(t, filepath.Join("testdata", "archives", "tree."+ext))
			tree := t.TempDir()
			normalisedTree(t, tree, ext != "zip")
			name := "pkg-1.0-" + strings.ReplaceAll(ext, ".", "-")
			wantNar := sriFromHex(t, o.narHashHex(tree))
			wantPath := o.addNar(tree, name)
			url := e.serve("/tree-"+ext+"/"+"tree."+ext, 200, data)
			req := fetch.Request{URLs: []string{url}, Kind: fetch.Archive, Hash: sriOf(data), Name: name, Strip: 1, Label: "demo"}
			hash, nar, err := e.f.Resolve(e.ctx, req)
			if err != nil {
				t.Fatalf("Resolve: %v", err)
			}
			if hash != sriOf(data) || nar != wantNar {
				t.Errorf("Resolve = (%s, %s), want (%s, %s)", hash, nar, sriOf(data), wantNar)
			}
			req.NarHash = nar
			got, err := e.f.Ensure(e.ctx, req)
			if err != nil {
				t.Fatalf("Ensure: %v", err)
			}
			fo, _ := drv.FixedOutputPath("sha256", hexFromSRI(t, nar), true, name)
			if got != wantPath || got != fo {
				t.Errorf("Ensure = %s; nix store add says %s; FixedOutputPath %s", got, wantPath, fo)
			}
			// A wrong NarHash is a 3001 even though the download hash is right.
			req.NarHash = sriOf([]byte("not the tree"))
			req.Name = name + "-wrong"
			_, err = e.f.Ensure(e.ctx, req)
			diagOf(t, err, 3001)
		})
	}
}
