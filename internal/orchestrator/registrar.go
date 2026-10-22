package orchestrator

import (
	"context"
	"crypto/ed25519"
	"time"

	"github.com/megakuul/bob/internal/store"
	"github.com/megakuul/bob/pkg/bob/diag"
)

// Registrar is the registrar controller and the worker authentication of an orchestrator
// (SPEC §05.6.3, §05.9.2).
type Registrar struct{}

// RegistrarConfig configures a Registrar.
type RegistrarConfig struct {
	Control, Fleet *store.Bucket
	Name           string             // orchestrator id
	Key            ed25519.PrivateKey // signs and verifies tokens (FleetConfig key kid)
	Kid            string
	TokenTTL       time.Duration // 1 h
	RegWindow      time.Duration // registrations older than this are rejected (24 h)
	Heartbeat      time.Duration // fleet.heartbeat
	MaxPerPass     int           // approvals per pass (50)
	Now            func() time.Time
}

// NewRegistrar performs no I/O.
func NewRegistrar(cfg RegistrarConfig) (*Registrar, error) {
	return nil, diag.NotImplemented("orchestrator.NewRegistrar")
}

// RegPass is what one registrar pass did.
type RegPass struct {
	Approved int
	Rejected []string // reasons, "worker registration rejected: <reason>"
}

// Pass reads `wrs/reg/s/<cursor+1…>` until 404, validates each registration (S11 §5.2) and
// approves at most MaxPerPass of them. Validation failures are rejected and skipped.
func (r *Registrar) Pass(ctx context.Context) (RegPass, error) {
	return RegPass{}, diag.NotImplemented("orchestrator.Registrar.Pass")
}

// Authenticate checks `Authorization: Bob-Worker <token>` and `Bob-PoP` for one request and
// returns the worker id. Errors read like "unauthenticated: proof of possession invalid",
// "unauthenticated: replayed", "unauthenticated: worker revoked",
// "unauthenticated: superseded serial 3 (current 5)", "unauthenticated: token expired".
func (r *Registrar) Authenticate(ctx context.Context, authorization, pop, method, path string) (string, error) {
	return "", diag.NotImplemented("orchestrator.Registrar.Authenticate")
}

// Rotate issues new tokens at half-life for workers with a fresh signed heartbeat.
func (r *Registrar) Rotate(ctx context.Context) (rotated int, err error) {
	return 0, diag.NotImplemented("orchestrator.Registrar.Rotate")
}

// ForceRotate issues a new token for wid at once (admin / test).
func (r *Registrar) ForceRotate(ctx context.Context, wid string) error {
	return diag.NotImplemented("orchestrator.Registrar.ForceRotate")
}

// Revoke revokes a worker: generation + 1, sealed token cleared.
func (r *Registrar) Revoke(ctx context.Context, wid, reason string) error {
	return diag.NotImplemented("orchestrator.Registrar.Revoke")
}

// Checkout is the credential a worker gets for one job (SPEC §6.9.6).
type Checkout struct {
	CloneURL string
	Username string
	Token    string
	Expires  time.Time
}

// ForgeMinter is the forge adapter's token part (IssueCheckout / RevokeCheckout).
type ForgeMinter interface {
	Mint(ctx context.Context, repo string, expires time.Time) (handle string, c Checkout, err error)
	Revoke(ctx context.Context, handle string) error
}

// Checkouts serves GetCheckoutCredential and the forge-sweeper.
type Checkouts struct {
	Control *store.Bucket
	Queue   *Queue
	Forge   ForgeMinter
	Now     func() time.Time
}

// Get returns a checkout credential for worker wid, job id at epoch. A worker that does not
// own the job at that epoch (owner + epoch on job/<id>, state claimed or running) gets
// "permission_denied: worker holds no lease on job <id>".
func (c *Checkouts) Get(ctx context.Context, wid, jobID string, epoch int64) (Checkout, error) {
	return Checkout{}, diag.NotImplemented("orchestrator.Checkouts.Get")
}

// JobEnded revokes the job's tokens (called at job end).
func (c *Checkouts) JobEnded(ctx context.Context, jobID string) error {
	return diag.NotImplemented("orchestrator.Checkouts.JobEnded")
}

// Sweep is one forge-sweeper pass (every 60 s) over ftok/*: revoke tokens of terminal jobs.
func (c *Checkouts) Sweep(ctx context.Context) (revoked int, err error) {
	return 0, diag.NotImplemented("orchestrator.Checkouts.Sweep")
}

// Hub is the Attach side of an orchestrator: it routes wake-ups, live subscriptions and
// cancel requests only to the worker that owns the job, and accepts job events and live
// chunks only from it (R-FLEET-002).
type Hub struct{}

// NewHub performs no I/O.
func NewHub(q *Queue) *Hub { return nil }

// Conn is one authenticated Attach stream of worker wid.
type Conn struct{}

// Attach registers the stream of an already authenticated worker.
func (h *Hub) Attach(wid string) *Conn { return nil }

// Recv delivers what the orchestrator sends to this worker as text lines:
// "wake <class> <slot> <job>", "subscribe <job>", "cancel <job>", "drain".
func (c *Conn) Recv() <-chan string { return nil }

// JobEvent is a hint from the worker. For a job the worker does not hold (owner + epoch) it
// fails with "permission_denied: worker holds no lease on job <id>".
func (c *Conn) JobEvent(ctx context.Context, jobID string, epoch int64, kind string) error {
	return diag.NotImplemented("orchestrator.Conn.JobEvent")
}

// LiveChunk carries log lines of a job; the same ownership rule as JobEvent.
func (c *Conn) LiveChunk(ctx context.Context, jobID string, epoch int64) error {
	return diag.NotImplemented("orchestrator.Conn.LiveChunk")
}

// Subscribe asks the owner of jobID for its live log.
func (h *Hub) Subscribe(ctx context.Context, jobID string) error {
	return diag.NotImplemented("orchestrator.Hub.Subscribe")
}

// CancelJob sends a cancel request to the owner of jobID.
func (h *Hub) CancelJob(ctx context.Context, jobID string) error {
	return diag.NotImplemented("orchestrator.Hub.CancelJob")
}
