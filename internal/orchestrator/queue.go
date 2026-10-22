package orchestrator

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/megakuul/bob/internal/fleet"
	"github.com/megakuul/bob/internal/store"
)

// Job states (SPEC §05.6.1).
const (
	StateQueued    = "queued"
	StateClaimed   = "claimed"
	StateRunning   = "running"
	StateSucceeded = "succeeded"
	StateFailed    = "failed"
	StateCancelled = "cancelled"
)

// Terminal reports whether a job state is absorbing.
func Terminal(state string) bool {
	return state == StateSucceeded || state == StateFailed || state == StateCancelled
}

// Queue is the orchestrator side of one class queue in the fleet bucket (SPEC §05.6.1).
type Queue struct {
	Fleet   *store.Bucket
	Control *store.Bucket      // build records (finalise)
	Class   string             // class id
	Shards  int                // fleet.job_shards
	Key     ed25519.PrivateKey // signs JobSpecs (the FleetConfig key kid)
	Kid     string
	// WorkerKey returns the Ed25519 public key of a registered worker (from worker/<wid>.sp);
	// nil when unknown. Results not signed by the owner's key are forged and ignored.
	WorkerKey func(wid string) ed25519.PublicKey
	// BetweenWrites is called between the two writes of Enqueue (test hook; may be nil). An
	// error aborts Enqueue at that point, like a crash of the enqueuer.
	BetweenWrites func(ctx context.Context) error
	// Now is the clock of the queue times (qa, ch); nil means time.Now.
	Now func() time.Time
}

// Job is one job to enqueue.
type Job struct {
	ID       string
	Build    string
	Spec     []byte // canonical CBOR JobSpec, at most 64 KiB
	Repo     string // forge id + repo id (no URL credentials), JobSpec.Repo
	Prefer   string // affinity hint (wake order only)
	Attempts int64  // for tests that start mid-life; 0 normally
}

// Order is the write order of Enqueue.
type Order int

const (
	// JobFirst writes the job record before its slot (R-FLEET-011, the only correct order).
	JobFirst Order = iota
	// SlotFirst is the order V2 run 1 used; it exists only for the regression test, which
	// expects it to lose a job.
	SlotFirst
)

func (q *Queue) now() time.Time {
	if q.Now != nil {
		return q.Now().UTC()
	}
	return time.Now().UTC()
}

func (q *Queue) shards() int {
	if q.Shards > 0 {
		return q.Shards
	}
	return 16
}

func (q *Queue) active() *store.ShardSet[store.ActiveJob] {
	return store.ActiveJobs(q.Fleet, q.shards())
}

// spec returns the canonical JobSpec stored in job/<id>.sp: j.Spec with the queue's fields
// (job, build, class, repo, prefer) filled in. A Spec that is not a JobSpec (opaque bytes of
// in-process tests) is replaced by one built from the Job's fields (decision D-IB2-3).
func (q *Queue) spec(j Job, now time.Time) ([]byte, error) {
	s, ok := fleet.DecodeSpec(j.Spec)
	if !ok {
		s = store.JobSpec{V: 1}
	}
	set := func(field *string, v, name string) error {
		switch {
		case v == "":
		case *field == "":
			*field = v
		case *field != v:
			return fmt.Errorf("orchestrator: job %s: spec %s %q differs from %q", j.ID, name, *field, v)
		}
		return nil
	}
	for _, f := range []struct {
		field   *string
		v, name string
	}{{&s.JobID, j.ID, "job"}, {&s.BuildID, j.Build, "build"}, {&s.Class, q.Class, "class"}, {&s.Repo, j.Repo, "repo"}, {&s.Prefer, j.Prefer, "prefer"}} {
		if err := set(f.field, f.v, f.name); err != nil {
			return nil, err
		}
	}
	if s.Created == 0 {
		s.Created = now.Unix()
	}
	b, err := fleet.Encode(s)
	if err != nil {
		return nil, err
	}
	if len(b) > store.MaxJobSpec {
		return nil, fmt.Errorf("orchestrator: job %s: spec is %d bytes, the limit is %d", j.ID, len(b), store.MaxJobSpec)
	}
	return b, nil
}

// Enqueue is the build controller's enqueue: CAS jact (intent first), create job (signed
// spec), append slot. It returns the slot number.
func (q *Queue) Enqueue(ctx context.Context, j Job, o Order) (uint64, error) {
	if _, err := store.Segment(j.ID); err != nil {
		return 0, fmt.Errorf("orchestrator: job id %q: %w", j.ID, err)
	}
	if len(q.Key) != ed25519.PrivateKeySize {
		return 0, errors.New("orchestrator: queue has no signing key")
	}
	now := q.now()
	spec, err := q.spec(j, now)
	if err != nil {
		return 0, err
	}
	if err := q.active().Add(ctx, j.ID, store.ActiveJob{Build: j.Build, Class: q.Class, Created: now}); err != nil {
		return 0, err
	}
	rec := &store.JobRecord{ID: store.ID(j.ID), Spec: store.Set(spec), SpecSig: store.Set(fleet.SignSpec(q.Key, spec)), Kid: store.Set(q.Kid),
		State: store.Set[string](StateQueued), Queued: store.Set(now), Attempt: store.Set(j.Attempts)}
	createJob := func() error { return store.Create(ctx, q.Fleet, rec) }
	appendSlot := func() (uint64, error) {
		return store.Queue(q.Fleet, q.Class).Append(ctx, &store.QueueSlot{Job: store.Set(j.ID), At: store.Set(now)})
	}
	between := func() error {
		if q.BetweenWrites == nil {
			return nil
		}
		return q.BetweenWrites(ctx)
	}
	if o == SlotFirst {
		seq, err := appendSlot()
		if err != nil {
			return 0, err
		}
		if err := between(); err != nil {
			return seq, err
		}
		return seq, createJob()
	}
	if err := createJob(); err != nil {
		return 0, err
	}
	if err := between(); err != nil {
		return 0, err
	}
	return appendSlot()
}

// JobView is a read-only view of job/<id> for observers.
type JobView struct {
	State     string // queued|claimed|running|succeeded|failed|cancelled
	Owner     string
	Epoch     int64
	Attempt   int64
	Cancel    bool
	Result    string // ok|failed|infra when finished
	Promotion string
}

func viewOf(r *store.JobRecord) *JobView {
	v := &JobView{State: r.State.Value(), Owner: r.Owner.Value(), Epoch: r.Epoch.Value(), Attempt: r.Attempt.Value(), Cancel: r.Cancel.Value(), Promotion: r.Promotion.Value()}
	if res, ok := fleet.DecodeResult(r.Result.Value()); ok {
		v.Result = res.Status
	}
	return v
}

func (q *Queue) record(ctx context.Context, id string) (*store.JobRecord, error) {
	if _, err := store.Segment(id); err != nil {
		return nil, fmt.Errorf("orchestrator: job id %q: %w", id, store.ErrNotFound)
	}
	return store.Get(ctx, q.Fleet, &store.JobRecord{ID: store.ID(id)})
}

// Job reads job/<id>. store.ErrNotFound when the record does not exist (never created or finalised).
func (q *Queue) Job(ctx context.Context, id string) (*JobView, error) {
	r, err := q.record(ctx, id)
	if err != nil {
		return nil, err
	}
	return viewOf(r), nil
}

// SlotTaken reads the `tk` field of slot seq: a worker id, "stale", "orphan", or "" when open.
func (q *Queue) SlotTaken(ctx context.Context, seq uint64) (string, error) {
	s, err := store.Get(ctx, q.Fleet, store.QueueSlotKey(q.Class, seq))
	if err != nil {
		return "", err
	}
	return s.Taken.Value(), nil
}

// Cancel requests cancellation of a job (queued: cancelled at once; claimed or running: cx).
func (q *Queue) Cancel(ctx context.Context, id string) error {
	now := q.now()
	_, err := store.CAS(ctx, q.Fleet, &store.JobRecord{ID: store.ID(id)}, func(cur *store.JobRecord) (*store.JobRecord, error) {
		if cur == nil {
			return nil, store.ErrNotFound
		}
		next := *cur
		switch cur.State.Value() {
		case StateQueued:
			next.State, next.Changed = store.Set[string](StateCancelled), store.Set(now)
		case StateClaimed, StateRunning:
			if cur.Cancel.Value() {
				return nil, store.ErrNoChange
			}
			next.Cancel = store.Set[bool](true)
		default:
			return nil, store.ErrNoChange
		}
		return &next, nil
	})
	return err
}

// forged is the metric bob_fleet_forged_results_total of this process.
var forged atomic.Int64

// ForgedResults is the value of the metric bob_fleet_forged_results_total of this process.
func ForgedResults() int64 { return forged.Load() }

// errJobChanged is returned by Finalise when the record changed between its read and its
// delete; the caller runs Finalise again.
var errJobChanged = errors.New("job changed during finalise")

// Finalise is the build controller's finalise step for one terminal job: copy the summary
// into the build record, delete job/<id> (If-Match), remove it from jact. Result is returned
// as accepted only when it was signed by the job's owner at the job's epoch.
func (q *Queue) Finalise(ctx context.Context, id string) (accepted bool, err error) {
	rec, err := q.record(ctx, id)
	if err != nil {
		return false, err
	}
	if !Terminal(rec.State.Value()) {
		return false, fmt.Errorf("orchestrator: job %s is %s, not terminal", id, rec.State.Value())
	}
	if rec.State.Value() != StateCancelled && !q.signedByOwner(rec) {
		return false, q.dropForged(ctx, rec)
	}
	spec, _ := fleet.DecodeSpec(rec.Spec.Value())
	if q.Control != nil && spec.BuildID != "" {
		if err := q.summarise(ctx, spec.BuildID, rec); err != nil {
			return false, err
		}
	}
	_, err = store.CAS(ctx, q.Fleet, &store.JobRecord{ID: store.ID(id)}, func(cur *store.JobRecord) (*store.JobRecord, error) {
		if cur == nil {
			return nil, store.ErrNoChange
		}
		if cur.State.Value() != rec.State.Value() || cur.Epoch.Value() != rec.Epoch.Value() || cur.Owner.Value() != rec.Owner.Value() {
			return nil, errJobChanged
		}
		return nil, nil
	})
	if err != nil {
		return false, err
	}
	if err := q.active().Remove(ctx, id); err != nil {
		return true, err
	}
	return true, nil
}

func (q *Queue) signedByOwner(rec *store.JobRecord) bool {
	if q.WorkerKey == nil {
		return false
	}
	return fleet.VerifyResult(q.WorkerKey(rec.Owner.Value()), rec.ID.Value(), rec.Epoch.Value(), rec.Result.Value(), rec.ResultSig.Value())
}

// dropForged counts a result that the owner did not sign and puts the job back to the state
// the owner left it in, so that the owner's own result can still land (R-FLEET-002).
func (q *Queue) dropForged(ctx context.Context, rec *store.JobRecord) error {
	forged.Add(1)
	_, err := store.CAS(ctx, q.Fleet, &store.JobRecord{ID: rec.ID}, func(cur *store.JobRecord) (*store.JobRecord, error) {
		if cur == nil || store.ETagOf(cur.ETag) != store.ETagOf(rec.ETag) {
			return nil, store.ErrNoChange
		}
		next := *cur
		next.State, next.Result, next.ResultSig = store.Set[string](StateRunning), nil, nil
		if cur.Changed.Value().IsZero() {
			next.State = store.Set[string](StateClaimed)
		}
		return &next, nil
	})
	return err
}

// summarise copies the job's final row into build/<id>.jb (absent build: nothing to do).
func (q *Queue) summarise(ctx context.Context, buildID string, rec *store.JobRecord) error {
	_, err := store.CAS(ctx, q.Control, &store.BuildRecord{ID: store.ID(buildID)}, func(cur *store.BuildRecord) (*store.BuildRecord, error) {
		if cur == nil {
			return nil, store.ErrNoChange
		}
		next := *cur
		row := store.BuildJob{ID: rec.ID.Value(), State: rec.State.Value(), Attempts: rec.Attempt.Value(), Epoch: rec.Epoch.Value(), Worker: rec.Owner.Value(), Promotion: rec.Promotion.Value()}
		if spec, ok := fleet.DecodeSpec(rec.Spec.Value()); ok {
			row.Kind, row.Class = spec.Kind, spec.Class
		}
		if !rec.Claimed.Value().IsZero() && !rec.Changed.Value().IsZero() {
			row.Duration = rec.Changed.Value().Sub(rec.Claimed.Value())
		}
		next.Jobs = store.Set(append([]store.BuildJob(nil), cur.Jobs.Value()...))
		for i := range next.Jobs.Value() {
			if next.Jobs.Value()[i].ID == rec.ID.Value() {
				if next.Jobs.Value()[i] == row {
					return nil, store.ErrNoChange
				}
				next.Jobs.Value()[i] = row
				return &next, nil
			}
		}
		if len(next.Jobs.Value()) >= store.MaxBuildJobs {
			return nil, fmt.Errorf("orchestrator: build %s has %d jobs, the limit is %d", buildID, len(next.Jobs.Value()), store.MaxBuildJobs)
		}
		next.Jobs = store.Set(append(next.Jobs.Value(), row))
		return &next, nil
	})
	return err
}
