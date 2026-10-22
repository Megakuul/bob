package store

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/megakuul/bob/tests/harness"
	"github.com/megakuul/bob/tests/parity"
	"github.com/megakuul/bob/tests/serverenv/s3count"
	"github.com/megakuul/bob/tests/store/env"
	"github.com/megakuul/bob/tools/devstack/stack"
)

// The tests of this file age the data by running one more orchestrator whose clock is shifted
// by BOB_TEST_CLOCK_OFFSET (a Go duration added to the server clock; Decision D-T5a-1). The
// hourly controllers (retention, staging-sweep, cache GC) run their first pass shortly after
// start, then at their interval (Decision D-T5a-2); the tests wait up to 90 s for the effect.

const passWait = 90 * time.Second

func keyCount(t testing.TB, s *stack.Stack, bucket, prefix string) int {
	return len(env.ListKeys(t, s, bucket, prefix))
}

func keysContaining(t testing.TB, s *stack.Stack, bucket, prefix, sub string) []string {
	var out []string
	for _, k := range env.ListKeys(t, s, bucket, prefix) {
		if strings.Contains(k, sub) {
			out = append(out, k)
		}
	}
	return out
}

// onlySweepsListed asserts the LIST accounting of R-STORE-007: LIST appears only on the
// analytics and artifacts buckets, only from sweep controllers.
func onlySweepsListed(t testing.TB, s *stack.Stack) {
	t.Helper()
	for _, r := range env.ProxyCounts(t, s).Rows {
		if r.Op != s3count.OpList {
			continue
		}
		if r.Sweep == "" || (r.Bucket != "bob-analytics" && r.Bucket != "bob-artifacts") {
			t.Errorf("LIST on %s prefix %q by %q (sweep %q) x%d: only the R-STORE-101 sweeps may list", r.Bucket, r.Prefix, r.Cred, r.Sweep, r.Count)
		}
	}
}

// remoteRelease builds the polyglot release //deploy:api as version v on the stack.
func remoteRelease(t *testing.T, x env.Extra, v string) {
	t.Helper()
	r := env.RemoteBuild(t, x, harness.Fixture(t, "polyglot"), "//deploy:api", "--version", v)
	if r.Exit != 0 {
		t.Fatalf("remote release build %s: exit %d\n%s", v, r.Exit, r.Combined())
	}
}

func swapOrchestrator(t *testing.T, s *stack.Stack, name, offset string) {
	t.Helper()
	for _, o := range s.Info.Orchestrators {
		_ = s.Kill(o.Name)
	}
	env.ExtraOrchestrator(t, s, name, "BOB_TEST_CLOCK_OFFSET="+offset)
}

// TestRSTORE105StagingCleanup: staged objects of promoted attempts are deleted by the
// promoting orchestrator within one promotion pass; abandoned staging is removed by the
// staging sweep once it is older than 7 days.
func TestRSTORE105StagingCleanup(t *testing.T) {
	parity.Covers(t, "R-STORE-105")
	s := env.StartStack(t, stack.Options{Orchestrators: 1, Workers: 3})
	if s == nil {
		return
	}
	x := env.MustExtra(t, s, "api", "admin")
	// An attempt that died before COMPLETE.json: nobody promotes or deletes it.
	env.PutRaw(t, s, "bob-artifacts", "staging/dead-build/dead-job/1/blob.nar.zst", []byte("abandoned"))
	// One worker is killed while builds run (the SPEC drain kills 20 %).
	done := make(chan harness.Result, 1)
	go func() { done <- env.RemoteBuild(t, x, harness.Fixture(t, "acme"), "//...") }()
	time.Sleep(2 * time.Second)
	_ = s.Kill("worker-0")
	r := <-done
	if r.Exit != 0 {
		t.Fatalf("remote build with a killed worker: exit %d\n%s", r.Exit, r.Combined())
	}
	// Within one promotion pass (10 s) + margin, nothing of a successful attempt is left.
	if !env.Wait(30*time.Second, func() bool {
		for _, k := range env.ListKeys(t, s, "bob-artifacts", "staging/") {
			if !strings.HasPrefix(k, "staging/dead-build/") {
				return false
			}
		}
		return true
	}) {
		t.Errorf("staged objects of successful attempts remain 30 s after the build: %v", env.ListKeys(t, s, "bob-artifacts", "staging/"))
	}
	if keyCount(t, s, "bob-artifacts", "staging/dead-build/") != 1 {
		t.Errorf("a pass before the 7-day mark deleted the abandoned attempt")
	}
	// Clock +7 d 1 h: the sweep removes it; staging/ is empty.
	swapOrchestrator(t, s, "orchestrator-aged", "169h")
	if !env.Wait(passWait, func() bool { return keyCount(t, s, "bob-artifacts", "staging/") == 0 }) {
		t.Errorf("staging/ is not empty after the sweep with the clock advanced 7 d: %v", env.ListKeys(t, s, "bob-artifacts", "staging/"))
	}
	onlySweepsListed(t, s)
}

// TestRSTORE007Retention advances the clock by 31 d, 91 d and 366 d: each class of data is
// gone and releases remain; only the sweeps issued a LIST.
func TestRSTORE007Retention(t *testing.T) {
	parity.Covers(t, "R-STORE-007")
	s, x := startE2E(t)
	if s == nil {
		return
	}
	env.ResetProxy(t, s)
	if r := env.RemoteBuild(t, x, harness.Fixture(t, "acme"), "//..."); r.Exit != 0 { // never released
		t.Fatalf("remote build: exit %d\n%s", r.Exit, r.Combined())
	}
	remoteRelease(t, x, "1.0.0")

	releases := func() []string { return env.ListKeys(t, s, "bob-control", "rel/") }
	releaseObjects := func() []string { return env.ListKeys(t, s, "bob-artifacts", "rel/") }
	nRel, nAssets := len(releases()), len(releaseObjects())
	if nRel == 0 || nAssets == 0 {
		t.Fatalf("the release left %d records and %d assets", nRel, nAssets)
	}
	logs := func() int { return len(keysContaining(t, s, "bob-analytics", "", "log_line")) }
	runs := func() int { return len(keysContaining(t, s, "bob-analytics", "", "package_run")) }
	builds := func() int { return keyCount(t, s, "bob-control", "build/") + keyCount(t, s, "bob-control", "bnodes/") }
	paths := func() int { return keyCount(t, s, "bob-artifacts", "nix/narinfo/") }
	if logs() == 0 || runs() == 0 || builds() == 0 || paths() == 0 {
		t.Fatalf("seed data missing: log %d, run %d, build records %d, cache paths %d", logs(), runs(), builds(), paths())
	}
	cachePaths := paths()

	// 31 d: log partitions older than retention.logs (30 d) are dropped; the rest stays.
	swapOrchestrator(t, s, "orchestrator-31d", "744h")
	if !env.Wait(passWait, func() bool { return logs() == 0 }) {
		t.Errorf("log partitions remain after the clock advanced 31 d")
	}
	if runs() == 0 || builds() == 0 || paths() < cachePaths {
		t.Errorf("31 d removed more than logs: run %d, build records %d, cache paths %d (was %d)", runs(), builds(), paths(), cachePaths)
	}

	// 91 d: cache paths no release or recent build references are removed.
	swapOrchestrator(t, s, "orchestrator-91d", "2184h")
	if !env.Wait(passWait, func() bool { return paths() < cachePaths }) {
		t.Errorf("no cache path was removed after the clock advanced 91 d")
	}
	if paths() == 0 || len(releaseObjects()) != nAssets {
		t.Errorf("91 d removed the release: %d cache paths left, %d of %d release assets", paths(), len(releaseObjects()), nAssets)
	}

	// 366 d: terminal build records and their bnodes are gone; run/job partitions stay until
	// 400 d; releases remain.
	swapOrchestrator(t, s, "orchestrator-366d", "8784h")
	if !env.Wait(passWait, func() bool { return builds() == 0 }) {
		t.Errorf("terminal build records remain after the clock advanced 366 d: %d", builds())
	}
	if runs() == 0 {
		t.Errorf("run partitions were dropped at 366 d; retention.analytics is 400 d")
	}
	if len(releases()) != nRel || len(releaseObjects()) != nAssets {
		t.Errorf("releases were collected: records %d of %d, assets %d of %d", len(releases()), nRel, len(releaseObjects()), nAssets)
	}
	onlySweepsListed(t, s)
	// The fleet queue slots below head - 1000 are deleted by known key (no LIST): nothing to
	// assert beyond the accounting above on a stack that never queued 1000 jobs.
}

// TestRSTORE150ReleasesAreNeverCollected: with cache_unreferenced_days = 0 a GC run after 2
// builds and 1 release deletes the unreleased build's paths and keeps every path and asset of
// the release; after DeleteRelease and another GC run they are gone.
func TestRSTORE150ReleasesAreNeverCollected(t *testing.T) {
	parity.Covers(t, "R-STORE-150")
	s, x := startE2E(t)
	if s == nil {
		return
	}
	if c := env.RPC(t, x, "bob.settings.v1.SettingsService/UpdateSettings", "", map[string]any{
		"settings":    map[string]any{"retention": map[string]any{"cache_unreferenced_days": 0}},
		"update_mask": "retention.cacheUnreferencedDays",
	}); c.Status != 200 {
		t.Fatalf("UpdateSettings: %d %s", c.Status, c.Body)
	}
	if r := env.RemoteBuild(t, x, harness.Fixture(t, "acme"), "//..."); r.Exit != 0 { // build 1: not released
		t.Fatalf("remote build: exit %d\n%s", r.Exit, r.Combined())
	}
	before := env.ListKeys(t, s, "bob-artifacts", "nix/narinfo/")
	remoteRelease(t, x, "1.0.0") // build 2 and the release
	all := env.ListKeys(t, s, "bob-artifacts", "nix/narinfo/")
	var releasedOnly []string // paths that appeared with the release build
	seen := map[string]bool{}
	for _, k := range before {
		seen[k] = true
	}
	for _, k := range all {
		if !seen[k] {
			releasedOnly = append(releasedOnly, k)
		}
	}
	assets := env.ListKeys(t, s, "bob-artifacts", "rel/")
	if len(before) == 0 || len(releasedOnly) == 0 || len(assets) == 0 {
		t.Fatalf("seed data missing: unreleased %d, released %d, assets %d", len(before), len(releasedOnly), len(assets))
	}

	swapOrchestrator(t, s, "orchestrator-gc", "1h")
	gone := func(keys []string, bucket, prefix string) int {
		have := map[string]bool{}
		for _, k := range env.ListKeys(t, s, bucket, prefix) {
			have[k] = true
		}
		n := 0
		for _, k := range keys {
			if !have[k] {
				n++
			}
		}
		return n
	}
	// Shared paths (referenced by the release too) must stay; the unreleased build's own
	// paths go. "before" includes paths the release build reuses, so only require that
	// something of the unreleased build went and that nothing of the release did.
	if !env.Wait(passWait, func() bool { return gone(before, "bob-artifacts", "nix/narinfo/") > 0 }) {
		t.Errorf("GC with cache_unreferenced_days = 0 deleted no path of the unreleased build")
	}
	if n := gone(releasedOnly, "bob-artifacts", "nix/narinfo/"); n != 0 {
		t.Errorf("GC deleted %d paths of the release", n)
	}
	if n := gone(assets, "bob-artifacts", "rel/"); n != 0 {
		t.Errorf("GC deleted %d assets of the release", n)
	}

	// DeleteRelease, then another GC run: everything of the release is gone.
	var list struct{ Releases []struct{ ID, Etag string } }
	c := env.RPC(t, x, "bob.release.v1.ReleaseService/ListReleases", "", map[string]any{"page_size": 10})
	if c.Status != 200 || json.Unmarshal(c.Body, &list) != nil || len(list.Releases) != 1 {
		t.Fatalf("ListReleases = %d %s, want one release", c.Status, c.Body)
	}
	if c := env.RPC(t, x, "bob.release.v1.ReleaseService/DeleteRelease", "", map[string]any{"id": list.Releases[0].ID, "etag": list.Releases[0].Etag}); c.Status != 200 {
		t.Fatalf("DeleteRelease: %d %s", c.Status, c.Body)
	}
	swapOrchestrator(t, s, "orchestrator-gc2", "2h")
	if !env.Wait(passWait, func() bool {
		return gone(releasedOnly, "bob-artifacts", "nix/narinfo/") == len(releasedOnly) && gone(assets, "bob-artifacts", "rel/") == len(assets)
	}) {
		t.Errorf("paths or assets of the deleted release remain after another GC run")
	}
	onlySweepsListed(t, s)
}
