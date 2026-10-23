package gitlab

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
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
	"strconv"
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
	dir, err := os.MkdirTemp("", "bob-gitlab-test-")
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
	token      = "glpat-test-token"
	signingKey = "gitlab-unit-signing-key-0123456789"
	projectID  = 42
	sha        = "0123456789abcdef0123456789abcdef01234567"
)

// fakeGitLab answers the GitLab REST v4 endpoints the adapter uses and records the requests.
type fakeGitLab struct {
	*httptest.Server
	mu        sync.Mutex
	statuses  []map[string]any
	tokens    []map[string]any
	deleted   []string
	hookCalls []string // "METHOD path body"
	hooks     []map[string]any
	status    int    // forced status for every request (0 = normal)
	statusMsg string // message body of a forced status
}

func newFake(t *testing.T) *fakeGitLab {
	f := &fakeGitLab{}
	f.Server = httptest.NewTLSServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.Close)
	return f
}

func (f *fakeGitLab) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	if r.Header.Get("Private-Token") != token {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"message":"401 Unauthorized"}`)
		return
	}
	if f.status != 0 {
		w.WriteHeader(f.status)
		_, _ = io.WriteString(w, f.statusMsg)
		return
	}
	body := map[string]any{}
	if r.Body != nil {
		b, _ := io.ReadAll(r.Body)
		if len(b) > 0 {
			_ = json.Unmarshal(b, &body)
		}
	}
	p := strings.TrimPrefix(r.URL.EscapedPath(), "/api/v4/")
	switch {
	case r.Method == http.MethodPost && p == fmt.Sprintf("projects/%d/statuses/%s", projectID, sha):
		f.statuses = append(f.statuses, body)
		_ = json.NewEncoder(w).Encode(map[string]any{"id": len(f.statuses), "sha": sha, "status": body["state"], "name": body["name"]})
	case r.Method == http.MethodPost && p == fmt.Sprintf("projects/%d/deploy_tokens", projectID):
		f.tokens = append(f.tokens, body)
		_ = json.NewEncoder(w).Encode(map[string]any{"id": 7, "name": body["name"], "username": "gitlab+deploy-token-7",
			"expires_at": body["expires_at"], "token": "gldt-secret", "scopes": body["scopes"]})
	case r.Method == http.MethodDelete && strings.HasPrefix(p, fmt.Sprintf("projects/%d/deploy_tokens/", projectID)):
		id := strings.TrimPrefix(p, fmt.Sprintf("projects/%d/deploy_tokens/", projectID))
		if id != "7" {
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, `{"message":"404 Not Found"}`)
			return
		}
		f.deleted = append(f.deleted, id)
		w.WriteHeader(http.StatusNoContent)
	case r.Method == http.MethodGet && p == "projects/alice%2Fdemo":
		_ = json.NewEncoder(w).Encode(map[string]any{"id": projectID, "path_with_namespace": "alice/demo",
			"http_url_to_repo": "https://gitlab.test/alice/demo.git", "web_url": "https://gitlab.test/alice/demo",
			"default_branch": "main", "visibility": "private"})
	case r.Method == http.MethodGet && p == "projects/alice%2Fplain":
		_ = json.NewEncoder(w).Encode(map[string]any{"id": projectID + 1, "path_with_namespace": "alice/plain",
			"http_url_to_repo": "http://gitlab.test/alice/plain.git", "default_branch": "main", "visibility": "public"})
	case r.Method == http.MethodGet && p == fmt.Sprintf("projects/%d/hooks", projectID):
		_ = json.NewEncoder(w).Encode(f.hooks)
	case (r.Method == http.MethodPost || r.Method == http.MethodPut) && strings.HasPrefix(p, fmt.Sprintf("projects/%d/hooks", projectID)):
		b, _ := json.Marshal(body)
		f.hookCalls = append(f.hookCalls, r.Method+" "+p+" "+string(b))
		_ = json.NewEncoder(w).Encode(map[string]any{"id": 3, "url": body["url"]})
	default:
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, `{"message":"404 Project Not Found"}`)
	}
}

func newAdapter(t *testing.T, f *fakeGitLab, tok string) forge.Adapter {
	t.Helper()
	a, err := New(Config{BaseURL: f.URL, AccessToken: forge.NewSecret(tok),
		SigningToken: forge.NewSecret("whsec_" + base64.StdEncoding.EncodeToString([]byte(signingKey)))})
	if err != nil {
		t.Fatal(err)
	}
	return a
}

var repo = forge.Repo{Forge: forge.GitLab, ID: projectID, FullName: "alice/demo", CloneURL: "https://gitlab.test/alice/demo.git"}

func TestSetStatusMapping(t *testing.T) {
	f := newFake(t)
	a := newAdapter(t, f, token)
	ctx := context.Background()
	cases := []struct {
		state forge.State
		want  string
	}{
		{forge.StatePending, "pending"}, {forge.StateRunning, "running"}, {forge.StateSuccess, "success"},
		{forge.StateFailure, "failed"}, {forge.StateError, "failed"}, {forge.StateCancelled, "canceled"},
	}
	for _, c := range cases {
		s := forge.Status{SHA: sha, Context: "bob/ci", State: c.state, Description: "#3 " + string(c.state), TargetURL: "https://bob.test/p/x/builds/b"}
		if err := a.SetStatus(ctx, repo, s); err != nil {
			t.Fatalf("SetStatus %s: %v", c.state, err)
		}
	}
	if len(f.statuses) != len(cases) {
		t.Fatalf("statuses sent: %d, want %d", len(f.statuses), len(cases))
	}
	for i, c := range cases {
		got := f.statuses[i]
		if got["state"] != c.want || got["name"] != "bob/ci" || got["target_url"] != "https://bob.test/p/x/builds/b" {
			t.Errorf("status %d: %v, want state %s, name bob/ci and the target URL", i, got, c.want)
		}
	}
	if d, _ := f.statuses[5]["description"].(string); !strings.Contains(d, "#3") {
		t.Errorf("cancelled description %q lost the build text", d)
	}
	if err := a.SetStatus(ctx, repo, forge.Status{SHA: sha, Context: "bob/ci", State: "bogus"}); err == nil {
		t.Errorf("unknown state accepted")
	}
}

func TestSetStatusErrors(t *testing.T) {
	f := newFake(t)
	ctx := context.Background()
	s := forge.Status{SHA: sha, Context: "bob/ci", State: forge.StateSuccess, Description: "#1 succeeded"}

	// GitLab refuses a transition to the current state; a repeated report is not an error
	f.status, f.statusMsg = http.StatusBadRequest, `{"message":"Cannot transition status via :run from :running"}`
	if err := newAdapter(t, f, token).SetStatus(ctx, repo, s); err != nil {
		t.Errorf("repeated state: %v, want nil", err)
	}
	f.status, f.statusMsg = http.StatusBadRequest, `{"message":"name is too long"}`
	if err := newAdapter(t, f, token).SetStatus(ctx, repo, s); err == nil {
		t.Errorf("other 400 accepted")
	}
	f.status = 0
	if err := newAdapter(t, f, "wrong").SetStatus(ctx, repo, s); !errors.Is(err, forge.ErrAuth) {
		t.Errorf("rejected token: %v, want ErrAuth", err)
	}
	f.status, f.statusMsg = http.StatusNotFound, `{"message":"404 Project Not Found"}`
	if err := newAdapter(t, f, token).SetStatus(ctx, repo, s); !errors.Is(err, forge.ErrNotFound) {
		t.Errorf("unknown project: %v, want ErrNotFound", err)
	}
}

func TestCheckoutDeployToken(t *testing.T) {
	f := newFake(t)
	a := newAdapter(t, f, token)
	ctx := context.Background()
	before := time.Now()
	co, err := a.IssueCheckout(ctx, forge.CheckoutRequest{Repo: repo, JobID: "j-1", TTL: 10 * time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	if co.Username != "gitlab+deploy-token-7" || co.Token.Reveal() != "gldt-secret" || !co.ForgeExpires ||
		co.CloneURL != repo.CloneURL || co.Handle != strconv.Itoa(projectID)+":7" {
		t.Errorf("checkout %+v", co)
	}
	if co.ExpiresAt.Before(before.Add(9*time.Minute)) || co.ExpiresAt.After(before.Add(11*time.Minute)) {
		t.Errorf("expires %v, want ~10 min from now", co.ExpiresAt)
	}
	tok := f.tokens[0]
	if tok["name"] != "bob-job-j-1" || fmt.Sprint(tok["scopes"]) != "[read_repository]" || tok["expires_at"] == nil {
		t.Errorf("deploy token request %v", tok)
	}
	// TTL above the 1 h cap is capped
	if _, err := a.IssueCheckout(ctx, forge.CheckoutRequest{Repo: repo, JobID: "j-2", TTL: 5 * time.Hour}); err != nil {
		t.Fatal(err)
	}
	exp, err := time.Parse(time.RFC3339, fmt.Sprint(f.tokens[1]["expires_at"]))
	if err != nil || exp.After(time.Now().Add(time.Hour+time.Minute)) {
		t.Errorf("capped expiry %v (%v), want <= 1 h", f.tokens[1]["expires_at"], err)
	}
	if err := a.RevokeCheckout(ctx, co); err != nil || len(f.deleted) != 1 {
		t.Errorf("revoke: %v, deleted %v", err, f.deleted)
	}
	// an already deleted token is gone: idempotent
	if err := a.RevokeCheckout(ctx, forge.Checkout{Handle: strconv.Itoa(projectID) + ":99"}); err != nil {
		t.Errorf("revoke of a missing token: %v, want nil", err)
	}
	if err := a.RevokeCheckout(ctx, forge.Checkout{Handle: "garbage"}); err == nil {
		t.Errorf("garbage handle accepted")
	}
}

func TestLookupRepoAndWebhook(t *testing.T) {
	f := newFake(t)
	a := newAdapter(t, f, token)
	ctx := context.Background()
	r, err := a.LookupRepo(ctx, "alice/demo")
	if err != nil {
		t.Fatal(err)
	}
	if r.ID != projectID || r.FullName != "alice/demo" || r.DefaultBranch != "main" || !r.Private || r.CloneURL != "https://gitlab.test/alice/demo.git" {
		t.Errorf("repo %+v", r)
	}
	if _, err := a.LookupRepo(ctx, "alice/missing"); !errors.Is(err, forge.ErrNotFound) {
		t.Errorf("missing project: %v, want ErrNotFound", err)
	}
	if _, err := a.LookupRepo(ctx, "alice/plain"); err == nil || !strings.Contains(err.Error(), "not https") {
		t.Errorf("http clone URL: %v, want refused", err)
	}

	secret := forge.NewSecret("whsec_" + base64.StdEncoding.EncodeToString([]byte(signingKey)))
	hook := "https://bob.test/hooks/gl"
	if err := a.EnsureWebhook(ctx, r, hook, secret); err != nil {
		t.Fatal(err)
	}
	f.hooks = []map[string]any{{"id": 1, "url": "https://other.test/x"}, {"id": 3, "url": hook}}
	if err := a.EnsureWebhook(ctx, r, hook, secret); err != nil {
		t.Fatal(err)
	}
	if len(f.hookCalls) != 2 || !strings.HasPrefix(f.hookCalls[0], fmt.Sprintf("POST projects/%d/hooks ", projectID)) ||
		!strings.HasPrefix(f.hookCalls[1], fmt.Sprintf("PUT projects/%d/hooks/3 ", projectID)) {
		t.Fatalf("hook calls %q, want create then update of hook 3", f.hookCalls)
	}
	for _, c := range f.hookCalls {
		for _, want := range []string{`"signing_token":"whsec_`, `"push_events":true`, `"tag_push_events":true`, `"merge_requests_events":true`, `"enable_ssl_verification":true`} {
			if !strings.Contains(c, want) {
				t.Errorf("hook call %s lacks %s", c, want)
			}
		}
		if strings.Contains(c, `"token":`) {
			t.Errorf("hook call %s sends the plaintext token field", c)
		}
	}
	if err := a.EnsureWebhook(ctx, r, hook, forge.Secret{}); err == nil {
		t.Errorf("empty secret accepted")
	}
}

func sign(id, ts string, body []byte) string {
	m := hmac.New(sha256.New, []byte(signingKey))
	m.Write([]byte(id + "." + ts + "."))
	m.Write(body)
	return "v1," + base64.StdEncoding.EncodeToString(m.Sum(nil))
}

func TestParseWebhookSignature(t *testing.T) {
	f := newFake(t)
	a := newAdapter(t, f, token)
	body := []byte(fmt.Sprintf(`{"object_kind":"push","ref":"refs/heads/main","before":"%s","after":"%s","checkout_sha":"%s",
		"user_username":"alice","project_id":%d,"project":{"id":%d,"path_with_namespace":"alice/demo","git_http_url":"https://gitlab.test/alice/demo.git","default_branch":"main","visibility":"private"}}`,
		strings.Repeat("1", 40), sha, sha, projectID, projectID))
	now := time.Unix(1_800_000_000, 0)
	ts := strconv.FormatInt(now.Unix(), 10)
	h := func(sig, ts string) http.Header {
		return http.Header{"X-Gitlab-Event": {"Push Hook"}, "Webhook-Id": {"msg_1"}, "Webhook-Timestamp": {ts}, "Webhook-Signature": {sig}}
	}
	ev, err := a.ParseWebhook(h("v0,xxx "+sign("msg_1", ts, body), ts), body, now)
	if err != nil {
		t.Fatal(err)
	}
	if ev.Type != forge.EventPush || ev.DeliveryID != "msg_1" || ev.Push.Branch != "main" || ev.Push.After != sha || ev.Repo.ID != projectID || ev.Sender != "alice" {
		t.Errorf("event %+v push %+v", ev, ev.Push)
	}
	if _, err := a.ParseWebhook(h(sign("msg_1", ts, body), ts), append(body, ' '), now); !errors.Is(err, forge.ErrSignature) {
		t.Errorf("changed body: %v, want ErrSignature", err)
	}
	if _, err := a.ParseWebhook(h(sign("msg_1", ts, body), ts), body, now.Add(6*time.Minute)); !errors.Is(err, forge.ErrStale) {
		t.Errorf("old delivery: %v, want ErrStale", err)
	}
	if _, err := a.ParseWebhook(http.Header{"X-Gitlab-Event": {"Push Hook"}, "X-Gitlab-Token": {"whsec_x"}}, body, now); !errors.Is(err, forge.ErrSignature) {
		t.Errorf("plaintext token without opt-in: %v, want ErrSignature", err)
	}
}
