package fleet

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/megakuul/bob/internal/orchestrator"
	"github.com/megakuul/bob/internal/worker"
	"github.com/megakuul/bob/tests/parity"
	"github.com/megakuul/bob/tests/store/env"
)

// reqs counts every request on the lab's control and fleet buckets since the last Reset.
func (l *lab) reqs() int {
	c := l.be.Proxy.Counts()
	return env.Total(c, l.fname) + env.Total(c, l.cname)
}

// TestRFLEET009RequestBudgetPerJob: 300 jobs enqueued with wake-ups, claimed by woken workers,
// run and finalised cost a mean of at most 26 requests on control+fleet each, and 0 LIST.
// Heartbeats and job watch are excluded by their own rule (shared per worker, 1 per 30 s) and
// idle polls by construction: the workers' timers are set to an hour, so only wake-ups,
// claims, results and the build controller's work generate requests. (Budget id R-NFR-100…
// in tests/budgets covers the pickup latency; this is the request count.)
func TestRFLEET009RequestBudgetPerJob(t *testing.T) {
	parity.Covers(t, "R-FLEET-009")
	l := newLab(t)
	if l == nil {
		return
	}
	const jobs = 300
	g := newLedger()
	var targets []orchestrator.Wake
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
		targets = append(targets, w)
	}
	time.Sleep(2 * time.Second) // start-up reads
	l.be.Proxy.Reset()
	d := &orchestrator.Dispatcher{Queue: l.q, Workers: targets, SecondWakeAfter: 2 * time.Second, Seed: 9}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	var wg sync.WaitGroup
	errs := make(chan error, 3)
	for e := 0; e < 3; e++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := e; i < jobs; i += 3 {
				if _, _, err := d.Enqueue(ctx, l.job(jobID(i))); err != nil {
					errs <- err
					return
				}
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if env.Red(t, err) {
			return
		}
		t.Fatal(err)
	}
	if !env.Wait(2*time.Minute, func() bool {
		for i := 0; i < jobs; i++ {
			if v := l.view(jobID(i)); v == nil || v.State != "succeeded" {
				return false
			}
		}
		return true
	}) {
		t.Fatalf("the 300 jobs did not all succeed")
	}
	for i := 0; i < jobs; i++ {
		if ok, err := l.q.Finalise(ctx, jobID(i)); err != nil || !ok {
			t.Fatalf("finalise %s: accepted=%v, %v", jobID(i), ok, err)
		}
	}
	total := l.reqs()
	if mean := float64(total) / jobs; mean > 26 {
		t.Errorf("mean %.1f requests per job on control+fleet (%d total), want <= 26", mean, total)
	}
	l.be.Proxy.AssertNoList(t, l.fname, l.cname)
}

// idleFleet starts n idle workers with the default intervals (poll 30 s ± 20 %, heartbeat 15 s,
// config 30 s, worker record 60 s) on one class.
func idleFleet(l *lab, n int) bool {
	for i := 0; i < n; i++ {
		cfg := l.workerCfg(fmt.Sprintf("idle%03d", i), func(context.Context, worker.Claim) worker.Outcome {
			return worker.Outcome{Status: "ok"}
		})
		cfg.PollInterval, cfg.Heartbeat, cfg.ConfigRefresh, cfg.RecordRefresh, cfg.JobWatch = 30*time.Second, 15*time.Second, 30*time.Second, 60*time.Second, 30*time.Second
		cfg.Now = time.Now
		cfg.Seed = uint64(i + 1)
		if l.start(cfg) == nil {
			return false
		}
	}
	return true
}

// TestRFLEET010IdleRequestMix is the structure of the idle cost on a short run: 20 idle
// workers on one class make only heartbeats (PUT whb/), idle polls (GET qs/), config refreshes
// (GET fleet/) and worker-record refreshes (GET worker/), each at its configured rate, with
// no LIST and nothing on the control bucket. The exact 100-worker, 10-minute bound of the
// SPEC is TestRFLEET010IdleFleetCost (slow).
func TestRFLEET010IdleRequestMix(t *testing.T) {
	parity.Covers(t, "R-FLEET-010")
	l := newLab(t)
	if l == nil {
		return
	}
	const n, warm, window = 20, 15 * time.Second, 75 * time.Second
	if !idleFleet(l, n) {
		return
	}
	time.Sleep(warm)
	l.be.Proxy.Reset()
	time.Sleep(window)
	c := l.be.Proxy.Counts()
	kinds := map[string]int{}
	for _, r := range c.Rows {
		if r.Bucket == l.fname {
			kinds[r.Op+" "+r.Prefix] += r.Count
		}
	}
	rate := func(key string) float64 { return float64(kinds[key]) / (n * window.Seconds()) }
	for _, k := range []struct {
		key      string
		interval float64
	}{{"PUT whb/", 15}, {"GET qs/", 30}, {"GET fleet/", 30}, {"GET worker/", 60}} {
		want := 1 / k.interval
		if got := rate(k.key); got < 0.7*want || got > 1.3*want {
			t.Errorf("%s: %.4f req/s per worker, want about %.4f (every %.0f s)", k.key, got, want, k.interval)
		}
		delete(kinds, k.key)
	}
	for key, n := range kinds {
		t.Errorf("an idle worker made %d unexpected %s requests; the idle mix is heartbeat, poll, config and record refresh only", n, key)
	}
	if ctl := env.Total(c, l.cname); ctl != 0 {
		t.Errorf("%d requests on the control bucket from idle workers, want 0", ctl)
	}
	if per := float64(env.Total(c, l.fname)) / (n * window.Seconds()); per > 0.15*1.25 {
		t.Errorf("%.3f req/s per idle worker, want about 0.15", per)
	}
	l.be.Proxy.AssertNoList(t, l.fname, l.cname)
}
