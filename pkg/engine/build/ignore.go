package build

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/megakuul/bob/pkg/bob/diag"
)

// BobDir is bob's own state directory in a repository; git must ignore it (BOB-E-1013).
const BobDir = ".bob/"

// IgnoreFindings is BOB-E-1013 for each of paths (repo-relative) that git does not ignore
// (R-CLI-009). A directory that is not a git work tree, or a host without git, has nothing to
// check.
func IgnoreFindings(ctx context.Context, root string, paths []string) []*diag.Error {
	if len(paths) == 0 {
		return nil
	}
	if _, err := os.Stat(filepath.Join(root, ".git")); err != nil {
		return nil
	}
	git, err := exec.LookPath("git")
	if err != nil {
		return nil
	}
	cmd := exec.CommandContext(ctx, git, "check-ignore", "--no-index", "--stdin")
	cmd.Dir = root
	cmd.Stdin = strings.NewReader(strings.Join(paths, "\n") + "\n")
	var out bytes.Buffer
	cmd.Stdout = &out
	err = cmd.Run()
	var ee *exec.ExitError
	if err != nil && !(errors.As(err, &ee) && ee.ExitCode() == 1) { // 1: none is ignored
		return nil
	}
	ignored := map[string]bool{}
	for _, l := range strings.Split(out.String(), "\n") {
		ignored[l] = true
	}
	var findings []*diag.Error
	for _, p := range paths {
		if !ignored[p] {
			findings = append(findings, diag.New(1013, "path", p))
		}
	}
	return findings
}
