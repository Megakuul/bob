//go:build live && slow

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
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/megakuul/bob/internal/forge"
	gitlabad "github.com/megakuul/bob/internal/forge/gitlab"
	"github.com/megakuul/bob/pkg/bob/diag"
	"github.com/megakuul/bob/tests/harness"
	"github.com/megakuul/bob/tests/parity"
	"github.com/megakuul/bob/tests/serverenv/gitlab"
)

func init() {
	liveCAs = append(liveCAs, func(dir string) ([]string, error) {
		p, err := gitlab.PrepareTLS(filepath.Join(dir, "gitlab"))
		return []string{p}, err
	})
}

// TestGitLabLive is the slow live GitLab round trip of R-PIPE-001/-007 (A13): the same flow as
// TestForgejoLive plus the deploy-token expiry (expires_at = job deadline).
func TestGitLabLive(t *testing.T) {
	parity.Covers(t, "R-PIPE-001", "R-PIPE-007")
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Minute)
	defer cancel()
	gl, err := gitlab.Start(ctx, gitlab.Options{Dir: filepath.Join(liveDir, "gitlab")})
	if err != nil {
		t.Fatalf("harness error: gitlab: %v", err)
	}
	t.Cleanup(func() { _ = gl.Stop() })

	token := forge.NewSecret(gl.RootToken)
	// GitLab accepts a 32-byte signing key (as the S16 spike and the recorded SIGNING_TOKEN.test).
	a, err := gitlabad.New(gitlabad.Config{BaseURL: gl.URL, AccessToken: token, SigningToken: forge.NewSecret("whsec_" + base64.StdEncoding.EncodeToString([]byte("gitlab-live-signing-key-01234567")))})
	if err != nil {
		var de *diag.Error
		if errors.As(err, &de) && de.Code == diag.CodeNotImplemented {
			harness.RedFeature(t, "forge/gitlab.New")
			return
		}
		t.Fatalf("gitlab.New: %v", err)
	}
	mk := func(name string) int {
		var p struct{ ID int }
		if err := gl.API(ctx, http.MethodPost, "/api/v4/projects", map[string]any{"name": name, "visibility": "private", "initialize_with_readme": true}, &p); err != nil {
			t.Fatalf("harness error: create project %s: %v", name, err)
		}
		return p.ID
	}
	demoID, otherID := mk("demo"), mk("other")
	repo, err := a.LookupRepo(ctx, "root/demo")
	if err != nil || repo.ID != int64(demoID) || repo.Forge != forge.GitLab || !repo.Private || !strings.HasPrefix(repo.CloneURL, "https://") {
		t.Fatalf("LookupRepo(root/demo) = %+v, %v", repo, err)
	}
	commit := func(branch, file string) string {
		var c struct{ ID string }
		err := gl.API(ctx, http.MethodPost, fmt.Sprintf("/api/v4/projects/%d/repository/commits", demoID), map[string]any{
			"branch": branch, "start_branch": "main", "commit_message": "add " + file,
			"actions": []map[string]string{{"action": "create", "file_path": file, "content": "x"}}}, &c)
		if err != nil {
			t.Fatalf("harness error: commit: %v", err)
		}
		return c.ID
	}
	git := func(dir, user, tok string, args ...string) (string, error) {
		pre := []string{}
		if tok != "" {
			pre = append(pre, "-c", "http.extraHeader=Authorization: Basic "+base64.StdEncoding.EncodeToString([]byte(user+":"+tok)))
		}
		cmd := exec.Command("git", append(pre, args...)...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_SSL_CAINFO="+gl.CA, "GIT_TERMINAL_PROMPT=0", "HOME="+dir)
		out, err := cmd.CombinedOutput()
		return strings.TrimSpace(string(out)), err
	}
	fetch := func(co forge.Checkout, ref string) (string, error) {
		dir := t.TempDir()
		_, _ = git(dir, "", "", "init", "-q")
		if out, err := git(dir, co.Username, co.Token.Reveal(), "fetch", "--depth=1", co.CloneURL, ref); err != nil {
			return out, err
		}
		sha, _ := git(dir, "", "", "rev-parse", "FETCH_HEAD")
		return sha, nil
	}

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
		for i := 0; i < 2; i++ {
			if err := a.EnsureWebhook(ctx, repo, recv.URL, forge.NewSecret("whsec_"+base64.StdEncoding.EncodeToString([]byte("gitlab-live-signing-key-01234567")))); err != nil {
				t.Fatalf("EnsureWebhook #%d: %v", i+1, err)
			}
		}
		var hooks []struct{ ID int }
		if err := gl.API(ctx, http.MethodGet, fmt.Sprintf("/api/v4/projects/%d/hooks", demoID), nil, &hooks); err != nil || len(hooks) != 1 {
			t.Fatalf("hooks after two EnsureWebhook: %d (%v), want exactly 1", len(hooks), err)
		}
		sha := commit("main", "hook.txt")
		for deadline := time.Now().Add(30 * time.Second); time.Now().Before(deadline); time.Sleep(100 * time.Millisecond) {
			mu.Lock()
			n := len(got)
			mu.Unlock()
			if n > 0 {
				break
			}
		}
		mu.Lock()
		defer mu.Unlock()
		if len(got) == 0 {
			t.Fatalf("GitLab delivered no webhook for a push")
		}
		d := got[0]
		ev, err := a.ParseWebhook(d.h, d.body, d.now)
		if err != nil || ev.Type != forge.EventPush || ev.Push == nil || ev.Push.After != sha || ev.Repo.ID != repo.ID {
			t.Errorf("live push delivery parsed as %+v, %v; want push of %s", ev, err, sha)
		}
	})

	t.Run("statuses", func(t *testing.T) {
		sha := commit("main", "status.txt")
		want := map[forge.State]string{forge.StatePending: "pending", forge.StateRunning: "running", forge.StateSuccess: "success",
			forge.StateFailure: "failed", forge.StateError: "failed", forge.StateCancelled: "canceled"}
		for st := range want {
			if err := a.SetStatus(ctx, repo, forge.Status{SHA: sha, Context: "bob/" + string(st), State: st, Description: "#7 " + string(st), TargetURL: "https://bob.example/builds/7"}); err != nil {
				t.Fatalf("SetStatus %s: %v", st, err)
			}
		}
		var statuses []struct{ Name, Status string }
		if err := gl.API(ctx, http.MethodGet, fmt.Sprintf("/api/v4/projects/%d/repository/commits/%s/statuses?all=true", demoID, sha), nil, &statuses); err != nil {
			t.Fatalf("harness error: %v", err)
		}
		seen := map[string]string{}
		for _, s := range statuses {
			seen[s.Name] = s.Status
		}
		for st, w := range want {
			if seen["bob/"+string(st)] != w {
				t.Errorf("state %s stored as %q, want %q", st, seen["bob/"+string(st)], w)
			}
		}
	})

	t.Run("checkout", func(t *testing.T) {
		sha := commit("main", "checkout.txt")
		ttl := 30 * time.Minute
		co, err := a.IssueCheckout(ctx, forge.CheckoutRequest{Repo: repo, JobID: "01JOBONE", TTL: ttl})
		if err != nil {
			t.Fatalf("IssueCheckout: %v", err)
		}
		if !co.ForgeExpires || !strings.HasPrefix(co.Username, "gitlab+deploy-token-") || co.Handle == "" {
			t.Errorf("Checkout = %+v, want a deploy token (gitlab+deploy-token-N) that expires on GitLab's side", co)
		}
		// deploy-token expiry: expires_at = job deadline
		var dts []struct {
			Name      string
			ExpiresAt time.Time `json:"expires_at"`
			Revoked   bool
		}
		if err := gl.API(ctx, http.MethodGet, fmt.Sprintf("/api/v4/projects/%d/deploy_tokens", demoID), nil, &dts); err != nil {
			t.Fatalf("harness error: %v", err)
		}
		okExpiry := false
		for _, d := range dts {
			if strings.Contains(d.Name, "01JOBONE") && !d.ExpiresAt.IsZero() && d.ExpiresAt.Sub(time.Now().Add(ttl)) < 2*time.Minute && time.Now().Add(ttl).Sub(d.ExpiresAt) < 2*time.Minute {
				okExpiry = true
			}
		}
		if !okExpiry {
			t.Errorf("no deploy token for job 01JOBONE with expires_at ~ now+%v: %+v", ttl, dts)
		}
		if got, err := fetch(co, sha); err != nil || got != sha {
			t.Fatalf("shallow fetch of %s: %q %v", sha, got, err)
		}
		other := strings.Replace(co.CloneURL, "root/demo", "root/other", 1)
		if out, err := git(t.TempDir(), co.Username, co.Token.Reveal(), "ls-remote", other); err == nil {
			t.Errorf("git ls-remote of root/other (%d) with the deploy token succeeded: %s", otherID, out)
		}
		for i := 0; i < 2; i++ {
			if err := a.RevokeCheckout(ctx, co); err != nil {
				t.Errorf("RevokeCheckout #%d: %v", i+1, err)
			}
		}
		if _, err := fetch(co, sha); err == nil {
			t.Errorf("fetch with a revoked deploy token succeeded")
		}
		if n, err := a.SweepCheckouts(ctx, time.Now()); n != 0 || err != nil {
			t.Errorf("SweepCheckouts = %d, %v, want 0, nil (GitLab expires deploy tokens itself)", n, err)
		}
	})

	t.Run("merge request ref", func(t *testing.T) {
		sha := commit("feature", "mr.txt")
		var mr struct{ IID int }
		if err := gl.API(ctx, http.MethodPost, fmt.Sprintf("/api/v4/projects/%d/merge_requests", demoID), map[string]any{
			"source_branch": "feature", "target_branch": "main", "title": "MR"}, &mr); err != nil {
			t.Fatalf("harness error: %v", err)
		}
		co, err := a.IssueCheckout(ctx, forge.CheckoutRequest{Repo: repo, JobID: "01JOBMR", TTL: time.Hour})
		if err != nil {
			t.Fatalf("IssueCheckout: %v", err)
		}
		defer func() { _ = a.RevokeCheckout(ctx, co) }()
		ref := fmt.Sprintf("refs/merge-requests/%d/head", mr.IID)
		if got, err := fetch(co, ref); err != nil || got != sha {
			t.Errorf("fetch of %s: %q %v, want %s", ref, got, err, sha)
		}
	})

	t.Run("credentials", func(t *testing.T) {
		bad, err := gitlabad.New(gitlabad.Config{BaseURL: gl.URL, AccessToken: forge.NewSecret("glpat-wrong"), SigningToken: forge.NewSecret("whsec_AAAA")})
		if err != nil {
			t.Fatalf("gitlab.New: %v", err)
		}
		if _, err := bad.LookupRepo(ctx, "root/demo"); !errors.Is(err, forge.ErrAuth) {
			t.Errorf("LookupRepo with a wrong token: %v, want ErrAuth", err)
		}
		if _, err := a.LookupRepo(ctx, "root/nope"); !errors.Is(err, forge.ErrNotFound) {
			t.Errorf("LookupRepo of a missing project: %v, want ErrNotFound", err)
		}
	})
}
