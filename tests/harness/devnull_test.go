package harness

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The /dev/null guard (I-X1) recognises the null device and rejects what a clobbered /dev/null
// looks like: a regular file, a missing path, another character device. Only temporary files
// and read-only stats of /dev are used.
func TestCheckNull(t *testing.T) {
	if err := checkNull(os.DevNull); err != nil {
		t.Fatalf("the host's /dev/null: %v", err)
	}
	reg := filepath.Join(t.TempDir(), "null")
	if err := os.WriteFile(reg, []byte("#files: 996\tversion: 5.9.1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := checkNull(reg); err == nil || !strings.Contains(err.Error(), "not a character device") {
		t.Errorf("a regular file passed as /dev/null: %v", err)
	}
	if err := checkNull(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Error("a missing /dev/null passed")
	}
	if err := checkNull("/dev/zero"); err == nil || !strings.Contains(err.Error(), "not 1,3") {
		t.Errorf("/dev/zero passed as /dev/null: %v", err)
	}
}
