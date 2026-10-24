package fleet

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/megakuul/bob/internal/orchestrator"
	"github.com/megakuul/bob/internal/worker"
	"github.com/megakuul/bob/tests/parity"
	"github.com/megakuul/bob/tests/store/env"
)

// attached is a fake attached worker for the selection tests: it records its wake-ups.
type attached struct {
	id   string
	free int
	mu   sync.Mutex
	got  []string
}

func (a *attached) ID() string     { return a.id }
func (a *attached) FreeSlots() int { return a.free }
func (a *attached) Wake(class string, slot uint64, job string) {
	a.mu.Lock()
	a.got = append(a.got, job)
	a.mu.Unlock()
}

func (a *attached) woken(job string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, j := range a.got {
		if j == job {
			return true
		}
	}
	return false
}

func p99(d []time.Duration) time.Duration {
	sort.Slice(d, func(i, j int) bool { return d[i] < d[j] })
	return d[(len(d)*99+99)/100-1]
}

// latencies enqueues n jobs one after another into a fleet of idle workers and measures
// enqueue -> Exec start for each. wake selects whether the dispatcher wakes workers.
func latencies(l *lab, workers, n int, poll time.Duration, wake bool) ([]time.Duration, *orchestrator.Dispatcher) {
	t := l.t
	t.Helper()
	var mu sync.Mutex
	started := map[string]time.Time{}
	var targets []orchestrator.Wake
	for i := 0; i < workers; i++ {
		cfg := l.workerCfg(fmt.Sprintf("w%02d", i), func(_ context.Context, c worker.Claim) worker.Outcome {
			mu.Lock()
			started[c.JobID] = time.Now()
			mu.Unlock()
			return worker.Outcome{Status: "ok"}
		})
		cfg.PollInterval = poll
		cfg.Seed = uint64(i + 1)
		w := l.start(cfg)
		if w == nil {
			return nil, nil
		}
		targets = append(targets, w)
	}
	d := &orchestrator.Dispatcher{Queue: l.q, SecondWakeAfter: 2 * time.Second, Seed: 12}
	if wake {
		d.Workers = targets
	}
	time.Sleep(poll + 200*time.Millisecond) // every worker has polled once: all idle
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	var out []time.Duration
	for i := 0; i < n; i++ {
		id := jobID(i)
		at := time.Now()
		if _, _, err := d.Enqueue(ctx, l.job(id)); err != nil {
			if env.Red(t, err) {
				return nil, nil
			}
			t.Fatalf("enqueue: %v", err)
		}
		if !env.Wait(30*time.Second, func() bool { mu.Lock(); defer mu.Unlock(); _, ok := started[id]; return ok }) {
			t.Fatalf("job %s was not picked up within 30 s", id)
		}
		mu.Lock()
		out = append(out, started[id].Sub(at))
		mu.Unlock()
		time.Sleep(50 * time.Millisecond)
	}
	return out, d
}

// TestRFLEET012WakeLatency: with wake-ups, enqueue -> claim p99 <= 1 s with 20 idle workers
// that poll only every 30 s.
func TestRFLEET012WakeLatency(t *testing.T) {
	parity.Covers(t, "R-FLEET-012")
	l := newLab(t)
	if l == nil {
		return
	}
	d, _ := latencies(l, 20, 200, 30*time.Second, true)
	if d == nil {
		return
	}
	if got := p99(d); got > time.Second {
		t.Errorf("enqueue -> claim p99 = %v with wake-ups and 20 idle workers, want <= 1 s", got)
	}
	l.be.Proxy.AssertNoList(t, l.fname, l.cname)
}

// TestRFLEET012PollingOnlyLatency: with wake-ups disabled the pickup is bounded by the poll
// interval: p99 <= poll_interval x 1.2 + 1 s.
func TestRFLEET012PollingOnlyLatency(t *testing.T) {
	parity.Covers(t, "R-FLEET-012")
	l := newLab(t)
	if l == nil {
		return
	}
	const poll = 2 * time.Second
	d, _ := latencies(l, 20, 40, poll, false)
	if d == nil {
		return
	}
	if got, bound := p99(d), time.Duration(float64(poll)*1.2)+time.Second; got > bound {
		t.Errorf("enqueue -> claim p99 = %v without wake-ups, want <= poll_interval x 1.2 + 1 s = %v", got, bound)
	}
}

// TestRFLEET012SelectionPolicy: the Prefer worker is woken first, otherwise the one with the
// most free slots, ties at random; a job still queued after SecondWakeAfter wakes a second
// worker; a job claimed in time wakes only one.
func TestRFLEET012SelectionPolicy(t *testing.T) {
	parity.Covers(t, "R-FLEET-012")
	l := newLab(t)
	if l == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	mk := func(frees ...int) ([]*attached, *orchestrator.Dispatcher) {
		var as []*attached
		var ws []orchestrator.Wake
		for i, f := range frees {
			a := &attached{id: fmt.Sprintf("a%d", i), free: f}
			as = append(as, a)
			ws = append(ws, a)
		}
		return as, &orchestrator.Dispatcher{Queue: l.q, Workers: ws, SecondWakeAfter: 300 * time.Millisecond, Seed: 3}
	}
	count := func(as []*attached, job string) (n int, ids []string) {
		for _, a := range as {
			if a.woken(job) {
				n++
				ids = append(ids, a.id)
			}
		}
		return
	}

	// Prefer wins over free slots.
	_, d := mk(1, 8, 2, 4)
	j := l.job("pref")
	j.Prefer = "a2"
	_, woken, err := d.Enqueue(ctx, j)
	if env.Red(t, err) {
		return
	}
	if err != nil || len(woken) == 0 || woken[0] != "a2" {
		t.Errorf("Prefer=a2: woken %v (%v), want a2 first", woken, err)
	}
	// Most free slots otherwise.
	if _, woken, err = d.Enqueue(ctx, l.job("most")); err != nil || len(woken) == 0 || woken[0] != "a1" {
		t.Errorf("most free slots: woken %v (%v), want a1 first", woken, err)
	}
	// Ties are broken at random: over 120 jobs each of 3 equal workers is first at least 20 times.
	as, d := mk(2, 2, 2)
	first := map[string]int{}
	for i := 0; i < 120; i++ {
		_, woken, err := d.Enqueue(ctx, l.job(fmt.Sprintf("tie%03d", i)))
		if err != nil || len(woken) == 0 {
			t.Fatalf("tie enqueue: %v %v", woken, err)
		}
		first[woken[0]]++
	}
	for _, a := range as {
		if first[a.id] < 20 {
			t.Errorf("tie-break chose %s first %d of 120 times: %v (want random, >= 20 each)", a.id, first[a.id], first)
		}
	}
	// Nobody claims: after SecondWakeAfter a second, different worker is woken; never a third.
	as, d = mk(3, 2, 1)
	if _, _, err := d.Enqueue(ctx, l.job("slow")); err != nil {
		t.Fatal(err)
	}
	time.Sleep(900 * time.Millisecond)
	if n, ids := count(as, "slow"); n != 2 {
		t.Errorf("job unclaimed after 3 x SecondWakeAfter: %d workers woken %v, want exactly 2", n, ids)
	}
	// Claimed in time: only one worker is woken. A real worker claims on the first wake.
	got := make(chan struct{}, 1)
	cfg := l.workerCfg("real", func(_ context.Context, c worker.Claim) worker.Outcome {
		got <- struct{}{}
		return worker.Outcome{Status: "ok"}
	})
	cfg.PollInterval = time.Hour
	real := l.start(cfg)
	if real == nil {
		return
	}
	bystander := &attached{id: "bystander", free: 1}
	d = &orchestrator.Dispatcher{Queue: l.q, Workers: []orchestrator.Wake{real, bystander}, SecondWakeAfter: 500 * time.Millisecond, Seed: 1}
	real.FreeSlots()
	if _, woken, err := d.Enqueue(ctx, l.job("fast")); err != nil || len(woken) == 0 {
		t.Fatalf("enqueue: %v %v", woken, err)
	}
	select {
	case <-got:
	case <-time.After(5 * time.Second):
		t.Fatalf("a woken worker did not claim within 5 s")
	}
	time.Sleep(time.Second)
	if bystander.woken("fast") {
		t.Errorf("a second worker was woken although the first claimed the job in time")
	}
}

// TestRFLEET012NoHerdAndNoDoubleClaim: duplicate wake-ups never cause a double claim, and a
// wake-up makes exactly one worker read exactly one slot (no herd).
func TestRFLEET012NoHerdAndNoDoubleClaim(t *testing.T) {
	parity.Covers(t, "R-FLEET-012")
	l := newLab(t)
	if l == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	g := newLedger()
	var ws []*worker.Worker
	for i := 0; i < 20; i++ {
		id := fmt.Sprintf("w%02d", i)
		cfg := l.workerCfg(id, func(_ context.Context, c worker.Claim) worker.Outcome {
			g.note(id, c)
			return worker.Outcome{Status: "ok"}
		})
		cfg.PollInterval, cfg.Heartbeat, cfg.ConfigRefresh, cfg.RecordRefresh, cfg.JobWatch = time.Hour, time.Hour, time.Hour, time.Hour, time.Hour
		w := l.start(cfg)
		if w == nil {
			return
		}
		ws = append(ws, w)
	}
	time.Sleep(time.Second) // start-up reads are done
	// Every worker gets the wake-up for every job, at once, three times over.
	for i := 0; i < 40; i++ {
		id := jobID(i)
		slot, err := l.q.Enqueue(ctx, l.job(id), orchestrator.JobFirst)
		if env.Red(t, err) {
			return
		}
		if err != nil {
			t.Fatal(err)
		}
		var wg sync.WaitGroup
		for rep := 0; rep < 3; rep++ {
			for _, w := range ws {
				wg.Add(1)
				go func() { defer wg.Done(); w.Wake(l.classID, slot, id) }()
			}
		}
		wg.Wait()
	}
	env.Wait(30*time.Second, func() bool { return g.jobs() == 40 })
	time.Sleep(time.Second)
	for i := 0; i < 40; i++ {
		if r := g.of(jobID(i)); len(r) != 1 {
			t.Errorf("job %s ran %d times after duplicate wake-ups: %+v", jobID(i), len(r), r)
		}
	}

	// One wake-up of one worker: the request cost is a claim, not a herd of 20 GETs.
	l.be.Proxy.Reset()
	slot, err := l.q.Enqueue(ctx, l.job("herd"), orchestrator.JobFirst)
	if err != nil {
		t.Fatal(err)
	}
	enq := env.Total(l.be.Proxy.Counts(), l.fname) + env.Total(l.be.Proxy.Counts(), l.cname)
	ws[0].Wake(l.classID, slot, "herd")
	env.Wait(10*time.Second, func() bool { return len(g.of("herd")) == 1 })
	time.Sleep(500 * time.Millisecond)
	total := env.Total(l.be.Proxy.Counts(), l.fname) + env.Total(l.be.Proxy.Counts(), l.cname)
	if claim := total - enq; claim > 8 { // claim 4 (+2 head advance) + result 2 + slack
		t.Errorf("one wake-up cost %d requests after the enqueue, want a single claim (about 6)", claim)
	}
}

// TestRFLEET012BacklogSpread: a polling worker with a backlog picks a random slot in
// [cursor, tail) before falling back to its cursor, so a burst of pollers does not all
// contend for the same slot.
func TestRFLEET012BacklogSpread(t *testing.T) {
	parity.Covers(t, "R-FLEET-012")
	l := newLab(t)
	if l == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	slots := map[string]uint64{}
	for i := 0; i < 100; i++ {
		s, err := l.q.Enqueue(ctx, l.job(jobID(i)), orchestrator.JobFirst)
		if env.Red(t, err) {
			return
		}
		if err != nil {
			t.Fatal(err)
		}
		slots[jobID(i)] = s
	}
	var mu sync.Mutex
	firstSlot := map[string]uint64{}
	for i := 0; i < 10; i++ {
		id := fmt.Sprintf("p%02d", i)
		cfg := l.workerCfg(id, func(ctx context.Context, c worker.Claim) worker.Outcome {
			mu.Lock()
			if _, ok := firstSlot[id]; !ok {
				firstSlot[id] = slots[c.JobID]
			}
			mu.Unlock()
			<-ctx.Done() // keeps its only slot busy: it never claims a second job
			return worker.Outcome{Status: "infra"}
		})
		cfg.PollInterval = 5 * time.Second
		cfg.Seed = uint64(100 + i)
		if l.start(cfg) == nil {
			return
		}
	}
	env.Wait(30*time.Second, func() bool { mu.Lock(); defer mu.Unlock(); return len(firstSlot) == 10 })
	mu.Lock()
	defer mu.Unlock()
	distinct := map[uint64]bool{}
	lowest := 0
	for _, s := range firstSlot {
		distinct[s] = true
		if s <= 10 {
			lowest++
		}
	}
	if len(firstSlot) != 10 {
		t.Fatalf("only %d of 10 pollers claimed a job", len(firstSlot))
	}
	if lowest == 10 {
		t.Errorf("all 10 pollers took one of the 10 lowest slots: %v; a backlog poll picks a random slot in [cursor, tail)", firstSlot)
	}
}
