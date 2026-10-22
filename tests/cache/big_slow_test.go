//go:build slow

package cache

import (
	"bufio"
	"flag"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/megakuul/bob/tests/harness"
	"github.com/megakuul/bob/tests/parity"
	"github.com/megakuul/bob/tests/serverenv/clients"
	"github.com/megakuul/bob/tests/serverenv/regenv"
)

// These tests need fixture tests/fixtures/repos/bigclosure: a release "big" 1.0.0 whose
// closure holds one incompressible output of about 1.1 GiB (owner: fixtures). They run in the
// slow suite (go test -tags slow -timeout 3h ./tests/cache).

var chaosSeed = flag.Uint64("chaos-seed", 1, "seed of the random SIGKILL points")

// bigDir is a committed checkout of fixture "bigclosure": release "big" 1.0.0 whose closure has
// one incompressible output of about 1.1 GiB (needs tests/fixtures/repos/bigclosure; owner: fixtures).
func bigDir(t testing.TB) string { return harness.Fixture(t, "bigclosure") }

// bigRelease publishes the big fixture into a fresh stack and returns the release, the path
// of its biggest NAR and the NAR size.
func bigRelease(t *testing.T, e *regenv.Env) (string, string, int64) {
	t.Helper()
	e.Project("acme", "demo")
	dir := bigDir(t)
	e.Push(dir, "demo", "main")
	e.Tag(dir, "demo", "v1.0.0")
	rel := e.WaitRelease("acme", "big", "1.0.0", 45*time.Minute)
	var best, bestPath string
	var bestSize int64
	for _, root := range roots(t, rel) {
		u, size := narURL(t, e, root, e.Admin)
		if size > bestSize {
			best, bestPath, bestSize = u, root, size
		}
	}
	if bestSize < 1<<30 {
		t.Fatalf("the biggest NAR of release %s has %d bytes, the acceptance needs a 1 GiB NAR", rel.ID, bestSize)
	}
	return best, bestPath, bestSize
}

func rssKiB(pid int) int {
	f, err := os.Open("/proc/" + strconv.Itoa(pid) + "/status")
	if err != nil {
		return 0
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if v, ok := strings.CutPrefix(sc.Text(), "VmRSS:"); ok {
			n, _ := strconv.Atoi(strings.Fields(v)[0])
			return n
		}
	}
	return 0
}

// TestRREG151BigNARStreaming covers (1) bounded memory under 10 parallel 1 GiB downloads and
// (2) resumption after a cut connection.
func TestRREG151BigNARStreaming(t *testing.T) {
	parity.Covers(t, "R-REG-151")
	e := regenv.Up(t, 1)
	if e == nil {
		return
	}
	nar, root, size := bigRelease(t, e)
	pid := e.OrchestratorPID(0)

	// (1) 10 parallel downloads, orchestrator RSS <= 64 MiB (sampled every 50 ms)
	var peak atomic.Int64
	stop := make(chan struct{})
	var sampler sync.WaitGroup
	sampler.Add(1)
	go func() {
		defer sampler.Done()
		for {
			select {
			case <-stop:
				return
			case <-time.After(50 * time.Millisecond):
				if v := int64(rssKiB(pid)); v > peak.Load() {
					peak.Store(v)
				}
			}
		}
	}()
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req, _ := http.NewRequest(http.MethodGet, nar, nil)
			req.SetBasicAuth("bob", e.Admin)
			r, err := e.HTTP.Do(req)
			if err != nil {
				t.Errorf("download: %v", err)
				return
			}
			defer r.Body.Close()
			if n, err := io.Copy(io.Discard, r.Body); err != nil || n != size {
				t.Errorf("download: %d of %d bytes: %v", n, size, err)
			}
		}()
	}
	wg.Wait()
	close(stop)
	sampler.Wait()
	if mib := float64(peak.Load()) / 1024; mib > 64 {
		t.Errorf("(1) orchestrator RSS peaked at %.1f MiB during 10 parallel 1 GiB downloads, want <= 64 MiB", mib)
	}

	// (2) the connection is cut at 10,000,000 bytes; Nix resumes with Range and the path verifies
	keys := e.PublicKeys()
	cut := newCutProxy(t, e.API, 10_000_000)
	n := clients.Nix(t)
	l := newLocal(n)
	if r := l.copyFrom(cut.URL+"/nix", regenv.Netrc(t, n, cut.URL, e.Admin), keys, root); r.Exit != 0 {
		t.Fatalf("(2) nix copy through the cutting proxy: exit %d: %s", r.Exit, r.Combined())
	}
	if cut.cuts.Load() == 0 {
		t.Errorf("(2) the proxy never cut a NAR transfer")
	}
	if got := cut.resumedFrom.Load(); got < 10_000_000 {
		t.Errorf("(2) Nix resumed from byte %d (0 = no Range request), want >= 10,000,000", got)
	}
	if v := l.verify(keys, root); v.Exit != 0 {
		t.Errorf("(2) the resumed path does not verify: %s", v.Combined())
	}
	e.AssertNoList()
}

// TestRREG152KilledPromotion kills an orchestrator at a random moment of the promotion of a
// 1.1 GiB job, 20 times (fresh stack each run, kill point from -chaos-seed): never a narinfo
// whose reference is 404, and the surviving orchestrator completes the promotion.
func TestRREG152KilledPromotion(t *testing.T) {
	parity.Covers(t, "R-REG-152")
	for run := 0; run < 20; run++ {
		t.Run(fmt.Sprintf("run%02d", run), func(t *testing.T) {
			rng := rand.New(rand.NewPCG(*chaosSeed, uint64(run)))
			e := regenv.Up(t, 2)
			if e == nil {
				return
			}
			e.Project("acme", "demo")
			e.DelayStaging(2 * time.Second) // widen the promotion window to a few seconds
			dir := bigDir(t)
			e.Push(dir, "demo", "main")
			e.Tag(dir, "demo", "v1.0.0")
			awaitStaged(t, e)
			delay := time.Duration(rng.IntN(8000)) * time.Millisecond
			t.Logf("seed %d run %d: SIGKILL orchestrator-0 %v after COMPLETE.json", *chaosSeed, run, delay)
			time.Sleep(delay)
			e.KillOrchestrator(0)
			assertNoDanglingReferences(t, e)
			rel := e.WaitRelease("acme", "big", "1.0.0", 30*time.Minute) // a re-run completes
			assertNoDanglingReferences(t, e)
			n := clients.Nix(t)
			l := newLocal(n)
			keys := e.PublicKeys()
			base := e.APIs[len(e.APIs)-1]
			root := roots(t, rel)[0]
			if r := l.copyFrom(base+"/nix", regenv.Netrc(t, n, base, e.Admin), keys, root); r.Exit != 0 {
				t.Fatalf("nix copy after the kill: %s", r.Combined())
			}
			if v := l.verify(keys, l.closure(t, root)...); v.Exit != 0 {
				t.Errorf("nix store verify after the kill: %s", v.Combined())
			}
		})
	}
}

// assertNoDanglingReferences reads every served narinfo straight from S3 and checks that all
// its references have a narinfo and a NAR (the invariant of SPEC 6.6.4).
func assertNoDanglingReferences(t *testing.T, e *regenv.Env) {
	t.Helper()
	have := map[string]bool{}
	for _, k := range e.S3List("nix/narinfo/") {
		have[hashOfKey(k)] = true
	}
	for _, k := range e.S3List("nix/narinfo/") {
		_, body := e.S3Do(e.S3Creds("root"), http.MethodGet, artifacts, k, nil)
		for _, ref := range strings.Fields(refsLine(string(body))) {
			if h := strings.SplitN(ref, "-", 2)[0]; !have[h] {
				t.Errorf("narinfo %s references %s, which has no narinfo", k, ref)
			}
		}
	}
}
