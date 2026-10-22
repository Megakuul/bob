package worker

import (
	"context"
	"crypto/ed25519"
	"errors"
	"time"

	"github.com/megakuul/bob/internal/store"
	"github.com/megakuul/bob/pkg/bob/diag"
)

// Claim is a job a worker holds.
type Claim struct {
	JobID   string
	Epoch   int64
	Attempt int64
	Spec    []byte
}

// Outcome is what a job run reports.
type Outcome struct {
	Status string // "ok" | "failed" | "infra"
	Error  string
}

// Config configures one in-process worker (the server worker mode builds the same value).
type Config struct {
	Fleet   *store.Bucket
	ID      string
	Profile store.WorkerProfile
	Classes []store.Class
	Slots   int
	// Keys are the orchestrator public keys of FleetConfig.k; a JobSpec that none of them
	// signed is never run (its slot is marked stale).
	Keys []ed25519.PublicKey
	// SigKey signs the worker's job results (the worker identity's Ed25519 key).
	SigKey ed25519.PrivateKey

	PollInterval  time.Duration // fleet.poll_interval, ± 20 %
	Heartbeat     time.Duration // fleet.heartbeat
	ConfigRefresh time.Duration // 30 s
	RecordRefresh time.Duration // 60 s
	LeaseTTL      time.Duration
	Skew          time.Duration
	JobWatch      time.Duration // 30 s
	Seed          uint64
	Now           func() time.Time

	// Events receives the worker's job events (for tests and metrics); may be nil.
	Events func(Event)

	// Exec runs one claimed job. ctx is cancelled when the lease is lost or the job is cancelled.
	Exec func(ctx context.Context, c Claim) Outcome
}

// Event is one thing the worker did with a job.
type Event struct {
	Kind  string // "claimed" | "started" | "finished" | "lease-lost" | "stale-spec" | "orphan-slot"
	JobID string
	Epoch int64
	Err   error // for "lease-lost": wraps ErrLeaseLost
}

// Worker is an in-process worker: the loop of SPEC §05.7 without the job container.
type Worker struct{}

// New validates cfg. It performs no I/O.
func New(cfg Config) (*Worker, error) { return nil, diag.NotImplemented("worker.New") }

// Run polls, claims, executes and heartbeats until ctx ends. It never lists.
func (w *Worker) Run(ctx context.Context) error { return diag.NotImplemented("worker.Worker.Run") }

// ID is the worker id.
func (w *Worker) ID() string { return "" }

// FreeSlots is the number of idle slots.
func (w *Worker) FreeSlots() int { return 0 }

// Wake delivers a wake-up (class, slot, job): the worker GETs exactly that slot and tries to
// claim it. It never blocks.
func (w *Worker) Wake(class string, slot uint64, job string) {}

// Pause stops the worker's heartbeat and job watch without ending its process, like SIGSTOP
// (chaos tests); Resume continues.
func (w *Worker) Pause() {}

// Resume undoes Pause.
func (w *Worker) Resume() {}

// Kill ends the worker like SIGKILL: no further S3 request, no cleanup, running jobs abandoned.
func (w *Worker) Kill() {}

// ErrLeaseLost is returned (wrapped) to a worker that writes after its job was requeued or
// taken: "fleet: lease lost (job requeued or taken by another worker)".
var ErrLeaseLost = errors.New("fleet: lease lost (job requeued or taken by another worker)")
