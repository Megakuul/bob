package forge

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/megakuul/bob/internal/forge"
	"github.com/megakuul/bob/internal/forge/github"
	"github.com/megakuul/bob/pkg/bob/diag"
	"github.com/megakuul/bob/tests/harness"
	"github.com/megakuul/bob/tests/parity"
	"github.com/megakuul/bob/tests/serverenv/forgefix"
)

const (
	ghRepoID   = 1296269
	ghAppID    = 4242
	ghInstall  = 77
	ghFullName = "octocat/Hello-World"
)

// githubReplay answers like GitHub's documented API (fixtures github/api) and records what the
// adapter sent.
type githubReplay struct {
	*httptest.Server
	pub *rsa.PublicKey
	t   *testing.T

	mu       sync.Mutex
	tokens   []tokenRequest
	statuses []statusRequest
	revoked  []string
	failAuth bool
	tokenSeq int
}

type tokenRequest struct {
	jwtErr string
	body   struct {
		RepositoryIDs []int64           `json:"repository_ids"`
		Permissions   map[string]string `json:"permissions"`
	}
}

type statusRequest struct {
	path, auth string
	body       struct{ State, Description, Context, TargetURL string }
}

func newGitHubReplay(t *testing.T, pub *rsa.PublicKey) *githubReplay {
	g := &githubReplay{pub: pub, t: t}
	api, err := forgefix.Dir("github", "api")
	if err != nil {
		t.Fatalf("harness error: %v", err)
	}
	read := func(name string) []byte {
		b, err := os.ReadFile(api + "/" + name)
		if err != nil {
			t.Fatalf("harness error: %v", err)
		}
		return b
	}
	tokenDoc, statusDoc, installDoc := read("installation-token.json"), read("status.json"), read("installation.json")
	mux := http.NewServeMux()
	mux.HandleFunc(fmt.Sprintf("POST /app/installations/%d/access_tokens", ghInstall), func(w http.ResponseWriter, r *http.Request) {
		var tr tokenRequest
		tr.jwtErr = g.checkJWT(r.Header.Get("Authorization"))
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &tr.body)
		g.mu.Lock()
		g.tokens = append(g.tokens, tr)
		g.tokenSeq++
		seq, fail := g.tokenSeq, g.failAuth
		g.mu.Unlock()
		if fail || tr.jwtErr != "" {
			http.Error(w, `{"message":"Bad credentials"}`, http.StatusUnauthorized)
			return
		}
		var doc map[string]any
		_ = json.Unmarshal(tokenDoc, &doc)
		doc["token"] = fmt.Sprintf("ghs_replay%04d", seq)
		doc["expires_at"] = time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(doc)
	})
	mux.HandleFunc(fmt.Sprintf("GET /app/installations/%d", ghInstall), func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(installDoc)
	})
	mux.HandleFunc("POST /repos/"+ghFullName+"/statuses/{sha}", func(w http.ResponseWriter, r *http.Request) {
		var s statusRequest
		s.path, s.auth = r.URL.Path, r.Header.Get("Authorization")
		b, _ := io.ReadAll(r.Body)
		var raw map[string]string
		_ = json.Unmarshal(b, &raw)
		s.body.State, s.body.Description, s.body.Context, s.body.TargetURL = raw["state"], raw["description"], raw["context"], raw["target_url"]
		g.mu.Lock()
		g.statuses = append(g.statuses, s)
		g.mu.Unlock()
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write(statusDoc)
	})
	mux.HandleFunc("GET /repos/"+ghFullName, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"id": ghRepoID, "full_name": ghFullName, "private": true,
			"default_branch": "main", "clone_url": "https://github.example/" + ghFullName + ".git", "html_url": "https://github.example/" + ghFullName})
	})
	mux.HandleFunc("DELETE /installation/token", func(w http.ResponseWriter, r *http.Request) {
		g.mu.Lock()
		g.revoked = append(g.revoked, r.Header.Get("Authorization"))
		first := len(g.revoked) == 1
		g.mu.Unlock()
		if first {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		http.Error(w, `{"message":"Bad credentials"}`, http.StatusUnauthorized) // already revoked
	})
	g.Server = httptest.NewUnstartedServer(mux)
	g.Server.TLS = &tls.Config{Certificates: []tls.Certificate{replayCert}}
	g.Server.StartTLS()
	t.Cleanup(g.Close)
	return g
}

// checkJWT verifies the App JWT: RS256, iss = app id, iat <= now, exp within 10 minutes.
func (g *githubReplay) checkJWT(auth string) string {
	tok, ok := strings.CutPrefix(auth, "Bearer ")
	if !ok {
		return "no Bearer JWT"
	}
	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		return "not a JWT"
	}
	dec := func(s string) []byte { b, _ := base64.RawURLEncoding.DecodeString(s); return b }
	var hdr struct{ Alg string }
	var claims struct {
		Iss      any
		Iat, Exp int64
	}
	if json.Unmarshal(dec(parts[0]), &hdr) != nil || json.Unmarshal(dec(parts[1]), &claims) != nil {
		return "malformed JWT"
	}
	if hdr.Alg != "RS256" {
		return "alg " + hdr.Alg
	}
	sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if err := rsa.VerifyPKCS1v15(g.pub, crypto.SHA256, sum[:], dec(parts[2])); err != nil {
		return "bad signature"
	}
	if fmt.Sprint(claims.Iss) != fmt.Sprint(ghAppID) {
		return fmt.Sprintf("iss %v", claims.Iss)
	}
	now := time.Now().Unix()
	if claims.Iat > now || claims.Exp <= now || claims.Exp-claims.Iat > 11*60 {
		return fmt.Sprintf("iat/exp %d/%d at %d", claims.Iat, claims.Exp, now)
	}
	return ""
}

func gitHubAdapter(t *testing.T) (forge.Adapter, *githubReplay, bool) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	g := newGitHubReplay(t, &key.PublicKey)
	der, _ := x509.MarshalPKCS8PrivateKey(key)
	a, err := github.New(github.Config{BaseURL: g.URL, AppID: ghAppID, InstallationID: ghInstall,
		PrivateKeyPEM: forge.NewSecret(pemBlock(der)), WebhookSecret: forge.NewSecret(githubSecret)})
	if err != nil {
		var de *diag.Error
		if errors.As(err, &de) && de.Code == diag.CodeNotImplemented {
			harness.RedFeature(t, "github.New")
			return nil, nil, false
		}
		t.Fatalf("github.New: %v", err)
	}
	return a, g, true
}

func TestGitHubReplayStatusAndCheckout(t *testing.T) {
	parity.Covers(t, "R-PIPE-007")
	a, g, ok := gitHubAdapter(t)
	if !ok {
		return
	}
	ctx := t.Context()
	repo, err := a.LookupRepo(ctx, ghFullName)
	if err != nil || repo.ID != ghRepoID || repo.Forge != forge.GitHub || repo.FullName != ghFullName || !strings.HasPrefix(repo.CloneURL, "https://") {
		t.Fatalf("LookupRepo = %+v, %v", repo, err)
	}

	// statuses: all six states, description truncated to 140 runes, one status token for all
	long := strings.Repeat("é", 300)
	sha := strings.Repeat("a", 40)
	wantState := map[forge.State]string{forge.StatePending: "pending", forge.StateRunning: "pending", forge.StateSuccess: "success",
		forge.StateFailure: "failure", forge.StateError: "error", forge.StateCancelled: "error"}
	order := []forge.State{forge.StatePending, forge.StateRunning, forge.StateSuccess, forge.StateFailure, forge.StateError, forge.StateCancelled}
	for _, st := range order {
		err := a.SetStatus(ctx, repo, forge.Status{SHA: sha, Context: "bob/ci", State: st, Description: long, TargetURL: "https://bob.example/builds/1"})
		if err != nil {
			t.Fatalf("SetStatus %s: %v", st, err)
		}
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if len(g.statuses) != len(order) {
		t.Fatalf("%d status requests, want %d", len(g.statuses), len(order))
	}
	for i, s := range g.statuses {
		if s.body.State != wantState[order[i]] || s.body.Context != "bob/ci" || s.body.TargetURL != "https://bob.example/builds/1" {
			t.Errorf("status %s sent as %+v, want state %s", order[i], s.body, wantState[order[i]])
		}
		if n := utf8.RuneCountInString(s.body.Description); n > 140 {
			t.Errorf("status %s description has %d runes, want <= 140", order[i], n)
		}
		if order[i] == forge.StateCancelled && !strings.Contains(s.body.Description+s.body.Context, "cancelled") && !strings.Contains(strings.ToLower(s.body.Description), "cancel") {
			t.Errorf("cancelled status does not say cancelled: %+v", s.body)
		}
		if !strings.HasSuffix(s.path, "/statuses/"+sha) || !strings.Contains(s.auth, "ghs_replay0001") {
			t.Errorf("status %s: path %s auth %q, want the one cached installation token", order[i], s.path, s.auth)
		}
	}
	if len(g.tokens) != 1 {
		t.Errorf("%d installation token requests for 6 statuses, want 1 (status token cached)", len(g.tokens))
	}
	for i, tr := range g.tokens {
		if tr.jwtErr != "" {
			t.Errorf("token request %d: invalid App JWT: %s", i, tr.jwtErr)
		}
		if len(tr.body.RepositoryIDs) != 1 || tr.body.RepositoryIDs[0] != ghRepoID {
			t.Errorf("token request %d: repository_ids %v, want [%d]", i, tr.body.RepositoryIDs, ghRepoID)
		}
	}
	g.mu.Unlock()

	// checkout: a separate token, contents: read only, repository_ids = [id], fixed 1 h
	co, err := a.IssueCheckout(ctx, forge.CheckoutRequest{Repo: repo, JobID: "01job", TTL: 20 * time.Minute})
	g.mu.Lock()
	if err != nil {
		t.Fatalf("IssueCheckout: %v", err)
	}
	last := g.tokens[len(g.tokens)-1]
	if len(g.tokens) != 2 || len(last.body.RepositoryIDs) != 1 || last.body.RepositoryIDs[0] != ghRepoID ||
		len(last.body.Permissions) != 1 || last.body.Permissions["contents"] != "read" {
		t.Errorf("checkout token request %+v (total %d requests), want repository_ids [%d] and permissions {contents: read} only", last.body, len(g.tokens), ghRepoID)
	}
	if last.jwtErr != "" {
		t.Errorf("checkout token request: invalid App JWT: %s", last.jwtErr)
	}
	if co.Username != "x-access-token" || co.Token.Reveal() != "ghs_replay0002" || !co.ForgeExpires || co.Handle == "" || !strings.HasPrefix(co.CloneURL, "https://") {
		t.Errorf("Checkout = %+v", co)
	}
	if d := time.Until(co.ExpiresAt); d < 50*time.Minute || d > 61*time.Minute {
		t.Errorf("Checkout.ExpiresAt in %v, want the fixed 1 h of GitHub installation tokens", d)
	}
	for _, s := range []string{fmt.Sprint(co.Token), fmt.Sprintf("%+v", co), fmt.Sprintf("%#v", co.Token)} {
		if strings.Contains(s, "ghs_replay0002") {
			t.Errorf("the checkout token leaks through formatting: %s", s)
		}
	}
	if b, _ := json.Marshal(co); strings.Contains(string(b), "ghs_replay0002") {
		t.Errorf("the checkout token leaks through JSON: %s", b)
	}
	g.mu.Unlock()

	if err := a.RevokeCheckout(ctx, co); err != nil {
		t.Errorf("RevokeCheckout: %v", err)
	}
	if err := a.RevokeCheckout(ctx, co); err != nil {
		t.Errorf("second RevokeCheckout (the forge answers 401 for an already revoked token): %v, want nil (idempotent)", err)
	}
	if n, err := a.SweepCheckouts(ctx, time.Now()); n != 0 || err != nil {
		t.Errorf("SweepCheckouts = %d, %v, want 0, nil (GitHub expires credentials itself)", n, err)
	}
	if err := a.EnsureWebhook(ctx, repo, "https://bob.example/hooks/x", forge.NewSecret("s")); !errors.Is(err, forge.ErrUnsupported) {
		t.Errorf("EnsureWebhook = %v, want ErrUnsupported (app-level hook)", err)
	}
	g.mu.Lock()
}

func TestGitHubReplayRejectedCredentialsAreErrAuth(t *testing.T) {
	parity.Covers(t, "R-PIPE-007")
	a, g, ok := gitHubAdapter(t)
	if !ok {
		return
	}
	g.mu.Lock()
	g.failAuth = true
	g.mu.Unlock()
	err := a.SetStatus(t.Context(), forge.Repo{Forge: forge.GitHub, ID: ghRepoID, FullName: ghFullName}, forge.Status{
		SHA: strings.Repeat("b", 40), Context: "bob/ci", State: forge.StateSuccess})
	if !errors.Is(err, forge.ErrAuth) {
		t.Errorf("SetStatus with rejected credentials: %v, want ErrAuth", err)
	}
}
