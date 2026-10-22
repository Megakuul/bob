package orchestrator

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/megakuul/bob/internal/fleet"
	"github.com/megakuul/bob/internal/safe"
	"github.com/megakuul/bob/internal/store"
)

// Reconciler is the job-reconciler controller (SPEC §05.6.1, §05.11).
type Reconciler struct {
	Fleet     *store.Bucket
	Shards    int
	LeaseTTL  time.Duration // fleet.lease_ttl
	Skew      time.Duration // fleet.skew
	ReslotAge time.Duration // fleet.reslot_after
	Now       func() time.Time
	Name      string // orchestrator id
	// UseLease makes Pass run only while this orchestrator holds ctl/job-reconciler.
	UseLease bool

	// Queue, when set, lets Pass finalise terminal jobs that nobody finalised within
	// FinaliseAfter (an orchestrator died between the result and the finalise step).
	Queue         *Queue
	FinaliseAfter time.Duration // 0 means 10 min

	mu       sync.Mutex
	jobs     store.Cache[store.JobRecord]
	beats    map[string]time.Time // wid -> newest heartbeat seen
	workers  map[string]workerSeen
	terminal map[string]time.Time // job -> when Pass last saw it terminal
	held     map[string]heldJob   // claimed or running jobs
	// leaseUntil is when the held ctl/job-reconciler lease expires (wall clock, as the lease).
	leaseUntil time.Time
}

type workerSeen struct {
	state  string
	sigPub []byte
	at     time.Time
}

// workerCheck is how often Pass re-reads the record of a worker that holds jobs (revocation).
const workerCheck = 10 * time.Second

// terminalRecheck is how long Pass trusts that a job it saw terminal is still terminal
// (terminal states are absorbing; only a dropped forged result reopens one).
const terminalRecheck = time.Minute

// leaseInterval is the controller interval the lease TTL is derived from (3 × 10 s).
const leaseInterval = 10 * time.Second

// PassResult counts what one reconcile pass did.
type PassResult struct {
	Requeued  int
	Reslotted int
	Finalised int
}

func (r *Reconciler) now() time.Time {
	if r.Now != nil {
		return r.Now().UTC()
	}
	return time.Now().UTC()
}

// Pass runs one reconcile pass: requeue jobs of dead or revoked owners, re-slot stale queued
// jobs, finalise orphaned terminal jobs. A failure on one job is logged and retried on the
// next pass; Pass fails only when it cannot read the active-job set.
//
// The requeue bound is lease_ttl + interval + skew after the owner's last sign of life, so the
// claimed jobs Pass already knows are checked first and again at the end: a lease that
// expires while the pass reads the active set is not left for the next pass.
func (r *Reconciler) Pass(ctx context.Context) (PassResult, error) {
	var res PassResult
	if held, err := r.leader(ctx); err != nil || !held {
		return res, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.beats == nil {
		r.beats, r.workers, r.terminal, r.held = map[string]time.Time{}, map[string]workerSeen{}, map[string]time.Time{}, map[string]heldJob{}
	}
	r.sweepHeld(ctx, &res)
	shards := r.Shards
	if shards <= 0 {
		shards = 16
	}
	all, err := store.ActiveJobs(r.Fleet, shards).All(ctx)
	if err != nil {
		return res, err
	}
	now := r.now()
	for id, at := range r.terminal {
		if _, ok := all[id]; !ok || now.Sub(at) > terminalRecheck || now.Before(at) {
			delete(r.terminal, id)
		}
	}
	for id := range r.held {
		if _, ok := all[id]; !ok {
			delete(r.held, id)
		}
	}
	var ids []string
	for id := range all {
		if _, ok := r.terminal[id]; !ok || r.Queue != nil {
			ids = append(ids, id)
		}
	}
	recs := r.readJobs(ctx, ids)
	for _, id := range ids {
		if ctx.Err() != nil {
			return res, ctx.Err()
		}
		rec, ok := recs[id]
		if !ok {
			continue // missing (intent entry or finalised) or unreadable this pass
		}
		if err := r.job(ctx, rec, all[id], &res); err != nil && !errors.Is(err, store.ErrNotFound) {
			slog.Warn("job-reconciler: job skipped this pass", "job", id, "err", err, "orchestrator", r.Name)
		}
	}
	r.sweepHeld(ctx, &res)
	return res, nil
}

// heldJob is a claimed or running job as Pass last read it.
type heldJob struct {
	rec   *store.JobRecord
	class string
}

// sweepHeld requeues the known held jobs whose lease has expired by now.
func (r *Reconciler) sweepHeld(ctx context.Context, res *PassResult) {
	now := r.now()
	for id, h := range r.held {
		if ctx.Err() != nil || !r.deadByCache(h.rec, now) {
			continue
		}
		if !r.leaseExpired(ctx, h.rec, now) {
			continue
		}
		won, err := r.requeue(ctx, h.rec, h.class, now)
		if won {
			res.Requeued++
		}
		if err != nil {
			slog.Warn("job-reconciler: requeue failed", "job", id, "err", err, "orchestrator", r.Name)
			continue
		}
		delete(r.held, id)
	}
}

// leader reports whether this reconciler may work: always without UseLease, else while it
// holds ctl/job-reconciler. A held lease is renewed only when a third of its TTL is used up.
func (r *Reconciler) leader(ctx context.Context) (bool, error) {
	if !r.UseLease {
		return true, nil
	}
	const ttl = 3 * leaseInterval
	r.mu.Lock()
	until := r.leaseUntil
	r.mu.Unlock()
	if time.Until(until) > 2*ttl/3 {
		return true, nil
	}
	held, _, err := store.NewLease(r.Fleet, "ctl/job-reconciler", r.Name, ttl).TryAcquire(ctx)
	r.mu.Lock()
	r.leaseUntil = time.Time{}
	if held {
		r.leaseUntil = time.Now().Add(ttl)
	}
	r.mu.Unlock()
	return held, err
}

// readParallel bounds the concurrent job reads of one pass: a pass must finish well inside
// the controller interval for the requeue bound (lease_ttl + interval + skew) to hold.
const readParallel = 16

// readJobs reads the job records (GetCached, 1 request each) with bounded concurrency.
func (r *Reconciler) readJobs(ctx context.Context, ids []string) map[string]*store.JobRecord {
	var mu sync.Mutex
	out := make(map[string]*store.JobRecord, len(ids))
	next := make(chan string)
	var done []<-chan error
	for range min(readParallel, len(ids)) {
		done = append(done, safe.Go(func() error {
			for id := range next {
				rec, _, err := store.GetCached(ctx, r.Fleet, &r.jobs, &store.JobRecord{ID: id})
				if err != nil {
					if !errors.Is(err, store.ErrNotFound) && ctx.Err() == nil {
						slog.Warn("job-reconciler: job unreadable this pass", "job", id, "err", err)
					}
					continue
				}
				mu.Lock()
				out[id] = rec
				mu.Unlock()
			}
			return nil
		}))
	}
	for _, id := range ids {
		next <- id
	}
	close(next)
	for _, d := range done {
		<-d
	}
	return out
}

func (r *Reconciler) job(ctx context.Context, rec *store.JobRecord, aj store.ActiveJob, res *PassResult) error {
	id := rec.ID
	now := r.now()
	if rec.State != StateClaimed && rec.State != StateRunning {
		delete(r.held, id)
	}
	switch rec.State {
	case StateQueued:
		if !rec.Queued.IsZero() && rec.Queued.Add(r.ReslotAge).Before(now) {
			won, err := r.reslot(ctx, rec, aj.Class, now)
			if won {
				res.Reslotted++
			}
			return err
		}
	case StateClaimed, StateRunning:
		r.held[id] = heldJob{rec: rec, class: aj.Class}
		if r.leaseExpired(ctx, rec, now) {
			won, err := r.requeue(ctx, rec, aj.Class, now)
			if won {
				res.Requeued++
			}
			if err == nil {
				delete(r.held, id)
			}
			return err
		}
	case StateSucceeded, StateFailed, StateCancelled:
		if _, ok := r.terminal[id]; !ok {
			r.terminal[id] = now
		}
		after := r.FinaliseAfter
		if after <= 0 {
			after = 10 * time.Minute
		}
		// A succeeded job waits for its promotion verdict (pm): finalising deletes the record the
		// promotion controller works from (I-B9/I-B11).
		pending := rec.State == StateSucceeded && rec.Promotion == ""
		if r.Queue != nil && !pending && !rec.Changed.IsZero() && rec.Changed.Add(after).Before(now) {
			ok, err := r.Queue.Finalise(ctx, id)
			if ok {
				res.Finalised++
			}
			return err
		}
	}
	return nil
}

// leaseExpired is the requeue guard of §05.6.1: max(ca, whb/W.at) + lease_ttl + skew < now,
// or the owner's record says revoked or gone. A cached heartbeat can only be older than the
// stored one, so "alive" from the cache is final; "expired" is confirmed by a fresh read.
func (r *Reconciler) leaseExpired(ctx context.Context, rec *store.JobRecord, now time.Time) bool {
	if r.deadByCache(rec, now) {
		hb, err := store.Get(ctx, r.Fleet, &store.Heartbeat{Wid: rec.Owner})
		if errors.Is(err, store.ErrNotFound) {
			return true
		}
		if err != nil {
			return false
		}
		if r.dead(hb.At, now) {
			return true
		}
		// A fresh heartbeat counts only when it is signed by the registered key (when known).
		if w := r.worker(ctx, rec.Owner, now); w.sigPub != nil && !fleet.VerifyHeartbeat(w.sigPub, hb) {
			return true
		}
		if hb.At.After(r.beats[rec.Owner]) {
			r.beats[rec.Owner] = hb.At
		}
		return false
	}
	w := r.worker(ctx, rec.Owner, now)
	return w.state == "revoked" || w.state == "gone"
}

func (r *Reconciler) dead(t, now time.Time) bool { return t.Add(r.LeaseTTL + r.Skew).Before(now) }

// deadByCache: neither the claim nor the newest heartbeat seen keeps the lease alive.
func (r *Reconciler) deadByCache(rec *store.JobRecord, now time.Time) bool {
	return r.dead(rec.Claimed, now) && r.dead(r.beats[rec.Owner], now)
}

// worker returns what Pass knows of the owner's record, re-read every workerCheck.
func (r *Reconciler) worker(ctx context.Context, wid string, now time.Time) workerSeen {
	w, ok := r.workers[wid]
	if ok && now.Sub(w.at) < workerCheck && !now.Before(w.at) {
		return w
	}
	w = workerSeen{at: now}
	if rec, err := store.Get(ctx, r.Fleet, &store.WorkerRecord{Wid: wid}); err == nil {
		w.state, w.sigPub = rec.State, rec.SigPub
	}
	r.workers[wid] = w
	return w
}

// requeue puts a job whose lease expired back into the queue (epoch kept; the next claim
// increments it), or cancels it when a cancel was requested. won is true for the CAS winner.
func (r *Reconciler) requeue(ctx context.Context, rec *store.JobRecord, class string, now time.Time) (won bool, err error) {
	var queued bool
	_, err = store.CAS(ctx, r.Fleet, &store.JobRecord{ID: rec.ID}, func(cur *store.JobRecord) (*store.JobRecord, error) {
		won = false
		if cur == nil || (cur.State != StateClaimed && cur.State != StateRunning) || cur.Owner != rec.Owner || cur.Epoch != rec.Epoch {
			return nil, store.ErrNoChange
		}
		next := *cur
		next.Owner, next.Changed = "", now
		if cur.Cancel {
			next.State = StateCancelled
		} else {
			next.State, next.Queued = StateQueued, now
		}
		won, queued = true, !cur.Cancel
		return &next, nil
	})
	if err != nil || !won {
		return false, err
	}
	slog.Info("job requeued: lease expired", "job", rec.ID, "worker", rec.Owner, "epoch", rec.Epoch, "orchestrator", r.Name)
	if queued {
		_, err = store.Queue(r.Fleet, class).Append(ctx, &store.QueueSlot{Job: rec.ID, At: now})
	}
	return true, err
}

// reslot appends a new slot for a job that stayed queued for reslot_after (its slot was lost
// or skipped). won is true for the CAS winner.
func (r *Reconciler) reslot(ctx context.Context, rec *store.JobRecord, class string, now time.Time) (won bool, err error) {
	_, err = store.CAS(ctx, r.Fleet, &store.JobRecord{ID: rec.ID}, func(cur *store.JobRecord) (*store.JobRecord, error) {
		won = false
		if cur == nil || cur.State != StateQueued || !cur.Queued.Equal(rec.Queued) {
			return nil, store.ErrNoChange
		}
		next := *cur
		next.Queued = now
		won = true
		return &next, nil
	})
	if err != nil || !won {
		return false, err
	}
	_, err = store.Queue(r.Fleet, class).Append(ctx, &store.QueueSlot{Job: rec.ID, At: now})
	return true, err
}
