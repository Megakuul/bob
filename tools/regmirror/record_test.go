package regmirror

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestNpmRecord(t *testing.T) {
	var hits atomic.Int32
	accepts := map[string]string{}
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		accepts[r.URL.Path] = r.Header.Get("Accept")
		switch r.URL.Path {
		case "/@types/node":
			_, _ = w.Write([]byte(`{"name":"@types/node","versions":{"24.0.0":{"dist":{"tarball":"https://registry.npmjs.org/@types/node/-/node-24.0.0.tgz"}}}}`))
		case "/esbuild/0.25.0":
			_, _ = w.Write([]byte(`{"name":"esbuild","version":"0.25.0","os":["linux"],"dist":{"tarball": "https://registry.npmjs.org/esbuild/-/esbuild-0.25.0.tgz"}}`))
		case "/zod/-/zod-4.0.0.tgz":
			_, _ = w.Write([]byte("TARBALL"))
		case "/broken":
			_, _ = w.Write([]byte("<html>"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer up.Close()
	old := npmUpstreamURL
	npmUpstreamURL = up.URL + "/"
	defer func() { npmUpstreamURL = old }()
	man, blobs := t.TempDir(), t.TempDir()
	srv := httptest.NewServer(New(Config{Manifests: man, Blobs: blobs, RecordNpm: true}))
	defer srv.Close()

	for i := 0; i < 2; i++ {
		status, body, _ := get(t, srv, "/npm/@types%2fnode")
		if status != 200 || !strings.Contains(body, `"tarball":"`+srv.URL+`/npm/@types/node/-/node-24.0.0.tgz"`) {
			t.Fatalf("packument = %d %q", status, body)
		}
	}
	if !strings.HasPrefix(accepts["/@types/node"], "application/vnd.npm.install-v1+json") {
		t.Errorf("packument fetched with Accept %q, want the abbreviated form", accepts["/@types/node"])
	}
	b, err := os.ReadFile(filepath.Join(man, "npm", "packuments", "@types%2fnode.json"))
	if err != nil || !strings.Contains(string(b), `"tarball":"@REGISTRY@/@types/node/-/node-24.0.0.tgz"`) {
		t.Errorf("recorded packument = %q, %v", b, err)
	}
	if status, body, _ := get(t, srv, "/npm/esbuild/0.25.0"); status != 200 || !strings.Contains(body, `"os":["linux"]`) || !strings.Contains(body, srv.URL+"/npm/esbuild/-/") {
		t.Errorf("per-version document = %d %q", status, body)
	}
	if accepts["/esbuild/0.25.0"] != "application/json" {
		t.Errorf("per-version document fetched with Accept %q", accepts["/esbuild/0.25.0"])
	}
	if _, err := os.Stat(filepath.Join(man, "npm", "versions", "esbuild", "0.25.0.json")); err != nil {
		t.Errorf("per-version document not recorded: %v", err)
	}
	if status, body, _ := get(t, srv, "/npm/zod/-/zod-4.0.0.tgz"); status != 200 || body != "TARBALL" {
		t.Errorf("tarball = %d %q", status, body)
	}
	if b, err := os.ReadFile(filepath.Join(blobs, "npm", "zod", "-", "zod-4.0.0.tgz")); err != nil || string(b) != "TARBALL" {
		t.Errorf("recorded tarball = %q, %v", b, err)
	}
	before := hits.Load()
	get(t, srv, "/npm/zod/-/zod-4.0.0.tgz")
	if hits.Load() != before {
		t.Errorf("a recorded tarball was fetched again")
	}
	if status, _, _ := get(t, srv, "/npm/nope"); status != 404 {
		t.Errorf("a package upstream does not have = %d, want 404", status)
	}
	if status, _, _ := get(t, srv, "/npm/broken"); status != http.StatusBadGateway {
		t.Errorf("a non-JSON upstream document = %d, want 502", status)
	}
	if _, err := os.Stat(filepath.Join(man, "npm", "packuments", "broken.json")); err == nil {
		t.Errorf("a non-JSON upstream document was recorded")
	}
}

func TestOpamRecord(t *testing.T) {
	commit := strings.Repeat("a", 40)
	hash := "ab" + strings.Repeat("c", 62)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repo/" + commit + ".tar.gz":
			_, _ = w.Write([]byte("REPO"))
		case "/cache/sha256/ab/" + hash:
			_, _ = w.Write([]byte("ARCHIVE"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer up.Close()
	oldRepo, oldCache := opamRepoUpstreamURL, opamCacheUpstreamURL
	opamRepoUpstreamURL, opamCacheUpstreamURL = up.URL+"/repo/", up.URL+"/cache/"
	defer func() { opamRepoUpstreamURL, opamCacheUpstreamURL = oldRepo, oldCache }()
	blobs := t.TempDir()
	srv := httptest.NewServer(New(Config{Manifests: t.TempDir(), Blobs: blobs, RecordOpam: true}))
	defer srv.Close()
	if status, body, _ := get(t, srv, "/opam/repository/"+commit+".tar.gz"); status != 200 || body != "REPO" {
		t.Errorf("repository = %d %q", status, body)
	}
	if status, body, _ := get(t, srv, "/opam/cache/sha256/ab/"+hash); status != 200 || body != "ARCHIVE" {
		t.Errorf("cache entry = %d %q", status, body)
	}
	if _, err := os.Stat(filepath.Join(blobs, "opam", "cache", "sha256", "ab", hash)); err != nil {
		t.Errorf("cache entry not recorded: %v", err)
	}
	if status, _, _ := get(t, srv, "/opam/cache/sha256/ab/ab"+strings.Repeat("d", 62)); status != 404 {
		t.Errorf("missing cache entry = %d, want 404", status)
	}
}

func TestUpdateIndex(t *testing.T) {
	dir := t.TempDir()
	day1 := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	if err := UpdateIndex(dir, day1, true, ""); err != nil {
		t.Fatal(err)
	}
	commit := strings.Repeat("f", 40)
	if err := UpdateIndex(dir, day1.Add(48*time.Hour), true, commit); err != nil {
		t.Fatal(err)
	}
	var idx Index
	b, _ := os.ReadFile(filepath.Join(dir, "index.json"))
	if err := json.Unmarshal(b, &idx); err != nil {
		t.Fatal(err)
	}
	if idx.V != 1 || idx.Captured != "2026-10-22" || idx.Npm.Snapshot != "2026-10-22" || idx.Opam.Commit != commit {
		t.Errorf("index.json = %s", b)
	}
	if err := UpdateIndex(dir, day1, false, "HEAD"); err == nil {
		t.Errorf("a non-hex opam commit was accepted")
	}
}
