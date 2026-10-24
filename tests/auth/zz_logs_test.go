package auth

import (
	"testing"

	"github.com/megakuul/bob/tests/auth/authenv"
	"github.com/megakuul/bob/tests/parity"
)

// TestRAUTH151LogsStaySecretFree is case 4 of R-AUTH-151: after the full auth suite (this file
// sorts last, so Go runs it last) no orchestrator log contains a password or token any test used.
func TestRAUTH151LogsStaySecretFree(t *testing.T) {
	parity.Covers(t, "R-AUTH-151")
	s := authenv.Shared(t)
	secrets := authenv.Secrets()
	if len(secrets) < 5 {
		t.Fatalf("only %d secrets were used by the suite: run the whole package, not this test alone", len(secrets))
	}
	leaks, scanned := s.LogLeaks(t, secrets)
	if scanned == 0 {
		t.Fatalf("the orchestrator logs are empty after the whole auth suite: nothing to check")
	}
	for _, l := range leaks {
		t.Errorf("secret in a server log: %s", l)
	}
}

// TestRAPI151WholeSuite: after everything this package did to the shared stack (the API
// conformance run), the S3 proxy counted 0 ListObjects requests on control and fleet.
func TestRAPI151WholeSuite(t *testing.T) {
	parity.Covers(t, "R-API-151")
	assertNoList(t, authenv.Shared(t))
}
