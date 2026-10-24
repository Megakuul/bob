package server

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/megakuul/bob/tests/parity"
	"github.com/megakuul/bob/tests/server/srvtest"
)

// TestRLOG003Attribution: GetLog(build, package) returns only the lines of that package; the failing
// package view of a 50-package build shows the lines of that one package; GetLog(build) is ordered by
// (package, seq). The p99 <= 500 ms row is measured here on the build's own data; the S10 100M-line
// data set is not reachable through the product path (see the M1.8 report, SPEC problems).
func TestRLOG003Attribution(t *testing.T) {
	parity.Covers(t, "R-LOG-003")
	const pkgs, per, failing = 50, 20, 7
	e := srvtest.Start(t, srvtest.Options{Orchestrators: 1, Workers: 2, Env: fastFleet(map[string]string{"BOB_WORKER_SLOTS": "8"})})
	if e == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	ps := make([]srvtest.Pkg, pkgs)
	for i := range ps {
		cmd := fmt.Sprintf(`for k in $(seq 1 %d); do echo "pkg%d line $k"; done`, per, i)
		if i == failing {
			cmd += "; exit 1"
		}
		ps[i] = srvtest.Pkg{Name: fmt.Sprintf("a%d", i), Cmd: cmd}
	}
	id, err := e.Submit(srvtest.Repo(t, ps), "//...")
	if err != nil {
		t.Fatal(err)
	}
	if st, err := e.WaitBuild(id, 3*time.Minute); err != nil || st != "BUILD_STATE_FAILED" {
		t.Fatalf("build: state %q err %v, want FAILED (package a%d fails)", st, err, failing)
	}
	for i := range ps {
		label := fmt.Sprintf("//a%d:t", i)
		var ls []srvtest.Line
		if why := srvtest.Eventually(60*time.Second, func() string { // job end commits the package's lines
			var err error
			if ls, err = e.Log(ctx, id, label, 0); err != nil {
				return err.Error()
			}
			if len(ls) < per {
				return fmt.Sprintf("%d lines", len(ls))
			}
			return ""
		}); why != "" {
			t.Errorf("%s: %s", label, why)
			continue
		}
		prefix := fmt.Sprintf("pkg%d line ", i)
		for _, l := range ls {
			if l.Package != label || !strings.HasPrefix(l.Text, prefix) {
				t.Errorf("GetLog(%s) returned a line of another package: %s %q", label, l.Package, l.Text)
				break
			}
		}
		for k := 0; k < per && k < len(ls); k++ {
			if want := fmt.Sprintf("%s%d", prefix, k+1); ls[k].Text != want || int64(ls[k].Seq) != int64(k+1) {
				t.Errorf("GetLog(%s) line %d is seq %d %q, want %q", label, k+1, ls[k].Seq, ls[k].Text, want)
				break
			}
		}
	}
	// the whole build: ordered by (package, seq), every package complete
	all, err := e.Log(ctx, id, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if !sort.SliceIsSorted(all, func(i, j int) bool {
		if all[i].Package != all[j].Package {
			return all[i].Package < all[j].Package
		}
		return all[i].Seq < all[j].Seq
	}) {
		t.Errorf("GetLog(build) is not ordered by (package, seq)")
	}
	if len(all) < pkgs*per {
		t.Errorf("GetLog(build) has %d lines, want at least %d", len(all), pkgs*per)
	}
	// latency of the failing-package view
	var lat []time.Duration
	for range 100 {
		t0 := time.Now()
		if _, err := e.Log(ctx, id, fmt.Sprintf("//a%d:t", failing), 0); err != nil {
			t.Fatal(err)
		}
		lat = append(lat, time.Since(t0))
	}
	if p99 := srvtest.Percentile(lat, 0.99); p99 > 500*time.Millisecond {
		t.Errorf("GetLog(failing package) p99 = %v, want <= 500 ms", p99)
	}
	e.AssertNoList()
}
