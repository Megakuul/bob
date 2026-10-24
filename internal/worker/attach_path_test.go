package worker

import (
	"testing"
	"time"
)

// TestAttachPoPPath: the PoP signs the path the orchestrator verifies, "/api/" + the rule's
// "<service>/<method>" (internal/api interceptor). "/api/" + the connect procedure (which starts
// with "/") gave "/api//bob…" and every Attach was refused as "proof of possession invalid".
func TestAttachPoPPath(t *testing.T) {
	if want := "/api/bob.worker.v1.WorkerControlService/Attach"; attachPath != want {
		t.Errorf("attachPath = %q, want %q", attachPath, want)
	}
}

// TestAttachJitter: reconnect delays stay within [d/2, d] (A291).
func TestAttachJitter(t *testing.T) {
	for _, d := range []time.Duration{time.Second, 30 * time.Second} {
		for range 1000 {
			if j := attachJitter(d); j < d/2 || j > d {
				t.Fatalf("attachJitter(%v) = %v, want within [%v, %v]", d, j, d/2, d)
			}
		}
	}
}
