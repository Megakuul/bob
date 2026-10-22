package server

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/megakuul/bob/tests/parity"
	"github.com/megakuul/bob/tests/server/srvtest"
)

// fastFleet makes lease expiry and polling quick enough for a 5 minute test; it changes
// timings only, never semantics (fleet.* and log.* are SPEC §05.11.1 settings).
func fastFleet(extra map[string]string) map[string]string {
	m := map[string]string{
		"BOB_FLEET_POLL_INTERVAL": "2s",
		"BOB_FLEET_HEARTBEAT":     "1s",
		"BOB_FLEET_LEASE_TTL":     "5s",
		"BOB_FLEET_SKEW":          "1s",
		"BOB_WORKER_SLOTS":        "1",
	}
	for k, v := range extra {
		m[k] = v
	}
	return m
}

// logCount returns how many lines of pkg are readable from the persisted log now.
func logCount(ctx context.Context, e *srvtest.Env, build, pkg string) int {
	ls, err := e.Log(ctx, build, pkg, 0)
	if err != nil {
		return -1
	}
	return len(ls)
}

// TestRLOG001SizeTriggeredCommit: a worker commits its buffer at 10,000 lines or 2 MiB without
// waiting for log.flush_interval (set to 10 min here, so only the size triggers can commit).
func TestRLOG001SizeTriggeredCommit(t *testing.T) {
	parity.Covers(t, "R-LOG-001")
	e := srvtest.Start(t, srvtest.Options{Orchestrators: 1, Workers: 1,
		Env: fastFleet(map[string]string{"BOB_LOG_FLUSH_INTERVAL": "10m"})})
	if e == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	repo := srvtest.Repo(t, []srvtest.Pkg{
		{Name: "many", Cmd: `seq 1 12000; sleep 150`},
		{Name: "big", Cmd: `for i in $(seq 1 2500); do printf '%05d %0990d\n' $i 0; done; sleep 150`},
	})
	for _, c := range []struct {
		label, pkg string
		min        int
		what       string
	}{
		{"//many:t", "//many:t", 10000, "10,000 lines"},
		{"//big:t", "//big:t", 2000, "2 MiB (2,000 lines of 1,000 bytes)"},
	} {
		id, err := e.Submit(repo, c.label)
		if err != nil {
			t.Fatal(err)
		}
		if why := srvtest.Eventually(60*time.Second, func() string {
			if n := logCount(ctx, e, id, c.pkg); n < c.min {
				return fmt.Sprintf("%d lines readable", n)
			}
			return ""
		}); why != "" {
			t.Errorf("%s: the %s trigger did not commit within 60 s with a 10 min flush interval (%s)", c.label, c.what, why)
		}
		_ = e.Call(ctx, "bob.build.v1.BuildService", "CancelBuild", map[string]string{"id": id}, nil)
	}
	e.AssertNoList()
}

// TestRLOG001KillLosesOnlyUncommitted: a worker killed with SIGKILL loses only the lines after
// its last commit. Everything readable before the kill is still there afterwards, in a
// gap-free attempt 1 prefix; attempt 2 continues at the next seq (SPEC 05.10.2).
func TestRLOG001KillLosesOnlyUncommitted(t *testing.T) {
	parity.Covers(t, "R-LOG-001")
	e := srvtest.Start(t, srvtest.Options{Orchestrators: 1, Workers: 1,
		Env: fastFleet(map[string]string{"BOB_LOG_FLUSH_INTERVAL": "2s"})})
	if e == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	repo := srvtest.Repo(t, []srvtest.Pkg{{Name: "k", Cmd: `for i in $(seq 1 1500); do echo "$i"; sleep 0.02; done`}})
	id, err := e.Submit(repo, "//k:t")
	if err != nil {
		t.Fatal(err)
	}
	if why := srvtest.Eventually(90*time.Second, func() string {
		if n := logCount(ctx, e, id, "//k:t"); n < 100 {
			return fmt.Sprintf("%d lines readable", n)
		}
		return ""
	}); why != "" {
		t.Fatalf("no committed lines before the kill: %s", why)
	}
	committed := logCount(ctx, e, id, "//k:t")
	if err := e.Kill("worker-0"); err != nil {
		t.Fatal(err)
	}
	if err := e.Restart("worker-0"); err != nil {
		t.Fatal(err)
	}
	state, err := e.WaitBuild(id, 3*time.Minute)
	if err != nil || state != "BUILD_STATE_SUCCEEDED" {
		t.Fatalf("build after the worker kill: state %q err %v (the job must be retried)", state, err)
	}
	ls, err := e.Log(ctx, id, "//k:t", 0)
	if err != nil {
		t.Fatal(err)
	}
	var first []int64
	next2 := int64(0)
	for _, l := range ls {
		if l.Attempt == 1 {
			first = append(first, int64(l.Seq))
		} else if next2 == 0 {
			next2 = int64(l.Seq)
		}
	}
	for i, s := range first {
		if s != int64(i+1) {
			t.Fatalf("attempt 1 lines are not the gap-free prefix 1..k: position %d has seq %d", i, s)
		}
	}
	if len(first) < committed {
		t.Errorf("lost committed lines: %d lines were readable before SIGKILL, %d of attempt 1 remain", committed, len(first))
	}
	if next2 != int64(len(first))+1 {
		t.Errorf("attempt 2 starts at seq %d, want %d (after the last persisted seq)", next2, len(first)+1)
	}
	e.AssertNoList()
}
