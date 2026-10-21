//go:build live

// Live tests against a local Forgejo started by ../../setup-forgejo.sh. Run: ./measure.sh
package forgejo

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	gitea "code.gitea.io/sdk/gitea"
	fj "codeberg.org/mvdkleijn/forgejo-sdk/forgejo/v3"

	"bobspike/s16/forge"
)

func env(t *testing.T, k string) string {
	v := os.Getenv(k)
	if v == "" {
		t.Skipf("%s not set", k)
	}
	return v
}

func httpClient(t *testing.T) *http.Client {
	pem, err := os.ReadFile(env(t, "S16_CA"))
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(pem)
	return &http.Client{Timeout: 30 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool}}}
}

func liveAdapter(t *testing.T) (*Adapter, forge.Repo) {
	a, err := New(Config{
		BaseURL: env(t, "S16_FORGEJO_URL"), BotUser: "bobbot",
		BotPassword: forge.NewSecret(env(t, "S16_BOT_PASS")), StatusToken: forge.NewSecret(env(t, "S16_STATUS_TOKEN")),
		WebhookSecret: forge.NewSecret(env(t, "S16_HOOK_SECRET")), HTTPClient: httpClient(t), ServerVersion: "16.0.6",
	})
	if err != nil {
		t.Fatal(err)
	}
	u := strings.TrimSuffix(env(t, "S16_FORGEJO_URL"), "/")
	return a, forge.Repo{Forge: forge.Forgejo, ID: 1, FullName: "alice/demo", CloneURL: u + "/alice/demo.git", Private: true}
}

func headSHA(t *testing.T, a *Adapter, owner, repo string) string {
	c, err := a.client(context.Background(), fj.SetToken(a.cfg.StatusToken.Reveal()))
	if err != nil {
		t.Fatal(err)
	}
	b, _, err := c.GetRepoBranch(owner, repo, "main")
	if err != nil {
		t.Fatal(err)
	}
	return b.Commit.ID
}

func TestLiveStatuses(t *testing.T) {
	a, r := liveAdapter(t)
	ctx := context.Background()
	sha := headSHA(t, a, "alice", "demo")
	want := map[forge.State]fj.StatusState{
		forge.StatePending: "pending", forge.StateRunning: "pending", forge.StateSuccess: "success",
		forge.StateFailure: "failure", forge.StateError: "error", forge.StateCancelled: "warning",
	}
	for st, fs := range want {
		start := time.Now()
		err := a.SetStatus(ctx, r, forge.Status{SHA: sha, Context: "bob/" + string(st), State: st,
			Description: strings.Repeat("d", 300), TargetURL: "https://bob.example/builds/01J"})
		if err != nil {
			t.Fatalf("%s: %v", st, err)
		}
		t.Logf("SetStatus %s: %v", st, time.Since(start))
		_ = fs
	}
	c, _ := a.client(ctx, fj.SetToken(a.cfg.StatusToken.Reveal()))
	sts, _, err := c.ListStatuses("alice", "demo", sha, fj.ListStatusesOption{})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]fj.StatusState{}
	for _, s := range sts {
		if _, seen := got[s.Context]; !seen {
			got[s.Context] = s.State
		}
	}
	for st, fs := range want {
		if got["bob/"+string(st)] != fs {
			t.Errorf("%s: forge shows %q, want %q", st, got["bob/"+string(st)], fs)
		}
	}
	// Status token is repo-scoped: other repo must be refused.
	if err := a.SetStatus(ctx, forge.Repo{FullName: "alice/other"}, forge.Status{SHA: headSHA(t, a, "alice", "demo"), Context: "x", State: forge.StatePending}); !errors.Is(err, forge.ErrAuth) && !errors.Is(err, forge.ErrNotFound) {
		t.Errorf("status on other repo: %v", err)
	}
}

// gitClone clones like a worker: credentials only in a one-shot -c header, never in .git/config.
func gitClone(ctx context.Context, dir string, co forge.Checkout, extra ...string) (time.Duration, string, error) {
	auth := base64.StdEncoding.EncodeToString([]byte(co.Username + ":" + co.Token.Reveal()))
	args := append([]string{"-c", "http.extraHeader=Authorization: Basic " + auth, "clone", "-q"}, extra...)
	args = append(args, co.CloneURL, dir)
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_SSL_CAINFO="+os.Getenv("S16_CA"))
	start := time.Now()
	out, err := cmd.CombinedOutput()
	return time.Since(start), string(out), err
}

func TestLiveCheckout(t *testing.T) {
	a, r := liveAdapter(t)
	ctx := context.Background()
	work := filepath.Join(env(t, "S16_WORKDIR"), fmt.Sprintf("job-%d", time.Now().UnixNano()))
	t0 := time.Now()
	co, err := a.IssueCheckout(ctx, forge.CheckoutRequest{Repo: r, JobID: "01JLIVE", TTL: 30 * time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("IssueCheckout: %v (handle %s, forge expires: %v)", time.Since(t0), co.Handle, co.ForgeExpires)
	if s := fmt.Sprintf("%v %+v", co.Token, co); strings.Contains(s, co.Token.Reveal()) {
		t.Fatal("token leaked through fmt")
	}
	d, out, err := gitClone(ctx, filepath.Join(work, "src"), co)
	if err != nil {
		t.Fatalf("clone: %v %s", err, out)
	}
	t.Logf("clone (full): %v", d)
	cfg, _ := os.ReadFile(filepath.Join(work, "src", ".git", "config"))
	if strings.Contains(string(cfg), co.Token.Reveal()) {
		t.Fatal("token persisted in .git/config")
	}
	// Fork PR head is fetchable from the base repo with the same credential.
	auth := base64.StdEncoding.EncodeToString([]byte(co.Username + ":" + co.Token.Reveal()))
	fetch := exec.Command("git", "-C", filepath.Join(work, "src"), "-c", "http.extraHeader=Authorization: Basic "+auth, "fetch", "-q", "--depth=1", "origin", "refs/pull/2/head")
	fetch.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_SSL_CAINFO="+os.Getenv("S16_CA"))
	if out, err := fetch.CombinedOutput(); err != nil {
		t.Errorf("fetch fork PR ref: %v %s", err, out)
	}
	// Shallow fetch of one SHA (what a worker wants: exactly the commit of the event).
	sha := headSHA(t, a, "alice", "demo")
	sdir := filepath.Join(work, "sha")
	init := exec.Command("sh", "-c", fmt.Sprintf(`set -e; git init -q %s; cd %s; git -c "http.extraHeader=Authorization: Basic %s" fetch -q --depth=1 %s %s; git checkout -q FETCH_HEAD; git rev-parse HEAD`, sdir, sdir, auth, co.CloneURL, sha))
	init.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_SSL_CAINFO="+os.Getenv("S16_CA"))
	ts := time.Now()
	if out, err := init.CombinedOutput(); err != nil || !strings.Contains(string(out), sha) {
		t.Errorf("fetch by SHA: %v %s", err, out)
	} else {
		t.Logf("fetch --depth=1 <sha>: %v", time.Since(ts))
	}
	// Scope: another private repo the bot can read must be refused with this token.
	other := co
	other.CloneURL = strings.Replace(co.CloneURL, "/demo.git", "/other.git", 1)
	if _, out, err := gitClone(ctx, filepath.Join(work, "other"), other); err == nil {
		t.Fatal("token not repository-scoped")
	} else {
		t.Logf("other repo refused: %s", strings.TrimSpace(out))
	}
	// Scope: no write.
	push := exec.Command("git", "-C", filepath.Join(work, "src"), "-c", "http.extraHeader=Authorization: Basic "+auth, "push", "-q", "origin", "HEAD:refs/heads/evil")
	push.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_SSL_CAINFO="+os.Getenv("S16_CA"))
	if out, err := push.CombinedOutput(); err == nil {
		t.Fatal("read token could push")
	} else {
		t.Logf("push refused: %s", strings.TrimSpace(lastLine(string(out))))
	}
	// Scope: API status write refused.
	sc, _ := fj.NewClient(a.cfg.BaseURL, fj.SetHTTPClient(a.cfg.HTTPClient), fj.SetForgejoVersion("16.0.6"), fj.SetToken(co.Token.Reveal()))
	if _, _, err := sc.CreateStatus("alice", "demo", sha, fj.CreateStatusOption{State: "pending", Context: "evil"}); err == nil {
		t.Fatal("read token could write status")
	}
	t1 := time.Now()
	if err := a.RevokeCheckout(ctx, co); err != nil {
		t.Fatal(err)
	}
	t.Logf("RevokeCheckout: %v", time.Since(t1))
	if err := a.RevokeCheckout(ctx, co); err != nil {
		t.Fatalf("second revoke: %v", err)
	}
	if _, _, err := gitClone(ctx, filepath.Join(work, "after"), co); err == nil {
		t.Fatal("revoked token still clones")
	}
}

func lastLine(s string) string {
	l := strings.Split(strings.TrimSpace(s), "\n")
	return l[len(l)-1]
}

func TestLiveSweep(t *testing.T) {
	a, r := liveAdapter(t)
	ctx := context.Background()
	var cos []forge.Checkout
	for i := 0; i < 3; i++ {
		co, err := a.IssueCheckout(ctx, forge.CheckoutRequest{Repo: r, JobID: fmt.Sprintf("crash%d", i)})
		if err != nil {
			t.Fatal(err)
		}
		cos = append(cos, co)
	}
	// Simulate: worker crashed, no revoke. Sweep everything older than now+1s.
	n, err := a.SweepCheckouts(ctx, time.Now().Add(2*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if n < 3 {
		t.Fatalf("swept %d, want >= 3", n)
	}
	for _, co := range cos {
		if _, _, err := gitClone(ctx, filepath.Join(env(t, "S16_WORKDIR"), "swept-"+co.Handle), co); err == nil {
			t.Fatal("swept token still works")
		}
	}
	// The status token is not a job token and must survive.
	if err := a.SetStatus(ctx, r, forge.Status{SHA: headSHA(t, a, "alice", "demo"), Context: "bob/sweep", State: forge.StateSuccess}); err != nil {
		t.Fatalf("status token swept: %v", err)
	}
}

// End to end: hook created via SDK -> real push -> delivery -> ParseWebhook.
func TestLiveWebhookRoundTrip(t *testing.T) {
	a, _ := liveAdapter(t)
	ctx := context.Background()
	owner := env(t, "S16_OWNER_TOKEN")
	oc, err := fj.NewClient(a.cfg.BaseURL, fj.SetHTTPClient(a.cfg.HTTPClient), fj.SetToken(owner))
	if err != nil {
		t.Fatalf("SDK client with version probe: %v", err)
	}
	got := make(chan forge.Event, 4)
	errs := make(chan error, 4)
	ln, err := net.Listen("tcp", "127.0.0.1:21603")
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, err := forge.ReadBody(r)
		if err != nil {
			errs <- err
			return
		}
		e, err := a.ParseWebhook(r.Header, b, time.Now())
		if err != nil {
			w.WriteHeader(401)
			errs <- err
			return
		}
		w.WriteHeader(204)
		got <- e
	})}
	go srv.Serve(ln)
	defer srv.Close()
	hook, _, err := oc.CreateRepoHook("alice", "demo", fj.CreateHookOption{Type: "forgejo", Active: true,
		Events: []string{"push"}, BranchFilter: "*",
		Config: map[string]string{"url": "http://127.0.0.1:21603/", "content_type": "json", "secret": a.cfg.WebhookSecret.Reveal()}})
	if err != nil {
		t.Fatal(err)
	}
	defer oc.DeleteRepoHook("alice", "demo", hook.ID)
	// push a commit through the API (contents API = a real git commit + push hook)
	start := time.Now()
	_, _, err = oc.CreateFile("alice", "demo", fmt.Sprintf("f-%d.txt", time.Now().UnixNano()), fj.CreateFileOptions{
		FileOptions: fj.FileOptions{Message: "live test", BranchName: "main"}, Content: base64.StdEncoding.EncodeToString([]byte("x"))})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case e := <-got:
		t.Logf("push -> parsed event in %v: %s %s %s", time.Since(start), e.Type, e.Push.Branch, e.Push.After)
		if e.Type != forge.EventPush || e.Push.Branch != "main" {
			t.Fatalf("%+v", e)
		}
	case err := <-errs:
		t.Fatal(err)
	case <-time.After(15 * time.Second):
		t.Fatal("no delivery")
	}
	_ = ctx
}

// The Gitea SDK (official for Gitea) against Forgejo 16: does the version probe and a status call work?
func TestLiveGiteaSDKCompat(t *testing.T) {
	a, _ := liveAdapter(t)
	c, err := gitea.NewClient(a.cfg.BaseURL, gitea.SetHTTPClient(a.cfg.HTTPClient), gitea.SetToken(a.cfg.StatusToken.Reveal()))
	if err != nil {
		t.Fatalf("gitea SDK NewClient: %v", err)
	}
	v, _, err := c.ServerVersion()
	t.Logf("gitea SDK sees version %q err=%v", v, err)
	_, _, err = c.CreateStatus("alice", "demo", headSHA(t, a, "alice", "demo"), gitea.CreateStatusOption{State: gitea.StatusSuccess, Context: "bob/gitea-sdk"})
	if err != nil {
		t.Fatalf("gitea SDK CreateStatus: %v", err)
	}
	resp, err := a.cfg.HTTPClient.Get(a.cfg.BaseURL + "/api/v1/version")
	if err == nil {
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		t.Logf("raw /version: %s", b)
	}
}
