package orchestrator_test

import (
	"crypto/ed25519"
	"crypto/rand"
	"strings"
	"testing"
	"time"

	"github.com/megakuul/bob/internal/fleet"
	"github.com/megakuul/bob/internal/orchestrator"
	"github.com/megakuul/bob/internal/store"
)

// hold rewrites job id the way any fleet writer can (ADR 0013 T10): owner wid, epoch, running.
func (l *jobLab) hold(id, wid string, epoch int64) {
	l.t.Helper()
	err := store.Change(l.ctx, l.fleet, &store.JobRecord{ID: store.ID(id)}, func(cur *store.JobRecord) (*store.JobRecord, error) {
		next := cur.Draft()
		next.State, next.Owner, next.Epoch = store.Set(orchestrator.StateRunning), store.Set(wid), store.Set(epoch)
		return next, nil
	})
	if err != nil {
		l.t.Fatal(err)
	}
}

// worker registers wid (active) and writes a heartbeat signed by its key at at, listing jobs.
func (l *jobLab) worker(wid string, at time.Time, jobs ...store.HeldJobEpoch) ed25519.PrivateKey {
	l.t.Helper()
	pub, key, _ := ed25519.GenerateKey(rand.Reader)
	if err := store.Create(l.ctx, l.fleet, &store.WorkerRecord{Wid: store.ID(wid), State: store.Set("active"), SigPub: store.Set([]byte(pub))}); err != nil {
		l.t.Fatal(err)
	}
	l.beat(wid, key, at, jobs...)
	return key
}

func (l *jobLab) beat(wid string, key ed25519.PrivateKey, at time.Time, jobs ...store.HeldJobEpoch) {
	l.t.Helper()
	err := store.Change(l.ctx, l.fleet, &store.Heartbeat{Wid: store.ID(wid)}, func(*store.Heartbeat) (*store.Heartbeat, error) {
		hb := &store.Heartbeat{Wid: store.ID(wid), At: store.Set(at), Sig: store.Set(fleet.SignHeartbeat(key, wid, at, jobs))}
		if len(jobs) > 0 {
			hb.Jobs = store.Set(jobs)
		}
		return hb, nil
	})
	if err != nil {
		l.t.Fatal(err)
	}
}

func denied(err error) bool {
	return err != nil && strings.HasPrefix(err.Error(), "permission_denied: worker holds no lease on job ")
}

// TestCheckoutRefusesForgedJobRecords is the regression test of I-R2 F2: a worker can write
// job records, so a record it forged (a spec signed by no orchestrator, a signed spec copied
// from another job) or rewrote (owner of a job another live worker holds) gets no checkout
// credential and no download token; a lease moves on only when the reconciler would requeue
// the job (its holder revoked).
func TestCheckoutRefusesForgedJobRecords(t *testing.T) {
	l := newJobLab(t)
	if l == nil {
		return
	}
	dl := &fakeDownloads{}
	co := &orchestrator.Checkouts{Control: l.control, Queue: l.q, Forge: nil, Downloads: dl}
	now := time.Now()

	// 1. A record with a spec signed by a key that is no orchestrator's.
	_, evil, _ := ed25519.GenerateKey(rand.Reader)
	spec, err := fleet.Encode(store.JobSpec{V: 1, JobID: "forged", BuildID: "b-1", Project: "victim", Kind: "evalbuild"})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Create(l.ctx, l.fleet, &store.JobRecord{ID: store.ID("forged"), Spec: store.Set(spec), SpecSig: store.Set(fleet.SignSpec(evil, spec)),
		State: store.Set(orchestrator.StateRunning), Owner: store.Set("w2"), Epoch: store.Set(int64(1))}); err != nil {
		t.Fatal(err)
	}
	if _, err := co.Lease(l.ctx, "w2", "forged", 1); !denied(err) {
		t.Errorf("forged spec: %v, want permission_denied", err)
	}

	// 2. The signed spec of a real job copied into a record of another id.
	l.enqueue("real", "victim")
	src, err := store.Get(l.ctx, l.fleet, &store.JobRecord{ID: store.ID("real")})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Create(l.ctx, l.fleet, &store.JobRecord{ID: store.ID("copy"), Spec: store.Set(src.Spec.Value()), SpecSig: store.Set(src.SpecSig.Value()),
		State: store.Set(orchestrator.StateRunning), Owner: store.Set("w2"), Epoch: store.Set(int64(1))}); err != nil {
		t.Fatal(err)
	}
	if _, err := co.Lease(l.ctx, "w2", "copy", 1); !denied(err) {
		t.Errorf("signed spec of another job: %v, want permission_denied", err)
	}

	// 3. w1 holds the real job (live heartbeat listing it); w2 rewrites the owner.
	k1 := l.worker("w1", now)
	l.worker("w2", now)
	l.hold("real", "w1", 1)
	if _, err := co.Lease(l.ctx, "w1", "real", 1); err != nil {
		t.Fatalf("the holder: %v", err)
	}
	l.beat("w1", k1, now.Add(10*time.Second), store.HeldJobEpoch{Job: "real", Epoch: 1})
	l.hold("real", "w2", 2)
	if _, err := co.Lease(l.ctx, "w2", "real", 2); !denied(err) {
		t.Errorf("owner rewritten while the holder is alive: %v, want permission_denied", err)
	}
	if dl.n != 1 {
		t.Fatalf("refused leases minted download tokens: %d minted, want 1 (the holder's)", dl.n)
	}
	// An older epoch never comes back either.
	l.hold("real", "w1", 0)
	if _, err := co.Lease(l.ctx, "w1", "real", 0); err == nil {
		t.Errorf("a stale epoch got a lease")
	}

	// 4. The holder is revoked: the reconciler requeues, the next claim gets the lease.
	err = store.Change(l.ctx, l.fleet, &store.WorkerRecord{Wid: store.ID("w1")}, func(cur *store.WorkerRecord) (*store.WorkerRecord, error) {
		next := *cur
		next.State = store.Set("revoked")
		return &next, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	l.hold("real", "w2", 2)
	if _, err := co.Lease(l.ctx, "w2", "real", 2); err != nil {
		t.Errorf("after the holder was revoked: %v, want the lease", err)
	}
}
