//go:build slow

package fleet

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/megakuul/bob/tests/harness"
	"github.com/megakuul/bob/tests/parity"
	"github.com/megakuul/bob/tests/serverenv/stack"
	"github.com/megakuul/bob/tests/store/env"
)

// TestRFLEET005SplitBuildOn4Workers is the SPEC real run (budget B-091, [8c/15G]): on 4 local
// workers a build of the 73-drv layer graph is split by the planner and is done within 15 s
// (S14 quiet: closure job 20.6 s, per-drv 9.3 s), so it also beats the closure time.
// Needs fixture tests/fixtures/repos/layers73 (73 derivations in layers; owner: fixtures).
// Until the fixture and the planner exist the test is red.
func TestRFLEET005SplitBuildOn4Workers(t *testing.T) {
	parity.Covers(t, "R-FLEET-005")
	if _, err := os.Stat(filepath.Join(harness.Root(t), "tests", "fixtures", "repos", "layers73")); err != nil {
		harness.RedFeature(t, "R-FLEET-005 split build: fixture layers73 (73-drv layer graph) and the S14 planner")
		return
	}
	s := env.StartStack(t, stack.Options{Orchestrators: 1, Workers: 4})
	if s == nil {
		return
	}
	x := env.MustExtra(t, s, "api", "admin")
	env.Wait(60*time.Second, func() bool { return activeWorkers(t, x) >= 4 })
	dir := harness.Fixture(t, "layers73")
	start := time.Now()
	r := env.RemoteBuild(t, x, dir, "//...")
	took := time.Since(start)
	if harness.Red(t, r) {
		return
	}
	if r.Exit != 0 {
		t.Fatalf("build failed (exit %d):\n%s", r.Exit, r.Combined())
	}
	if took > 15*time.Second {
		t.Errorf("split build of the 73-drv layer graph on 4 workers took %v, want <= 15 s (closure job: 20.6 s)", took)
	}
}
