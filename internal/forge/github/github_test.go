package github

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/megakuul/bob/internal/forge"
)

// TestMain makes the process trust httptest's TLS certificate before the first TLS use (the
// adapter has no CA option; Go reads SSL_CERT_FILE once).
func TestMain(m *testing.M) {
	srv := httptest.NewTLSServer(http.NotFoundHandler())
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw})
	srv.Close()
	dir, err := os.MkdirTemp("", "bob-github-test-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	if err := os.WriteFile(filepath.Join(dir, "ca.pem"), pemBytes, 0o644); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	os.Setenv("SSL_CERT_FILE", filepath.Join(dir, "ca.pem"))
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

const (
	appID     = 4242
	install   = 77
	repoID    = 1296269
	fullName  = "octocat/Hello-World"
	commitSHA = "6dcb09b5b57875f334f61aebed695e2e4193db5e"
)

type tokenCall struct {
	RepositoryIDs []int64           `json:"repository_ids"`
	Repositories  []string          `json:"repositories"`
	Permissions   map[string]string `json:"permissions"`
}

// fakeGitHub answers the documented GitHub endpoints the adapter uses and records the calls.
type fakeGitHub struct {
	*httptest.Server
	pub *rsa.PublicKey

	mu         sync.Mutex
	tokens     []tokenCall
	statuses   []map[string]any
	statusAuth []string
	revoked    []string
	reject     map[string]bool // installation tokens the status endpoint refuses (401)
	jwtRepo    bool            // GET /repos/{o}/{n} accepts the App JWT
	foreignIDs bool            // installation tokens report a repository other than the requested
	seq        int
}

func newFake(t *testing.T, pub *rsa.PublicKey) *fakeGitHub {
	f := &fakeGitHub{pub: pub, reject: map[string]bool{}}
	f.Server = httptest.NewTLSServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.Close)
	return f
}

// validJWT verifies the RS256 App JWT and its issuer.
func (f *fakeGitHub) validJWT(auth string) bool {
	tok, ok := strings.CutPrefix(auth, "Bearer ")
	parts := strings.Split(tok, ".")
	if !ok || len(parts) != 3 {
		return false
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return false
	}
	sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if rsa.VerifyPKCS1v15(f.pub, crypto.SHA256, sum[:], sig) != nil {
		return false
	}
	raw, _ := base64.RawURLEncoding.DecodeString(parts[1])
	var c struct {
		Iss      string
		Exp, Iat int64
	}
	return json.Unmarshal(raw, &c) == nil && c.Iss == fmt.Sprint(appID) && c.Exp-c.Iat <= 600
}

func (f *fakeGitHub) repoJSON(id int64) map[string]any {
	return map[string]any{"id": id, "full_name": fullName, "clone_url": "https://github.test/" + fullName + ".git",
		"html_url": "https://github.test/" + fullName, "default_branch": "main", "private": true}
}

func (f *fakeGitHub) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	auth := r.Header.Get("Authorization")
	deny := func() {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"message":"Bad credentials"}`)
	}
	switch {
	case r.Method == http.MethodPost && r.URL.Path == fmt.Sprintf("/app/installations/%d/access_tokens", install):
		if !f.validJWT(auth) {
			deny()
			return
		}
		var c tokenCall
		_ = json.NewDecoder(r.Body).Decode(&c)
		f.tokens = append(f.tokens, c)
		f.seq++
		var repos []map[string]any
		for _, id := range c.RepositoryIDs {
			if f.foreignIDs {
				id++
			}
			repos = append(repos, f.repoJSON(id))
		}
		for _, n := range c.Repositories {
			if "octocat/"+n == fullName {
				repos = append(repos, f.repoJSON(repoID))
			}
		}
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{"token": fmt.Sprintf("ghs_%d", f.seq),
			"expires_at": time.Now().Add(time.Hour).UTC().Format(time.RFC3339), "repositories": repos})
	case r.Method == http.MethodGet && r.URL.Path == "/repos/"+fullName:
		if !f.jwtRepo || !f.validJWT(auth) {
			w.WriteHeader(http.StatusForbidden)
			_, _ = io.WriteString(w, `{"message":"Resource not accessible by integration"}`)
			return
		}
		_ = json.NewEncoder(w).Encode(f.repoJSON(repoID))
	case r.Method == http.MethodPost && r.URL.Path == "/repos/"+fullName+"/statuses/"+commitSHA:
		tok, _ := strings.CutPrefix(auth, "Bearer ")
		if !strings.HasPrefix(tok, "ghs_") || f.reject[tok] {
			deny()
			return
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.statuses = append(f.statuses, body)
		f.statusAuth = append(f.statusAuth, tok)
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(body)
	case r.Method == http.MethodDelete && r.URL.Path == "/installation/token":
		tok, _ := strings.CutPrefix(auth, "Bearer ")
		f.revoked = append(f.revoked, tok)
		w.WriteHeader(http.StatusNoContent)
	default:
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, `{"message":"Not Found"}`)
	}
}

func newAdapter(t *testing.T) (forge.Adapter, *fakeGitHub) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	f := newFake(t, &key.PublicKey)
	p := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	a, err := New(Config{BaseURL: f.URL, AppID: appID, InstallationID: install, PrivateKeyPEM: forge.NewSecret(string(p)),
		WebhookSecret: forge.NewSecret("hook-secret")})
	if err != nil {
		t.Fatal(err)
	}
	return a, f
}

var repo = forge.Repo{Forge: forge.GitHub, ID: repoID, FullName: fullName, CloneURL: "https://github.test/" + fullName + ".git"}

func TestStatusesMappingAndTokenCache(t *testing.T) {
	a, f := newAdapter(t)
	ctx := context.Background()
	cases := []struct {
		state forge.State
		want  string
	}{
		{forge.StatePending, "pending"}, {forge.StateRunning, "pending"}, {forge.StateSuccess, "success"},
		{forge.StateFailure, "failure"}, {forge.StateError, "error"}, {forge.StateCancelled, "error"},
	}
	for _, c := range cases {
		if err := a.SetStatus(ctx, repo, forge.Status{SHA: commitSHA, Context: "bob/ci", State: c.state,
			Description: "#4 superseded by #5", TargetURL: "https://bob.test/p/x/builds/b"}); err != nil {
			t.Fatalf("SetStatus %s: %v", c.state, err)
		}
	}
	for i, c := range cases {
		if s := f.statuses[i]; s["state"] != c.want || s["context"] != "bob/ci" || s["target_url"] != "https://bob.test/p/x/builds/b" {
			t.Errorf("status %d: %v, want state %s", i, s, c.want)
		}
	}
	if d := fmt.Sprint(f.statuses[5]["description"]); !strings.Contains(d, "cancelled") || !strings.Contains(d, "superseded by #5") {
		t.Errorf("cancelled description %q, want the cancelled marker and the build text", d)
	}
	// one statuses:write token for the repository, reused for every status
	if len(f.tokens) != 1 || fmt.Sprint(f.tokens[0].RepositoryIDs) != fmt.Sprint([]int64{repoID}) ||
		len(f.tokens[0].Permissions) != 1 || f.tokens[0].Permissions["statuses"] != "write" {
		t.Fatalf("installation token calls %+v, want one limited to repository %d with statuses:write", f.tokens, repoID)
	}
	long := strings.Repeat("é", 200)
	if err := a.SetStatus(ctx, repo, forge.Status{SHA: commitSHA, Context: "bob/ci", State: forge.StateFailure, Description: long}); err != nil {
		t.Fatal(err)
	}
	if d := fmt.Sprint(f.statuses[6]["description"]); len([]rune(d)) > 140 {
		t.Errorf("description of %d runes, want <= 140", len([]rune(d)))
	}

	// a refused token is dropped and the next status mints a fresh one
	f.reject[f.statusAuth[0]] = true
	if err := a.SetStatus(ctx, repo, forge.Status{SHA: commitSHA, Context: "bob/ci", State: forge.StateSuccess}); !errors.Is(err, forge.ErrAuth) {
		t.Fatalf("refused token: %v, want ErrAuth", err)
	}
	if err := a.SetStatus(ctx, repo, forge.Status{SHA: commitSHA, Context: "bob/ci", State: forge.StateSuccess}); err != nil {
		t.Fatalf("status after the refusal: %v", err)
	}
	if len(f.tokens) != 2 || f.statusAuth[len(f.statusAuth)-1] == f.statusAuth[0] {
		t.Errorf("token calls %d, last status token %s: want a second, different token", len(f.tokens), f.statusAuth[len(f.statusAuth)-1])
	}
	if err := a.SetStatus(ctx, forge.Repo{FullName: fullName}, forge.Status{SHA: commitSHA, Context: "bob/ci", State: forge.StateSuccess}); err == nil {
		t.Errorf("repository without id accepted")
	}
}

func TestCheckoutAndRevoke(t *testing.T) {
	a, f := newAdapter(t)
	ctx := context.Background()
	co, err := a.IssueCheckout(ctx, forge.CheckoutRequest{Repo: repo, JobID: "j-1", TTL: 10 * time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	if co.Username != "x-access-token" || !co.ForgeExpires || co.Handle == "" || !strings.HasPrefix(co.Token.Reveal(), "ghs_") {
		t.Errorf("checkout %+v", co)
	}
	if c := f.tokens[0]; fmt.Sprint(c.RepositoryIDs) != fmt.Sprint([]int64{repoID}) || len(c.Permissions) != 1 || c.Permissions["contents"] != "read" {
		t.Errorf("checkout token request %+v, want repository %d with contents:read only", c, repoID)
	}
	// records keep only the handle: revoking by handle uses the token this process minted
	if err := a.RevokeCheckout(ctx, forge.Checkout{Handle: co.Handle}); err != nil {
		t.Fatal(err)
	}
	if len(f.revoked) != 1 || f.revoked[0] != co.Token.Reveal() {
		t.Errorf("revoked %v, want the checkout token", f.revoked)
	}
	// a handle of another process: nothing to revoke with, GitHub expires it
	if err := a.RevokeCheckout(ctx, forge.Checkout{Handle: "unknown"}); err != nil || len(f.revoked) != 1 {
		t.Errorf("unknown handle: %v, revoked %v", err, f.revoked)
	}
	f.foreignIDs = true
	if _, err := a.IssueCheckout(ctx, forge.CheckoutRequest{Repo: repo, JobID: "j-2"}); err == nil || !strings.Contains(err.Error(), "not limited") {
		t.Errorf("token for another repository: %v, want refused", err)
	}
}

func TestLookupRepo(t *testing.T) {
	a, f := newAdapter(t)
	ctx := context.Background()
	f.jwtRepo = true
	r, err := a.LookupRepo(ctx, fullName)
	if err != nil {
		t.Fatal(err)
	}
	if r.ID != repoID || r.DefaultBranch != "main" || !r.Private || r.CloneURL != "https://github.test/"+fullName+".git" || len(f.tokens) != 0 {
		t.Errorf("repo %+v (token calls %d), want it from the JWT call alone", r, len(f.tokens))
	}
	// the JWT is refused on the repository endpoint: a metadata:read token named for the
	// repository carries the record and is revoked at once
	f.jwtRepo = false
	r, err = a.LookupRepo(ctx, fullName)
	if err != nil || r.ID != repoID {
		t.Fatalf("fallback lookup: %+v %v", r, err)
	}
	if c := f.tokens[0]; fmt.Sprint(c.Repositories) != "[Hello-World]" || c.Permissions["metadata"] != "read" || len(c.Permissions) != 1 {
		t.Errorf("metadata token request %+v", c)
	}
	if len(f.revoked) != 1 {
		t.Errorf("metadata token not revoked: %v", f.revoked)
	}
	if _, err := a.LookupRepo(ctx, "octocat/missing"); !errors.Is(err, forge.ErrNotFound) {
		t.Errorf("missing repository: %v, want ErrNotFound", err)
	}
	if _, err := a.LookupRepo(ctx, "no-slash"); !errors.Is(err, forge.ErrNotFound) {
		t.Errorf("malformed name: %v, want ErrNotFound", err)
	}
	if err := a.EnsureWebhook(ctx, r, "https://bob.test/hooks/gh", forge.NewSecret("x")); !errors.Is(err, forge.ErrUnsupported) {
		t.Errorf("EnsureWebhook: %v, want ErrUnsupported", err)
	}
}
