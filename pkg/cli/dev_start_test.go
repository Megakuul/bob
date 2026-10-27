package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// TestDevStartError (A551): a $SHELL that does not start in bob dev is reported with its cause
// (BOB-E-7011), not as "not runnable ... bob run needs a binary package" (BOB-E-0005).
func TestDevStartError(t *testing.T) {
	dir := t.TempDir()
	// The layout of /run/current-system/sw/bin/fish on NixOS: a link outside the store to a link
	// into it; the store path does not exist here.
	if err := os.MkdirAll(filepath.Join(dir, "sw", "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/nix/store/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa-fish-4.0/bin/fish", filepath.Join(dir, "sw", "bin", "fish")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(dir, "sw"), filepath.Join(dir, "current")); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "current", "bin", "fish")
	for _, c := range []struct {
		program string
		err     error
		want    string
	}{
		{link, syscall.ENOENT, "links to /nix/store/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa-fish-4.0/bin/fish in the host's Nix store"},
		{filepath.Join(dir, "nope"), syscall.ENOENT, "it does not exist"},
		{link, syscall.EACCES, "not executable"},
	} {
		d := devStartError(c.program, c.err)
		if d.Code != 7011 {
			t.Errorf("%s: code %d, want 7011", c.program, d.Code)
		}
		msg := d.Error()
		if !strings.Contains(msg, c.want) || strings.Contains(msg, "bob run needs a binary package") {
			t.Errorf("%s (%v): %q, want it to contain %q", c.program, c.err, msg, c.want)
		}
	}
}

// TestHostViewOnlyForDevSessions (A551): the host Nix store is shown to the programs of a dev
// session only. A build, a shim or Nix never starts through store.StartHost, so builds stay
// hermetic.
func TestHostViewOnlyForDevSessions(t *testing.T) {
	out, err := exec.Command("grep", "-rl", "--include=*.go", "store.StartHost(", "../../pkg", "../../internal", "../../cmd").Output()
	if err != nil {
		t.Skipf("grep: %v", err)
	}
	allowed := map[string]bool{
		"../../pkg/cli/dev_start.go":              true,
		"../../pkg/cli/dev_start_test.go":         true,
		"../../pkg/engine/store/hostview.go":      true,
		"../../pkg/engine/store/hostview_test.go": true,
	}
	for _, f := range strings.Fields(string(out)) {
		if !allowed[f] {
			t.Errorf("%s starts a program with the host Nix store visible; only the dev session may (dev_start.go)", f)
		}
	}
}
