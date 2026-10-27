package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

func init() {
	register(Stage{Name: "workflows", Order: 6, Run: runWorkflows})
}

// workflowScripts are the shell scripts the workflows call; they are checked with shellcheck.
var workflowScripts = []string{"scripts/ci-changes.sh"}

// runWorkflows catches a broken GitHub workflow before it is pushed: every `uses:` is pinned by
// a full commit SHA with its version in a comment, actionlint (with shellcheck for the run
// blocks) finds nothing, and the scripts the workflows call pass shellcheck. A pipeline that
// fails on its first run costs minutes of waiting on GitHub (A563).
func runWorkflows(ctx context.Context, env *Env) error {
	files, err := filepath.Glob(filepath.Join(env.Root, ".github", "workflows", "*.yml"))
	if err != nil {
		return harnessErr(err)
	}
	if len(files) == 0 {
		return nil
	}
	var problems []string
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			return harnessErr(err)
		}
		rel, _ := filepath.Rel(env.Root, f)
		for _, p := range unpinnedActions(string(b)) {
			problems = append(problems, rel+": "+p)
		}
	}
	if len(problems) > 0 {
		return fmt.Errorf("actions must be pinned by full commit SHA with the version in a comment:\n%s", strings.Join(problems, "\n"))
	}
	lint, err := nixTool(ctx, env.Root, "actionlint")
	if err != nil {
		return err
	}
	shellcheck, err := nixTool(ctx, env.Root, "shellcheck.bin")
	if err != nil {
		return err
	}
	path := []string{"PATH=" + filepath.Join(shellcheck, "bin") + string(os.PathListSeparator) + os.Getenv("PATH")}
	if _, err := command(ctx, env.Root, path, filepath.Join(lint, "bin", "actionlint"), "-shellcheck", filepath.Join(shellcheck, "bin", "shellcheck")); err != nil {
		return err
	}
	for _, s := range workflowScripts {
		if _, err := command(ctx, env.Root, nil, filepath.Join(shellcheck, "bin", "shellcheck"), "-s", "sh", s); err != nil {
			return err
		}
	}
	return nil
}

var (
	usesLine   = regexp.MustCompile(`^\s*(?:-\s+)?uses:\s*(\S+)(.*)$`)
	pinnedUses = regexp.MustCompile(`^[\w.-]+/[\w./-]+@[0-9a-f]{40}$`)
	versionTag = regexp.MustCompile(`^\s*#\s*v\d+(\.\d+){0,2}\b`)
)

// unpinnedActions lists the `uses:` lines of a workflow that are not pinned by a full commit
// SHA followed by a "# vX.Y.Z" comment. Local actions ("./...") are fine.
func unpinnedActions(workflow string) []string {
	var out []string
	for i, l := range strings.Split(workflow, "\n") {
		m := usesLine.FindStringSubmatch(l)
		if m == nil || strings.HasPrefix(m[1], "./") {
			continue
		}
		if !pinnedUses.MatchString(m[1]) || !versionTag.MatchString(m[2]) {
			out = append(out, fmt.Sprintf("line %d: %s", i+1, strings.TrimSpace(l)))
		}
	}
	return out
}
