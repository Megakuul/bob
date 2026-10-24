package worker

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/cgi"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/megakuul/bob/internal/cache"
	"github.com/megakuul/bob/internal/fleet"
	"github.com/megakuul/bob/internal/promote"
	"github.com/megakuul/bob/internal/store"
	"github.com/megakuul/bob/pkg/engine/nar"
)

// TestStageSkippedCarriesUpstreamNarinfo (I-R2 F3): a path the upstream cache serves is
// skipped together with the upstream's narinfo (the orchestrator checks its signature); an
// answer that is not that path's narinfo does not make a path skipped.
func TestStageSkippedCarriesUpstreamNarinfo(t *testing.T) {
	dir := t.TempDir()
	other := "/nix/store/1123456789abcdfghijklmnpqrsvwxyz-other"
	var infos []PathInfo
	for _, sp := range []string{testPath, other} {
		host := filepath.Join(dir, "store", "store", strings.TrimPrefix(sp, "/nix/store/"))
		if err := os.MkdirAll(host, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(host, "f"), []byte(sp+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		sum, size, err := nar.Hash(host, nil)
		if err != nil {
			t.Fatal(err)
		}
		infos = append(infos, PathInfo{Path: sp, NarHash: "sha256:" + cache.Nix32(sum[:]), NarSize: uint64(size)})
	}
	signed := fmt.Sprintf("StorePath: %s\nURL: nar/x.nar.xz\nCompression: xz\nNarHash: %s\nNarSize: %d\nReferences: \nSig: cache.nixos.org-1:c2ln\n",
		testPath, infos[0].NarHash, infos[0].NarSize)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/" + cache.HashOf(testPath) + ".narinfo":
			fmt.Fprint(w, signed)
		case "/" + cache.HashOf(other) + ".narinfo":
			fmt.Fprint(w, strings.Replace(signed, testPath, "/nix/store/2123456789abcdfghijklmnpqrsvwxyz-x", 1)) // not this path
		default:
			http.NotFound(w, r)
		}
	}))
	defer up.Close()
	work := filepath.Join(dir, "work")
	if err := os.MkdirAll(filepath.Join(work, workOut), 0o755); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(RunResult{Paths: infos})
	if err := os.WriteFile(filepath.Join(work, workOut, resultFile), b, 0o644); err != nil {
		t.Fatal(err)
	}
	art := &memArtifacts{m: map[string][]byte{}}
	p := &Process{store: filepath.Join(dir, "store")}
	p.UseArtifacts(art, up.URL)
	spec := store.JobSpec{V: 1, JobID: "b.j0001", BuildID: "bid", Project: "acme", Staging: "staging/bid/b.j0001/"}
	o := Outcome{Status: "ok"}
	p.stage(context.Background(), Claim{JobID: "b.j0001", Attempt: 1}, spec, work, &o)
	if o.Status != "ok" {
		t.Fatalf("outcome %+v", o)
	}
	staging := "staging/bid/b.j0001/1/"
	c, err := promote.ParseComplete(art.m[o.Complete], "b.j0001", "bid", "acme")
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Skipped) != 1 || c.Skipped[0] != testPath || len(c.Paths) != 1 || c.Paths[0] != other {
		t.Fatalf("skipped %v paths %v; want %s skipped and %s staged", c.Skipped, c.Paths, testPath, other)
	}
	if got := string(art.m[staging+"upstream/"+cache.HashOf(testPath)+".narinfo"]); got != signed {
		t.Errorf("staged upstream narinfo %q, want the upstream's %q", got, signed)
	}
	if _, ok := art.m[staging+"upstream/"+cache.HashOf(other)+".narinfo"]; ok {
		t.Errorf("an upstream narinfo was staged for a path that is not skipped")
	}
}

// TestGitFetchKeepsTokenOffCommandLine (I-R2 F7): the checkout token reaches git only
// through its environment; no git command line carries it (any local process can read
// /proc/<pid>/cmdline), and the forge still receives the Basic credential.
func TestGitFetchKeepsTokenOffCommandLine(t *testing.T) {
	gitBin, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git not installed")
	}
	execPath, err := exec.Command(gitBin, "--exec-path").Output()
	if err != nil {
		t.Skip("git --exec-path: ", err)
	}
	backend := filepath.Join(strings.TrimSpace(string(execPath)), "git-http-backend")
	if _, err := os.Stat(backend); err != nil {
		t.Skip("git-http-backend not installed")
	}
	dir := t.TempDir()
	repo := filepath.Join(dir, "work")
	run := func(args ...string) string {
		t.Helper()
		cmd := exec.Command(gitBin, args...)
		cmd.Env = append(os.Environ(), "HOME="+dir, "GIT_CONFIG_NOSYSTEM=1", "GIT_AUTHOR_NAME=a", "GIT_AUTHOR_EMAIL=a@x",
			"GIT_COMMITTER_NAME=a", "GIT_COMMITTER_EMAIL=a@x")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	run("init", "-q", repo)
	if err := os.WriteFile(filepath.Join(repo, "bob.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("-C", repo, "add", "bob.go")
	run("-C", repo, "commit", "-q", "-m", "init")
	sha := run("-C", repo, "rev-parse", "HEAD")
	run("clone", "-q", "--bare", repo, filepath.Join(dir, "srv", "r.git"))
	run("-C", filepath.Join(dir, "srv", "r.git"), "config", "uploadpack.allowAnySHA1InWant", "true")
	const token = "checkout-token-7f3e"
	var mu sync.Mutex
	var auths []string
	cgiH := &cgi.Handler{Path: backend, Env: []string{"GIT_PROJECT_ROOT=" + filepath.Join(dir, "srv"), "GIT_HTTP_EXPORT_ALL=1"}}
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		auths = append(auths, r.Header.Get("Authorization"))
		mu.Unlock()
		cgiH.ServeHTTP(w, r)
	}))
	defer srv.Close()
	caFile := filepath.Join(dir, "ca.pem")
	if err := os.WriteFile(caFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw}), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SSL_CERT_FILE", caFile)
	// a git in front of the real one that records every command line it gets
	wrap := filepath.Join(dir, "bin")
	argv := filepath.Join(dir, "argv.log")
	if err := os.MkdirAll(wrap, 0o755); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> " + argv + "\nexec " + gitBin + " \"$@\"\n"
	if err := os.WriteFile(filepath.Join(wrap, "git"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", wrap+string(os.PathListSeparator)+os.Getenv("PATH"))
	co := fleet.CheckoutPlain{CloneURL: srv.URL + "/r.git", Username: "bob", Token: token}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	dst := filepath.Join(dir, "dst")
	if err := gitFetch(ctx, dst, co, sha); err != nil {
		t.Fatalf("fetch: %v", err)
	}
	lines, err := os.ReadFile(argv)
	if err != nil || !strings.Contains(string(lines), "fetch") {
		t.Fatalf("the wrapper saw no fetch (%v): %s", err, lines)
	}
	b64 := base64.StdEncoding.EncodeToString([]byte("bob:" + token))
	if strings.Contains(string(lines), token) || strings.Contains(string(lines), b64) {
		t.Errorf("a git command line carries the checkout token:\n%s", lines)
	}
	mu.Lock()
	defer mu.Unlock()
	sent := false
	for _, a := range auths {
		sent = sent || a == "Basic "+b64
	}
	if !sent {
		t.Errorf("the forge never received the Basic credential: %q", auths)
	}
}
