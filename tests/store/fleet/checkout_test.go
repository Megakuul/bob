package fleet

import (
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/megakuul/bob/internal/orchestrator"
	"github.com/megakuul/bob/internal/worker"
	"github.com/megakuul/bob/tests/parity"
	"github.com/megakuul/bob/tests/store/env"
)

// fakeForge is the forge adapter's token side: it mints and revokes per-job tokens and
// remembers which are live.
type fakeForge struct {
	mu    sync.Mutex
	n     int
	live  map[string]string // handle -> token value
	repos []string
}

func (f *fakeForge) Mint(_ context.Context, repo string, expires time.Time) (string, orchestrator.Checkout, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.n++
	if f.live == nil {
		f.live = map[string]string{}
	}
	h, tok := fmt.Sprintf("h%d", f.n), fmt.Sprintf("secret-token-%d-9f3a", f.n)
	f.live[h] = tok
	f.repos = append(f.repos, repo)
	return h, orchestrator.Checkout{CloneURL: "https://forge.example/" + repo + ".git", Username: "bob", Token: tok, Expires: expires}, nil
}

func (f *fakeForge) Revoke(_ context.Context, handle string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.live, handle)
	return nil
}

func (f *fakeForge) liveCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.live)
}

func (f *fakeForge) tokens() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, t := range f.live {
		out = append(out, t)
	}
	return out
}

// TestRFLEET006CheckoutOnlyForTheHeldJob: a worker gets a checkout credential only for a job it
// holds (owner + epoch, state claimed or running), for the job's one repository; the token
// is revoked at job end; the forge-sweeper revokes what an orchestrator that died between
// mint and revoke left behind; the record never holds the token value (D62).
// The Forgejo-live bullets (clone of R works, R2 gives 403, 401 after the job, fork-PR ref)
// belong to the forge-forgejo-live suite.
func TestRFLEET006CheckoutOnlyForTheHeldJob(t *testing.T) {
	parity.Covers(t, "R-FLEET-006")
	l := newLab(t)
	if l == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	forge := &fakeForge{}
	co := &orchestrator.Checkouts{Control: l.control, Queue: l.q, Forge: forge, Now: l.clk.Now}
	// Ruling G3: the denial text is this one everywhere (SPEC §6.9.6 aligned); the forge status
	// for a repo outside the token scope is 403 (forge-forgejo-live, tests/forge).
	denied := func(job string) string { return "permission_denied: worker holds no lease on job " + job }

	release := make(chan struct{})
	if l.start(l.workerCfg("w1", func(ctx context.Context, c worker.Claim) worker.Outcome {
		select {
		case <-release:
		case <-ctx.Done():
		}
		return worker.Outcome{Status: "ok"}
	})) == nil {
		return
	}
	j := l.job("j1")
	j.Repo = "forge1/repo-R"
	if _, err := l.q.Enqueue(ctx, j, orchestrator.JobFirst); err != nil {
		if env.Red(t, err) {
			return
		}
		t.Fatal(err)
	}
	v := l.waitState("j1", 20*time.Second, "claimed", "running")
	if v == nil || v.Owner != "w1" {
		t.Fatalf("j1 not held by w1: %+v", v)
	}

	// Not the owner, stale epoch, future epoch, unknown job: all the same refusal, nothing minted.
	for name, c := range map[string]struct {
		wid, job string
		epoch    int64
	}{
		"another worker": {"w2", "j1", v.Epoch},
		"stale epoch":    {"w1", "j1", v.Epoch - 1},
		"future epoch":   {"w1", "j1", v.Epoch + 1},
		"unknown job":    {"w1", "nope", 1},
	} {
		_, err := co.Get(ctx, c.wid, c.job, c.epoch)
		if env.Red(t, err) {
			return
		}
		if err == nil || err.Error() != denied(c.job) {
			t.Errorf("%s: error %v, want %q", name, err, denied(c.job))
		}
	}
	if forge.liveCount() != 0 || len(forge.repos) != 0 {
		t.Fatalf("a refused request minted tokens: %d live, repos %v", forge.liveCount(), forge.repos)
	}

	// The holder gets one credential for the job's repository.
	cred, err := co.Get(ctx, "w1", "j1", v.Epoch)
	if err != nil {
		t.Fatalf("Get by the holder: %v", err)
	}
	if len(forge.repos) != 1 || forge.repos[0] != "forge1/repo-R" {
		t.Errorf("token minted for %v, want exactly the job's repository forge1/repo-R", forge.repos)
	}
	if cred.Token == "" || strings.Contains(cred.CloneURL, cred.Token) || !strings.HasPrefix(cred.CloneURL, "https://") {
		t.Errorf("credential = %+v: want an https clone URL without credentials and a token", cred)
	}
	if !cred.Expires.After(l.clk.Now()) {
		t.Errorf("credential already expired: %v", cred.Expires)
	}
	assertNoTokenInControl(t, l, forge.tokens())

	// The sweeper leaves the token of a running job alone.
	if n, err := co.Sweep(ctx); err != nil || n != 0 || forge.liveCount() != 1 {
		t.Errorf("sweep with a running job: revoked %d (%v), %d live; want 0 and 1", n, err, forge.liveCount())
	}

	// The orchestrator that minted it dies before the job ends: no JobEnded call. The job ends.
	close(release)
	if v := l.waitState("j1", 20*time.Second, "succeeded"); v == nil || v.State != "succeeded" {
		t.Fatalf("j1 did not finish: %+v", v)
	}
	if forge.liveCount() != 1 {
		t.Fatalf("the token vanished without a revoke")
	}
	l.clk.Advance(61 * time.Second) // one sweeper interval + skew later
	if n, err := co.Sweep(ctx); err != nil || n != 1 || forge.liveCount() != 0 {
		t.Errorf("sweep after the job ended: revoked %d (%v), %d live; want 1 and 0", n, err, forge.liveCount())
	}
	// A finished job gets no credential any more.
	if _, err := co.Get(ctx, "w1", "j1", v.Epoch); err == nil || err.Error() != denied("j1") {
		t.Errorf("Get for a finished job: %v, want %q", err, denied("j1"))
	}
}

// TestRFLEET006RevokedAtJobEndAndExpiry: JobEnded revokes the job's tokens at once; a token
// past its expiry is revoked by the sweeper even while the job runs.
func TestRFLEET006RevokedAtJobEndAndExpiry(t *testing.T) {
	parity.Covers(t, "R-FLEET-006")
	l := newLab(t)
	if l == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	forge := &fakeForge{}
	co := &orchestrator.Checkouts{Control: l.control, Queue: l.q, Forge: forge, Now: l.clk.Now}
	hold := make(chan struct{})
	t.Cleanup(func() { close(hold) })
	if l.start(l.workerCfg("w1", func(ctx context.Context, c worker.Claim) worker.Outcome {
		select {
		case <-hold:
		case <-ctx.Done():
		}
		return worker.Outcome{Status: "ok"}
	})) == nil {
		return
	}
	j := l.job("j2")
	j.Repo = "forge1/repo-R"
	if _, err := l.q.Enqueue(ctx, j, orchestrator.JobFirst); err != nil {
		if env.Red(t, err) {
			return
		}
		t.Fatal(err)
	}
	v := l.waitState("j2", 20*time.Second, "claimed", "running")
	cred, err := co.Get(ctx, "w1", "j2", v.Epoch)
	if err != nil {
		t.Fatal(err)
	}
	l.clk.Advance(cred.Expires.Sub(l.clk.Now()) + time.Second)
	if n, err := co.Sweep(ctx); err != nil || n != 1 || forge.liveCount() != 0 {
		t.Errorf("sweep past the token expiry: revoked %d (%v), %d live; want the expired token revoked", n, err, forge.liveCount())
	}
	if _, err := co.Get(ctx, "w1", "j2", v.Epoch); err != nil {
		t.Fatal(err)
	}
	if forge.liveCount() != 1 {
		t.Fatalf("second credential not live")
	}
	if err := co.JobEnded(ctx, "j2"); err != nil || forge.liveCount() != 0 {
		t.Errorf("JobEnded: %v, %d tokens still live; want all revoked at job end", err, forge.liveCount())
	}
}

// assertNoTokenInControl checks that no object under ftok/ in the control bucket holds a
// token value (the record keeps only the forge handle, D62).
func assertNoTokenInControl(t *testing.T, l *lab, tokens []string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	resp, err := l.be.FS.Do(ctx, l.be.FS.Root, http.MethodGet, "/"+l.cname+"?list-type=2&prefix=ftok/", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	var out struct{ Contents []struct{ Key string } }
	err = xml.NewDecoder(resp.Body).Decode(&out)
	resp.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Contents) == 0 {
		t.Errorf("no checkout record under ftok/ after a credential was minted")
	}
	for _, c := range out.Contents {
		r, err := l.be.FS.Do(ctx, l.be.FS.Root, http.MethodGet, "/"+l.cname+"/"+c.Key, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(r.Body)
		r.Body.Close()
		for _, tok := range tokens {
			if strings.Contains(string(b), tok) {
				t.Errorf("%s holds the token value", c.Key)
			}
		}
	}
}
