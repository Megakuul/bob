//go:build slow

package soak

import (
	"context"
	"fmt"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/megakuul/bob/tests/parity"
	"github.com/megakuul/bob/tests/server/srvtest"
)

// leaders returns who holds ctl/lake-maintenance according to the orchestrators' leader_of.
func leaders(ctx context.Context, e *srvtest.Env) []string {
	os, err := e.Orchestrators(ctx)
	if err != nil {
		return nil
	}
	var out []string
	for _, o := range os {
		if slices.Contains(o.LeaderOf, "lake-maintenance") {
			out = append(out, o.ID)
		}
	}
	return out
}

// TestRLOG100LakeWorkarounds is the end-to-end form of the S10 L3 (two compactors) and L4 (read
// during compaction) repros: log partitions are written with many small commits by two workers,
// two orchestrators run the lake-maintenance controller, and readers on both orchestrators query
// continuously while the partitions age past 10 min and are compacted. The rules hold when
// exactly one orchestrator compacts at any time, no query fails, and every log reads back as
// seq 1..n exactly once, also after the compactor is killed mid-run and the lease moves.
func TestRLOG100LakeWorkarounds(t *testing.T) {
	parity.Covers(t, "R-LOG-100")
	e := srvtest.Start(t, srvtest.Options{Orchestrators: 2, Workers: 2, Env: map[string]string{
		"BOB_LOG_FLUSH_INTERVAL": "1s", "BOB_WORKER_SLOTS": "4", "BOB_FLEET_POLL_INTERVAL": "2s",
		"BOB_FLEET_HEARTBEAT": "2s", "BOB_FLEET_LEASE_TTL": "10s",
	}})
	if e == nil {
		return
	}
	const nBuilds, nLines = 8, 600
	pkgs := make([]srvtest.Pkg, nBuilds)
	for i := range pkgs {
		pkgs[i] = srvtest.Pkg{Name: fmt.Sprintf("c%d", i), Cmd: fmt.Sprintf(`for i in $(seq 1 %d); do echo $i; sleep 0.1; done`, nLines)}
	}
	repo := srvtest.Repo(t, pkgs)
	ids := make([]string, nBuilds)
	for i := range pkgs {
		id, err := e.Submit(repo, "//"+pkgs[i].Name+":t")
		if err != nil {
			t.Fatal(err)
		}
		ids[i] = id
	}
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Minute)
	defer cancel()
	var failed, queries atomic.Int64
	var cur atomic.Int64 // the orchestrator the readers query: always one that is alive
	var wg sync.WaitGroup
	stop := make(chan struct{})
	for r := range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for n := 0; ; n++ {
				select {
				case <-stop:
					return
				case <-time.After(300 * time.Millisecond):
				}
				i := (n + r) % nBuilds
				queries.Add(1)
				if _, err := e.Via(int(cur.Load())).Log(ctx, ids[i], "//"+pkgs[i].Name+":t", 0); err != nil {
					failed.Add(1)
					t.Errorf("query failed during compaction: %v", err)
				}
			}
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			case <-time.After(time.Second):
			}
			if l := leaders(ctx, e.Via(int(cur.Load()))); len(l) > 1 {
				t.Errorf("two compactors at once: %v hold ctl/lake-maintenance", l)
			}
		}
	}()
	for _, id := range ids {
		if st, err := e.WaitBuild(id, 10*time.Minute); err != nil || st != "BUILD_STATE_SUCCEEDED" {
			t.Fatalf("build %s: %q %v", id, st, err)
		}
	}
	// the partitions were written minutes ago; compaction needs them older than 10 min
	time.Sleep(11 * time.Minute)
	if l := leaders(ctx, e); len(l) != 1 {
		t.Errorf("lake-maintenance has %d leaders after 11 min, want exactly 1: %v", len(l), l)
	}
	// kill each orchestrator in turn: the lease holder dies mid-compaction and the lease moves
	for _, victim := range []int{0, 1} {
		cur.Store(int64(1 - victim))
		name := fmt.Sprintf("orchestrator-%d", victim)
		if err := e.Kill(name); err != nil {
			t.Fatal(err)
		}
		time.Sleep(90 * time.Second)
		if l := leaders(ctx, e.Via(1-victim)); len(l) != 1 {
			t.Errorf("after killing %s lake-maintenance has %d leaders, want 1", name, len(l))
		}
		if err := e.Restart(name); err != nil {
			t.Fatal(err)
		}
		time.Sleep(30 * time.Second)
	}
	close(stop)
	wg.Wait()
	if failed.Load() != 0 {
		t.Errorf("%d of %d queries failed", failed.Load(), queries.Load())
	}
	for i, id := range ids {
		ls, err := e.Log(ctx, id, "//"+pkgs[i].Name+":t", 0)
		if err != nil {
			t.Errorf("log %d: %v", i, err)
			continue
		}
		check(t, i, ls, nLines)
	}
	e.AssertNoList()
}

func check(t *testing.T, i int, ls []srvtest.Line, n int) {
	t.Helper()
	if len(ls) != n {
		t.Errorf("log %d: %d lines after compaction, want %d (duplicates or losses)", i, len(ls), n)
		return
	}
	for j, l := range ls {
		if int64(l.Seq) != int64(j+1) {
			t.Errorf("log %d: position %d has seq %d", i, j, l.Seq)
			return
		}
	}
}
