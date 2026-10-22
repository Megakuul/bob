package nix

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/megakuul/bob/pkg/engine/nixidx"
	"github.com/megakuul/bob/pkg/engine/store"
	"github.com/megakuul/bob/tests/errors/leakcheck"
	"github.com/megakuul/bob/tests/harness"
	"github.com/megakuul/bob/tests/parity"
)

// narinfoServer is a binary cache that has narinfo for the given hashes and counts HEAD requests.
type narinfoServer struct {
	*httptest.Server
	mu    sync.Mutex
	have  map[string]bool
	heads map[string]int
}

func newNarinfoServer(have ...string) *narinfoServer {
	s := &narinfoServer{have: map[string]bool{}, heads: map[string]int{}}
	for _, h := range have {
		s.have[h] = true
	}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		if r.Method == http.MethodHead || r.Method == http.MethodGet {
			s.heads[r.Method+" "+r.URL.Path]++
		}
		h := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/"), ".narinfo")
		if strings.HasSuffix(r.URL.Path, ".narinfo") && s.have[h] {
			w.WriteHeader(200)
			return
		}
		w.WriteHeader(404)
	}))
	return s
}

func (s *narinfoServer) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, c := range s.heads {
		n += c
	}
	return n
}

// TestRNIX027IndexLookup runs the index over the miniature nixpkgs of testdata/fakepkgs:
// per-attribute data (missing, broken, insecure, unfree, unavailable, nested sets), the names
// list, the on-disk cache, and the per-attribute error 1017 for an attribute whose evaluation
// aborts (not 9999, and the other attributes are still answered).
func TestRNIX027IndexLookup(t *testing.T) {
	parity.Covers(t, "R-NIX-027")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	home := filepath.Join(t.TempDir(), "bobhome")
	st, err := store.Open(ctx, store.Config{Home: home})
	if notImplemented(t, err, "store.Open") {
		return
	}
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	src, err := filepath.Abs(filepath.Join("testdata", "fakepkgs"))
	if err != nil {
		t.Fatal(err)
	}
	system := hostSystem(t)
	cfg := nixidx.Config{Source: src, Rev: "fake-rev-1", System: system}
	idx, err := nixidx.Open(ctx, st, cfg)
	if notImplemented(t, err, "nixidx.Open") {
		return
	}
	if err != nil {
		t.Fatalf("nixidx.Open: %v", err)
	}
	attrs := []string{"hello", "broken-pkg", "insecure-pkg", "unfree-pkg", "wrong-system", "sub.inner", "nope"}
	got, err := idx.Lookup(ctx, attrs)
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	e := got["hello"]
	if e.Missing || e.Broken || e.Insecure || e.Unfree || !e.Available || e.Version != "1.0" ||
		!strings.HasSuffix(e.DrvPath, "-hello.drv") || e.Outputs["out"] == "" || e.Outputs["dev"] == "" {
		t.Errorf("hello = %+v", e)
	}
	if !got["nope"].Missing {
		t.Errorf("nope = %+v, want missing", got["nope"])
	}
	if !got["broken-pkg"].Broken {
		t.Errorf("broken-pkg = %+v, want broken", got["broken-pkg"])
	}
	if i := got["insecure-pkg"]; !i.Insecure || len(i.KnownVulnerabilities) != 1 || i.KnownVulnerabilities[0] != "CVE-2099-0001" {
		t.Errorf("insecure-pkg = %+v", i)
	}
	if !got["unfree-pkg"].Unfree {
		t.Errorf("unfree-pkg = %+v, want unfree", got["unfree-pkg"])
	}
	if got["wrong-system"].Available {
		t.Errorf("wrong-system = %+v, want unavailable", got["wrong-system"])
	}
	if got["sub.inner"].Missing || !strings.HasSuffix(got["sub.inner"].DrvPath, "-inner.drv") {
		t.Errorf("sub.inner = %+v", got["sub.inner"])
	}
	names, err := idx.Names(ctx)
	if err != nil {
		t.Fatalf("Names: %v", err)
	}
	for _, want := range []string{"hello", "bad", "sub"} {
		found := false
		for _, n := range names {
			found = found || n == want
		}
		if !found {
			t.Errorf("Names lacks %q", want)
		}
	}
	// The cache file is keyed by (rev, system) and holds the answers.
	cache := filepath.Join(home, "cache", "nixpkgs", "fake-rev-1-"+system+".json")
	b1, err := os.ReadFile(cache)
	if err != nil || !strings.Contains(string(b1), "hello") {
		t.Fatalf("index cache %s missing or without hello: %v", cache, err)
	}
	// A warm lookup of known attributes does not rewrite the cache.
	idx2, err := nixidx.Open(ctx, st, cfg)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if _, err := idx2.Lookup(ctx, []string{"hello", "broken-pkg"}); err != nil {
		t.Fatal(err)
	}
	if b2, _ := os.ReadFile(cache); string(b2) != string(b1) {
		t.Errorf("a warm lookup changed the index cache")
	}
	// An attribute that aborts is isolated and reported per attribute: 1017, never 9999.
	res, err := idx2.Lookup(ctx, []string{"hello", "bad", "sub.inner"})
	text, ok := diagOf(t, err, 1017)
	if ok {
		for _, want := range []string{"bad", system} {
			if !strings.Contains(text, want) {
				t.Errorf("1017 does not name %q:\n%s", want, text)
			}
		}
		if l := leakcheck.Check([]byte(text)); len(l) > 0 {
			t.Errorf("1017 shows raw Nix text: %v", l)
		}
	}
	if res["hello"].DrvPath == "" || res["sub.inner"].DrvPath == "" {
		t.Errorf("the attributes next to the aborting one were not answered: %+v", res)
	}
	// Budgets B-078 (cold ≤ 2 s, warm ≤ 50 ms for the polyglot fixture) belong to the budgets stage.
}

// TestRNIX027ResolveNixShape: resolve.nix is embedded, ≤ 60 lines, takes the documented arguments
// and holds no build logic.
func TestRNIX027ResolveNixShape(t *testing.T) {
	parity.Covers(t, "R-NIX-027")
	p := filepath.Join(harness.Root(t), "pkg", "engine", "nixidx", "resolve.nix")
	b, err := os.ReadFile(p)
	if err != nil {
		harness.RedFeature(t, "pkg/engine/nixidx/resolve.nix does not exist yet")
		return
	}
	text := string(b)
	if n := strings.Count(strings.TrimRight(text, "\n"), "\n") + 1; n > 60 {
		t.Errorf("resolve.nix has %d lines, want ≤ 60", n)
	}
	for _, arg := range []string{"src", "system", "attrs", "names", "allowUnfree", "permittedInsecure"} {
		if !strings.Contains(text, arg) {
			t.Errorf("resolve.nix does not mention the argument %q", arg)
		}
	}
	for _, field := range []string{"missing", "broken", "insecure", "knownVulnerabilities", "unfree", "available", "version", "drvPath", "outputs", "evalRefused"} {
		if !strings.Contains(text, field) {
			t.Errorf("resolve.nix does not return the field %q", field)
		}
	}
	for _, bad := range []string{"mkDerivation", "buildPhase", "runCommand", "writeText", "fetchurl"} {
		if strings.Contains(text, bad) {
			t.Errorf("resolve.nix contains build logic (%s)", bad)
		}
	}
}

// TestRNIX027CachedAsksSubstituters: Index.Cached sends one HEAD <substituter>/<hash>.narinfo per
// configured substituter and remembers the answer for the run.
func TestRNIX027CachedAsksSubstituters(t *testing.T) {
	parity.Covers(t, "R-NIX-027")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	const hit = "/nix/store/0123456789abcdfghijklmnpqrsvwxyz-present-1.0"
	const miss = "/nix/store/zyxwvsrqpnmlkjihgfdcba9876543210-absent-1.0"
	srv := newNarinfoServer("0123456789abcdfghijklmnpqrsvwxyz")
	defer srv.Close()
	st, err := store.Open(ctx, store.Config{Home: filepath.Join(t.TempDir(), "bobhome"), Substituters: []store.Substituter{{URL: srv.URL, PublicKey: "test-1:AAAA"}}})
	if notImplemented(t, err, "store.Open") {
		return
	}
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	src, _ := filepath.Abs(filepath.Join("testdata", "fakepkgs"))
	idx, err := nixidx.Open(ctx, st, nixidx.Config{Source: src, Rev: "fake-rev-2", System: hostSystem(t)})
	if err != nil {
		t.Fatalf("nixidx.Open: %v", err)
	}
	if ok, err := idx.Cached(ctx, hit); err != nil || !ok {
		t.Errorf("Cached(present) = %v, %v", ok, err)
	}
	if ok, err := idx.Cached(ctx, miss); err != nil || ok {
		t.Errorf("Cached(absent) = %v, %v", ok, err)
	}
	n := srv.count()
	if n < 2 {
		t.Errorf("%d requests reached the substituter for two paths", n)
	}
	srv.mu.Lock()
	for req, c := range srv.heads {
		if !strings.HasPrefix(req, "HEAD ") || c != 1 {
			t.Errorf("request %q seen %d times, want one HEAD per path", req, c)
		}
	}
	srv.mu.Unlock()
	_, _ = idx.Cached(ctx, hit)
	_, _ = idx.Cached(ctx, miss)
	if srv.count() != n {
		t.Errorf("repeated Cached calls asked the substituter again (%d → %d requests)", n, srv.count())
	}
}
