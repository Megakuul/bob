//go:build live

package server

import (
	"net/http"
	"testing"
	"time"

	"github.com/megakuul/bob/tests/harness"
	"github.com/megakuul/bob/tests/parity"
	"github.com/megakuul/bob/tests/serverenv/regenv"
)

// TestRSRV100CheckoutNeedsAnOrchestrator is the checkout bullet of R-SRV-100: with every Attach
// stream blocked, a job that needs checkout credentials (a push to a linked Forgejo repository)
// fails with an infrastructure error after 3 attempts, and no fourth attempt is made.
func TestRSRV100CheckoutNeedsAnOrchestrator(t *testing.T) {
	parity.Covers(t, "R-SRV-100")
	e := regenv.Up(t, 1)
	if e == nil {
		return
	}
	front, _ := e.Info["attach_proxy_url"].(string)
	if front == "" {
		harness.RedFeature(t, "devstack: no attach_proxy_url (TCP front of the orchestrators)")
		return
	}
	r, err := e.HTTP.Post(front+"/block", "text/plain", nil)
	if err != nil || r.StatusCode != http.StatusNoContent {
		t.Fatalf("harness error: attach proxy block: %v", err)
	}
	r.Body.Close()
	e.Project("acme", "demo")
	e.Push(harness.Fixture(t, "acme"), "demo", "main")
	b := e.WaitBuilds("acme", 1, 30*time.Second)[0]
	b = e.WaitBuild(b.ID, 10*time.Minute, regenv.Terminal)
	if b.State == "BUILD_STATE_SUCCEEDED" {
		t.Fatalf("a build that needs checkout succeeded with every Attach stream blocked")
	}
	var three bool
	for _, j := range e.JobsAttempts(b.ID) {
		if j.Attempt > 3 {
			t.Errorf("job %s ran %d attempts, want at most 3", j.ID, j.Attempt)
		}
		three = three || j.Attempt == 3 && j.State == "JOB_STATE_FAILED"
	}
	if !three {
		t.Errorf("no job failed after 3 attempts; jobs: %+v, build errors %+v", e.JobsAttempts(b.ID), b.Errors)
	}
}
