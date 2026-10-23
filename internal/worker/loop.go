package worker

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"hash/fnv"
	"log/slog"
	"math/rand/v2"
	"regexp"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/megakuul/bob/internal/fleet"
	"github.com/megakuul/bob/internal/safe"
	"github.com/megakuul/bob/internal/store"
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
	// LogLast is, per package, the last log seq persisted in the lake (JobResult.LogLast).
	LogLast map[string]int64
	// Complete, CompSHA, EvalSHA and Failed go into the JobResult as they are: the staged
	// COMPLETE.json (key, hex SHA-256), the evaluation report's SHA-256, the failing labels.
	Complete, CompSHA, EvalSHA string
	Failed                     []string
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
	Kind  string // "claimed" | "started" | "finished" | "lease-lost" | "stale-spec" | "orphan-slot" | "cancelled"
	JobID string
	Epoch int64
	Err   error // for "lease-lost": wraps ErrLeaseLost
}

// ErrLeaseLost is returned (wrapped) to a worker that writes after its job was requeued or
// taken: "fleet: lease lost (job requeued or taken by another worker)".
var ErrLeaseLost = errors.New("fleet: lease lost (job requeued or taken by another worker)")

// Limits of the loop.
const (
	wakeBuffer = 256  // Wake never blocks; a full buffer drops the hint (a lost wake-up costs one poll)
	maxPending = 64   // wake-ups kept while every slot is busy
	maxWalk    = 1000 // slots one poll reads at most
	seenWakes  = 4096 // remembered (class, slot) wake-ups, so duplicates cost nothing
	spreadMin  = 8    // a backlog shorter than this is walked in order (no random pick)
)

type wake struct {
	class string
	slot  uint64
	job   string
}

// Worker is an in-process worker: the loop of SPEC §05.7 without the job container.
type Worker struct {
	cfg     Config
	classes []string // ids of the configured classes the profile satisfies
	// unknown are class ids of wake-ups that fleet/cfg did not list or this profile does not
	// satisfy (looked up once per Worker; the Process restarts the Worker when fleet/cfg changes)
	unknown map[string]bool

	wakes  chan wake
	freed  chan struct{}
	learns chan []string // class ids published in fleet/cfg since the Worker started (Learn)

	mu       sync.Mutex
	keys     []ed25519.PublicKey
	paused   bool
	resumed  chan struct{} // closed by Resume
	running  map[string]*job
	claiming int
	killed   chan struct{}
	killOnce sync.Once
	cancel   context.CancelFunc
	revoked  bool
	// lostJobs are jobs whose lease this worker lost (it was paused or cut off longer than the
	// lease): their next slot is left to the other workers (see skipLost).
	lostJobs map[string]time.Time
	jobs     sync.WaitGroup
	backlog  atomic.Bool // the last finished job came from a poll: poll again at once
	// draining: finish the running jobs, claim nothing new (worker record state "draining",
	// Drain over Attach as a hint); FreeSlots reports 0 so nobody wakes this worker.
	draining atomic.Bool

	// Owned by the Run goroutine.
	rng     *rand.Rand
	cursor  map[string]uint64
	pending []wake
	seen    map[wake]bool
}

type job struct {
	claim   Claim
	class   string
	viaPoll bool
	seq     uint64 // the slot the job was claimed through
	cancel  context.CancelFunc
	check   chan struct{} // CheckJob: read job/<id> now instead of at the next watch tick
}

// New validates cfg. It performs no I/O.
func New(cfg Config) (*Worker, error) {
	if cfg.Fleet == nil || cfg.Exec == nil {
		return nil, errors.New("worker: config needs the fleet bucket and Exec")
	}
	if _, err := store.Segment(cfg.ID); err != nil {
		return nil, fmt.Errorf("worker: id %q: %w", cfg.ID, err)
	}
	if len(cfg.SigKey) != ed25519.PrivateKeySize {
		return nil, errors.New("worker: config needs the Ed25519 signing key")
	}
	if cfg.Slots <= 0 {
		cfg.Slots = 1
	}
	def := func(d *time.Duration, v time.Duration) {
		if *d <= 0 {
			*d = v
		}
	}
	def(&cfg.PollInterval, 30*time.Second)
	def(&cfg.Heartbeat, 15*time.Second)
	def(&cfg.ConfigRefresh, 30*time.Second)
	def(&cfg.RecordRefresh, 60*time.Second)
	def(&cfg.LeaseTTL, 60*time.Second)
	def(&cfg.Skew, 5*time.Second)
	def(&cfg.JobWatch, 30*time.Second)
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	w := &Worker{cfg: cfg, wakes: make(chan wake, wakeBuffer), freed: make(chan struct{}, 1), learns: make(chan []string, 1),
		keys: append([]ed25519.PublicKey(nil), cfg.Keys...), running: map[string]*job{}, lostJobs: map[string]time.Time{}, killed: make(chan struct{}),
		cursor: map[string]uint64{}, seen: map[wake]bool{}, unknown: map[string]bool{}}
	for _, c := range cfg.Classes {
		ok, err := store.Satisfies(cfg.Profile, c)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue // never read the slots of a class this worker does not satisfy (R-FLEET-004)
		}
		id, err := store.ClassID(c)
		if err != nil {
			return nil, err
		}
		w.classes = append(w.classes, id)
	}
	h := fnv.New64a()
	h.Write([]byte(cfg.ID))
	w.rng = rand.New(rand.NewPCG(cfg.Seed, h.Sum64()))
	return w, nil
}

// ID is the worker id.
func (w *Worker) ID() string { return w.cfg.ID }

// Learn hands the Worker class ids that fleet/cfg gained since it started; it reads those its
// profile satisfies from then on, without a restart that would drop wake-ups. It blocks only
// until the previous call was taken up by Run.
func (w *Worker) Learn(ctx context.Context, ids []string) {
	select {
	case w.learns <- ids:
	case <-ctx.Done():
	}
}

// Classes returns the ids of the classes the worker reads.
func (w *Worker) Classes() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return slices.Clone(w.classes)
}

// FreeSlots is the number of idle slots.
func (w *Worker) FreeSlots() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.free()
}

func (w *Worker) free() int {
	if w.draining.Load() {
		return 0
	}
	return max(0, w.cfg.Slots-len(w.running)-w.claiming)
}

// SetDraining switches draining on (finish the running jobs, then idle) or off.
func (w *Worker) SetDraining(on bool) {
	if w.draining.Swap(on) != on && !on {
		select { // slots are free again: let the loop poll
		case w.freed <- struct{}{}:
		default:
		}
		w.backlog.Store(true)
	}
}

// Draining reports whether the worker claims nothing new.
func (w *Worker) Draining() bool { return w.draining.Load() }

// Wake delivers a wake-up (class, slot, job): the worker GETs exactly that slot and tries to
// claim it. It never blocks.
func (w *Worker) Wake(class string, slot uint64, job string) {
	select {
	case w.wakes <- wake{class, slot, job}:
	default:
	}
}

// Pause stops the worker's heartbeat and job watch without ending its process, like SIGSTOP
// (chaos tests); Resume continues.
func (w *Worker) Pause() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.paused {
		w.paused, w.resumed = true, make(chan struct{})
	}
}

// Resume undoes Pause.
func (w *Worker) Resume() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.paused {
		w.paused = false
		close(w.resumed)
	}
}

// CheckJob makes the job watch of a held job read its record now (CancelJob over Attach is a
// hint; the cancel flag on job/<id> stays the truth).
func (w *Worker) CheckJob(jobID string) {
	w.mu.Lock()
	j := w.running[jobID]
	w.mu.Unlock()
	if j == nil || j.check == nil {
		return
	}
	select {
	case j.check <- struct{}{}:
	default:
	}
}

// Kill ends the worker like SIGKILL: no further S3 request, no cleanup, running jobs abandoned.
func (w *Worker) Kill() {
	w.killOnce.Do(func() {
		w.mu.Lock()
		close(w.killed)
		if w.cancel != nil {
			w.cancel()
		}
		w.mu.Unlock()
	})
}

// waitResumed blocks while the worker is paused. It reports false when the worker ends.
func (w *Worker) waitResumed(ctx context.Context) bool {
	for {
		w.mu.Lock()
		paused, ch := w.paused, w.resumed
		w.mu.Unlock()
		if !paused {
			return ctx.Err() == nil
		}
		select {
		case <-ch:
		case <-ctx.Done():
			return false
		}
	}
}

func (w *Worker) emit(e Event) {
	if w.cfg.Events != nil {
		w.cfg.Events(e)
	}
}

func (w *Worker) now() time.Time { return w.cfg.Now().UTC() }

// jitter is d ± 20 %.
func (w *Worker) jitter(d time.Duration) time.Duration {
	return time.Duration(float64(d) * (0.8 + 0.4*w.rng.Float64()))
}

// Run polls, claims, executes and heartbeats until ctx ends. It never lists. It returns nil
// when ctx ends or the worker is killed, and ErrRevoked when its record says revoked.
func (w *Worker) Run(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	w.mu.Lock()
	select {
	case <-w.killed:
		w.mu.Unlock()
		return nil
	default:
	}
	w.cancel = cancel
	w.mu.Unlock()

	bg := []<-chan error{safe.Go(func() error { return w.heartbeats(ctx) }), safe.Go(func() error { return w.refresh(ctx) })}
	defer func() {
		cancel()
		w.jobs.Wait()
		for _, c := range bg {
			if err := <-c; err != nil {
				slog.Error("worker: background loop failed", "worker", w.cfg.ID, "err", err)
			}
		}
	}()

	for _, c := range w.classes {
		head, err := store.Queue(w.cfg.Fleet, c).Head(ctx)
		if err != nil && ctx.Err() == nil {
			slog.Warn("worker: queue head unreadable, starting at slot 1", "class", c, "err", err)
		}
		w.cursor[c] = max(head, 1)
	}
	// The first poll comes after one interval, like every other: a worker that starts into a
	// backlog is woken for new jobs and picks up old ones on its schedule.
	timer := time.NewTimer(w.jitter(w.cfg.PollInterval))
	defer timer.Stop()
	for {
		if !w.waitResumed(ctx) {
			break
		}
		select {
		case <-ctx.Done():
		case wk := <-w.wakes:
			w.onWake(ctx, wk)
		case ids := <-w.learns:
			for _, id := range ids {
				if !w.eligible(id) {
					w.learnClass(ctx, id)
				}
			}
		case <-w.freed:
			w.drainPending(ctx)
			if w.backlog.Swap(false) {
				w.pollAll(ctx)
			}
		case <-timer.C:
			w.pollAll(ctx)
			timer.Reset(w.jitter(w.cfg.PollInterval))
		}
		if ctx.Err() != nil {
			break
		}
	}
	w.mu.Lock()
	revoked := w.revoked
	w.mu.Unlock()
	if revoked {
		return fmt.Errorf("worker %s: %w", w.cfg.ID, ErrRevoked)
	}
	return nil
}

func (w *Worker) eligible(class string) bool {
	for _, c := range w.classes {
		if c == class {
			return true
		}
	}
	return false
}

// learnClass handles a wake-up for a class this Worker does not read yet: the orchestrator
// published it in fleet/cfg when it enqueued the first job of the class, and the Process
// picks new classes up only every 30 s and only while idle. Without this, every idle worker
// ignores the wake-ups of a new class and the first poller after the refresh claims the whole
// burst (R-FLEET-012). It reads fleet/cfg once per unknown id and adds the class when this
// profile satisfies it (R-FLEET-004), starting at the queue head as Run does.
func (w *Worker) learnClass(ctx context.Context, id string) bool {
	if id == "" || w.unknown[id] {
		return false
	}
	fc, err := store.Get(ctx, w.cfg.Fleet, store.FleetConfigKey())
	if err != nil {
		return false // not published or unreadable: the next wake-up or poll tries again
	}
	for _, fcl := range fc.Classes.Value() {
		if fcl.ID != id {
			continue
		}
		c := store.Class{Platform: fcl.Platform, Caps: fcl.Caps, Labels: fcl.Labels}
		cid, err := store.ClassID(c)
		ok, serr := store.Satisfies(w.cfg.Profile, c)
		if err != nil || serr != nil || cid != id || !ok {
			break
		}
		head, err := store.Queue(w.cfg.Fleet, id).Head(ctx)
		if err != nil {
			return false
		}
		w.mu.Lock() // Classes reads it from the Attach goroutine; Run's own reads need no lock
		w.classes = append(slices.Clip(w.classes), id)
		w.mu.Unlock()
		w.cursor[id] = max(head, 1)
		return true
	}
	w.unknown[id] = true
	return false
}

// onWake handles one wake-up: exactly that slot, nothing else (no herd).
func (w *Worker) onWake(ctx context.Context, wk wake) {
	if wk.slot == 0 || w.seen[wk] || w.draining.Load() || !w.eligible(wk.class) && !w.learnClass(ctx, wk.class) {
		return
	}
	if w.FreeSlots() == 0 {
		for _, p := range w.pending {
			if p == wk {
				return
			}
		}
		if len(w.pending) < maxPending {
			w.pending = append(w.pending, wk)
		}
		return
	}
	if len(w.seen) >= seenWakes {
		clear(w.seen)
	}
	w.seen[wk] = true
	if w.skipLost(wk.job, false) {
		return
	}
	w.claim(ctx, wk.class, wk.slot, wk.job, false)
}

func (w *Worker) drainPending(ctx context.Context) {
	for len(w.pending) > 0 && w.FreeSlots() > 0 && ctx.Err() == nil {
		wk := w.pending[0]
		w.pending = w.pending[1:]
		w.onWake(ctx, wk)
	}
}

func (w *Worker) pollAll(ctx context.Context) {
	for _, c := range w.classes {
		if w.FreeSlots() == 0 || ctx.Err() != nil {
			return
		}
		if err := w.poll(ctx, c); err != nil && ctx.Err() == nil {
			slog.Warn("worker: poll failed", "worker", w.cfg.ID, "class", c, "err", err)
		}
	}
}

// poll is one idle poll of a class queue: GET the slot at the cursor (404: nothing new, 1
// request). With a backlog it first tries a random slot in [cursor, tail) so that pollers do
// not all contend for one slot, then walks from the cursor, claiming open slots of queued
// jobs, and raises the shared head hint over the slots it found closed.
func (w *Worker) poll(ctx context.Context, class string) error {
	q := store.Queue(w.cfg.Fleet, class)
	c := w.cursor[class]
	slot, err := q.Read(ctx, c)
	if errors.Is(err, store.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if slot.Taken.Value() != "" {
		// Wake-fed workers fall behind; the head hint skips what others found closed.
		if h, err := q.Head(ctx); err == nil && h > c+1 {
			c, slot = h, nil
		}
	}
	// Backlog: try one random slot in (cursor, tail) before the cursor (spread, no herd).
	if tail, err := q.Tail(ctx); err == nil && tail > c+spreadMin {
		r := c + 1 + w.rng.Uint64N(tail-c-1)
		if s, err := q.Read(ctx, r); err == nil && s.Taken.Value() == "" && !w.skipLost(s.Job.Value(), false) {
			w.claim(ctx, class, r, s.Job.Value(), true)
		}
	}
	start := c
	var skipped uint64 // first open slot left to the other workers: the head stays below it
	for steps := 0; steps < maxWalk && w.FreeSlots() > 0 && ctx.Err() == nil; steps++ {
		if slot == nil {
			slot, err = q.Read(ctx, c)
			if errors.Is(err, store.ErrNotFound) {
				break
			}
			if err != nil {
				w.cursor[class] = c
				return err
			}
		}
		if slot.Taken.Value() == "" {
			if w.skipLost(slot.Job.Value(), true) {
				if skipped == 0 {
					skipped = c
				}
			} else {
				w.claim(ctx, class, c, slot.Job.Value(), true)
			}
		}
		c++
		slot = nil
	}
	w.cursor[class] = c
	head := c
	if skipped != 0 {
		head = skipped
	}
	if head > start {
		return q.AdvanceHead(ctx, head)
	}
	return nil
}

// lostMemory bounds how long a lost lease keeps the worker off the job.
const lostMemory = 10 * time.Minute

// skipLost reports whether the open slot of jobID is left to other workers because this
// worker lost the job's lease: a worker that was paused or cut off for longer than the lease
// is the least healthy candidate for the retry. The walk passing the slot ends the skip
// (passed), so a fleet without another worker still runs the job from its next slot (the
// reconciler re-slots it after fleet.reslot_after); the head hint stays below the skipped
// slot, so the other workers' polls still read it.
func (w *Worker) skipLost(jobID string, passed bool) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	at, ok := w.lostJobs[jobID]
	if !ok {
		return false
	}
	if passed || w.now().Sub(at) > lostMemory {
		delete(w.lostJobs, jobID)
	}
	return w.now().Sub(at) <= lostMemory
}

// noteLost remembers a job whose lease this worker lost (skipLost).
func (w *Worker) noteLost(jobID string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	now := w.now()
	for id, at := range w.lostJobs {
		if now.Sub(at) > lostMemory {
			delete(w.lostJobs, id)
		}
	}
	w.lostJobs[jobID] = now
}

// claim tries to take job jobID through slot seq of class (SPEC §05.6.1): CAS the job record
// (queued → claimed, epoch+1, attempt+1), then mark the slot taken. A missing job marks the
// slot orphan, a job that is not queued marks it stale, a spec without a valid orchestrator
// signature is never run and marks it stale.
func (w *Worker) claim(ctx context.Context, class string, seq uint64, jobID string, viaPoll bool) {
	if jobID == "" || ctx.Err() != nil {
		return
	}
	w.mu.Lock()
	if w.free() == 0 {
		w.mu.Unlock()
		return
	}
	w.claiming++
	keys := w.keys
	w.mu.Unlock()
	release := func() { w.mu.Lock(); w.claiming--; w.mu.Unlock() }

	now := w.now()
	verdict := ""
	var claim Claim
	err := store.Change(ctx, w.cfg.Fleet, &store.JobRecord{ID: store.ID(jobID)}, func(cur *store.JobRecord) (*store.JobRecord, error) {
		switch {
		case cur == nil:
			verdict = "orphan"
			return nil, store.ErrNoChange
		case cur.State.Value() != "queued":
			verdict = "stale"
			return nil, store.ErrNoChange
		case !fleet.VerifySpec(keys, cur.Spec.Value(), cur.SpecSig.Value()):
			verdict = "bad-spec"
			return nil, store.ErrNoChange
		}
		if spec, ok := fleet.DecodeSpec(cur.Spec.Value()); ok && spec.Class != "" && spec.Class != class {
			verdict = "stale" // a slot in the wrong queue never makes a worker run a job of another class
			return nil, store.ErrNoChange
		}
		next := *cur
		next.State, next.Owner, next.Epoch, next.Attempt, next.Claimed = store.Set[string]("claimed"), store.Set(w.cfg.ID), store.Set(cur.Epoch.Value()+1), store.Set(cur.Attempt.Value()+1), store.Set(now)
		verdict = "claimed"
		claim = Claim{JobID: jobID, Epoch: cur.Epoch.Value() + 1, Attempt: cur.Attempt.Value() + 1, Spec: cur.Spec.Value()}
		return &next, nil
	})
	if err != nil {
		release()
		if ctx.Err() == nil {
			slog.Debug("worker: claim lost", "job", jobID, "err", err)
		}
		return
	}
	switch verdict {
	case "claimed":
		j := &job{claim: claim, class: class, viaPoll: viaPoll,
			check: make(chan struct{}, 1)}
		w.mu.Lock()
		w.claiming--
		w.running[jobID] = j
		w.mu.Unlock()
		w.emit(Event{Kind: "claimed", JobID: jobID, Epoch: claim.Epoch})
		j.seq = seq
		w.start(ctx, j)
		return
	case "orphan":
		w.emit(Event{Kind: "orphan-slot", JobID: jobID})
		w.mark(ctx, class, seq, jobID, "orphan")
	case "bad-spec":
		slog.Warn("worker: job spec signature invalid, not run", "job", jobID, "worker", w.cfg.ID)
		w.emit(Event{Kind: "stale-spec", JobID: jobID})
		w.mark(ctx, class, seq, jobID, "stale")
	case "stale":
		w.mark(ctx, class, seq, jobID, "stale")
	}
	release()
}

// mark sets the slot's taken-by field. The claimer's id may replace "stale" (another worker
// saw the job already claimed before the claimer wrote the slot); stale and orphan are only
// written on an open slot.
func (w *Worker) mark(ctx context.Context, class string, seq uint64, jobID, tk string) {
	err := store.Change(ctx, w.cfg.Fleet, store.QueueSlotKey(class, seq), func(cur *store.QueueSlot) (*store.QueueSlot, error) {
		if cur == nil || cur.Job.Value() != jobID || cur.Taken.Value() == tk {
			return nil, store.ErrNoChange
		}
		if cur.Taken.Value() != "" && !(cur.Taken.Value() == "stale" && tk == w.cfg.ID) {
			return nil, store.ErrNoChange
		}
		next := *cur
		next.Taken = store.Set(tk)
		return &next, nil
	})
	if err != nil && ctx.Err() == nil {
		slog.Debug("worker: slot mark failed", "class", class, "slot", seq, "err", err)
	}
}

// start runs the job in its own goroutine.
func (w *Worker) start(ctx context.Context, j *job) {
	jctx, cancel := context.WithCancel(ctx)
	j.cancel = cancel
	w.jobs.Add(1)
	safe.Go(func() error {
		defer w.jobs.Done()
		defer cancel()
		w.runJob(ctx, jctx, j)
		w.mu.Lock()
		delete(w.running, j.claim.JobID)
		w.mu.Unlock()
		select {
		case w.freed <- struct{}{}:
		default:
		}
		return nil
	})
}

// update is a CAS on the held job that only applies while this worker owns it at its epoch.
// It reports false (lease lost) when the guard fails.
func (w *Worker) update(ctx context.Context, j *job, fn func(cur, next *store.JobRecord) bool) (bool, error) {
	held := false
	err := store.Change(ctx, w.cfg.Fleet, &store.JobRecord{ID: store.ID(j.claim.JobID)}, func(cur *store.JobRecord) (*store.JobRecord, error) {
		held = false
		if cur == nil || cur.Owner.Value() != w.cfg.ID || cur.Epoch.Value() != j.claim.Epoch {
			return nil, store.ErrNoChange
		}
		next := *cur
		if !fn(cur, &next) {
			return nil, store.ErrNoChange
		}
		held = true
		return &next, nil
	})
	return held, err
}

func (w *Worker) runJob(ctx, jctx context.Context, j *job) {
	id, ep := j.claim.JobID, j.claim.Epoch
	lost := func() {
		w.noteLost(id)
		w.emit(Event{Kind: "lease-lost", JobID: id, Epoch: ep, Err: ErrLeaseLost})
	}
	pub := w.cfg.SigKey.Public().(ed25519.PublicKey)
	ok, err := w.update(ctx, j, func(cur, next *store.JobRecord) bool {
		switch cur.State.Value() {
		case "claimed":
		case "succeeded", "failed":
			// A result in this worker's name that it did not sign is forged (ADR 0013 T10).
			if fleet.VerifyResult(pub, id, ep, cur.Result.Value(), cur.ResultSig.Value()) {
				return false
			}
			next.Result, next.ResultSig = nil, nil
		default:
			return false
		}
		next.State, next.Changed = store.Set[string]("running"), store.Set(w.now())
		return true
	})
	if err != nil || !ok {
		if ctx.Err() == nil {
			lost()
		}
		return
	}
	w.emit(Event{Kind: "started", JobID: id, Epoch: ep})

	var out Outcome
	done := safe.Go(func() error { out = w.cfg.Exec(jctx, j.claim); return nil })
	// The slot is marked after the run started: it is only a hint for pollers (the job
	// record already says claimed), so pickup latency does not pay for it.
	w.mark(ctx, j.class, j.seq, id, w.cfg.ID)
	var leaseLost, cancelled bool
	tick := time.NewTicker(w.cfg.JobWatch)
	defer tick.Stop()
watch:
	for {
		select {
		case err := <-done:
			if err != nil {
				out = Outcome{Status: "infra", Error: "job run crashed"}
			}
			break watch
		case <-tick.C:
		case <-j.check:
		}
		if leaseLost || cancelled || !w.active() {
			continue
		}
		rec, err := store.Get(ctx, w.cfg.Fleet, &store.JobRecord{ID: store.ID(id)})
		switch {
		case errors.Is(err, store.ErrNotFound):
			leaseLost = true
		case err != nil:
			continue
		case rec.Owner.Value() != w.cfg.ID || rec.Epoch.Value() != ep:
			leaseLost = true
		case rec.Cancel.Value() || rec.State.Value() == "cancelled":
			cancelled = true
		default:
			continue
		}
		j.cancel()
	}
	if !w.waitResumed(ctx) {
		return // killed or shut down: no cleanup (the lease expires)
	}
	switch {
	case leaseLost:
		lost()
	case cancelled:
		if ok, err := w.update(ctx, j, func(cur, next *store.JobRecord) bool {
			if cur.State.Value() != "claimed" && cur.State.Value() != "running" {
				return false
			}
			next.State, next.Changed = store.Set[string]("cancelled"), store.Set(w.now())
			return true
		}); err == nil && ok {
			w.emit(Event{Kind: "cancelled", JobID: id, Epoch: ep})
		} else if ctx.Err() == nil {
			lost()
		}
	default:
		w.finish(ctx, j, out, lost)
	}
}

// userinfoRE matches the credentials part of a URL ("https://user:token@host").
var userinfoRE = regexp.MustCompile(`([A-Za-z][A-Za-z0-9+.-]*://)[^/@\s]+@`)

// redactURLs removes URL credentials from an error text before it is logged or persisted
// (tools such as git may echo a clone URL; the checkout token itself is redacted where it is
// known).
func redactURLs(s string) string { return userinfoRE.ReplaceAllString(s, "${1}[redacted]@") }

// finish writes the signed result while the lease holds (SPEC §05.6.1 "result").
func (w *Worker) finish(ctx context.Context, j *job, out Outcome, lost func()) {
	id, ep := j.claim.JobID, j.claim.Epoch
	status, state := out.Status, "failed"
	switch status {
	case "ok":
		state = "succeeded"
	case "failed", "infra":
	default:
		status = "infra"
	}
	errText := redactURLs(out.Error)
	if len(errText) > 8<<10 {
		errText = errText[:8<<10]
	}
	if status != "ok" {
		// the reason stays in the signed result (JobResult.error → §06 Job.error) and here
		slog.Warn("worker: job did not succeed", "job", id, "epoch", ep, "status", status, "error", errText)
	}
	now := w.now()
	rs, err := fleet.Encode(store.JobResult{V: 1, Status: status, Error: errText, Finished: now.Unix(), LogLast: out.LogLast,
		Complete: out.Complete, CompSHA: out.CompSHA, EvalSHA: out.EvalSHA, Failed: out.Failed})
	if err != nil {
		slog.Error("worker: encode result", "job", id, "err", err)
		return
	}
	pub := w.cfg.SigKey.Public().(ed25519.PublicKey)
	ok, err := w.update(ctx, j, func(cur, next *store.JobRecord) bool {
		switch cur.State.Value() {
		case "claimed", "running":
		case "succeeded", "failed":
			// A result this worker did not sign was forged by another fleet writer
			// (ADR 0013 T10); the owner's own result replaces it.
			if fleet.VerifyResult(pub, id, ep, cur.Result.Value(), cur.ResultSig.Value()) {
				return false
			}
		default:
			return false
		}
		next.State, next.Result, next.ResultSig, next.Changed = store.Set(state), store.Set(rs), store.Set(fleet.SignResult(w.cfg.SigKey, id, ep, rs)), store.Set(now)
		return true
	})
	switch {
	case err == nil && ok:
		w.emit(Event{Kind: "finished", JobID: id, Epoch: ep})
		if j.viaPoll {
			w.backlog.Store(true)
		}
	case ctx.Err() == nil:
		lost()
	}
}

// active reports that the worker is neither paused nor killed.
func (w *Worker) active() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	select {
	case <-w.killed:
		return false
	default:
	}
	return !w.paused
}

// heartbeats writes whb/<wid> every Heartbeat (the first one after one interval).
func (w *Worker) heartbeats(ctx context.Context) error {
	t := time.NewTicker(w.cfg.Heartbeat)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
		}
		w.mu.Lock()
		if w.paused {
			w.mu.Unlock()
			continue
		}
		// The time is read under the lock: after Pause returns no heartbeat carries a later time.
		at := w.now()
		free := w.free()
		held := make([]store.HeldJobEpoch, 0, len(w.running))
		for _, j := range w.running {
			held = append(held, store.HeldJobEpoch{Job: j.claim.JobID, Epoch: j.claim.Epoch})
		}
		w.mu.Unlock()
		if err := heartbeat(ctx, w.cfg.Fleet, w.cfg.ID, w.cfg.SigKey, free, held, at); err != nil && ctx.Err() == nil {
			slog.Warn("worker: heartbeat failed", "worker", w.cfg.ID, "err", err)
		}
	}
}

// refresh re-reads fleet/cfg (orchestrator keys) every ConfigRefresh and worker/<wid> every
// RecordRefresh (revocation), both with GetCached (1 request when unchanged).
func (w *Worker) refresh(ctx context.Context) error {
	var cfgCache store.Cache[store.FleetConfig]
	var recCache store.Cache[store.WorkerRecord]
	readCfg := func() {
		fc, changed, err := store.GetCached(ctx, w.cfg.Fleet, &cfgCache, store.FleetConfigKey())
		if err != nil || !changed {
			return
		}
		keys := append([]ed25519.PublicKey(nil), w.cfg.Keys...)
		for _, k := range fc.Keys.Value() {
			if len(k.Pub) == ed25519.PublicKeySize {
				keys = append(keys, ed25519.PublicKey(k.Pub))
			}
		}
		w.mu.Lock()
		w.keys = keys
		w.mu.Unlock()
	}
	readRec := func() {
		rec, _, err := store.GetCached(ctx, w.cfg.Fleet, &recCache, &store.WorkerRecord{Wid: store.ID(w.cfg.ID)})
		if err != nil {
			return
		}
		switch rec.State.Value() {
		case "draining":
			w.SetDraining(true)
			return
		case "active":
			w.SetDraining(false)
			return
		case "revoked":
		default:
			return
		}
		slog.Error("worker: revoked, exiting", "worker", w.cfg.ID)
		w.mu.Lock()
		w.revoked = true
		if w.cancel != nil {
			w.cancel()
		}
		w.mu.Unlock()
	}
	readCfg()
	readRec()
	ct, rt := time.NewTicker(w.cfg.ConfigRefresh), time.NewTicker(w.cfg.RecordRefresh)
	defer ct.Stop()
	defer rt.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ct.C:
			if w.active() {
				readCfg()
			}
		case <-rt.C:
			if w.active() {
				readRec()
			}
		}
	}
}
