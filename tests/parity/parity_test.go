package parity

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCoversWritesResults(t *testing.T) {
	out := filepath.Join(t.TempDir(), "results.jsonl")
	t.Setenv(EnvOut, out)
	t.Run("inner", func(t *testing.T) {
		t.Run("sub", func(t *testing.T) { Covers(t, "R-FW-001", "R-FW-002") })
	})
	b, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	if len(lines) != 2 {
		t.Fatalf("got %d lines: %s", len(lines), b)
	}
	for _, l := range lines {
		if !strings.Contains(l, `"test":"TestCoversWritesResults"`) || !strings.Contains(l, `"result":"pass"`) {
			t.Errorf("unexpected line %s", l)
		}
	}
}
