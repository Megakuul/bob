// Package fleet implements the spike's job queue on dynamitedb:
//
//	live/<platform>/j/<jobID>  job truth while non-terminal (queued|claimed|running), incl. lease
//	jobq/<platform>/j/<jobID>  queue hint, written before the job becomes queued, deleted after claim
//	jobs/<buildID>/j/<jobID>   job history, written once the job is terminal (terminal is absorbing)
//
// Rule: decide on a Get, write with the ETag of that Get (Put If-Match = one request).
// CustomUpdate is used only for owner renewals, because dynamitedb's Update always writes,
// even when the closure refuses the change.
package fleet

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"slices"
	"time"

	d "github.com/megakuul/dynamitedb"

	"bobspike/s09/internal/model"
	"bobspike/s09/internal/store"
)

var (
	ErrNoJob     = errors.New("fleet: no claimable job")
	ErrLostLease = errors.New("fleet: lease lost (job requeued or taken by another worker)")
)

type Q struct {
	C         *store.Conn
	B         *d.Bucket
	LeaseTTL  time.Duration
	PickAmong int // claimers pick randomly among the first N queue hints
	// Keys lists hints with raw ListObjectsV2 (no GET per hint) instead of dynamitedb.Query.
	Keys bool
	// CAS: "put" = Get + Put(If-Match) [default]; "update" = Get + Update(ETag); "custom" = Update(CustomUpdate).
	CAS string
	Now func() time.Time
	// Stats (not synchronised; one Q per goroutine)
	Conflicts, StaleHints, AmbiguousWins, Polls int64
}

func New(c *store.Conn) *Q {
	return &Q{C: c, B: c.Bucket, LeaseTTL: 30 * time.Second, PickAmong: 50, Keys: true, CAS: "put", Now: time.Now}
}

type JobRef struct{ Platform, ID, Build string }

func hintPrefix(platform string) string { return "jobq/" + platform + "/j/" }

// Enqueue writes the queue hint first, then the live job (idempotent).
func (q *Q) Enqueue(ctx context.Context, r JobRef, needs []string) error {
	now := q.Now()
	if err := store.Put(ctx, q.B, &model.QueueEntry{Platform: d.Key(r.Platform), Job: d.Key(r.ID),
		Build: d.Set(r.Build), Needs: d.Set(needs), Since: d.Set(now)}); err != nil {
		return err
	}
	err := store.Create(ctx, q.B, &model.LiveJob{Platform: d.Key(r.Platform), ID: d.Key(r.ID),
		Lease: d.Set(model.Lease{State: model.JobQueued, Changed: now}),
		Build: d.Set(r.Build), Needs: d.Set(needs), Project: d.Set("p"), Created: d.Set(now)})
	if errors.Is(err, d.ErrAlreadyExists) {
		return nil
	}
	return err
}

func liveKey(platform, id string) *model.LiveJob {
	return &model.LiveJob{Platform: d.Key(platform), ID: d.Key(id)}
}

// putLive writes j with a new lease, conditional on j's ETag (one PUT If-Match).
func (q *Q) putLive(ctx context.Context, j *model.LiveJob, l model.Lease) error {
	return store.Put(ctx, q.B, &model.LiveJob{Platform: d.Key(j.Platform.Value()), ID: d.Key(j.ID.Value()), ETag: j.ETag,
		Lease: d.Set(l), Build: d.Set(j.Build.Value()), Needs: d.Set(j.Needs.Value()),
		Project: d.Set(j.Project.Value()), Created: d.Set(j.Created.Value())})
}

// cas applies fn to the current lease with the configured CAS mode. ok=false: fn refused.
func (q *Q) cas(ctx context.Context, platform, id string, fn func(l *model.Lease) bool) (bool, error) {
	if q.CAS == "custom" {
		var ok bool
		m := liveKey(platform, id)
		m.Lease = d.CustomUpdate(func(l model.Lease) model.Lease { ok = fn(&l); return l })
		if err := store.Update(ctx, q.B, m); err != nil {
			return false, err
		}
		return ok, nil
	}
	j, err := store.Get(ctx, q.B, liveKey(platform, id))
	if err != nil {
		return false, err
	}
	l := j.Lease.Value()
	if !fn(&l) {
		return false, nil
	}
	if q.CAS == "update" {
		m := liveKey(platform, id)
		m.ETag, m.Lease = j.ETag, d.Set(l)
		return true, store.Update(ctx, q.B, m)
	}
	return true, q.putLive(ctx, j, l)
}

// Claimed is a lease held by one worker.
type Claimed struct {
	Ref   JobRef
	Epoch int
	Owner string
}

type hint struct{ id, etag string }

func (q *Q) hints(ctx context.Context, platform string) ([]hint, error) {
	if q.Keys {
		objs, err := q.C.ListObjects(ctx, hintPrefix(platform), q.PickAmong)
		if err != nil {
			return nil, err
		}
		out := make([]hint, len(objs))
		for i, o := range objs {
			out[i] = hint{store.SortKeyOf(o.Key), o.ETag}
		}
		return out, nil
	}
	hs, err := store.Query(ctx, q.B, &model.QueueEntry{Platform: d.Key(platform), Job: d.KeyPrefix("")}, d.WithLimit(q.PickAmong))
	if err != nil {
		return nil, err
	}
	out := make([]hint, len(hs))
	for i, h := range hs {
		out[i] = hint{h.Job.Value(), *h.ETag}
	}
	return out, nil
}

// Claim takes one job for platform. caps are the worker's capabilities.
func (q *Q) Claim(ctx context.Context, worker, platform string, caps []string) (*Claimed, error) {
	q.Polls++
	hs, err := q.hints(ctx, platform)
	if err != nil {
		return nil, fmt.Errorf("list hints: %w", err)
	}
	rand.Shuffle(len(hs), func(i, j int) { hs[i], hs[j] = hs[j], hs[i] })
	for _, h := range hs {
		var epoch int
		now := q.Now()
		capsOK := true
		ok, err := q.cas(ctx, platform, h.id, func(l *model.Lease) bool {
			if l.State != model.JobQueued {
				return false
			}
			l.State, l.Owner, l.Epoch, l.Attempts = model.JobClaimed, worker, l.Epoch+1, l.Attempts+1
			l.Expires, l.Changed = now.Add(q.LeaseTTL), now
			epoch = l.Epoch
			return true
		})
		_ = capsOK
		switch {
		case err == nil && ok:
			q.dropHint(ctx, platform, h)
			return &Claimed{Ref: JobRef{platform, h.id, ""}, Epoch: epoch, Owner: worker}, nil
		case err == nil && !ok, errors.Is(err, d.ErrNotFound):
			q.StaleHints++
			q.dropHint(ctx, platform, h)
		case errors.Is(err, d.ErrConcurrencyConflict):
			q.Conflicts++
			// ambiguous: our PUT may have succeeded and only its response was lost (SDK retry -> 412).
			if j, gerr := store.Get(ctx, q.B, liveKey(platform, h.id)); gerr == nil {
				l := j.Lease.Value()
				if l.Owner == worker && l.Epoch == epoch && l.State == model.JobClaimed {
					q.AmbiguousWins++
					q.dropHint(ctx, platform, h)
					return &Claimed{Ref: JobRef{platform, h.id, ""}, Epoch: epoch, Owner: worker}, nil
				}
			}
		default:
			return nil, fmt.Errorf("claim %s: %w", h.id, err)
		}
	}
	return nil, ErrNoJob
}

// dropHint deletes a hint only if it is still the version we listed (requeue rewrites it).
func (q *Q) dropHint(ctx context.Context, platform string, h hint) {
	etag := h.etag
	_ = store.Delete(ctx, q.B, &model.QueueEntry{Platform: d.Key(platform), Job: d.Key(h.id), ETag: &etag})
}

// Renew extends the lease (owner-only write; CustomUpdate = GET + PUT If-Match).
func (q *Q) Renew(ctx context.Context, c *Claimed, state string) error {
	now := q.Now()
	var ok bool
	m := liveKey(c.Ref.Platform, c.Ref.ID)
	m.Lease = d.CustomUpdate(func(l model.Lease) model.Lease {
		ok = l.Owner == c.Owner && l.Epoch == c.Epoch && (l.State == model.JobClaimed || l.State == model.JobRunning)
		if ok {
			if state != "" {
				l.State = state
			}
			l.Expires, l.Changed = now.Add(q.LeaseTTL), now
		}
		return l
	})
	err := store.Update(ctx, q.B, m)
	if errors.Is(err, d.ErrNotFound) || (err == nil && !ok) {
		return ErrLostLease
	}
	return err
}

// Complete: fenced CAS to the terminal state, then history, then remove the live record.
func (q *Q) Complete(ctx context.Context, c *Claimed, state, result string) error {
	j, err := store.Get(ctx, q.B, liveKey(c.Ref.Platform, c.Ref.ID))
	if errors.Is(err, d.ErrNotFound) {
		return ErrLostLease
	}
	if err != nil {
		return err
	}
	l := j.Lease.Value()
	if l.Owner != c.Owner || l.Epoch != c.Epoch || (l.State != model.JobClaimed && l.State != model.JobRunning) {
		return ErrLostLease
	}
	l.State, l.Changed, l.Expires = state, q.Now(), time.Time{}
	if err := q.putLive(ctx, j, l); err != nil {
		if errors.Is(err, d.ErrConcurrencyConflict) {
			return ErrLostLease
		}
		return err
	}
	j2, err := store.Get(ctx, q.B, liveKey(c.Ref.Platform, c.Ref.ID))
	if err != nil {
		return nil // reconciler finalises
	}
	_ = q.finalize(ctx, j2, result)
	return nil
}

func (q *Q) finalize(ctx context.Context, j *model.LiveJob, result string) error {
	if err := store.Put(ctx, q.B, &model.JobRecord{Build: d.Key(j.Build.Value()), ID: d.Key(j.ID.Value()),
		Lease: d.Set(j.Lease.Value()), Platform: d.Set(j.Platform.Value()), Created: d.Set(j.Created.Value()),
		Result: d.Set(result)}); err != nil {
		return err
	}
	return store.Delete(ctx, q.B, &model.LiveJob{Platform: d.Key(j.Platform.Value()), ID: d.Key(j.ID.Value()), ETag: j.ETag})
}

func terminal(s string) bool {
	return s == model.JobSucceeded || s == model.JobFailed || s == model.JobCancelled
}

// ReconcileStats counts what one pass did.
type ReconcileStats struct{ Seen, Requeued, Finalized, Conflicts int }

// Reconcile is the orchestrator controller for one platform: requeue expired leases, finalise terminal jobs.
func (q *Q) Reconcile(ctx context.Context, platform string, skew time.Duration) (ReconcileStats, error) {
	var st ReconcileStats
	page, err := store.List(ctx, q.C, "live/"+platform+"/j/", "", 0, 16, func(k string) *model.LiveJob {
		return liveKey(platform, store.SortKeyOf(k))
	})
	if err != nil {
		return st, err
	}
	now := q.Now()
	for _, j := range page.Items {
		st.Seen++
		l := j.Lease.Value()
		switch {
		case terminal(l.State):
			if err := q.finalize(ctx, j, "finalized-by-reconciler"); err == nil {
				st.Finalized++
			} else if errors.Is(err, d.ErrConcurrencyConflict) {
				st.Conflicts++
			}
		case (l.State == model.JobClaimed || l.State == model.JobRunning) && now.After(l.Expires.Add(skew)):
			h := &model.QueueEntry{Platform: d.Key(platform), Job: d.Key(j.ID.Value()), Build: d.Set(j.Build.Value()),
				Needs: d.Set(j.Needs.Value()), Since: d.Set(now)}
			if err := store.Put(ctx, q.B, h); err != nil {
				return st, err
			}
			l.State, l.Owner, l.Expires, l.Changed = model.JobQueued, "", time.Time{}, now
			err := q.putLive(ctx, j, l)
			if errors.Is(err, d.ErrConcurrencyConflict) {
				st.Conflicts++
				continue
			}
			if err != nil {
				return st, err
			}
			st.Requeued++
			h.Since = d.Set(q.Now())
			_ = store.Put(ctx, q.B, h) // second put defeats a concurrent stale-hint delete (new ETag)
		}
	}
	return st, nil
}

// TryLead acquires or renews the controller lease ctl/<name>; returns true if we lead.
func (q *Q) TryLead(ctx context.Context, name, me string, ttl time.Duration) (bool, error) {
	now := q.Now()
	err := store.Create(ctx, q.B, &model.Controller{Name: d.Key(name),
		Lease: d.Set(model.Lease{State: "held", Owner: me, Epoch: 1, Expires: now.Add(ttl), Changed: now})})
	if err == nil {
		return true, nil
	}
	if !errors.Is(err, d.ErrAlreadyExists) {
		return false, err
	}
	c, err := store.Get(ctx, q.B, &model.Controller{Name: d.Key(name)})
	if err != nil {
		return false, err
	}
	l := c.Lease.Value()
	if l.Owner != me && !now.After(l.Expires) {
		return false, nil
	}
	if l.Owner != me {
		l.Epoch++
	}
	l.Owner, l.Expires, l.Changed = me, now.Add(ttl), now
	err = store.Put(ctx, q.B, &model.Controller{Name: d.Key(name), ETag: c.ETag, Lease: d.Set(l)})
	if errors.Is(err, d.ErrConcurrencyConflict) {
		return false, nil
	}
	return err == nil, err
}

func subset(need, have []string) bool {
	for _, n := range need {
		if !slices.Contains(have, n) {
			return false
		}
	}
	return true
}
