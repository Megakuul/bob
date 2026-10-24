//go:build slow

package fleet

import (
	"testing"
	"time"

	"github.com/megakuul/bob/tests/parity"
	"github.com/megakuul/bob/tests/serverenv/s3count"
	"github.com/megakuul/bob/tests/store/env"
)

// TestRFLEET010IdleFleetCost is the SPEC run (HARNESS.md §14 #36 style, `-tags slow`): 100
// in-process idle workers for 10 minutes cost at most 15 req/s in total, at most 7 PUT/s
// (the heartbeats), and no LIST.
func TestRFLEET010IdleFleetCost(t *testing.T) {
	parity.Covers(t, "R-FLEET-010")
	l := newLab(t)
	if l == nil {
		return
	}
	if !idleFleet(l, 100) {
		return
	}
	time.Sleep(30 * time.Second)
	l.be.Proxy.Reset()
	const window = 10 * time.Minute
	time.Sleep(window)
	c := l.be.Proxy.Counts()
	secs := window.Seconds()
	total := float64(env.Total(c, l.fname)+env.Total(c, l.cname)) / secs
	puts := float64(env.Ops(c, l.fname, s3count.OpPut)) / secs
	if total > 15 {
		t.Errorf("100 idle workers made %.2f req/s, want <= 15", total)
	}
	if puts > 7 {
		t.Errorf("%.2f PUT/s, want <= 7", puts)
	}
	l.be.Proxy.AssertNoList(t, l.fname, l.cname)
}
