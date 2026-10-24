package server

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/megakuul/bob/tests/parity"
	"github.com/megakuul/bob/tests/server/srvtest"
	"github.com/megakuul/bob/tests/serverenv/s3count"
)

const mergeFlush = 3 * time.Second // log.flush_interval of the matrix runs

// stampedShort prints 50 lines/s for 6 s: "<emit time> <n>".
const stampedShort = `n=0; for s in $(seq 1 60); do for j in 1 2 3 4 5; do n=$((n+1)); echo "$EPOCHREALTIME $n"; done; sleep 0.1; done`

// followAll follows one package to its end like a client would: on a broken stream it
// reconnects to the next orchestrator with its cursor.
func followAll(ctx context.Context, e *srvtest.Env, order []int, id, label string, st *srvtest.Stream, onMsg func(srvtest.FollowMsg)) error {
	var err error
	for try := 0; try < 60; try++ {
		seq, att := st.Cursor(label)
		f := e.Via(order[try%len(order)]).FollowFrom(id, label, seq, att)
		err = drain(ctx, f, st, onMsg)
		f.Close()
		if err == nil {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		time.Sleep(300 * time.Millisecond)
	}
	return err
}

type failure struct {
	name   string
	follow []int // orchestrators the follower tries, in order
	inject func(e *srvtest.Env) (recover func())
}

// matrixRun is one run of the S17 failure matrix with pull instead of push: one build, one follower, one
// failure injected 1.5 s (early) or 3.5 s (late) after the first line, then removed. 0 gaps, 0 duplicates, the
// final stream equals the lake content, and lines that arrived while degraded (delayed) are at most
// log.flush_interval + 10 s old.
func matrixRun(t *testing.T, e *srvtest.Env, f failure, delay time.Duration, n int) {
	repo := srvtest.Repo(t, []srvtest.Pkg{{Name: fmt.Sprintf("m%d", n), Cmd: stampedShort}})
	label := fmt.Sprintf("//m%d:t", n)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	e.SetFaults(s3count.Script{})
	id, err := e.Submit(repo, label)
	if err != nil {
		t.Fatal(err)
	}
	st := srvtest.NewStream()
	started := make(chan struct{}, 1)
	var degraded []time.Duration
	done := make(chan error, 1)
	go func() {
		done <- followAll(ctx, e, f.follow, id, label, st, func(m srvtest.FollowMsg) {
			select {
			case started <- struct{}{}:
			default:
			}
			if m.Delayed {
				for _, l := range m.Lines {
					if at, ok := emitTime(l.Text); ok {
						degraded = append(degraded, m.At.Sub(at))
					}
				}
			}
		})
	}()
	select {
	case <-started:
	case <-time.After(90 * time.Second):
		t.Fatal("the follower got no message within 90 s")
	}
	time.Sleep(delay)
	recoverFn := f.inject(e)
	time.Sleep(3 * time.Second)
	recoverFn()
	if err := <-done; err != nil {
		t.Fatalf("follower: %v", err)
	}
	for _, s := range st.Errs {
		t.Errorf("%s", s)
	}
	state, err := e.WaitBuild(id, 90*time.Second)
	if err != nil || state != "BUILD_STATE_SUCCEEDED" {
		t.Fatalf("build: %q %v", state, err)
	}
	lake, err := e.Log(ctx, id, label, 0)
	if err != nil {
		t.Fatal(err)
	}
	got := st.Lines[label]
	if len(got) != len(lake) {
		t.Fatalf("final stream has %d lines, the lake %d", len(got), len(lake))
	}
	for i := range got {
		if got[i].Seq != lake[i].Seq || got[i].Text != lake[i].Text {
			t.Fatalf("final stream differs from the lake at %d: %d %q vs %d %q", i, got[i].Seq, got[i].Text, lake[i].Seq, lake[i].Text)
		}
	}
	if p99 := srvtest.Percentile(degraded, 0.99); p99 > mergeFlush+10*time.Second {
		t.Errorf("degraded latency p99 = %v, want <= flush_interval + 10 s = %v", p99, mergeFlush+10*time.Second)
	}
}

func runMatrix(t *testing.T, e *srvtest.Env, fs []failure) {
	n := 0
	for _, f := range fs {
		for _, ph := range []struct {
			name  string
			delay time.Duration
		}{{"early", 1500 * time.Millisecond}, {"late", 3500 * time.Millisecond}} {
			n++
			t.Run(f.name+"/"+ph.name, func(t *testing.T) { matrixRun(t, e, f, ph.delay, n) })
		}
	}
	e.AssertNoList()
}

// TestRLOG101MatrixLive covers the failures of the live path (6 of the 12 runs): the worker's Attach
// stream to the follower's orchestrator is cut, every Attach stream is cut (degraded: the lake is
// read every 5 s and the stream says delayed), and lake commits fail (live view unaffected).
func TestRLOG101MatrixLive(t *testing.T) {
	parity.Covers(t, "R-LOG-101")
	e := srvtest.Start(t, srvtest.Options{Orchestrators: 3, Workers: 2,
		Env: fastFleet(map[string]string{"BOB_LOG_FLUSH_INTERVAL": "3s"})})
	if e == nil {
		return
	}
	if !e.AttachProxy(false, -1) {
		return
	}
	runMatrix(t, e, []failure{
		{"attach-cut", []int{0}, func(e *srvtest.Env) func() {
			e.AttachProxy(true, 0)
			return func() { e.AttachProxy(false, 0) }
		}},
		{"all-live-paths-down", []int{0}, func(e *srvtest.Env) func() {
			e.AttachProxy(true, -1)
			return func() { e.AttachProxy(false, -1) }
		}},
		{"lake-commit-failure", []int{0}, func(e *srvtest.Env) func() {
			e.SetFaults(s3count.Script{Seed: 1, Faults: []s3count.Fault{{Kind: s3count.Fault503, Bucket: "bob-analytics", Op: s3count.OpPut}}})
			return func() { e.SetFaults(s3count.Script{}) }
		}},
	})
}

// TestRLOG101MatrixProcess covers the process failures (the other 6 runs): the follower's orchestrator is
// killed (the client reconnects to another one with its cursor), the worker running the job is killed
// (retry with a Restart marker), and an orchestrator without followers is killed and restarted.
func TestRLOG101MatrixProcess(t *testing.T) {
	parity.Covers(t, "R-LOG-101")
	e := srvtest.Start(t, srvtest.Options{Orchestrators: 3, Workers: 1,
		Env: fastFleet(map[string]string{"BOB_LOG_FLUSH_INTERVAL": "3s"})})
	if e == nil {
		return
	}
	bounce := func(name string) func() {
		if err := e.Kill(name); err != nil {
			t.Error(err)
		}
		return func() {
			if err := e.Restart(name); err != nil {
				t.Error(err)
			}
		}
	}
	runMatrix(t, e, []failure{
		{"follower-orchestrator-killed", []int{0, 1, 2}, func(e *srvtest.Env) func() { return bounce("orchestrator-0") }},
		{"worker-killed", []int{1}, func(e *srvtest.Env) func() { return bounce("worker-0") }},
		{"orchestrator-restart", []int{1}, func(e *srvtest.Env) func() { return bounce("orchestrator-2") }},
	})
}
