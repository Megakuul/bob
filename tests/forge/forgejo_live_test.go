//go:build live

package forge

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/megakuul/bob/internal/forge"
	forgejoad "github.com/megakuul/bob/internal/forge/forgejo"
	"github.com/megakuul/bob/pkg/bob/diag"
	"github.com/megakuul/bob/tests/harness"
	"github.com/megakuul/bob/tests/parity"
	"github.com/megakuul/bob/tests/serverenv"
	"github.com/megakuul/bob/tests/serverenv/forgejo"
)

func init() { liveCAs = append(liveCAs, forgejoCA) }

// forgejoCA creates the Forgejo certificate in <dir>/forgejo/tls (forgejo.Start keeps an existing
// one), so the test process trusts it from the start (see TestMain).
func forgejoCA(dir string) ([]string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	pkg, err := serverenv.NixTool(ctx, "forgejo")
	if err != nil {
		return nil, err
	}
	tlsDir := filepath.Join(dir, "forgejo", "tls")
	if err := os.MkdirAll(tlsDir, 0o755); err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, filepath.Join(pkg, "bin", "forgejo"), "cert", "--host", "127.0.0.1,localhost", "--ca")
	cmd.Dir, cmd.Env = tlsDir, append(os.Environ(), "HOME="+dir)
	if out, err := cmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("forgejo cert: %w: %s", err, out)
	}
	return []string{filepath.Join(tlsDir, "cert.pem")}, nil
}

type fjLive struct {
	t    *testing.T
	in   *forgejo.Instance
	a    forge.Adapter
	repo forge.Repo
}

func (l *fjLive) api(method, path string, a forgejo.Auth, body, out any) error {
	l.t.Helper()
	return l.in.API(context.Background(), method, path, a, body, out)
}

func (l *fjLive) owner() forgejo.Auth { return forgejo.Auth{Token: l.in.Seeded.OwnerToken} }

// git runs git against the instance with its CA trusted and an optional basic credential.
func (l *fjLive) git(dir, user, token string, args ...string) (string, error) {
	pre := []string{}
	if token != "" {
		pre = append(pre, "-c", "http.extraHeader=Authorization: Basic "+base64.StdEncoding.EncodeToString([]byte(user+":"+token)))
	}
	cmd := exec.Command("git", append(pre, args...)...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_SSL_CAINFO="+l.in.CA, "GIT_TERMINAL_PROMPT=0", "HOME="+dir)
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

// commit creates file name on branch of alice/<repo> through the API and returns the commit sha.
func (l *fjLive) commit(repo, branch, name, content string) string {
	l.t.Helper()
	var out struct{ Commit struct{ SHA string } }
	err := l.api(http.MethodPost, "/api/v1/repos/alice/"+repo+"/contents/"+name, l.owner(), map[string]any{
		"content": base64.StdEncoding.EncodeToString([]byte(content)), "message": "add " + name, "branch": branch}, &out)
	if err != nil {
		l.t.Fatalf("harness error: commit: %v", err)
	}
	return out.Commit.SHA
}

func (l *fjLive) shallowFetch(co forge.Checkout, ref string) (string, string, error) {
	dir := l.t.TempDir()
	if _, err := l.git(dir, "", "", "init", "-q"); err != nil {
		l.t.Fatalf("harness error: git init")
	}
	out, err := l.git(dir, co.Username, co.Token.Reveal(), "fetch", "--depth=1", co.CloneURL, ref)
	if err != nil {
		return "", out, err
	}
	sha, _ := l.git(dir, "", "", "rev-parse", "FETCH_HEAD")
	return sha, out, nil
}

func startForgejoLive(t *testing.T) *fjLive {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	in, err := forgejo.Start(ctx, forgejo.Options{Dir: filepath.Join(liveDir, "forgejo"), Seed: true})
	if err != nil {
		t.Fatalf("harness error: forgejo: %v", err)
	}
	t.Cleanup(func() { _ = in.Stop() })
	a, err := forgejoad.New(forgejoad.Config{BaseURL: in.URL, BotLogin: "bobbot",
		BotPassword: forge.NewSecret(forgejo.UserPass("bobbot")), WebhookSecret: forge.NewSecret(in.Seeded.HookSecret)})
	if err != nil {
		var de *diag.Error
		if errors.As(err, &de) && de.Code == diag.CodeNotImplemented {
			harness.RedFeature(t, "forge/forgejo.New")
			return nil
		}
		t.Fatalf("forgejo.New: %v", err)
	}
	l := &fjLive{t: t, in: in, a: a}
	if l.repo, err = a.LookupRepo(ctx, "alice/demo"); err != nil {
		t.Fatalf("LookupRepo(alice/demo): %v", err)
	}
	if l.repo.Forge != forge.Forgejo || l.repo.ID == 0 || !l.repo.Private || !strings.HasPrefix(l.repo.CloneURL, "https://") || l.repo.DefaultBranch != "main" {
		t.Fatalf("LookupRepo = %+v", l.repo)
	}
	return l
}

// TestForgejoLive is the live Forgejo round trip of R-PIPE-001/-007 (hooks, statuses for all six
// states, a per-job repo-scoped checkout token, revoke, sweeper, fork PR ref, shallow fetch).
// The webhook -> BuildRecord <= 500 ms part runs against the server (TestPIPE003...).
func TestForgejoLive(t *testing.T) {
	parity.Covers(t, "R-PIPE-001", "R-PIPE-007")
	l := startForgejoLive(t)
	if l == nil {
		return
	}
	ctx := context.Background()

	t.Run("webhook", func(t *testing.T) {
		var mu sync.Mutex
		var got []delivery
		recv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			b, _ := io.ReadAll(r.Body)
			mu.Lock()
			got = append(got, delivery{h: r.Header.Clone(), body: b, now: time.Now()})
			mu.Unlock()
		}))
		defer recv.Close()
		for i := 0; i < 2; i++ { // idempotent: the second call updates the hook
			if err := l.a.EnsureWebhook(ctx, l.repo, recv.URL, forge.NewSecret(l.in.Seeded.HookSecret)); err != nil {
				t.Fatalf("EnsureWebhook #%d: %v", i+1, err)
			}
		}
		var hooks []struct {
			Config map[string]string
			Events []string
		}
		if err := l.api(http.MethodGet, "/api/v1/repos/alice/demo/hooks", l.owner(), nil, &hooks); err != nil || len(hooks) != 1 {
			t.Fatalf("hooks after two EnsureWebhook: %d (%v), want exactly 1", len(hooks), err)
		}
		sha := l.commit("demo", "main", "hook.txt", "x")
		deadline := time.Now().Add(15 * time.Second)
		for {
			mu.Lock()
			n := len(got)
			mu.Unlock()
			if n > 0 || time.Now().After(deadline) {
				break
			}
			time.Sleep(50 * time.Millisecond)
		}
		mu.Lock()
		defer mu.Unlock()
		if len(got) == 0 {
			t.Fatalf("Forgejo delivered no webhook for a push")
		}
		d := got[0]
		ev, err := l.a.ParseWebhook(d.h, d.body, d.now)
		if err != nil || ev.Type != forge.EventPush || ev.Push == nil || ev.Push.After != sha || ev.Repo.ID != l.repo.ID {
			t.Errorf("live push delivery parsed as %+v, %v; want push of %s in repo %d", ev, err, sha, l.repo.ID)
		}
		d.body[0] ^= 1
		if _, err := l.a.ParseWebhook(d.h, d.body, d.now); !errors.Is(err, forge.ErrSignature) {
			t.Errorf("tampered live delivery: %v, want ErrSignature", err)
		}
	})

	t.Run("statuses", func(t *testing.T) {
		sha := l.commit("demo", "main", "status.txt", "x")
		want := map[forge.State]string{forge.StatePending: "pending", forge.StateRunning: "pending", forge.StateSuccess: "success",
			forge.StateFailure: "failure", forge.StateError: "error", forge.StateCancelled: "error"}
		for st := range want {
			err := l.a.SetStatus(ctx, l.repo, forge.Status{SHA: sha, Context: "bob/" + string(st), State: st,
				Description: "#7 " + string(st) + ": description", TargetURL: "https://bob.example/builds/7"})
			if err != nil {
				t.Fatalf("SetStatus %s: %v", st, err)
			}
		}
		var statuses []struct{ Context, Status, Description, TargetURL string }
		if err := l.api(http.MethodGet, "/api/v1/repos/alice/demo/commits/"+sha+"/statuses", l.owner(), nil, &statuses); err != nil {
			t.Fatalf("harness error: %v", err)
		}
		seen := map[string]string{}
		for _, s := range statuses {
			seen[s.Context] = s.Status
			if s.TargetURL != "https://bob.example/builds/7" {
				t.Errorf("status %s target_url %q", s.Context, s.TargetURL)
			}
			if s.Context == "bob/cancelled" && !strings.Contains(s.Description, "cancelled") {
				t.Errorf("cancelled status description %q does not say cancelled", s.Description)
			}
		}
		for st, w := range want {
			if seen["bob/"+string(st)] != w {
				t.Errorf("state %s stored as %q, want %q", st, seen["bob/"+string(st)], w)
			}
		}
	})

	t.Run("checkout", func(t *testing.T) {
		sha := l.commit("demo", "main", "checkout.txt", "x")
		co, err := l.a.IssueCheckout(ctx, forge.CheckoutRequest{Repo: l.repo, JobID: "01JOBONE", TTL: 30 * time.Minute})
		if err != nil {
			t.Fatalf("IssueCheckout: %v", err)
		}
		if co.ForgeExpires || co.Handle == "" || co.Token.Reveal() == "" || co.Username == "" {
			t.Errorf("Checkout = %+v, want ForgeExpires=false (Forgejo tokens do not expire) and a handle", co)
		}
		var toks []struct{ Name string }
		_ = l.api(http.MethodGet, "/api/v1/users/bobbot/tokens", forgejo.Auth{User: "bobbot", Pass: forgejo.UserPass("bobbot")}, nil, &toks)
		found := false
		for _, tk := range toks {
			found = found || regexp.MustCompile(`^bob-job-\d+-01JOBONE$`).MatchString(tk.Name)
		}
		if !found {
			t.Errorf("no token named bob-job-<unix-seconds>-01JOBONE among %v (D62)", toks)
		}
		// shallow fetch of the commit works and returns it
		if got, out, err := l.shallowFetch(co, sha); err != nil || got != sha {
			t.Fatalf("shallow fetch of %s: %q %v: %s", sha, got, err, out)
		}
		// the token is limited to the one repository: the other private repo answers 403
		if err := l.api(http.MethodGet, "/api/v1/repos/alice/other", forgejo.Auth{Token: co.Token.Reveal()}, nil, nil); !isStatus(err, http.StatusForbidden) {
			t.Errorf("job token on alice/other: %v, want 403", err)
		}
		if out, err := l.git(t.TempDir(), co.Username, co.Token.Reveal(), "ls-remote", strings.Replace(co.CloneURL, "alice/demo", "alice/other", 1)); err == nil {
			t.Errorf("git ls-remote of alice/other with the job token succeeded: %s", out)
		}
		// read-only: a push is refused
		err = l.api(http.MethodPost, "/api/v1/repos/alice/demo/contents/pushed.txt", forgejo.Auth{Token: co.Token.Reveal()},
			map[string]any{"content": "eA==", "message": "x", "branch": "main"}, nil)
		if !isStatus(err, http.StatusForbidden) {
			t.Errorf("write with the job token: %v, want 403", err)
		}
		// revoke: idempotent, and the token stops working
		for i := 0; i < 2; i++ {
			if err := l.a.RevokeCheckout(ctx, co); err != nil {
				t.Errorf("RevokeCheckout #%d: %v", i+1, err)
			}
		}
		if _, _, err := l.shallowFetch(co, sha); err == nil {
			t.Errorf("fetch with a revoked job token succeeded")
		}
	})

	t.Run("sweeper", func(t *testing.T) {
		old, err := l.a.IssueCheckout(ctx, forge.CheckoutRequest{Repo: l.repo, JobID: "01JOBOLD", TTL: time.Hour})
		if err != nil {
			t.Fatalf("IssueCheckout: %v", err)
		}
		time.Sleep(1100 * time.Millisecond) // token names carry unix seconds
		cutoff := time.Now()
		time.Sleep(1100 * time.Millisecond)
		fresh, err := l.a.IssueCheckout(ctx, forge.CheckoutRequest{Repo: l.repo, JobID: "01JOBNEW", TTL: time.Hour})
		if err != nil {
			t.Fatalf("IssueCheckout: %v", err)
		}
		n, err := l.a.SweepCheckouts(ctx, cutoff)
		if err != nil || n < 1 {
			t.Fatalf("SweepCheckouts = %d, %v, want >= 1", n, err)
		}
		sha := l.commit("demo", "main", "sweep.txt", "x")
		if _, _, err := l.shallowFetch(old, sha); err == nil {
			t.Errorf("a token created before the cutoff still works after the sweep")
		}
		if _, out, err := l.shallowFetch(fresh, sha); err != nil {
			t.Errorf("a token created after the cutoff was swept: %v: %s", err, out)
		}
		_ = l.a.RevokeCheckout(ctx, fresh)
	})

	t.Run("fork PR ref", func(t *testing.T) {
		mal := forgejo.Auth{User: "mallory", Pass: forgejo.UserPass("mallory")}
		if err := l.api(http.MethodPut, "/api/v1/repos/alice/demo/collaborators/mallory", l.owner(), map[string]any{"permission": "read"}, nil); err != nil {
			t.Fatalf("harness error: %v", err)
		}
		if err := l.api(http.MethodPost, "/api/v1/repos/alice/demo/forks", mal, map[string]any{"name": "demo"}, nil); err != nil {
			t.Fatalf("harness error: fork: %v", err)
		}
		dir := t.TempDir()
		fork := strings.Replace(l.repo.CloneURL, "alice/demo", "mallory/demo", 1)
		if out, err := l.git(dir, "mallory", forgejo.UserPass("mallory"), "clone", "-q", fork, "w"); err != nil {
			t.Fatalf("harness error: clone fork: %s", out)
		}
		w := filepath.Join(dir, "w")
		_ = os.WriteFile(filepath.Join(w, "evil.txt"), []byte("evil"), 0o644)
		for _, args := range [][]string{{"checkout", "-q", "-b", "evil"}, {"add", "-A"}, {"-c", "user.name=m", "-c", "user.email=m@x", "commit", "-qm", "evil"}} {
			if out, err := l.git(w, "", "", args...); err != nil {
				t.Fatalf("harness error: git %v: %s", args, out)
			}
		}
		if out, err := l.git(w, "mallory", forgejo.UserPass("mallory"), "push", "-q", "origin", "evil"); err != nil {
			t.Fatalf("harness error: push: %s", out)
		}
		head, _ := l.git(w, "", "", "rev-parse", "HEAD")
		var pr struct{ Number int }
		err := l.api(http.MethodPost, "/api/v1/repos/alice/demo/pulls", mal, map[string]any{"head": "mallory:evil", "base": "main", "title": "fork PR"}, &pr)
		if err != nil {
			t.Fatalf("harness error: create PR: %v", err)
		}
		co, err := l.a.IssueCheckout(ctx, forge.CheckoutRequest{Repo: l.repo, JobID: "01JOBFORK", TTL: time.Hour})
		if err != nil {
			t.Fatalf("IssueCheckout: %v", err)
		}
		defer func() { _ = l.a.RevokeCheckout(ctx, co) }()
		ref := fmt.Sprintf("refs/pull/%d/head", pr.Number)
		if got, out, err := l.shallowFetch(co, ref); err != nil || got != head {
			t.Errorf("fetch of %s from the base repo: %q %v (want %s): %s", ref, got, err, head, out)
		}
	})

	t.Run("credentials", func(t *testing.T) {
		bad, err := forgejoad.New(forgejoad.Config{BaseURL: l.in.URL, BotLogin: "bobbot",
			BotPassword: forge.NewSecret("wrong"), WebhookSecret: forge.NewSecret("s")})
		if err != nil {
			t.Fatalf("forgejo.New: %v", err)
		}
		if err := bad.SetStatus(ctx, l.repo, forge.Status{SHA: strings.Repeat("a", 40), Context: "bob/x", State: forge.StateSuccess}); !errors.Is(err, forge.ErrAuth) {
			t.Errorf("SetStatus with a wrong bot password: %v, want ErrAuth", err)
		}
		if _, err := l.a.LookupRepo(ctx, "alice/does-not-exist"); !errors.Is(err, forge.ErrNotFound) {
			t.Errorf("LookupRepo of a missing repository: %v, want ErrNotFound", err)
		}
	})
}

func isStatus(err error, code int) bool {
	var se *forgejo.StatusError
	return errors.As(err, &se) && se.Code == code
}
