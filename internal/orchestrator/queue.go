package orchestrator

import (
	"context"
	"crypto/ed25519"
	"time"

	"github.com/megakuul/bob/internal/store"
	"github.com/megakuul/bob/pkg/bob/diag"
)

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

// Enqueue is the build controller's enqueue: CAS jact (intent first), create job (signed
// spec), append slot. It returns the slot number.
func (q *Queue) Enqueue(ctx context.Context, j Job, o Order) (uint64, error) {
	return 0, diag.NotImplemented("orchestrator.Queue.Enqueue")
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

// Job reads job/<id>. store.ErrNotFound when the record does not exist (never created or finalised).
func (q *Queue) Job(ctx context.Context, id string) (*JobView, error) {
	return nil, diag.NotImplemented("orchestrator.Queue.Job")
}

// SlotTaken reads the `tk` field of slot seq: a worker id, "stale", "orphan", or "" when open.
func (q *Queue) SlotTaken(ctx context.Context, seq uint64) (string, error) {
	return "", diag.NotImplemented("orchestrator.Queue.SlotTaken")
}

// Cancel requests cancellation of a job (queued: cancelled at once; claimed or running: cx).
func (q *Queue) Cancel(ctx context.Context, id string) error {
	return diag.NotImplemented("orchestrator.Queue.Cancel")
}

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
}

// PassResult counts what one reconcile pass did.
type PassResult struct {
	Requeued  int
	Reslotted int
	Finalised int
}

// Pass runs one reconcile pass: requeue jobs of dead or revoked owners, re-slot stale queued
// jobs, finalise orphaned terminal jobs.
func (r *Reconciler) Pass(ctx context.Context) (PassResult, error) {
	return PassResult{}, diag.NotImplemented("orchestrator.Reconciler.Pass")
}

// Finalise is the build controller's finalise step for one terminal job: copy the summary
// into the build record, delete job/<id> (If-Match), remove it from jact. Result is returned
// as accepted only when it was signed by the job's owner at the job's epoch.
func (q *Queue) Finalise(ctx context.Context, id string) (accepted bool, err error) {
	return false, diag.NotImplemented("orchestrator.Queue.Finalise")
}

// ForgedResults is the value of the metric bob_fleet_forged_results_total of this process.
func ForgedResults() int64 { return -1 }

// Wake is the transport of a wake-up to one attached worker (Attach, SPEC §05.7).
type Wake interface {
	ID() string
	FreeSlots() int
	Wake(class string, slot uint64, job string)
}

// Dispatcher chooses whom to wake after an enqueue (R-FLEET-012).
type Dispatcher struct {
	Queue   *Queue
	Workers []Wake
	// SecondWakeAfter is the delay before a second worker is woken for a job that is still
	// queued (2 s).
	SecondWakeAfter time.Duration
	Seed            uint64
}

// Enqueue enqueues j and wakes one idle eligible worker: the Prefer worker first, else the one
// with the most free slots, ties at random; after SecondWakeAfter it wakes a second one if the
// job is still queued.
func (d *Dispatcher) Enqueue(ctx context.Context, j Job) (slot uint64, woken []string, err error) {
	return 0, nil, diag.NotImplemented("orchestrator.Dispatcher.Enqueue")
}
