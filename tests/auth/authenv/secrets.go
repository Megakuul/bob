package authenv

import (
	"os"
	"strings"
	"sync"
	"testing"
)

var (
	secretsMu sync.Mutex
	secrets   []string
)

// Secret registers a password or token the suite used; LogLeaks later checks that no server log
// contains it (R-AUTH-151 case 4). It returns v for inline use.
func Secret(v string) string {
	if v == "" {
		return v
	}
	secretsMu.Lock()
	defer secretsMu.Unlock()
	secrets = append(secrets, v)
	return v
}

// Secrets returns every registered secret.
func Secrets() []string {
	secretsMu.Lock()
	defer secretsMu.Unlock()
	return append([]string(nil), secrets...)
}

// LogLeaks returns "<log file>: <what>" for every orchestrator log of s that contains one of the
// given secrets. It also reports the number of bytes scanned so a test can insist the logs exist.
func (s *Server) LogLeaks(t testing.TB, secrets []string) (leaks []string, scanned int) {
	t.Helper()
	for _, f := range s.OrchestratorLogs() {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("harness error: %v", err)
		}
		scanned += len(b)
		for _, sec := range secrets {
			if strings.Contains(string(b), sec) {
				leaks = append(leaks, f+": contains a secret of "+sec[:min(len(sec), 6)]+"…")
			}
		}
	}
	return leaks, scanned
}
