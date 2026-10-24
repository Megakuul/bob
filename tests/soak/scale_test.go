//go:build slow

package soak

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/megakuul/bob/tests/parity"
	"github.com/megakuul/bob/tests/server/srvtest"
)

// TestRNFR005RunningBuildsLimit is the "running builds" row of R-NFR-005: 2,000 builds run at the same
// time; build number 2,001 is refused with an error that names the limit.
//
// The other three rows (5,000 projects, 1,000 workers, 1,024 jobs per build) are not covered: projects
// need a forge integration per repository, 1,000 workers need 1,000 registered worker processes, and the
// job count of a build depends on the batching planner (S14), so "exactly 1,024 jobs" cannot be built
// from outside. They are listed in the M1.8 report.
func TestRNFR005RunningBuildsLimit(t *testing.T) {
	parity.Covers(t, "R-NFR-005")
	const limit = 2000
	e := srvtest.Start(t, srvtest.Options{Orchestrators: 2, Workers: 2, Env: map[string]string{
		"BOB_WORKER_SLOTS": "1100", "BOB_FLEET_POLL_INTERVAL": "2s",
	}})
	if e == nil {
		return
	}
	ps := make([]srvtest.Pkg, limit+1)
	for i := range ps {
		ps[i] = srvtest.Pkg{Name: "r" + strconv.Itoa(i), Cmd: "sleep 3000"}
	}
	repo := srvtest.Repo(t, ps)
	ids := make([]string, limit)
	sem := make(chan struct{}, 32)
	var wg sync.WaitGroup
	for i := range limit {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			id, err := e.Submit(repo, "//"+ps[i].Name+":t")
			if err != nil {
				t.Errorf("build %d of %d (at the limit) was refused: %v", i+1, limit, err)
			}
			ids[i] = id
		}()
	}
	wg.Wait()
	if t.Failed() {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	if why := srvtest.Eventually(20*time.Minute, func() string {
		n := 0
		for _, id := range ids {
			if b, err := e.GetBuild(ctx, id); err == nil && b.State == "BUILD_STATE_RUNNING" {
				n++
			}
		}
		if n < limit {
			return fmt.Sprintf("%d of %d builds running", n, limit)
		}
		return ""
	}); why != "" {
		t.Fatalf("2,000 running builds not reached: %s", why)
	}
	_, err := e.Submit(repo, "//"+ps[limit].Name+":t")
	if err == nil {
		t.Fatalf("build 2,001 was accepted while %d builds run", limit)
	}
	if m := err.Error(); !strings.Contains(m, "2000") && !strings.Contains(m, "2,000") {
		t.Errorf("the refusal does not name the limit of 2,000 running builds: %v", err)
	}
	e.AssertNoList()
}
