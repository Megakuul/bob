package worker

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestJobEnvCachesInSlotHome: inside a job container os.UserCacheDir (the evaluator's seed
// GOCACHE) resolves below the slot's state home, which outlives the job; HOME does not (A290).
func TestJobEnvCachesInSlotHome(t *testing.T) {
	env := map[string]string{}
	for _, kv := range jobEnv() {
		k, v, _ := strings.Cut(kv, "=")
		env[k] = v
	}
	for k, v := range env {
		t.Setenv(k, v)
	}
	if _, ok := env["XDG_CACHE_HOME"]; !ok {
		t.Setenv("XDG_CACHE_HOME", "")
	}
	dir, err := os.UserCacheDir()
	if err != nil {
		t.Fatal(err)
	}
	if rel, err := filepath.Rel(JobHome, dir); err != nil || strings.HasPrefix(rel, "..") {
		t.Errorf("os.UserCacheDir() in a job container = %s, want a directory below the slot home %s", dir, JobHome)
	}
}
