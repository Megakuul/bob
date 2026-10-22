package chaos

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/megakuul/bob/tests/parity"
	"github.com/megakuul/bob/tests/server/srvtest"
	"github.com/megakuul/bob/tests/serverenv/chaos"
	"github.com/megakuul/bob/tests/serverenv/s3count"
)

// intervals are the controller intervals of SPEC 05.11 (R-SRV-004). The hourly and 10 minute
// controllers cannot be observed in a 5 minute test; their lease handover is checked by the soak suite.
var intervals = map[string]time.Duration{
	"membership": 10 * time.Second, "registrar": 2 * time.Second, "build": 5 * time.Second,
	"job-reconciler": 10 * time.Second, "promotion": 10 * time.Second, "release": 30 * time.Second,
	"lake-maintenance": 30 * time.Second, "forge-sweeper": 60 * time.Second,
}

func env() map[string]string {
	return map[string]string{
		"BOB_FLEET_POLL_INTERVAL": "2s", "BOB_FLEET_HEARTBEAT": "1s", "BOB_FLEET_LEASE_TTL": "5s",
		"BOB_FLEET_SKEW": "1s", "BOB_WORKER_SLOTS": "16",
	}
}

// driver returns the seeded chaos driver; the seed and every kill point are logged so a failure replays
// with -chaos-seed.
func driver(t *testing.T) *chaos.Driver {
	seed := chaos.Seed()
	f, err := os.Create(filepath.Join(t.TempDir(), "chaos.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close() })
	t.Logf("chaos seed %d (replay with -chaos-seed=%d)", seed, seed)
	return chaos.New(seed, f)
}

func targets(e *srvtest.Env, prefix string, n int, restartAfter time.Duration) []chaos.Target {
	var out []chaos.Target
	for i := range n {
		name := prefix + "-" + strconv.Itoa(i)
		out = append(out, chaos.Target{Name: name, Group: true, Pid: func() int { return e.Pid(name) },
			Restart: func(ctx context.Context) error {
				select {
				case <-time.After(restartAfter):
				case <-ctx.Done():
				}
				return e.Restart(name)
			}})
	}
	return out
}

// submitAll submits n single-package builds (each a distinct echo, so none is cached) with retries and
// returns the accepted build ids.
func submitAll(t *testing.T, e *srvtest.Env, n int) []string {
	ps := make([]srvtest.Pkg, n)
	for i := range ps {
		ps[i] = srvtest.Pkg{Name: "c" + strconv.Itoa(i), Cmd: "echo " + strconv.Itoa(i)}
	}
	repo := srvtest.Repo(t, ps)
	ids := make([]string, n)
	sem := make(chan struct{}, 16)
	var wg sync.WaitGroup
	for i := range ps {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			id, err := e.Submitting(repo, "//"+ps[i].Name+":t")
			if err != nil {
				t.Errorf("build %d was never accepted: %v", i, err)
			}
			ids[i] = id
		}()
	}
	t.Cleanup(wg.Wait)
	return ids
}

// settled waits until every accepted build is terminal and returns those that are not SUCCEEDED.
func settled(ctx context.Context, e *srvtest.Env, ids []string, timeout time.Duration) (bad []string) {
	for _, id := range ids {
		if id == "" {
			continue
		}
		var st string
		why := srvtest.Eventually(timeout, func() string {
			var b srvtest.Build
			err := e.ViaAny(func(v *srvtest.Env) error {
				var err error
				b, err = v.GetBuild(ctx, id)
				return err
			})
			if err != nil {
				return err.Error()
			}
			if st = b.State; !srvtest.Terminal(st) {
				return st
			}
			return ""
		})
		if why != "" {
			bad = append(bad, fmt.Sprintf("%s not terminal (%s)", id, why))
		} else if st != "BUILD_STATE_SUCCEEDED" {
			bad = append(bad, fmt.Sprintf("%s ended %s", id, st))
		}
	}
	return bad
}

// jobsHaveLeases checks the lease invariant on every job of the given builds: a job that is claimed or
// running names its worker; a finished job has been claimed.
func jobsHaveLeases(ctx context.Context, e *srvtest.Env, ids []string) []string {
	var bad []string
	for _, id := range ids {
		if id == "" {
			continue
		}
		var js []srvtest.Job
		if err := e.ViaAny(func(v *srvtest.Env) error {
			var err error
			js, err = v.Jobs(ctx, id)
			return err
		}); err != nil {
			bad = append(bad, fmt.Sprintf("jobs of %s: %v", id, err))
			continue
		}
		for _, j := range js {
			if (j.State == "JOB_STATE_CLAIMED" || j.State == "JOB_STATE_RUNNING") && j.WorkerID == "" {
				bad = append(bad, fmt.Sprintf("job %s is %s without an owner", j.ID, j.State))
			}
			if j.State == "JOB_STATE_SUCCEEDED" && (j.WorkerID == "" || j.ClaimedAt == "") {
				bad = append(bad, fmt.Sprintf("job %s succeeded without a claim", j.ID))
			}
		}
	}
	return bad
}

// TestRSRV004OrchestratorKill: 3 orchestrators, kill -9 of a random one every 20 s during a 300-job drain
// plus 20 build triggers. Every build reaches a terminal state, no job runs without a lease, every
// controller lease whose holder died is held by a live orchestrator within 3 x its interval, 0 LIST.
func TestRSRV004OrchestratorKill(t *testing.T) {
	parity.Covers(t, "R-SRV-004")
	e := srvtest.Start(t, srvtest.Options{Orchestrators: 3, Workers: 4, Env: env()})
	if e == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	d := driver(t)
	ids := submitAll(t, e, 320) // 300 drain jobs + 20 triggers

	type handover struct {
		victim, controller string
		deadline           time.Time
		started            string
	}
	var mu sync.Mutex
	var pending []handover
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { // the kill loop: a random orchestrator every 20 s
		defer wg.Done()
		ts := targets(e, "orchestrator", 3, 5*time.Second)
		for {
			select {
			case <-stop:
				return
			case <-time.After(20 * time.Second):
			}
			victim := d.Choose(ts, 1)[0]
			holds := map[string]string{} // controller -> started_at of the dying holder
			if err := e.ViaAny(func(v *srvtest.Env) error {
				os, err := v.Orchestrators(ctx)
				for _, o := range os {
					if v.OrchestratorName(o) == victim.Name {
						for _, c := range o.LeaderOf {
							holds[c] = o.StartedAt
						}
					}
				}
				return err
			}); err != nil {
				t.Errorf("harness: orchestrators: %v", err)
			}
			now := time.Now()
			_ = d.Kill(ctx, victim)
			mu.Lock()
			for c, started := range holds {
				if iv, ok := intervals[c]; ok {
					pending = append(pending, handover{victim.Name, c, now.Add(3 * iv), started})
				}
			}
			mu.Unlock()
		}
	}()
	wg.Add(1)
	go func() { // lease check at each deadline, and the "no job without a lease" sample
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			case <-time.After(time.Second):
			}
			mu.Lock()
			var due []handover
			rest := pending[:0]
			for _, h := range pending {
				if time.Now().After(h.deadline) {
					due = append(due, h)
				} else {
					rest = append(rest, h)
				}
			}
			pending = rest
			mu.Unlock()
			for _, h := range due {
				var holders []string
				_ = e.ViaAny(func(v *srvtest.Env) error {
					os, err := v.Orchestrators(ctx)
					for _, o := range os {
						for _, c := range o.LeaderOf {
							if c == h.controller && !(v.OrchestratorName(o) == h.victim && o.StartedAt == h.started) {
								holders = append(holders, v.OrchestratorName(o))
							}
						}
					}
					return err
				})
				if len(holders) != 1 {
					t.Errorf("controller %s: after %s was killed, %d live holders at 3 x %v, want 1", h.controller, h.victim, len(holders), intervals[h.controller])
				}
			}
		}
	}()
	bad := settled(ctx, e, ids, 6*time.Minute)
	close(stop)
	wg.Wait()
	for _, b := range bad {
		t.Errorf("build: %s", b)
	}
	for _, b := range jobsHaveLeases(ctx, e, ids) {
		t.Errorf("lease: %s", b)
	}
	if len(d.Events()) == 0 {
		t.Errorf("the run was too short: no orchestrator was killed")
	}
	e.AssertNoList()
}

// TestRNFR007CrashSafety runs the three crash sources together: orchestrator kill -9 every 20 s (R-SRV-004),
// 20 % of the workers SIGKILLed twice and one paused for 2 x TTL + 1 s (R-FLEET-003), and S3 faults (500,
// reset, response lost) on the control and fleet buckets (R-SRV-101 injection). Afterwards: 0 lost accepted
// work (every accepted build is terminal and succeeded), every stored record decodes and validates
// (`bob-server store fsck` exits 0 and reports nothing), 0 LIST.
func TestRNFR007CrashSafety(t *testing.T) {
	parity.Covers(t, "R-NFR-007")
	e := srvtest.Start(t, srvtest.Options{Orchestrators: 3, Workers: 10, Env: env()})
	if e == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	d := driver(t)
	var faults []s3count.Fault
	for _, k := range []string{s3count.Fault500, s3count.FaultReset, s3count.FaultLost} {
		for _, b := range []string{"bob-control", "bob-fleet"} {
			faults = append(faults, s3count.Fault{Kind: k, Bucket: b, Op: s3count.OpPut, Prob: 0.01})
		}
	}
	e.SetFaults(s3count.Script{Seed: chaos.Seed(), Faults: faults})
	ids := submitAll(t, e, 150)

	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		ts := targets(e, "orchestrator", 3, 5*time.Second)
		for {
			select {
			case <-stop:
				return
			case <-time.After(20 * time.Second):
			}
			_ = d.Kill(ctx, d.Choose(ts, 1)[0])
		}
	}()
	go func() {
		defer wg.Done()
		ws := targets(e, "worker", 10, 2*time.Second)
		for round := 0; round < 2; round++ {
			select {
			case <-stop:
				return
			case <-time.After(25 * time.Second):
			}
			_, _ = d.KillFraction(ctx, ws, 0.2)
		}
		_ = d.Pause(ctx, d.Choose(ws, 1)[0], 2*5*time.Second+time.Second) // 2 x lease_ttl (5 s) + 1 s
	}()
	bad := settled(ctx, e, ids, 6*time.Minute)
	close(stop)
	wg.Wait()
	e.SetFaults(s3count.Script{})
	for _, b := range bad {
		t.Errorf("lost accepted work: %s", b)
	}
	for _, b := range jobsHaveLeases(ctx, e, ids) {
		t.Errorf("lease: %s", b)
	}
	e.AssertNoList() // before fsck: fsck may LIST
	r := e.RunServer(5*time.Minute, "store", "fsck")
	if r.IsNotImplemented() {
		t.Errorf("red: not implemented yet: bob-server store fsck -> %s", strings.SplitN(strings.TrimSpace(r.Combined()), "\n", 2)[0])
		return
	}
	if r.Exit != 0 || strings.Contains(strings.ToLower(r.Combined()), "corrupt") || strings.Contains(strings.ToLower(r.Combined()), "invalid") {
		t.Errorf("bob-server store fsck: exit %d, want 0 and no corrupt or invalid record:\n%s", r.Exit, r.Combined())
	}
}
