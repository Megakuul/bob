package server

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/megakuul/bob/tests/parity"
	"github.com/megakuul/bob/tests/server/srvtest"
)

// stamped prints 20 lines per 100 ms (200 lines/s) for secs seconds; every line is
// "<emit time> <n>" so a follower can compute line -> follower latency (same host clock).
func stamped(secs int) string {
	return fmt.Sprintf(`n=0; for s in $(seq 1 %d); do for j in $(seq 1 20); do n=$((n+1)); echo "$EPOCHREALTIME $n"; done; sleep 0.1; done`, secs*10)
}

// emitTime parses the stamp of a stamped line.
func emitTime(text string) (time.Time, bool) {
	f, _, ok := strings.Cut(text, " ")
	if !ok {
		return time.Time{}, false
	}
	sec, frac, _ := strings.Cut(f, ".")
	s, err1 := strconv.ParseInt(sec, 10, 64)
	us, err2 := strconv.ParseInt((frac + "000000")[:6], 10, 64)
	if err1 != nil || err2 != nil {
		return time.Time{}, false
	}
	return time.Unix(s, us*1000), true
}

var errEnded = errors.New("follow stream ended before done")

// drain collects follower messages until done or the context ends.
func drain(ctx context.Context, f *srvtest.Follower, st *srvtest.Stream, onMsg func(srvtest.FollowMsg)) error {
	for {
		select {
		case m, ok := <-f.Msgs:
			if !ok {
				return errEnded
			}
			if m.Err != nil {
				return m.Err
			}
			st.Add(m)
			if onMsg != nil {
				onMsg(m)
			}
			if m.Done {
				return nil
			}
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// TestRLOG002LivePull: 20 builds x 200 lines/s on 20 workers, 3 orchestrators.
//  1. with 0 followers the workers send <= 1 KiB/s each to the orchestrators;
//  2. 50 followers of one build spread over the 3 orchestrators see every line within p99 <= 1 s;
//  3. cutting the worker's Attach stream to the follower's orchestrator stalls the follower <= 3 s
//     (lake fallback), with 0 gaps and 0 duplicates.
func TestRLOG002LivePull(t *testing.T) {
	parity.Covers(t, "R-LOG-002")
	const builds, secs, followers = 20, 150, 50
	e := srvtest.Start(t, srvtest.Options{Orchestrators: 3, Workers: builds, Env: fastFleet(nil)})
	if e == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	pkgs := make([]srvtest.Pkg, builds)
	for i := range pkgs {
		pkgs[i] = srvtest.Pkg{Name: "l" + strconv.Itoa(i), Cmd: stamped(secs)}
	}
	repo := srvtest.Repo(t, pkgs)
	ids := make([]string, builds)
	var wg sync.WaitGroup
	for i := range pkgs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			id, err := e.Submit(repo, "//"+pkgs[i].Name+":t")
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
	label := func(i int) string { return "//" + pkgs[i].Name + ":t" }
	if why := srvtest.Eventually(90*time.Second, func() string {
		for i, id := range ids {
			if ls, err := e.Log(ctx, id, label(i), 0); err != nil || len(ls) == 0 {
				return "build " + strconv.Itoa(i) + " has no log yet"
			}
		}
		return ""
	}); why != "" {
		// persisted lines may lag by log.flush_interval: fall back to job state
		if why := srvtest.Eventually(60*time.Second, func() string {
			for _, id := range ids {
				js, err := e.Jobs(ctx, id)
				running := false
				for _, j := range js {
					running = running || j.State == "JOB_STATE_RUNNING" && j.Kind != "JOB_KIND_EVAL"
				}
				if err != nil || !running {
					return "not running: " + id
				}
			}
			return ""
		}); why != "" {
			t.Fatalf("the builds did not start running: %s", why)
		}
	}

	// 1. no followers: Hello/SlotState/Pong only
	if !e.AttachProxy(false, -1) { // red when the devstack has no attach proxy
		return
	}
	b0 := e.AttachBytes()
	time.Sleep(20 * time.Second)
	perWorker := float64(e.AttachBytes()-b0) / 20 / builds
	if perWorker > 1024 {
		t.Errorf("0 followers: workers send %.0f bytes/s each to the orchestrators, want <= 1024", perWorker)
	}

	// 2. 50 followers of build 0 over 3 orchestrators
	type result struct {
		st  *srvtest.Stream
		lat []time.Duration
		err error
	}
	res := make([]*result, followers)
	connected := time.Now()
	for i := range res {
		res[i] = &result{st: srvtest.NewStream()}
		wg.Add(1)
		go func() {
			defer wg.Done()
			r := res[i]
			f := e.Via(i%3).Follow(ids[0], label(0))
			r.err = drain(ctx, f, r.st, func(m srvtest.FollowMsg) {
				for _, l := range m.Lines {
					if at, ok := emitTime(l.Text); ok && at.After(connected.Add(5*time.Second)) {
						r.lat = append(r.lat, m.At.Sub(at))
					}
				}
			})
		}()
	}

	// 3. while the 50 followers run, a separate follower of build 1 on orchestrator 0 loses
	// its worker's Attach stream
	f1 := e.Via(0).Follow(ids[1], label(1))
	st1 := srvtest.NewStream()
	var gapMu sync.Mutex
	var blockedAt time.Time
	var recovered time.Duration
	wg.Add(1)
	go func() {
		defer wg.Done()
		_ = drain(ctx, f1, st1, func(m srvtest.FollowMsg) {
			gapMu.Lock()
			defer gapMu.Unlock()
			if !blockedAt.IsZero() && recovered == 0 && (len(m.Lines) > 0 || m.Delayed) {
				recovered = m.At.Sub(blockedAt)
			}
		})
	}()
	time.Sleep(10 * time.Second)
	gapMu.Lock()
	blockedAt = time.Now()
	gapMu.Unlock()
	e.AttachProxy(true, 0)
	time.Sleep(15 * time.Second)
	e.AttachProxy(false, 0)
	wg.Wait()

	var all []time.Duration
	for i, r := range res {
		if r.err != nil {
			t.Errorf("follower %d: %v", i, r.err)
		}
		for _, s := range r.st.Errs {
			t.Errorf("follower %d: %s", i, s)
		}
		all = append(all, r.lat...)
	}
	if p99 := srvtest.Percentile(all, 0.99); len(all) == 0 || p99 > time.Second {
		t.Errorf("line -> follower p99 = %v over %d lines, want <= 1 s", p99, len(all))
	}
	gapMu.Lock()
	defer gapMu.Unlock()
	if recovered == 0 || recovered > 3*time.Second {
		t.Errorf("the follower stalled %v after its worker's Attach stream was cut, want <= 3 s (lake fallback)", recovered)
	}
	for _, s := range st1.Errs {
		t.Errorf("follower after the cut: %s", s)
	}
	if st, err := e.WaitBuild(ids[1], 4*time.Minute); err != nil || st != "BUILD_STATE_SUCCEEDED" {
		t.Fatalf("build %s: %q %v", ids[1], st, err)
	}
	lake, err := e.Log(ctx, ids[1], label(1), 0)
	if err != nil {
		t.Fatal(err)
	}
	got := st1.Lines[label(1)]
	if len(got) != len(lake) {
		t.Fatalf("follower has %d lines, the lake has %d", len(got), len(lake))
	}
	for i := range got {
		if got[i].Seq != lake[i].Seq || got[i].Text != lake[i].Text {
			t.Fatalf("follower line %d is %d %q, the lake has %d %q", i, got[i].Seq, got[i].Text, lake[i].Seq, lake[i].Text)
		}
	}
	e.AssertNoList()
}
