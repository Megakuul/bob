package orchestrator_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/megakuul/bob/internal/fleet"
	"github.com/megakuul/bob/internal/orchestrator"
	"github.com/megakuul/bob/internal/store"
	"github.com/megakuul/bob/tests/store/env"
)

// fakeDownloads is the auth side of the job download tokens: it remembers which are live.
type fakeDownloads struct {
	mu       sync.Mutex
	n        int
	live     map[string]string // handle -> token
	projects []string
}

func (f *fakeDownloads) IssueJobDownload(_ context.Context, jobID, project string, _ time.Time) (string, string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.n++
	if f.live == nil {
		f.live = map[string]string{}
	}
	h, tok := fmt.Sprintf("h%d", f.n), fmt.Sprintf("bob_dt_secret%d", f.n)
	f.live[h] = tok
	f.projects = append(f.projects, project+"/"+jobID)
	return tok, h, nil
}

func (f *fakeDownloads) RevokeJobDownload(_ context.Context, handle string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.live, handle)
	return nil
}

func (f *fakeDownloads) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.live)
}

type jobLab struct {
	t              *testing.T
	ctx            context.Context
	be             *env.Backend
	control, fleet *store.Bucket
	q              *orchestrator.Queue
	wkey           ed25519.PrivateKey
}

func newJobLab(t *testing.T) *jobLab {
	be := env.Start(t)
	control, fl := be.Open(t, "control"), be.Open(t, "fleet")
	if fl == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	t.Cleanup(cancel)
	_, okey, _ := ed25519.GenerateKey(rand.Reader)
	wpub, wkey, _ := ed25519.GenerateKey(rand.Reader)
	q := &orchestrator.Queue{Fleet: fl, Class: "c1", Shards: 4, Key: okey, Kid: "k1",
		WorkerKey: func(string) ed25519.PublicKey { return wpub }}
	return &jobLab{t: t, ctx: ctx, be: be, control: control, fleet: fl, q: q, wkey: wkey}
}

func (l *jobLab) enqueue(id, project string) {
	l.t.Helper()
	spec, err := fleet.Encode(store.JobSpec{V: 1, JobID: id, BuildID: "b-1", Project: project, Kind: "evalbuild"})
	if err != nil {
		l.t.Fatal(err)
	}
	if _, err := l.q.Enqueue(l.ctx, orchestrator.Job{ID: id, Build: "b-1", Spec: spec}, orchestrator.JobFirst); err != nil {
		l.t.Fatal(err)
	}
}

// set moves job id to state (owner w1 at epoch), with a result signed by the worker key when
// terminal, last changed at changed.
func (l *jobLab) set(id, state string, epoch int64, res *store.JobResult, changed time.Time) {
	l.t.Helper()
	err := store.Change(l.ctx, l.fleet, &store.JobRecord{ID: store.ID(id)}, func(cur *store.JobRecord) (*store.JobRecord, error) {
		next := *cur
		next.State, next.Owner, next.Epoch, next.Changed = store.Set(state), store.Set("w1"), store.Set(epoch), store.Set(changed)
		if res != nil {
			rs, err := fleet.Encode(*res)
			if err != nil {
				return nil, err
			}
			next.Result, next.ResultSig = store.Set(rs), store.Set(fleet.SignResult(l.wkey, id, epoch, rs))
		}
		return &next, nil
	})
	if err != nil {
		l.t.Fatal(err)
	}
}

// TestCheckoutLeaseDownloadToken: GetCheckoutCredential's download token (§05.9.1) is minted
// only for the worker that holds the job, for the job's project; the control bucket keeps its
// handle, never the token; a repeated call revokes the earlier token; the sweeper revokes it
// when the lease moves on (requeue) and when the job ends; JobEnded revokes it at once.
func TestCheckoutLeaseDownloadToken(t *testing.T) {
	l := newJobLab(t)
	if l == nil {
		return
	}
	dl := &fakeDownloads{}
	co := &orchestrator.Checkouts{Control: l.control, Queue: l.q, Forge: nil, Downloads: dl}
	l.enqueue("j1", "p1")
	l.set("j1", orchestrator.StateRunning, 1, nil, time.Now())

	for name, c := range map[string]struct {
		wid   string
		epoch int64
	}{"another worker": {"w2", 1}, "stale epoch": {"w1", 0}, "future epoch": {"w1", 2}} {
		if _, err := co.Lease(l.ctx, c.wid, "j1", c.epoch); err == nil || !strings.HasPrefix(err.Error(), "permission_denied: ") {
			t.Errorf("%s: %v, want permission_denied", name, err)
		}
	}
	if dl.n != 0 {
		t.Fatalf("a refused lease minted %d download tokens", dl.n)
	}

	lease, err := co.Lease(l.ctx, "w1", "j1", 1)
	if err != nil {
		t.Fatal(err)
	}
	if lease.DownloadToken == "" || !lease.DownloadExpires.After(time.Now()) || lease.Spec.Project != "p1" || dl.projects[0] != "p1/j1" {
		t.Fatalf("lease %+v (minted for %v): want a live download token for p1/j1", lease, dl.projects)
	}
	assertNoSecret(t, l, lease.DownloadToken)

	again, err := co.Lease(l.ctx, "w1", "j1", 1)
	if err != nil {
		t.Fatal(err)
	}
	if dl.count() != 1 || dl.live["h2"] != again.DownloadToken {
		t.Errorf("after a repeated call %d tokens live (%v), want only the new one", dl.count(), dl.live)
	}
	if n, err := co.Sweep(l.ctx); err != nil || n != 0 || dl.count() != 1 {
		t.Errorf("sweep with the job running: revoked %d (%v), %d live; want 0, 1", n, err, dl.count())
	}

	// The lease moves on (requeued, claimed again at epoch 2): the old token is due.
	l.set("j1", orchestrator.StateRunning, 2, nil, time.Now())
	if n, err := co.Sweep(l.ctx); err != nil || n != 1 || dl.count() != 0 {
		t.Errorf("sweep after the lease moved on: revoked %d (%v), %d live; want 1, 0", n, err, dl.count())
	}

	// Job end: the sweeper revokes the token of a terminal job, JobEnded at once.
	if _, err := co.Lease(l.ctx, "w1", "j1", 2); err != nil {
		t.Fatal(err)
	}
	l.set("j1", orchestrator.StateFailed, 2, &store.JobResult{V: 1, Status: "failed"}, time.Now())
	if n, err := co.Sweep(l.ctx); err != nil || n != 1 || dl.count() != 0 {
		t.Errorf("sweep after the job ended: revoked %d (%v), %d live; want 1, 0", n, err, dl.count())
	}
	if _, err := co.Lease(l.ctx, "w1", "j1", 2); err == nil {
		t.Errorf("a finished job got a download token")
	}
	l.enqueue("j2", "p2")
	l.set("j2", orchestrator.StateClaimed, 1, nil, time.Now())
	if _, err := co.Lease(l.ctx, "w1", "j2", 1); err != nil {
		t.Fatal(err)
	}
	if err := co.JobEnded(l.ctx, "j2"); err != nil || dl.count() != 0 {
		t.Errorf("JobEnded: %v, %d tokens live; want 0", err, dl.count())
	}
}

// assertNoSecret checks that no ftok/ object of the control bucket holds the token value.
func assertNoSecret(t *testing.T, l *jobLab, tok string) {
	t.Helper()
	resp, err := l.be.FS.Do(l.ctx, l.be.FS.Root, http.MethodGet, "/"+l.control.Name()+"?list-type=2&prefix=ftok/", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(b), "ftok/") {
		t.Fatalf("no ftok/ record after a download token was minted: %s", b)
	}
	for _, part := range strings.Split(string(b), "<Key>")[1:] {
		key, _, _ := strings.Cut(part, "</Key>")
		r, err := l.be.FS.Do(l.ctx, l.be.FS.Root, http.MethodGet, "/"+l.control.Name()+"/"+key, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(r.Body)
		r.Body.Close()
		if strings.Contains(string(body), tok) {
			t.Errorf("%s holds the download token", key)
		}
	}
}

// TestReconcilerKeepsJobsAwaitingPromotion: the reconciler finalises orphaned terminal jobs
// after FinaliseAfter, except those that await a promotion verdict: succeeded without one, and
// failed with staged outputs (A200). A failed job without staged outputs and a promoted job
// are finalised.
func TestReconcilerKeepsJobsAwaitingPromotion(t *testing.T) {
	l := newJobLab(t)
	if l == nil {
		return
	}
	old := time.Now().Add(-time.Hour)
	l.enqueue("staged", "p1")
	l.set("staged", orchestrator.StateFailed, 1, &store.JobResult{V: 1, Status: "failed", Complete: "staging/b-1/staged/1/COMPLETE.json"}, old)
	l.enqueue("nothing", "p1")
	l.set("nothing", orchestrator.StateFailed, 1, &store.JobResult{V: 1, Status: "failed"}, old)
	l.enqueue("ok", "p1")
	l.set("ok", orchestrator.StateSucceeded, 1, &store.JobResult{V: 1, Status: "ok", Complete: "staging/b-1/ok/1/COMPLETE.json"}, old)
	r := &orchestrator.Reconciler{Fleet: l.fleet, Shards: 4, LeaseTTL: time.Minute, Skew: time.Second, Name: "o1", Queue: l.q, FinaliseAfter: 10 * time.Minute}
	res, err := r.Pass(l.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if res.Finalised != 1 {
		t.Errorf("finalised %d, want 1 (only the failed job without staged outputs)", res.Finalised)
	}
	for id, want := range map[string]bool{"staged": true, "nothing": false, "ok": true} {
		_, err := store.Get(l.ctx, l.fleet, &store.JobRecord{ID: store.ID(id)})
		if got := err == nil; got != want {
			t.Errorf("job %s record kept = %v (%v), want %v", id, got, err, want)
		}
	}
}
