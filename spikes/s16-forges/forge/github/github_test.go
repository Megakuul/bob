package github

import (
	"context"
	"crypto"
	"crypto/hmac"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"bobspike/s16/forge"
)

const fix = "../../fixtures/github"

var secret = "s16-github-test-secret"

func sign(body []byte) string {
	m := hmac.New(sha256.New, []byte(secret))
	m.Write(body)
	return "sha256=" + hex.EncodeToString(m.Sum(nil))
}

func newKey(t *testing.T) (*rsa.PrivateKey, string) {
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	p := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(k)})
	return k, string(p)
}

func newAdapter(t *testing.T, apiURL string, hc *http.Client) (*Adapter, *rsa.PrivateKey) {
	k, p := newKey(t)
	a, err := New(Config{AppID: 4242, PrivateKeyPEM: forge.NewSecret(p), InstallationID: 1,
		WebhookSecret: forge.NewSecret(secret), APIURL: apiURL, HTTPClient: hc})
	if err != nil {
		t.Fatal(err)
	}
	return a, k
}

// GitHub's own documented test vector for webhook signatures.
func TestDocumentedSignatureVector(t *testing.T) {
	err := forge.VerifyHexHMACSHA256([]byte("It's a Secret to Everybody"), []byte("Hello, World!"),
		"sha256=757107ea0eb2509fc211221cce984b8a37570b6d7586c22c46f4379c8b043e17", "sha256=")
	if err != nil {
		t.Fatal(err)
	}
}

func deliver(t *testing.T, a *Adapter, event, file string, mutate func(h http.Header, b []byte) []byte) (forge.Event, error) {
	b, err := os.ReadFile(filepath.Join(fix, "webhooks", file))
	if err != nil {
		t.Fatal(err)
	}
	h := http.Header{}
	h.Set("X-GitHub-Event", event)
	h.Set("X-GitHub-Delivery", "72d3162e-cc78-11e3-81ab-4c9367dc0958")
	h.Set("X-Hub-Signature-256", sign(b))
	h.Set("Content-Type", "application/json")
	if mutate != nil {
		b = mutate(h, b)
	}
	return a.ParseWebhook(h, b, time.Now())
}

func TestParseFixtures(t *testing.T) {
	a, _ := newAdapter(t, "", nil)
	type c struct {
		event, file string
		check       func(e forge.Event) bool
	}
	for _, tc := range []c{
		{"push", "push.branch.json", func(e forge.Event) bool {
			return e.Type == forge.EventPush && e.Push.Branch == "master" && e.Push.Created && e.Push.After == "6113728f27ae82c7b1a177c8d03f9e96e0adf246" && e.Repo.ID == 186853002
		}},
		{"push", "push.tag-deleted.json", func(e forge.Event) bool {
			return e.Type == forge.EventTag && e.Tag.Name == "simple-tag" && e.Tag.Deleted
		}},
		{"push", "push.tag-created.derived.json", func(e forge.Event) bool {
			return e.Type == forge.EventTag && e.Tag.Name == "v1.0.0" && e.Tag.Commit == "6113728f27ae82c7b1a177c8d03f9e96e0adf246"
		}},
		{"pull_request", "pull_request.opened.json", func(e forge.Event) bool {
			p := e.PullRequest
			return e.Type == forge.EventPullRequest && p.Action == forge.PROpened && p.Number == 2 && !p.FromFork &&
				p.HeadSHA == "ec26c3e57ca3a959ca5aad62de7213c562f8c821" && p.FetchRef == "refs/pull/2/head"
		}},
		{"pull_request", "pull_request.synchronize.json", func(e forge.Event) bool { return e.PullRequest.Action == forge.PRSynchronized }},
		{"pull_request", "pull_request.closed.json", func(e forge.Event) bool { return e.PullRequest.Action == forge.PRClosed }},
		{"pull_request", "pull_request.opened-fork.derived.json", func(e forge.Event) bool {
			return e.PullRequest.FromFork && e.PullRequest.HeadRepo == "mallory/Hello-World"
		}},
		{"create", "create.json", func(e forge.Event) bool { return e.Type == forge.EventIgnored }},
		{"release", "release.published.json", func(e forge.Event) bool { return e.Type == forge.EventIgnored }},
		{"ping", "ping.json", func(e forge.Event) bool { return e.Type == forge.EventPing }},
	} {
		t.Run(tc.file, func(t *testing.T) {
			e, err := deliver(t, a, tc.event, tc.file, nil)
			if err != nil {
				t.Fatal(err)
			}
			if !tc.check(e) {
				j, _ := json.MarshalIndent(e, "", " ")
				t.Fatalf("unexpected:\n%s", j)
			}
		})
	}
}

func TestSignatureRejected(t *testing.T) {
	a, _ := newAdapter(t, "", nil)
	cases := map[string]func(h http.Header, b []byte) []byte{
		"tampered": func(h http.Header, b []byte) []byte { c := append([]byte(nil), b...); c[10] ^= 1; return c },
		"missing":  func(h http.Header, b []byte) []byte { h.Del("X-Hub-Signature-256"); return b },
		"sha1-only": func(h http.Header, b []byte) []byte {
			h.Del("X-Hub-Signature-256")
			h.Set("X-Hub-Signature", "sha1=0000000000000000000000000000000000000000")
			return b
		},
		"garbage": func(h http.Header, b []byte) []byte { h.Set("X-Hub-Signature-256", "sha256=zz"); return b },
	}
	for n, m := range cases {
		if _, err := deliver(t, a, "push", "push.branch.json", m); !errors.Is(err, forge.ErrSignature) {
			t.Errorf("%s: %v", n, err)
		}
	}
	// Signed but hostile: non-https clone URL / bad SHA are refused after verification.
	_, err := deliver(t, a, "push", "push.branch.json", func(h http.Header, b []byte) []byte {
		nb := []byte(strings.Replace(string(b), `"after": "6113728f27ae82c7b1a177c8d03f9e96e0adf246"`, `"after": "--upload-pack=evil"`, 1))
		h.Set("X-Hub-Signature-256", sign(nb))
		return nb
	})
	if !errors.Is(err, forge.ErrPayload) {
		t.Errorf("hostile sha: %v", err)
	}
}

// replay is an httptest server that answers with the documented example bodies from
// GitHub's OpenAPI description (fixtures/github/api) and records what the SDK sent.
type replay struct {
	t    *testing.T
	pub  *rsa.PublicKey
	mu   sync.Mutex
	reqs []recorded
}

type recorded struct {
	Method, Path, Auth string
	Body               map[string]any
}

func (r *replay) ServeHTTP(w http.ResponseWriter, q *http.Request) {
	b, _ := io.ReadAll(q.Body)
	var body map[string]any
	_ = json.Unmarshal(b, &body)
	r.mu.Lock()
	r.reqs = append(r.reqs, recorded{q.Method, q.URL.Path, q.Header.Get("Authorization"), body})
	r.mu.Unlock()
	path := strings.TrimPrefix(q.URL.Path, "/api/v3")
	auth := q.Header.Get("Authorization")
	switch {
	case q.Method == "POST" && path == "/app/installations/1/access_tokens":
		if !r.validJWT(strings.TrimPrefix(auth, "Bearer ")) {
			w.WriteHeader(401)
			return
		}
		ex := r.load("installation-token.json")
		// The documented example is from 2016; give it a future expiry like GitHub does.
		ex["expires_at"] = time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
		if ids, ok := body["repository_ids"].([]any); ok && len(ids) == 1 {
			repos := ex["repositories"].([]any)
			repo := repos[0].(map[string]any)
			repo["id"] = ids[0]
			ex["repositories"] = []any{repo}
		}
		w.WriteHeader(201)
		json.NewEncoder(w).Encode(ex)
	case q.Method == "POST" && strings.HasPrefix(path, "/repos/octocat/Hello-World/statuses/"):
		if !strings.HasPrefix(auth, "Bearer ghs_") {
			w.WriteHeader(401)
			return
		}
		w.WriteHeader(201)
		json.NewEncoder(w).Encode(r.load("status.json"))
	case q.Method == "DELETE" && path == "/installation/token":
		w.WriteHeader(204)
	default:
		w.WriteHeader(404)
	}
}

func (r *replay) load(n string) map[string]any {
	b, err := os.ReadFile(filepath.Join(fix, "api", n))
	if err != nil {
		r.t.Fatal(err)
	}
	var m map[string]any
	json.Unmarshal(b, &m)
	return m
}

func (r *replay) validJWT(tok string) bool {
	p := strings.Split(tok, ".")
	if len(p) != 3 {
		return false
	}
	sig, err := base64.RawURLEncoding.DecodeString(p[2])
	if err != nil {
		return false
	}
	sum := sha256.Sum256([]byte(p[0] + "." + p[1]))
	if rsa.VerifyPKCS1v15(r.pub, crypto.SHA256, sum[:], sig) != nil {
		return false
	}
	cb, _ := base64.RawURLEncoding.DecodeString(p[1])
	var cl struct {
		Iat, Exp int64
		Iss      string
	}
	json.Unmarshal(cb, &cl)
	now := time.Now().Unix()
	return cl.Iss == "4242" && cl.Iat <= now && cl.Exp > now && cl.Exp-cl.Iat <= 600
}

func TestAPIReplay(t *testing.T) {
	rp := &replay{t: t}
	srv := httptest.NewTLSServer(rp)
	defer srv.Close()
	a, k := newAdapter(t, srv.URL+"/api/v3/", srv.Client())
	rp.pub = &k.PublicKey
	ctx := context.Background()
	repo := forge.Repo{Forge: forge.GitHub, ID: 1296269, FullName: "octocat/Hello-World", CloneURL: "https://github.com/octocat/Hello-World.git"}

	co, err := a.IssueCheckout(ctx, forge.CheckoutRequest{Repo: repo, JobID: "01J", TTL: 10 * time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	if co.Username != "x-access-token" || !co.ForgeExpires || co.Token.Reveal() != "ghs_16C7e42F292c6912E7710c838347Ae178B4a" {
		t.Fatalf("%+v", co)
	}
	if strings.Contains(strings.Join([]string{co.Token.String()}, ""), "ghs_") {
		t.Fatal("token printed")
	}
	for _, st := range []forge.State{forge.StatePending, forge.StateRunning, forge.StateSuccess, forge.StateFailure, forge.StateError, forge.StateCancelled} {
		if err := a.SetStatus(ctx, repo, forge.Status{SHA: "6dcb09b5b57875f334f61aebed695e2e4193db5e", Context: "bob/ci", State: st,
			Description: strings.Repeat("x", 200), TargetURL: "https://bob.example/b/1"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := a.RevokeCheckout(ctx, co); err != nil {
		t.Fatal(err)
	}

	// What did the SDK send?
	var mint, statusMint int
	var states []string
	for _, r := range rp.reqs {
		switch {
		case strings.HasSuffix(r.Path, "/access_tokens"):
			perms := r.Body["permissions"].(map[string]any)
			if ids, ok := r.Body["repository_ids"].([]any); ok {
				mint++
				if len(ids) != 1 || ids[0].(float64) != 1296269 || perms["contents"] != "read" || len(perms) != 1 {
					t.Errorf("checkout mint body: %v", r.Body)
				}
			} else {
				statusMint++
				if perms["statuses"] != "write" || len(perms) != 1 {
					t.Errorf("status mint body: %v", r.Body)
				}
			}
		case strings.Contains(r.Path, "/statuses/"):
			states = append(states, r.Body["state"].(string))
			if d := r.Body["description"].(string); len([]rune(d)) > 140 {
				t.Errorf("description %d runes", len([]rune(d)))
			}
		case r.Method == "DELETE":
			if r.Auth != "Bearer "+co.Token.Reveal() {
				t.Errorf("revoke auth %q", r.Auth)
			}
		}
	}
	if mint != 1 || statusMint != 1 {
		t.Errorf("mints: checkout %d status %d (status token must be cached)", mint, statusMint)
	}
	want := "pending pending success failure error error"
	if strings.Join(states, " ") != want {
		t.Errorf("states %v want %s", states, want)
	}
}
