package worker

import "testing"

// TestAttachPoPPath: the PoP signs the path the orchestrator verifies, "/api/" + the rule's
// "<service>/<method>" (internal/api interceptor). "/api/" + the connect procedure (which starts
// with "/") gave "/api//bob…" and every Attach was refused as "proof of possession invalid".
func TestAttachPoPPath(t *testing.T) {
	if want := "/api/bob.worker.v1.WorkerControlService/Attach"; attachPath != want {
		t.Errorf("attachPath = %q, want %q", attachPath, want)
	}
}
