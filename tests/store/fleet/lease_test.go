package fleet

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/megakuul/bob/internal/orchestrator"
	"github.com/megakuul/bob/internal/store"
	"github.com/megakuul/bob/internal/worker"
	"github.com/megakuul/bob/tests/parity"
	"github.com/megakuul/bob/tests/store/env"
)

func (l *lab) reconciler(name string, lease bool) *orchestrator.Reconciler {
	return &orchestrator.Reconciler{Fleet: l.fleet, Shards: 16, LeaseTTL: 3 * time.Second, Skew: time.Second,
		ReslotAge: 60 * time.Second, Now: l.clk.Now, Name: name, UseLease: lease}
}

// events collects a worker's job events.
type events struct {
	mu  sync.Mutex
	all []worker.Event
}

func (e *events) add(ev worker.Event) {
	e.mu.Lock()
	e.all = append(e.all, ev)
	e.mu.Unlock()
}

func (e *events) of(kind string) []worker.Event {
	e.mu.Lock()
	defer e.mu.Unlock()
	var out []worker.Event
	for _, ev := range e.all {
		if ev.Kind == kind {
			out = append(out, ev)
		}
	}
	return out
}

// TestRFLEET003LeaseAndRequeue walks the job state machine of §05.6.1 with an injected clock:
// liveness comes from the owner's heartbeat, a claim has lease_ttl of grace before the first
// heartbeat counts, a requeue keeps the epoch, and a paused worker that writes late loses.
func TestRFLEET003LeaseAndRequeue(t *testing.T) {
	parity.Covers(t, "R-FLEET-003")
	l := newLab(t)
	if l == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	const ttl, skew = 3 * time.Second, time.Second
	g := newLedger()
	ev1, ev2 := &events{}, &events{}
	release1 := make(chan struct{})
	cfg1 := l.workerCfg("w1", func(ctx context.Context, c worker.Claim) worker.Outcome {
		g.note("w1", c)
		select {
		case <-release1:
			return worker.Outcome{Status: "ok"}
		case <-ctx.Done():
			return worker.Outcome{Status: "infra", Error: "cancelled"}
		}
	})
	cfg1.Events = ev1.add
	w1 := l.start(cfg1)
	if w1 == nil {
		return
	}
	if _, err := l.q.Enqueue(ctx, l.job("j1"), orchestrator.JobFirst); err != nil {
		t.Fatal(err)
	}
	v := l.waitState("j1", 20*time.Second, "claimed", "running")
	if v == nil || v.Owner != "w1" || v.Epoch != 1 || v.Attempt != 1 {
		t.Fatalf("after the claim: %+v, want owner w1, epoch 1, attempt 1", v)
	}
	r1, r2 := l.reconciler("o1", false), l.reconciler("o2", false)

	// No per-job renewals: a live worker keeps its job far beyond lease_ttl.
	l.clk.Advance(10 * time.Second)
	time.Sleep(time.Second) // heartbeats at the new time
	if res, err := r1.Pass(ctx); err != nil || res.Requeued != 0 {
		t.Fatalf("pass 10 s later with a heartbeating owner: %+v, %v; want no requeue", res, err)
	}

	// The owner stops heartbeating (paused, like SIGSTOP). Inside ttl + skew nothing happens.
	w1.Pause()
	l.clk.Advance(ttl)
	if res, _ := r1.Pass(ctx); res.Requeued != 0 {
		t.Fatalf("requeued %d jobs exactly lease_ttl after the last heartbeat, want 0 (skew allowance)", res.Requeued)
	}
	l.clk.Advance(skew + 500*time.Millisecond)
	// Two reconcilers at once: the requeue is a CAS, so the job is requeued exactly once.
	var wg sync.WaitGroup
	var requeued atomic.Int64
	for _, r := range []*orchestrator.Reconciler{r1, r2} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			res, err := r.Pass(ctx)
			if err != nil {
				t.Errorf("pass: %v", err)
			}
			requeued.Add(int64(res.Requeued))
		}()
	}
	wg.Wait()
	if requeued.Load() != 1 {
		t.Fatalf("two reconcilers requeued %d times after ttl + skew, want exactly 1", requeued.Load())
	}
	v = l.view("j1")
	if v == nil || v.State != "queued" || v.Owner != "" || v.Epoch != 1 {
		t.Fatalf("requeued job = %+v, want queued, no owner, epoch kept at 1", v)
	}

	// Another worker takes it: epoch + 1, attempt + 1.
	cfg2 := l.workerCfg("w2", func(_ context.Context, c worker.Claim) worker.Outcome {
		g.note("w2", c)
		return worker.Outcome{Status: "ok"}
	})
	cfg2.Events = ev2.add
	if l.start(cfg2) == nil {
		return
	}
	v = l.waitState("j1", 20*time.Second, "succeeded")
	if v == nil || v.State != "succeeded" || v.Owner != "w2" || v.Epoch != 2 || v.Attempt != 2 || v.Result != "ok" {
		t.Fatalf("after the second claim: %+v, want succeeded by w2 at epoch 2, attempt 2", v)
	}

	// The paused worker wakes up and tries to complete: it gets the lease-lost error and the
	// record stays w2's.
	w1.Resume()
	close(release1)
	if !env.Wait(20*time.Second, func() bool { return len(ev1.of("lease-lost")) > 0 }) {
		t.Fatalf("the late worker's write was not rejected with a lease-lost event; events %+v", ev1.all)
	}
	if err := ev1.of("lease-lost")[0].Err; !errors.Is(err, worker.ErrLeaseLost) ||
		err.Error() != "fleet: lease lost (job requeued or taken by another worker)" {
		t.Errorf("late write error = %v, want %q", err, "fleet: lease lost (job requeued or taken by another worker)")
	}
	if len(ev1.of("finished")) != 0 {
		t.Errorf("the stale worker's result was accepted: %+v", ev1.of("finished"))
	}
	if v := l.view("j1"); v == nil || v.Owner != "w2" || v.Epoch != 2 || v.Result != "ok" {
		t.Errorf("a stale result changed the record: %+v", v)
	}
	l.be.Proxy.AssertNoList(t, l.fname, l.cname)
}

// TestRFLEET003ClaimGrace: with no heartbeat at all the claim time starts the lease: the job is
// requeued only after claim + lease_ttl + skew.
func TestRFLEET003ClaimGrace(t *testing.T) {
	parity.Covers(t, "R-FLEET-003")
	l := newLab(t)
	if l == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	cfg := l.workerCfg("silent", func(ctx context.Context, c worker.Claim) worker.Outcome {
		<-ctx.Done()
		return worker.Outcome{Status: "infra"}
	})
	cfg.Heartbeat = time.Hour // the first heartbeat never comes
	w := l.start(cfg)
	if w == nil {
		return
	}
	if _, err := l.q.Enqueue(ctx, l.job("g1"), orchestrator.JobFirst); err != nil {
		t.Fatal(err)
	}
	if v := l.waitState("g1", 20*time.Second, "claimed", "running"); v == nil || v.Owner != "silent" {
		t.Fatalf("job not claimed: %+v", v)
	}
	w.Pause()
	r := l.reconciler("o1", false)
	l.clk.Advance(3 * time.Second) // claim + ttl
	if res, _ := r.Pass(ctx); res.Requeued != 0 {
		t.Errorf("requeued at claim + lease_ttl: the grace is ttl + skew")
	}
	l.clk.Advance(1500 * time.Millisecond) // claim + ttl + skew + 0.5 s
	if res, err := r.Pass(ctx); err != nil || res.Requeued != 1 {
		t.Errorf("pass after claim + ttl + skew = %+v, %v; want the job requeued", res, err)
	}
}

// TestRFLEET003Cancel: a queued job is cancelled at once; a running job gets cx and is
// cancelled when its worker acknowledges.
func TestRFLEET003Cancel(t *testing.T) {
	parity.Covers(t, "R-FLEET-003")
	l := newLab(t)
	if l == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	// Queued: a class nobody serves.
	other, err := store.ClassID(store.Class{Platform: "x86_64-linux", Caps: []string{"nobody"}})
	if env.Red(t, err) {
		return
	}
	q2 := *l.q
	q2.Class = other
	if _, err := q2.Enqueue(ctx, l.job("c-queued"), orchestrator.JobFirst); err != nil {
		t.Fatal(err)
	}
	if err := q2.Cancel(ctx, "c-queued"); err != nil {
		t.Fatal(err)
	}
	if v, err := q2.Job(ctx, "c-queued"); err != nil || v.State != "cancelled" {
		t.Errorf("queued job after cancel: %+v, %v; want cancelled", v, err)
	}
	// Running: the worker's context is cancelled and it acknowledges.
	started := make(chan struct{})
	stopped := make(chan struct{})
	if l.start(l.workerCfg("w1", func(ctx context.Context, c worker.Claim) worker.Outcome {
		close(started)
		<-ctx.Done()
		close(stopped)
		return worker.Outcome{Status: "infra", Error: "cancelled"}
	})) == nil {
		return
	}
	if _, err := l.q.Enqueue(ctx, l.job("c-running"), orchestrator.JobFirst); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(20 * time.Second):
		t.Fatal("job did not start")
	}
	if err := l.q.Cancel(ctx, "c-running"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-stopped:
	case <-time.After(20 * time.Second):
		t.Fatal("the worker did not see the cancel request within the job-watch interval")
	}
	if v := l.waitState("c-running", 20*time.Second, "cancelled"); v == nil || v.State != "cancelled" {
		t.Errorf("running job after cancel ack: %+v, want cancelled", v)
	}
}

// TestRFLEET003Reslot: a queued job whose slot nobody reached is re-slotted after
// fleet.reslot_after, and then claimed exactly once.
func TestRFLEET003Reslot(t *testing.T) {
	parity.Covers(t, "R-FLEET-003", "R-FLEET-011")
	l := newLab(t)
	if l == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if _, err := l.q.Enqueue(ctx, l.job("r1"), orchestrator.JobFirst); err != nil {
		if env.Red(t, err) {
			return
		}
		t.Fatal(err)
	}
	r := l.reconciler("o1", false)
	l.clk.Advance(59 * time.Second)
	if res, err := r.Pass(ctx); err != nil || res.Reslotted != 0 {
		t.Fatalf("pass before reslot_after: %+v, %v", res, err)
	}
	l.clk.Advance(2 * time.Second)
	if res, err := r.Pass(ctx); err != nil || res.Reslotted != 1 {
		t.Fatalf("pass after reslot_after: %+v, %v; want 1 job re-slotted", res, err)
	}
	g := newLedger()
	if l.start(l.workerCfg("w1", func(_ context.Context, c worker.Claim) worker.Outcome {
		g.note("w1", c)
		return worker.Outcome{Status: "ok"}
	})) == nil {
		return
	}
	if v := l.waitState("r1", 20*time.Second, "succeeded"); v == nil || v.State != "succeeded" {
		t.Fatalf("re-slotted job did not run: %+v", v)
	}
	time.Sleep(time.Second)
	if runs := g.of("r1"); len(runs) != 1 {
		t.Errorf("job ran %d times via its two slots, want exactly once", len(runs))
	}
}

// TestRFLEET003Chaos is the library form of the chaos run: 20 workers, 300 jobs, three
// reconcilers, with and without the leader lease; 20 % of the workers die (killed while
// holding a job) or pause for 2 x TTL + 1 s and then try to complete. 0 double acceptances,
// 0 lost jobs, 0 accepted stale results, 0 LIST, and dead workers' jobs are queued again
// within lease_ttl + reconcile interval + skew (the test profile uses a 3 s TTL). The
// process-level SIGKILL run is tests/chaos (HARNESS.md §8.2 a).
func TestRFLEET003Chaos(t *testing.T) {
	parity.Covers(t, "R-FLEET-003")
	for _, leader := range []bool{false, true} {
		name := map[bool]string{false: "no leader", true: "with leader lease"}[leader]
		t.Run(name, func(t *testing.T) { chaos(t, leader) })
	}
}

func chaos(t *testing.T, leader bool) {
	l := newLab(t)
	if l == nil {
		return
	}
	l.clk = realClock()
	const jobs, nWorkers = 300, 20
	const ttl, skew, interval = 3 * time.Second, time.Second, time.Second
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()

	g := newLedger()
	evs := &events{}
	var mu sync.Mutex
	workers := map[string]*worker.Worker{}
	killedAt := map[string]time.Time{} // job -> when its worker died holding it
	victimOf := map[string]string{}    // job -> victim worker
	var killed, paused atomic.Int64
	for i := 0; i < nWorkers; i++ {
		id := "w" + jobID(i)[3:]
		role := ""
		switch {
		case i < 2:
			role = "kill"
		case i < 4:
			role = "pause"
		}
		var once sync.Once
		cfg := l.workerCfg(id, nil)
		cfg.Events = evs.add
		cfg.Exec = func(ctx context.Context, c worker.Claim) worker.Outcome {
			g.note(id, c)
			first := false
			once.Do(func() { first = true })
			if first && role != "" {
				mu.Lock()
				killedAt[c.JobID], victimOf[c.JobID] = time.Now(), id
				w := workers[id]
				mu.Unlock()
				switch role {
				case "kill":
					killed.Add(1)
					w.Kill()
					<-ctx.Done()
					return worker.Outcome{Status: "infra"}
				case "pause":
					paused.Add(1)
					w.Pause()
					time.Sleep(2*ttl + time.Second)
					w.Resume()
					return worker.Outcome{Status: "ok"} // tries to complete late
				}
			}
			time.Sleep(20 * time.Millisecond)
			return worker.Outcome{Status: "ok"}
		}
		w := l.start(cfg)
		if w == nil {
			return
		}
		mu.Lock()
		workers[id] = w
		mu.Unlock()
	}
	rctx, rcancel := context.WithCancel(ctx)
	var rwg sync.WaitGroup
	for _, name := range []string{"o1", "o2", "o3"} {
		r := l.reconciler(name, leader)
		rwg.Add(1)
		go func() {
			defer rwg.Done()
			for rctx.Err() == nil {
				if _, err := r.Pass(rctx); err != nil && rctx.Err() == nil {
					t.Errorf("reconciler %s: %v", r.Name, err)
					return
				}
				time.Sleep(interval)
			}
		}()
	}
	defer func() { rcancel(); rwg.Wait() }()
	// Requeue observer: SPEC bounds the time until the dead worker's job is QUEUED again (the
	// reconciler's CAS), not until another worker happens to claim it. Poll the held jobs every 50 ms
	// and record the first moment one is seen queued without an owner after its worker died.
	requeuedAt := map[string]time.Time{}
	var rqMu sync.Mutex
	rwg.Add(1)
	go func() {
		defer rwg.Done()
		for rctx.Err() == nil {
			mu.Lock()
			var held []string
			for j := range killedAt {
				held = append(held, j)
			}
			mu.Unlock()
			for _, j := range held {
				rqMu.Lock()
				_, seen := requeuedAt[j]
				rqMu.Unlock()
				if seen {
					continue
				}
				if v := l.view(j); v != nil && v.State == "queued" && v.Owner == "" {
					rqMu.Lock()
					requeuedAt[j] = time.Now()
					rqMu.Unlock()
				}
			}
			time.Sleep(50 * time.Millisecond)
		}
	}()
	// Three enqueuers.
	var ewg sync.WaitGroup
	for e := 0; e < 3; e++ {
		ewg.Add(1)
		go func() {
			defer ewg.Done()
			for i := e; i < jobs; i += 3 {
				if _, err := l.q.Enqueue(ctx, l.job(jobID(i)), orchestrator.JobFirst); err != nil {
					t.Errorf("enqueue %d: %v", i, err)
					return
				}
			}
		}()
	}
	ewg.Wait()

	terminal := func() int {
		n := 0
		for i := 0; i < jobs; i++ {
			if v := l.view(jobID(i)); v != nil && v.State == "succeeded" {
				n++
			}
		}
		return n
	}
	env.Wait(2*time.Minute, func() bool { return terminal() == jobs })
	time.Sleep(2*ttl + 2*time.Second) // late completions of paused workers arrive
	if n := terminal(); n != jobs {
		t.Errorf("%d of %d jobs reached a terminal state; %d lost", n, jobs, jobs-n)
	}
	if killed.Load() == 0 || paused.Load() == 0 {
		t.Fatalf("chaos did not happen: %d workers killed, %d paused", killed.Load(), paused.Load())
	}
	// Accepted results: one "finished" event per job at most, at the job's final epoch.
	finished := map[string][]worker.Event{}
	for _, e := range evs.of("finished") {
		finished[e.JobID] = append(finished[e.JobID], e)
	}
	for i := 0; i < jobs; i++ {
		id := jobID(i)
		v := l.view(id)
		if v == nil {
			continue
		}
		if f := finished[id]; len(f) != 1 || f[0].Epoch != v.Epoch {
			t.Errorf("job %s: accepted results %+v, record owner %s epoch %d; want exactly one, at the final epoch", id, f, v.Owner, v.Epoch)
		}
	}
	if late := evs.of("lease-lost"); len(late) == 0 {
		t.Errorf("no late write of a paused worker was rejected (lease-lost); the pause did not outlive the lease")
	}
	// Requeue latency of the jobs the dead workers held.
	bound := ttl + interval + skew // SPEC: lease_ttl + reconcile interval + skew
	rqMu.Lock()
	observed := 0
	for job, at := range killedAt {
		if rq, ok := requeuedAt[job]; ok {
			observed++
			if d := rq.Sub(at); d > bound {
				t.Errorf("job %s: queued again %v after its worker died, want <= %v (lease_ttl + interval + skew)", job, d.Round(50*time.Millisecond), bound)
			}
		}
	}
	rqMu.Unlock()
	if observed == 0 {
		t.Errorf("the requeue of no dead worker's job was observed (%d held jobs)", len(killedAt))
	}
	for job := range killedAt {
		runs := g.of(job)
		var next time.Time
		for _, r := range runs {
			if r.wid != victimOf[job] && (next.IsZero() || r.at.Before(next)) {
				next = r.at
			}
		}
		if next.IsZero() {
			t.Errorf("job %s held by %s was never run by another worker", job, victimOf[job])
		}
	}
	l.be.Proxy.AssertNoList(t, l.fname, l.cname)
}
