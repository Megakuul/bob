package fleet

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/megakuul/bob/internal/orchestrator"
	"github.com/megakuul/bob/internal/store"
	"github.com/megakuul/bob/internal/worker"
	"github.com/megakuul/bob/tests/parity"
	"github.com/megakuul/bob/tests/store/env"
)

// drain enqueues n jobs from `enqueuers` goroutines in the given write order while `claimers`
// workers drain the queue, and returns how many distinct jobs ran and how many ran twice.
func drain(l *lab, n, claimers, enqueuers int, order orchestrator.Order, wait time.Duration) (claimed, doubles int, enqErr error) {
	t := l.t
	t.Helper()
	g := newLedger()
	exec := func(id string) func(context.Context, worker.Claim) worker.Outcome {
		return func(_ context.Context, c worker.Claim) worker.Outcome {
			g.note(id, c)
			return worker.Outcome{Status: "ok"}
		}
	}
	for i := 0; i < claimers; i++ {
		if l.start(l.workerCfg(fmt.Sprintf("w%02d", i), exec(fmt.Sprintf("w%02d", i)))) == nil {
			return 0, 0, nil
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	var wg sync.WaitGroup
	var mu sync.Mutex
	for e := 0; e < enqueuers; e++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := e; i < n; i += enqueuers {
				if _, err := l.q.Enqueue(ctx, l.job(jobID(i)), order); err != nil {
					mu.Lock()
					enqErr = err
					mu.Unlock()
					return
				}
			}
		}()
	}
	wg.Wait()
	if enqErr != nil {
		return 0, 0, enqErr
	}
	env.Wait(wait, func() bool { return g.jobs() >= n })
	time.Sleep(500 * time.Millisecond) // let a double claim show up
	for i := 0; i < n; i++ {
		switch r := g.of(jobID(i)); {
		case len(r) == 1:
			claimed++
		case len(r) > 1:
			claimed++
			doubles++
		}
	}
	return claimed, doubles, nil
}

// TestRFLEET011QueueLiveness: 300 jobs, 20 claimers, 3 enqueuers: 300/300 claimed, 0 doubles,
// no LIST. The job record is written before its slot (job-first).
func TestRFLEET011QueueLiveness(t *testing.T) {
	parity.Covers(t, "R-FLEET-011")
	l := newLab(t)
	if l == nil {
		return
	}
	claimed, doubles, err := drain(l, 300, 20, 3, orchestrator.JobFirst, 2*time.Minute)
	if env.Red(t, err) {
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	if claimed != 300 || doubles != 0 {
		t.Errorf("claimed %d of 300 jobs, %d of them twice; want 300/300 and 0 doubles", claimed, doubles)
	}
	l.be.Proxy.AssertNoList(t, l.fname, l.cname)
}

// TestRFLEET011SlotFirstLosesAJob is the protected regression test: with the order inverted
// (slot first, job second) a worker finds a slot whose job does not exist yet, marks it
// orphan, and the job is lost, as in V2 run 1. The drain must fail by losing at least one job.
func TestRFLEET011SlotFirstLosesAJob(t *testing.T) {
	parity.Covers(t, "R-FLEET-011")
	l := newLab(t)
	if l == nil {
		return
	}
	l.q.BetweenWrites = func(context.Context) error { time.Sleep(150 * time.Millisecond); return nil } // the window V2 hit by chance
	claimed, doubles, err := drain(l, 30, 20, 3, orchestrator.SlotFirst, 20*time.Second)
	if env.Red(t, err) {
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	if claimed >= 30 {
		t.Errorf("slot-first enqueue claimed %d of 30 jobs (%d doubles): the regression must lose a job, otherwise the job-first rule is not what protects the queue", claimed, doubles)
	}
}

// TestRFLEET011OrphanAndStaleSlots: a worker that finds a slot whose job is missing marks it
// orphan; one that finds a slot whose job is not queued marks it stale; neither runs anything.
func TestRFLEET011OrphanAndStaleSlots(t *testing.T) {
	parity.Covers(t, "R-FLEET-011")
	l := newLab(t)
	if l == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	g := newLedger()

	// Stale: the job is cancelled while queued, then its slot is reached.
	slotStale, err := l.q.Enqueue(ctx, l.job("stale-1"), orchestrator.JobFirst)
	if env.Red(t, err) {
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	if err := l.q.Cancel(ctx, "stale-1"); err != nil {
		t.Fatal(err)
	}
	// Orphan: the slot exists, the job record never will (the enqueuer died between the writes).
	l.q.BetweenWrites = func(context.Context) error { return errors.New("enqueuer died between the writes") }
	if _, err := l.q.Enqueue(ctx, l.job("orphan-1"), orchestrator.SlotFirst); err == nil {
		t.Fatalf("Enqueue ignored the error of BetweenWrites")
	}
	l.q.BetweenWrites = nil
	// A live job behind both slots proves the worker moved on.
	slotLive, err := l.q.Enqueue(ctx, l.job("live-1"), orchestrator.JobFirst)
	if err != nil {
		t.Fatal(err)
	}
	if l.start(l.workerCfg("w1", func(_ context.Context, c worker.Claim) worker.Outcome {
		g.note("w1", c)
		return worker.Outcome{Status: "ok"}
	})) == nil {
		return
	}
	if v := l.waitState("live-1", 20*time.Second, "succeeded"); v == nil || v.State != "succeeded" {
		t.Fatalf("live job did not run behind the stale and orphan slots: %+v", v)
	}
	if got, err := l.q.SlotTaken(ctx, slotStale); err != nil || got != "stale" {
		t.Errorf("slot of a cancelled job is marked %q (%v), want \"stale\"", got, err)
	}
	if got, err := l.q.SlotTaken(ctx, slotLive); err != nil || got != "w1" {
		t.Errorf("slot of the live job is marked %q (%v), want the worker id", got, err)
	}
	orphanSeen := false
	for s := slotStale + 1; s < slotLive; s++ {
		if got, err := l.q.SlotTaken(ctx, s); err == nil && got == "orphan" {
			orphanSeen = true
		}
	}
	if !orphanSeen {
		t.Errorf("no slot between %d and %d is marked orphan", slotStale, slotLive)
	}
	if g.of("stale-1") != nil || g.of("orphan-1") != nil {
		t.Errorf("the worker ran a cancelled or missing job: %v %v", g.of("stale-1"), g.of("orphan-1"))
	}
}

// TestRFLEET004WorkerIgnoresOtherClasses: workers only read the slots of classes they satisfy,
// so a job of class x86_64-linux is never claimed by an aarch64 worker or by a worker that
// lacks a required cap or label.
func TestRFLEET004WorkerIgnoresOtherClasses(t *testing.T) {
	parity.Covers(t, "R-FLEET-004")
	l := newLab(t)
	if l == nil {
		return
	}
	need := store.Class{Platform: "x86_64-linux", Caps: []string{"kvm"}, Labels: []string{"team-a"}}
	id, err := store.ClassID(need)
	if env.Red(t, err) {
		return
	}
	q := *l.q
	q.Class = id
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	g := newLedger()
	exec := func(wid string) func(context.Context, worker.Claim) worker.Outcome {
		return func(_ context.Context, c worker.Claim) worker.Outcome {
			g.note(wid, c)
			return worker.Outcome{Status: "ok"}
		}
	}
	cfg := func(wid string, p store.WorkerProfile) worker.Config {
		c := l.workerCfg(wid, exec(wid))
		c.Profile = p
		c.Classes = []store.Class{need}
		return c
	}
	wrong := []worker.Config{
		cfg("arm", store.WorkerProfile{Platform: "aarch64-linux", Caps: []string{"kvm"}, Labels: []string{"team-a"}}),
		cfg("nocap", store.WorkerProfile{Platform: "x86_64-linux", Labels: []string{"team-a"}}),
		cfg("nolabel", store.WorkerProfile{Platform: "x86_64-linux", Caps: []string{"kvm"}}),
	}
	for _, c := range wrong {
		if l.start(c) == nil {
			return
		}
	}
	slot, err := q.Enqueue(ctx, l.job("only-x86"), orchestrator.JobFirst)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(2 * time.Second) // 20 poll intervals
	if runs := g.of("only-x86"); len(runs) != 0 {
		t.Fatalf("a worker that does not satisfy the class ran the job: %+v", runs)
	}
	if got, _ := q.SlotTaken(ctx, slot); got != "" {
		t.Errorf("an ineligible worker touched the slot: tk=%q", got)
	}
	if v, err := q.Job(ctx, "only-x86"); err != nil || v.State != "queued" {
		t.Errorf("job state = %+v, %v; want queued", v, err)
	}
	// The right worker takes it.
	good := cfg("good", store.WorkerProfile{Platform: "x86_64-linux", Caps: []string{"kvm", "gpu"}, Labels: []string{"team-a", "prod"}})
	if l.start(good) == nil {
		return
	}
	env.Wait(20*time.Second, func() bool { return len(g.of("only-x86")) > 0 })
	if runs := g.of("only-x86"); len(runs) != 1 || runs[0].wid != "good" {
		t.Errorf("runs = %+v, want exactly one by the eligible worker", runs)
	}
}
