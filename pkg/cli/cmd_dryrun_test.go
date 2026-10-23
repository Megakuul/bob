package cli

import (
	"bytes"
	"strings"
	"testing"
)

// bob build --dry-run is a known flag (ruling G24); it plans locally only.
func TestDryRunFlag(t *testing.T) {
	var so, se bytes.Buffer
	if got := Main([]string{"-C", t.TempDir(), "build", "--dry-run"}, &so, &se); got != 2 || !strings.Contains(se.String(), "BOB-E-0002") {
		t.Errorf("outside a repository: exit %d: %s", got, se.String())
	}
	so.Reset()
	se.Reset()
	if got := Main([]string{"build", "--dry-run", "--remote"}, &so, &se); got != 2 || !strings.Contains(se.String(), "--dry-run needs a local build") {
		t.Errorf("--remote: exit %d: %s", got, se.String())
	}
}

// --dry-run names a planned derivation by hash and name only: no store directory, no .drv
// suffix (AGENTS §5.5).
func TestPlanEntry(t *testing.T) {
	got := planEntry("/nix/store/0123456789abcdfghijklmnpqrsvwxyz-rcore-0.1.0.drv")
	if got != "0123456789abcdfghijklmnpqrsvwxyz-rcore-0.1.0" {
		t.Errorf("planEntry = %q", got)
	}
}
