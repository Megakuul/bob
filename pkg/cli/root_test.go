package cli

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/megakuul/bob/pkg/bob/diag"
)

// TestFindRootNewProject: a brand-new project is a directory with nothing but bob.go (no
// bob.lock, no .git). `bob lock` (orGit) takes that directory as the root; every other command
// refuses with BOB-E-0002 whose cause names the exact command that creates the lock (Q4, A473:
// the owner's first `bob lock` failed with "not inside a bob repository").
func TestFindRootNewProject(t *testing.T) {
	dir := t.TempDir()
	proj := filepath.Join(dir, "testbober")
	sub := filepath.Join(proj, "cmd", "hello")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(proj, "bob.go"), []byte("//go:build bob\n\npackage x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got, err := findRoot(proj, true); err != nil || got != proj {
		t.Errorf("findRoot(new project, lock) = %q, %v; want %q", got, err, proj)
	}
	for _, c := range []struct{ dir, cmd string }{{proj, "run bob lock "}, {sub, "run bob -C " + proj + " lock "}} {
		_, err := findRoot(c.dir, false)
		var d *diag.Error
		if !errors.As(err, &d) || d.Code != 2 {
			t.Fatalf("findRoot(%s, build) = %v, want BOB-E-0002", c.dir, err)
		}
		if !strings.Contains(d.Cause, c.cmd) {
			t.Errorf("cause %q does not name the fix %q", d.Cause, c.cmd)
		}
	}
	// No bob.go anywhere: still BOB-E-0002, without a cause.
	if _, err := findRoot(dir, true); err == nil {
		t.Error("a directory without bob.go became a root")
	} else if d := (*diag.Error)(nil); errors.As(err, &d) && d.Cause != "" {
		t.Errorf("cause %q without a bob.go", d.Cause)
	}
}
