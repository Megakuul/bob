//go:build live

// Live tests against the local GitLab from ../../gitlab/up.sh. Env: S16_GITLAB_URL, S16_GL_CA,
// S16_GL_ROOT_TOKEN (root PAT, api scope), S16_WORKDIR. Captures deliveries to fixtures/gitlab/live.
package gitlab

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	gl "gitlab.com/gitlab-org/api/client-go"

	"bobspike/s16/forge"
)

func env(t *testing.T, k string) string {
	v := os.Getenv(k)
	if v == "" {
		t.Skipf("%s not set", k)
	}
	return v
}

func hc(t *testing.T) *http.Client {
	pem, err := os.ReadFile(env(t, "S16_GL_CA"))
	if err != nil {
		t.Fatal(err)
	}
	p := x509.NewCertPool()
	p.AppendCertsFromPEM(pem)
	return &http.Client{Timeout: 60 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: p}}}
}

type liveEnv struct {
	root    *gl.Client
	a       *Adapter
	proj    *gl.Project
	other   *gl.Project
	repo    forge.Repo
	signing string
}

var (
	once    sync.Once
	shared  *liveEnv
	initErr error
)

func setup(t *testing.T) *liveEnv {
	base := env(t, "S16_GITLAB_URL")
	tok := env(t, "S16_GL_ROOT_TOKEN")
	once.Do(func() { shared, initErr = doSetup(base, tok, hc(t)) })
	if initErr != nil {
		t.Fatal(initErr)
	}
	return shared
}

func project(root *gl.Client, name string) (*gl.Project, error) {
	p, _, err := root.Projects.GetProject("root/"+name, nil)
	if err == nil {
		return p, nil
	}
	p, _, err = root.Projects.CreateProject(&gl.CreateProjectOptions{Name: gl.Ptr(name), Visibility: gl.Ptr(gl.PrivateVisibility),
		InitializeWithReadme: gl.Ptr(true), DefaultBranch: gl.Ptr("main")})
	return p, err
}

func doSetup(base, rootTok string, h *http.Client) (*liveEnv, error) {
	root, err := gl.NewClient(rootTok, gl.WithBaseURL(base+"/api/v4"), gl.WithHTTPClient(h))
	if err != nil {
		return nil, err
	}
	// Local receivers are blocked by GitLab's SSRF guard by default.
	if _, _, err := root.Settings.UpdateSettings(&gl.UpdateSettingsOptions{AllowLocalRequestsFromWebHooksAndServices: gl.Ptr(true)}); err != nil {
		return nil, fmt.Errorf("settings: %w", err)
	}
	p, err := project(root, "demo")
	if err != nil {
		return nil, fmt.Errorf("project: %w", err)
	}
	o, err := project(root, "other")
	if err != nil {
		return nil, err
	}
	// bob's credential: a project access token (bot user), Maintainer, api scope.
	exp := gl.ISOTime(time.Now().AddDate(0, 0, 30))
	pat, _, err := root.ProjectAccessTokens.CreateProjectAccessToken(p.ID, &gl.CreateProjectAccessTokenOptions{
		Name: gl.Ptr(fmt.Sprintf("bob-%d", time.Now().UnixNano())), Scopes: &[]string{"api"},
		AccessLevel: gl.Ptr(gl.MaintainerPermissions), ExpiresAt: &exp})
	if err != nil {
		return nil, fmt.Errorf("project access token: %w", err)
	}
	k := make([]byte, 32)
	rand.Read(k)
	signing := "whsec_" + base64.StdEncoding.EncodeToString(k)
	a, err := New(Config{BaseURL: base, APIToken: forge.NewSecret(pat.Token), SigningToken: forge.NewSecret(signing), HTTPClient: h})
	if err != nil {
		return nil, err
	}
	return &liveEnv{root: root, a: a, proj: p, other: o, signing: signing,
		repo: forge.Repo{Forge: forge.GitLab, ID: p.ID, FullName: p.PathWithNamespace, CloneURL: p.HTTPURLToRepo, Private: true}}, nil
}

func TestLiveWebhooks(t *testing.T) {
	e := setup(t)
	outDir := "../../fixtures/gitlab/live"
	os.MkdirAll(outDir, 0o755)
	type delivery struct {
		ev  forge.Event
		err error
	}
	got := make(chan delivery, 32)
	var seq int
	var mu sync.Mutex
	ln, err := net.Listen("tcp", "127.0.0.1:21613")
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, err := forge.ReadBody(r)
		if err != nil {
			got <- delivery{err: err}
			return
		}
		mu.Lock()
		seq++
		n := seq
		mu.Unlock()
		name := fmt.Sprintf("%02d-%s", n, strings.ReplaceAll(strings.ToLower(r.Header.Get("X-Gitlab-Event")), " ", "_"))
		hm := map[string]string{}
		for k := range r.Header {
			hm[k] = r.Header.Get(k)
		}
		hb, _ := json.MarshalIndent(hm, "", "  ")
		os.WriteFile(filepath.Join(outDir, name+".headers.json"), hb, 0o644)
		os.WriteFile(filepath.Join(outDir, name+".body.json"), b, 0o644)
		ev, err := e.a.ParseWebhook(r.Header, b, time.Now())
		if err != nil {
			w.WriteHeader(401)
		} else {
			w.WriteHeader(204)
		}
		got <- delivery{ev, err}
	})}
	go srv.Serve(ln)
	defer srv.Close()
	os.WriteFile(filepath.Join(outDir, "SIGNING_TOKEN.test"), []byte(e.signing), 0o644)

	// Hook with signing token: client-go has no SigningToken option yet, so use its raw
	// request path (NewRequest/Do) with the documented parameter.
	req, err := e.root.NewRequest(http.MethodPost, fmt.Sprintf("projects/%d/hooks", e.proj.ID), map[string]any{
		"url": "http://127.0.0.1:21613/hook", "push_events": true, "tag_push_events": true, "merge_requests_events": true,
		"releases_events": true, "enable_ssl_verification": false, "signing_token": e.signing}, nil)
	if err != nil {
		t.Fatal(err)
	}
	var hook gl.ProjectHook
	if _, err := e.root.Do(req, &hook); err != nil {
		t.Fatalf("add hook: %v", err)
	}
	defer e.root.Projects.DeleteProjectHook(e.proj.ID, hook.ID)

	wait := func(want forge.EventType) forge.Event {
		t.Helper()
		deadline := time.After(60 * time.Second)
		for {
			select {
			case d := <-got:
				if d.err != nil {
					t.Fatalf("delivery rejected: %v", d.err)
				}
				if d.ev.Type == want {
					return d.ev
				}
				t.Logf("skip %s", d.ev.Type)
			case <-deadline:
				t.Fatalf("no %s delivery", want)
			}
		}
	}
	// push to main
	t0 := time.Now()
	c, _, err := e.root.Commits.CreateCommit(e.proj.ID, &gl.CreateCommitOptions{Branch: gl.Ptr("main"), CommitMessage: gl.Ptr("live push"),
		Actions: []*gl.CommitActionOptions{{Action: gl.Ptr(gl.FileCreate), FilePath: gl.Ptr(fmt.Sprintf("f%d.txt", time.Now().UnixNano())), Content: gl.Ptr("x")}}})
	if err != nil {
		t.Fatal(err)
	}
	ev := wait(forge.EventPush)
	t.Logf("push delivered+verified in %v", time.Since(t0))
	if ev.Push.After != c.ID || ev.Push.Branch != "main" || ev.Repo.ID != e.proj.ID || !strings.HasPrefix(ev.Repo.CloneURL, "https://") {
		t.Fatalf("%+v %+v", ev, ev.Push)
	}
	// branch + MR open + MR update with a new commit
	br := fmt.Sprintf("feat-%d", time.Now().UnixNano())
	if _, _, err := e.root.Branches.CreateBranch(e.proj.ID, &gl.CreateBranchOptions{Branch: gl.Ptr(br), Ref: gl.Ptr("main")}); err != nil {
		t.Fatal(err)
	}
	wait(forge.EventPush)
	e.root.Commits.CreateCommit(e.proj.ID, &gl.CreateCommitOptions{Branch: gl.Ptr(br), CommitMessage: gl.Ptr("feat 1"),
		Actions: []*gl.CommitActionOptions{{Action: gl.Ptr(gl.FileCreate), FilePath: gl.Ptr(br + "-1.txt"), Content: gl.Ptr("1")}}})
	wait(forge.EventPush)
	mr, _, err := e.root.MergeRequests.CreateMergeRequest(e.proj.ID, &gl.CreateMergeRequestOptions{Title: gl.Ptr("live MR"), SourceBranch: gl.Ptr(br), TargetBranch: gl.Ptr("main")})
	if err != nil {
		t.Fatal(err)
	}
	ev = wait(forge.EventPullRequest)
	if ev.PullRequest.Action != forge.PROpened || ev.PullRequest.Number != mr.IID || ev.PullRequest.FetchRef != fmt.Sprintf("refs/merge-requests/%d/head", mr.IID) {
		t.Fatalf("%+v", ev.PullRequest)
	}
	c2, _, _ := e.root.Commits.CreateCommit(e.proj.ID, &gl.CreateCommitOptions{Branch: gl.Ptr(br), CommitMessage: gl.Ptr("feat 2"),
		Actions: []*gl.CommitActionOptions{{Action: gl.Ptr(gl.FileCreate), FilePath: gl.Ptr(br + "-2.txt"), Content: gl.Ptr("2")}}})
	for {
		ev = wait(forge.EventPullRequest)
		if ev.PullRequest.Action == forge.PRSynchronized {
			break
		}
		t.Logf("MR action %s", ev.PullRequest.Action)
	}
	if ev.PullRequest.HeadSHA != c2.ID {
		t.Fatalf("MR update head %s want %s", ev.PullRequest.HeadSHA, c2.ID)
	}
	// annotated tag
	tagName := fmt.Sprintf("v0.0.%d", time.Now().Unix())
	if _, _, err := e.root.Tags.CreateTag(e.proj.ID, &gl.CreateTagOptions{TagName: gl.Ptr(tagName), Ref: gl.Ptr("main"), Message: gl.Ptr("annotated")}); err != nil {
		t.Fatal(err)
	}
	ev = wait(forge.EventTag)
	if ev.Tag.Name != tagName || ev.Tag.Commit != c.ID {
		t.Fatalf("tag %+v want commit %s", ev.Tag, c.ID)
	}
	// release (ignored by bob, captured as fixture)
	e.root.Releases.CreateRelease(e.proj.ID, &gl.CreateReleaseOptions{TagName: gl.Ptr(tagName), Name: gl.Ptr(tagName)})
	ev = wait(forge.EventIgnored)
	t.Logf("release -> %s (%s)", ev.Type, ev.IgnoredReason)
}

func TestLiveStatuses(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	b, _, err := e.root.Branches.GetBranch(e.proj.ID, "main")
	if err != nil {
		t.Fatal(err)
	}
	sha := b.Commit.ID
	seq := []forge.State{forge.StatePending, forge.StatePending, forge.StateRunning, forge.StateRunning, forge.StateSuccess}
	ctxName := fmt.Sprintf("bob/live-%d", time.Now().UnixNano())
	for _, st := range seq {
		t0 := time.Now()
		if err := e.a.SetStatus(ctx, e.repo, forge.Status{SHA: sha, Context: ctxName, State: st, Description: "d", TargetURL: "https://bob.example/b/1"}); err != nil {
			t.Fatalf("%s: %v", st, err)
		}
		t.Logf("SetStatus %s %v", st, time.Since(t0))
	}
	for _, st := range []forge.State{forge.StateFailure, forge.StateError, forge.StateCancelled} {
		if err := e.a.SetStatus(ctx, e.repo, forge.Status{SHA: sha, Context: "bob/" + string(st), State: st}); err != nil {
			t.Fatalf("%s: %v", st, err)
		}
	}
	sts, _, err := e.root.Commits.GetCommitStatuses(e.proj.ID, sha, &gl.GetCommitStatusesOptions{All: gl.Ptr(true)})
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]string{}
	for _, s := range sts {
		seen[s.Name] = s.Status
	}
	t.Logf("forge shows: %v", seen)
	if seen[ctxName] != "success" || seen["bob/failure"] != "failed" || seen["bob/cancelled"] != "canceled" {
		t.Errorf("statuses %v", seen)
	}
}

func clone(dir string, co forge.Checkout) (time.Duration, string, error) {
	auth := base64.StdEncoding.EncodeToString([]byte(co.Username + ":" + co.Token.Reveal()))
	cmd := exec.Command("git", "-c", "http.extraHeader=Authorization: Basic "+auth, "clone", "-q", co.CloneURL, dir)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_SSL_CAINFO="+os.Getenv("S16_GL_CA"))
	t0 := time.Now()
	out, err := cmd.CombinedOutput()
	return time.Since(t0), string(out), err
}

func TestLiveCheckout(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	work := filepath.Join(env(t, "S16_WORKDIR"), fmt.Sprintf("gl-%d", time.Now().UnixNano()))
	t0 := time.Now()
	co, err := e.a.IssueCheckout(ctx, forge.CheckoutRequest{Repo: e.repo, JobID: "01JLIVE", TTL: 20 * time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("IssueCheckout %v user=%s expires=%v", time.Since(t0), co.Username, co.ExpiresAt)
	d, out, err := clone(filepath.Join(work, "src"), co)
	if err != nil {
		t.Fatalf("clone: %v %s", err, out)
	}
	t.Logf("clone %v", d)
	other := co
	other.CloneURL = e.other.HTTPURLToRepo
	if _, out, err := clone(filepath.Join(work, "other"), other); err == nil {
		t.Fatal("deploy token cloned another project")
	} else {
		t.Logf("other project refused: %s", strings.TrimSpace(out))
	}
	auth := base64.StdEncoding.EncodeToString([]byte(co.Username + ":" + co.Token.Reveal()))
	push := exec.Command("git", "-C", filepath.Join(work, "src"), "-c", "http.extraHeader=Authorization: Basic "+auth, "push", "-q", "origin", "HEAD:refs/heads/evil")
	push.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_SSL_CAINFO="+os.Getenv("S16_GL_CA"))
	if out, err := push.CombinedOutput(); err == nil {
		t.Fatal("read deploy token pushed")
	} else {
		t.Logf("push refused: %s", strings.TrimSpace(string(out)))
	}
	// MR head ref is in the base project.
	fetch := exec.Command("git", "-C", filepath.Join(work, "src"), "-c", "http.extraHeader=Authorization: Basic "+auth, "fetch", "-q", "origin", "refs/merge-requests/1/head")
	fetch.Env = push.Env
	if out, err := fetch.CombinedOutput(); err != nil {
		t.Errorf("fetch MR ref: %v %s", err, out)
	}
	t1 := time.Now()
	if err := e.a.RevokeCheckout(ctx, co); err != nil {
		t.Fatal(err)
	}
	t.Logf("RevokeCheckout %v", time.Since(t1))
	if err := e.a.RevokeCheckout(ctx, co); err != nil {
		t.Fatalf("revoke twice: %v", err)
	}
	if _, _, err := clone(filepath.Join(work, "after"), co); err == nil {
		t.Fatal("revoked deploy token still clones")
	}
}

// Does GitLab honour a sub-day expires_at on deploy tokens?
func TestLiveDeployTokenExpiry(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	co, err := e.a.IssueCheckout(ctx, forge.CheckoutRequest{Repo: e.repo, JobID: "expiry", TTL: 20 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("requested expiry %v, GitLab returned %v", time.Now().Add(20*time.Second).UTC(), co.ExpiresAt.UTC())
	work := filepath.Join(env(t, "S16_WORKDIR"), fmt.Sprintf("glexp-%d", time.Now().UnixNano()))
	if _, out, err := clone(filepath.Join(work, "a"), co); err != nil {
		t.Fatalf("fresh token: %v %s", err, out)
	}
	time.Sleep(time.Until(co.ExpiresAt) + 5*time.Second)
	if _, out, err := clone(filepath.Join(work, "b"), co); err == nil {
		t.Errorf("token still valid after expires_at")
	} else {
		t.Logf("after expiry: %s", lastLine(out))
	}
}

func lastLine(s string) string {
	l := strings.Split(strings.TrimSpace(s), "\n")
	return l[len(l)-1]
}

var _ = errors.Is
