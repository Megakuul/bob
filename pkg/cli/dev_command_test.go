package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/megakuul/bob/pkg/cli/devenv"
)

// TestDevCommandUsesDevEnvironment (A454): `bob dev -- <tool>` runs a program that only the
// dev environment has (a shell tool, or a directory of the dev PATH), not only programs on
// bob's own PATH. Before, exec.Command's failed host lookup stayed in Cmd.Err and Start
// failed with BOB-E-0005 ("go is not runnable") although the tool was found.
func TestDevCommandUsesDevEnvironment(t *testing.T) {
	dir := t.TempDir()
	tool := filepath.Join(dir, "bobtest-devtool")
	if err := os.WriteFile(tool, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	f := &devenv.File{Tools: map[string]devenv.Tool{"bobtest-shelltool": {Path: tool}}}
	env := []string{"PATH=" + dir}
	for _, name := range []string{"bobtest-shelltool", "bobtest-devtool"} {
		c := devCommand(f, []string{name, "version"}, env)
		c.Env = env
		if c.Path != tool {
			t.Errorf("%s: path %q, want %q", name, c.Path, tool)
		}
		if err := c.Run(); err != nil {
			t.Errorf("%s: run: %v", name, err)
		}
	}
	// Not in the dev environment: bob's own PATH (sh, for `bob dev -- sh -c …`).
	if c := devCommand(f, []string{"sh", "-c", "true"}, env); c.Path == "sh" || c.Run() != nil {
		t.Errorf("sh: path %q", c.Path)
	}
}
