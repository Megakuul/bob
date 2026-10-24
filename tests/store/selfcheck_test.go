package store

import (
	"strings"
	"testing"
	"time"

	"github.com/megakuul/bob/tests/harness"
	"github.com/megakuul/bob/tests/parity"
	"github.com/megakuul/bob/tests/store/env"
)

// TestRSEC102OrchestratorRefusesReadableControlBucket starts an orchestrator against a
// control bucket without the bucket policy, with a "worker probe credential" that the bucket
// does not deny (the harness root credential stands in for a worker credential whose policy
// is too wide). The orchestrator must refuse to start: exit 78 and the SPEC text.
//
// Decision: storage.worker_probe_credential is passed as env
// BOB_STORAGE_WORKER_PROBE_CREDENTIAL=<access key>:<secret key> (SPEC §05.11.1 maps config
// keys to BOB_<UPPER_SNAKE> and does not fix the value format).
func TestRSEC102OrchestratorRefusesReadableControlBucket(t *testing.T) {
	parity.Covers(t, "R-SEC-102")
	be := env.Start(t)
	be.DefaultBuckets(t) // no bucket policy is applied to bob-control
	probe := be.FS.Root.AccessKey + ":" + be.FS.Root.SecretKey
	r := harness.Run(t, "bob-server", harness.Spec{
		Env:     be.ServerEnv("BOB_STORAGE_WORKER_PROBE_CREDENTIAL=" + probe),
		Timeout: time.Minute,
	}, "orchestrator")
	if harness.Red(t, r) {
		return
	}
	if r.Exit != 78 {
		t.Errorf("orchestrator exit = %d, want 78\n%s", r.Exit, r.Combined())
	}
	if !strings.Contains(r.Stderr, "✗ control bucket is readable with the worker credential") {
		t.Errorf("stderr lacks %q:\n%s", "✗ control bucket is readable with the worker credential", r.Stderr)
	}
	// The refusal happens before any state is written: nothing was created in bob-fleet.
	for _, row := range be.Proxy.Counts().Rows {
		// The conformance probe writes under _bob/ only; any other PUT is state.
		if row.Bucket == "bob-fleet" && row.Op == "PUT" && row.Prefix != "_bob/" {
			t.Errorf("the refused orchestrator wrote %d x %s to bob-fleet", row.Count, row.Prefix)
		}
	}
	be.Proxy.AssertNoList(t, "bob-control", "bob-fleet")
}
