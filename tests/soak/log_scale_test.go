//go:build slow

package soak

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

// counter prints n numbered lines, 20 per 100 ms (200 lines/s).
func counter(n int) string {
	return fmt.Sprintf(`n=0; for s in $(seq 1 %d); do for j in $(seq 1 20); do n=$((n+1)); echo $n; done; sleep 0.1; done`, n/20)
}

// TestRLOG001Scale is the SPEC acceptance of R-LOG-001: 50 workers x 200 lines/s for 5 min
// on RustFS; afterwards every line committed before each worker's last commit is readable
// exactly once with the right text (all lines, since every job ends with a commit), 0 lost.
func TestRLOG001Scale(t *testing.T) {
	parity.Covers(t, "R-LOG-001")
	const workers, lines = 50, 60000 // 200 lines/s for 300 s
	e := srvtest.Start(t, srvtest.Options{Orchestrators: 1, Workers: workers, Env: map[string]string{
		"BOB_WORKER_SLOTS": "1", "BOB_FLEET_POLL_INTERVAL": "2s",
	}})
	if e == nil {
		return
	}
	pkgs := make([]srvtest.Pkg, workers)
	for i := range pkgs {
		pkgs[i] = srvtest.Pkg{Name: "w" + strconv.Itoa(i), Cmd: counter(lines)}
	}
	repo := srvtest.Repo(t, pkgs)
	ids := make([]string, workers)
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
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()
	for i, id := range ids {
		if st, err := e.WaitBuild(id, 15*time.Minute); err != nil || st != "BUILD_STATE_SUCCEEDED" {
			t.Fatalf("build %d (%s): state %q err %v", i, id, st, err)
		}
	}
	for i, id := range ids {
		ls, err := e.Log(ctx, id, "//"+pkgs[i].Name+":t", 0)
		if err != nil {
			t.Fatal(err)
		}
		if len(ls) != lines {
			t.Errorf("worker %d: %d lines readable, want %d (lost or duplicated lines)", i, len(ls), lines)
			continue
		}
		for j, l := range ls {
			if int64(l.Seq) != int64(j+1) || l.Text != strconv.Itoa(j+1) {
				t.Errorf("worker %d: line %d is seq %d %q", i, j+1, l.Seq, l.Text)
				break
			}
		}
	}
	e.AssertNoList()
}
