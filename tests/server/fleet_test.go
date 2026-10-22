package server

import (
	"context"
	"fmt"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/megakuul/bob/tests/parity"
	"github.com/megakuul/bob/tests/server/srvtest"
)

// TestRSRV100WorkersWorkWithoutAttach: with every Attach stream blocked, a 50-job drain completes and
// pickup (created -> claimed) is bounded by the poll interval: p99 <= 1.2 x poll_interval + 1 s.
//
// Not covered here: "a job that needs checkout fails with an infrastructure error after 3 attempts"
// needs a project with a forge integration (a git source); the forge stack is not part of the
// devstack yet (M1.8 report, SPEC problems).
func TestRSRV100WorkersWorkWithoutAttach(t *testing.T) {
	parity.Covers(t, "R-SRV-100")
	const jobs, poll = 50, 3 * time.Second
	e := srvtest.Start(t, srvtest.Options{Orchestrators: 2, Workers: 10, Env: fastFleet(map[string]string{
		"BOB_FLEET_POLL_INTERVAL": poll.String(), "BOB_WORKER_SLOTS": "8",
	})})
	if e == nil {
		return
	}
	if !e.AttachProxy(true, -1) {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	ps := make([]srvtest.Pkg, jobs)
	for i := range ps {
		ps[i] = srvtest.Pkg{Name: "d" + strconv.Itoa(i), Cmd: "echo " + strconv.Itoa(i)}
	}
	repo := srvtest.Repo(t, ps)
	ids := make([]string, jobs)
	var wg sync.WaitGroup
	for i := range ps {
		wg.Add(1)
		go func() {
			defer wg.Done()
			id, err := e.Submit(repo, "//"+ps[i].Name+":t")
			if err != nil {
				t.Error(err)
			}
			ids[i] = id
		}()
	}
	wg.Wait()
	if t.Failed() {
		return
	}
	var lat []time.Duration
	for _, id := range ids {
		if st, err := e.WaitBuild(id, 3*time.Minute); err != nil || st != "BUILD_STATE_SUCCEEDED" {
			t.Fatalf("build %s with all Attach streams blocked: state %q err %v", id, st, err)
		}
		js, err := e.Jobs(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		for _, j := range js {
			c, err1 := time.Parse(time.RFC3339Nano, j.CreatedAt)
			k, err2 := time.Parse(time.RFC3339Nano, j.ClaimedAt)
			if err1 != nil || err2 != nil {
				t.Fatalf("job %s: created_at %q claimed_at %q", j.ID, j.CreatedAt, j.ClaimedAt)
			}
			lat = append(lat, k.Sub(c))
		}
	}
	if len(lat) < jobs {
		t.Errorf("only %d jobs for %d builds", len(lat), jobs)
	}
	if limit, p99 := time.Duration(1.2*float64(poll))+time.Second, srvtest.Percentile(lat, 0.99); p99 > limit {
		t.Errorf("pickup p99 = %v without wake-ups, want <= 1.2 x poll + 1 s = %v", p99, limit)
	}
	e.AssertNoList()
}

// waitOrchestrators waits until the API lists exactly n orchestrators.
func waitOrchestrators(ctx context.Context, e *srvtest.Env, n int, timeout time.Duration) (time.Duration, string) {
	t0 := time.Now()
	why := srvtest.Eventually(timeout, func() string {
		os, err := e.Orchestrators(ctx)
		if err != nil {
			return err.Error()
		}
		if len(os) != n {
			return fmt.Sprintf("%d orchestrators listed", len(os))
		}
		return ""
	})
	return time.Since(t0), why
}

// TestRSRV102Membership: a killed orchestrator leaves fleet/cfg within 40 s; a (re)started one is
// listed and receives Attach streams from all workers within 40 s; a worker that is redirected to a
// server with another TLS key (S17 security case) refuses it and opens no Attach stream there.
func TestRSRV102Membership(t *testing.T) {
	parity.Covers(t, "R-SRV-102")
	const workers = 4
	e := srvtest.Start(t, srvtest.Options{Orchestrators: 3, Workers: workers, Env: fastFleet(nil)})
	if e == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	if d, why := waitOrchestrators(ctx, e, 3, 40*time.Second); why != "" {
		t.Fatalf("3 orchestrators started, %s after %v", why, d)
	}
	attached := func(name string) string {
		if n := e.AttachStreams()[name]; n != workers {
			return fmt.Sprintf("%s has %d Attach streams, want %d", name, n, workers)
		}
		return ""
	}
	if why := srvtest.Eventually(40*time.Second, func() string { return attached("orchestrator-2") }); why != "" {
		t.Fatalf("start: %s", why)
	}
	if err := e.Kill("orchestrator-2"); err != nil {
		t.Fatal(err)
	}
	if d, why := waitOrchestrators(ctx, e.Via(0), 2, 40*time.Second); why != "" {
		t.Errorf("a killed orchestrator is still in fleet/cfg after %v (%s), want removed within 40 s", d, why)
	}
	if err := e.Restart("orchestrator-2"); err != nil {
		t.Fatal(err)
	}
	if d, why := waitOrchestrators(ctx, e.Via(0), 3, 40*time.Second); why != "" {
		t.Errorf("the new orchestrator is not listed after %v: %s", d, why)
	}
	if why := srvtest.Eventually(40*time.Second, func() string { return attached("orchestrator-2") }); why != "" {
		t.Errorf("the new orchestrator did not receive Attach streams from all workers within 40 s: %s", why)
	}

	// wrong key: orchestrator-2's address now leads to orchestrator-0's server
	e.AttachRedirect(2, 0)
	defer e.AttachRedirect(2, -1)
	if err := e.Kill("orchestrator-2"); err != nil { // the workers' streams to it end; they reconnect to the pinned address
		t.Fatal(err)
	}
	if err := e.Restart("orchestrator-2"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(30 * time.Second)
	if n := e.AttachStreams()["orchestrator-2"]; n != 0 {
		t.Errorf("%d Attach streams reached a server presenting another TLS key than the pinned one, want 0", n)
	}
	if why := attached("orchestrator-0"); why != "" {
		t.Errorf("redirecting one address disturbed the others: %s", why)
	}
	e.AssertNoList()
}
