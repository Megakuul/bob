package worker

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"net/http"
	"net/http/cgi"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	"github.com/megakuul/bob/internal/fleet"
	workerv1 "github.com/megakuul/bob/internal/gen/bob/worker/v1"
	"github.com/megakuul/bob/internal/gen/bob/worker/v1/workerv1connect"
)

// checkoutServer answers GetCheckoutCredential only for a proof of possession signed over
// the path the orchestrator verifies (internal/api: "/api/" + method).
type checkoutServer struct {
	workerv1connect.UnimplementedWorkerControlServiceHandler
	id   *Identity
	plan fleet.CheckoutPlain
}

func (s *checkoutServer) GetCheckoutCredential(_ context.Context, req *connect.Request[workerv1.GetCheckoutCredentialRequest]) (*connect.Response[workerv1.GetCheckoutCredentialResponse], error) {
	ct, err := fleet.SealCheckout(s.id.suite, s.id.kemPub, s.id.ID(), req.Msg.GetJobId(), req.Msg.GetEpoch(), s.plan)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&workerv1.GetCheckoutCredentialResponse{SealedCheckout: ct}), nil
}

// TestCheckoutPoPPath: the worker signs its proof of possession over "/api/<service>/<method>"
// (I-B22: it signed "/api//bob.worker.v1…" and every git job failed "proof of possession
// invalid").
func TestCheckoutPoPPath(t *testing.T) {
	id, err := NewIdentity(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	const token = "bwt1.claims.sig"
	const want = "/api/bob.worker.v1.WorkerControlService/GetCheckoutCredential"
	impl := &checkoutServer{id: id, plan: fleet.CheckoutPlain{CloneURL: "https://forge.example/r.git", Token: "t"}}
	path, h := workerv1connect.NewWorkerControlServiceHandler(impl)
	mux := http.NewServeMux()
	var seen []string
	mux.Handle("/api"+path, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.URL.Path)
		ts, nonce, sig, err := fleet.ParsePoP(r.Header.Get("Bob-PoP"))
		pub := id.sig.Public().(ed25519.PublicKey)
		if err != nil || r.Header.Get("Authorization") != "Bob-Worker "+token ||
			!ed25519.Verify(pub, fleet.PoPMessage(http.MethodPost, want, ts, nonce, token), sig) {
			http.Error(w, "proof of possession invalid", http.StatusUnauthorized)
			return
		}
		http.StripPrefix("/api", h).ServeHTTP(w, r)
	}))
	srv := httptest.NewUnstartedServer(mux)
	srv.Config.Protocols = new(http.Protocols)
	srv.Config.Protocols.SetHTTP1(true)
	srv.Config.Protocols.SetUnencryptedHTTP2(true)
	srv.Start()
	defer srv.Close()
	p := &Process{cfg: ProcessConfig{Orchestrators: []string{srv.URL + "/"}, Now: time.Now}, id: id, tok: &Token{raw: token}}
	co, err := p.checkout(context.Background(), Claim{JobID: "b.j0001", Epoch: 3})
	if err != nil {
		t.Fatalf("checkout: %v (paths %v)", err, seen)
	}
	if co.CloneURL != impl.plan.CloneURL || !slices.Equal(seen, []string{want}) {
		t.Errorf("checkout %+v via %v", co, seen)
	}
}

// TestGitTrustsWorkerCAs: the checkout's git gets the worker's CA bundle (SSL_CERT_FILE, also
// as GIT_SSL_CAINFO) and nothing else from the worker's environment, and a fetch from a forge
// with a private CA succeeds only with it (I-B22: Forgejo's self-signed CA).
func TestGitTrustsWorkerCAs(t *testing.T) {
	t.Setenv("SSL_CERT_FILE", "/etc/bob/ca.pem")
	t.Setenv("SSL_CERT_DIR", "/etc/bob/certs")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "worker-secret")
	env := gitEnv("/work/src")
	for _, w := range []string{"SSL_CERT_FILE=/etc/bob/ca.pem", "GIT_SSL_CAINFO=/etc/bob/ca.pem", "SSL_CERT_DIR=/etc/bob/certs", "HOME=/work/src", "GIT_CONFIG_NOSYSTEM=1"} {
		if !slices.Contains(env, w) {
			t.Errorf("git environment %v lacks %s", env, w)
		}
	}
	if strings.Contains(strings.Join(env, "\n"), "worker-secret") {
		t.Errorf("git environment leaks worker credentials: %v", env)
	}
	t.Setenv("SSL_CERT_FILE", "")
	t.Setenv("SSL_CERT_DIR", "")
	for _, e := range gitEnv("/w") {
		if strings.HasPrefix(e, "SSL_CERT") || strings.HasPrefix(e, "GIT_SSL_CAINFO") {
			t.Errorf("unset CA variables passed: %s", e)
		}
	}

	// real git over HTTPS with a private CA (git http-backend behind httptest's TLS server)
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
	srv := httptest.NewTLSServer(&cgi.Handler{Path: backend,
		Env: []string{"GIT_PROJECT_ROOT=" + filepath.Join(dir, "srv"), "GIT_HTTP_EXPORT_ALL=1"}})
	defer srv.Close()
	caFile := filepath.Join(dir, "ca.pem")
	if err := os.WriteFile(caFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw}), 0o644); err != nil {
		t.Fatal(err)
	}
	co := fleet.CheckoutPlain{CloneURL: srv.URL + "/r.git", Username: "bob", Token: "checkout-token"}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	if err := gitFetch(ctx, filepath.Join(dir, "without"), co, sha); err == nil {
		t.Fatalf("fetch from a private CA without the worker's CA bundle succeeded")
	} else if strings.Contains(err.Error(), "checkout-token") {
		t.Errorf("error leaks the checkout token: %v", err)
	}
	t.Setenv("SSL_CERT_FILE", caFile)
	dst := filepath.Join(dir, "with")
	if err := gitFetch(ctx, dst, co, sha); err != nil {
		t.Fatalf("fetch with the worker's CA bundle: %v", err)
	}
	if b, err := os.ReadFile(filepath.Join(dst, "bob.go")); err != nil || string(b) != "package main\n" {
		t.Errorf("checked out bob.go: %q %v", b, err)
	}
}

// TestRedactURLs: error texts lose URL credentials before they are logged or persisted in the
// job result (§06 Job.error).
func TestRedactURLs(t *testing.T) {
	for in, want := range map[string]string{
		"git fetch: exit status 128: fatal: unable to access 'https://bob:s3cr3t@forge/r.git/'": "git fetch: exit status 128: fatal: unable to access 'https://[redacted]@forge/r.git/'",
		"upload https://rustfs:9000/bucket failed":                                              "upload https://rustfs:9000/bucket failed",
		"BOB-E-2003 //cmd/api:api: header foo.h not found":                                      "BOB-E-2003 //cmd/api:api: header foo.h not found",
	} {
		if got := redactURLs(in); got != want {
			t.Errorf("redactURLs(%q) = %q, want %q", in, got, want)
		}
	}
}
