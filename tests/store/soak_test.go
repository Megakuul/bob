//go:build slow

package store

import (
	"testing"
	"time"

	"github.com/megakuul/bob/tests/parity"
	"github.com/megakuul/bob/tests/serverenv/s3count"
	"github.com/megakuul/bob/tests/store/env"
)

// TestRSTORE101TwoHourSweepRuns is the soak half of R-STORE-101 (HARNESS.md §14 #36, run with
// `go test -tags slow ./tests/store -run TwoHour -timeout 3h`): over 2 h there are at most 2
// sweep runs per controller and no LIST from any other code. A "run" is a burst of LISTs
// attributed to one controller; bursts are separated by at least 60 s of silence.
func TestRSTORE101TwoHourSweepRuns(t *testing.T) {
	parity.Covers(t, "R-STORE-101")
	s, _ := startE2E(t)
	if s == nil {
		return
	}
	const gap = 60 * time.Second
	lists := func() map[string]int {
		m := map[string]int{}
		for _, r := range env.ProxyCounts(t, s).Rows {
			if r.Op == s3count.OpList {
				m[r.Sweep] += r.Count
			}
		}
		return m
	}
	last := map[string]int{}
	lastChange := map[string]time.Time{}
	runs := map[string]int{}
	end := time.Now().Add(2 * time.Hour)
	for time.Now().Before(end) {
		time.Sleep(5 * time.Second)
		now := time.Now()
		for ctl, n := range lists() {
			if n == last[ctl] {
				continue
			}
			if last[ctl] == 0 || now.Sub(lastChange[ctl]) > gap {
				runs[ctl]++
			}
			last[ctl], lastChange[ctl] = n, now
		}
	}
	for ctl, n := range runs {
		if ctl == "" {
			t.Errorf("%d bursts of LIST requests came from code that is not a sweep controller", n)
			continue
		}
		if n > 2 {
			t.Errorf("controller %s ran %d sweeps in 2 h, want <= 2", ctl, n)
		}
	}
}
