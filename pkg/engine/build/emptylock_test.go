package build

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/megakuul/bob/pkg/bob/diag"
)

// TestEmptyLockIsNoLock: an empty bob.lock (`touch bob.lock`, the way the owner started a
// repository in Q4) pins nothing and is read like a missing one; a lock with content that is
// not JSON stays BOB-E-1019 (R-FW-048) (A472).
func TestEmptyLockIsNoLock(t *testing.T) {
	for _, content := range []string{"", "\n", "  \n\t\n"} {
		root := t.TempDir()
		if err := os.WriteFile(filepath.Join(root, "bob.lock"), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		f, err := readLock(root)
		if err != nil || f.Nixpkgs.Rev != "" || len(f.Sections) != 0 {
			t.Errorf("readLock(%q) = %+v, %v; want an empty lock", content, f, err)
		}
	}
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "bob.lock"), []byte("garbage\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var d *diag.Error
	if _, err := readLock(root); !errors.As(err, &d) || d.Code != 1019 {
		t.Errorf("readLock(garbage) = %v, want BOB-E-1019", err)
	}
}

// TestGoChangesFreshLock: a fresh go section without third-party modules is listed by its
// workspace and go version, not as a nameless "~ go go →" (Q4).
func TestGoChangesFreshLock(t *testing.T) {
	next := []byte(`{"inputs":"09fb3374f3e33f85c2e828af596f8e91de36a2d28f1222783feb849be746f76d","modules":[],"proxy":"https://proxy.golang.org","workspaces":{"example.com/hello":{"go":"1.26.0","modules":[]}}}`)
	got := goChanges(nil, next)
	if len(got) != 1 || got[0].Op != "add" || got[0].Name != "example.com/hello" || got[0].To != "go 1.26.0" {
		t.Errorf("goChanges(fresh) = %+v", got)
	}
	if got := goChanges(next, next); len(got) != 1 || got[0].Name != "go" || got[0].Op != "update" {
		t.Errorf("goChanges(details only) = %+v", got)
	}
}
