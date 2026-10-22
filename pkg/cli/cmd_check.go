package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/megakuul/bob/pkg/bob/diag"
	"github.com/megakuul/bob/pkg/engine/build"
	"github.com/megakuul/bob/pkg/engine/eval"
	"github.com/megakuul/bob/pkg/engine/translate"
)

// Bob's block in .git/info/exclude (SPEC §03.0.2).
const (
	excludeBegin = "# >>> bob generated files (do not edit) >>>"
	excludeEnd   = "# <<< bob <<<"
)

// runCheck is `bob check [--fix] [--determinism]` (SPEC R-CLI-009): definition errors of
// bob.go, paths git does not ignore (1013) and, with --determinism, a second evaluation
// (1018), without changing a file unless --fix. Drift (1011) and lock freshness (1012) need
// the engine's check stages (NEEDS in plan/progress/I-M4e.md).
func runCheck(e *env) int {
	root, err := e.root(false)
	if err != nil {
		return e.out.fail(err)
	}
	st, exit, done := e.enterStore()
	if done {
		return exit
	}
	o := build.Options{Root: root, Patterns: []string{"//..."}, Store: st, Offline: e.a.bool("offline")}
	if e.a.bool("fix") {
		if err := writeExclude(root, []string{".bob/"}); err != nil {
			return e.out.fail(err)
		}
		if _, err := build.Lock(e.ctx, o, build.LockOptions{}); err != nil {
			return e.out.fail(err)
		}
	}
	var findings []*diag.Error
	g, err := build.Evaluate(e.ctx, o)
	if err != nil {
		findings = append(findings, diagErrors(err)...)
	} else {
		recordLabels(root, graphLabels(graphOf(g)))
	}
	findings = append(findings, ignoreFindings(e.ctx, root, []string{".bob/"})...)
	if e.a.bool("determinism") && err == nil {
		if err := eval.CheckDeterminism(e.ctx, eval.Request{Root: root, Patterns: o.Patterns, GoRoot: o.GoRoot}); err != nil {
			findings = append(findings, diagErrors(err)...)
		}
	}
	return reportFindings(e, findings)
}

// reportFindings prints the findings of check (blocks, then `bob: N problems (exit E)`), or
// the result event with --json.
func reportFindings(e *env, findings []*diag.Error) int {
	objs := []translate.ErrorObject{}
	for _, f := range findings {
		objs = append(objs, translate.NewErrorObject(f, f.Hint()))
	}
	e.out.result(struct {
		Findings []translate.ErrorObject `json:"findings"`
	}{objs})
	for _, f := range findings {
		e.out.diagnose(f, "")
	}
	exit := diag.ExitCode(findings)
	if len(findings) == 0 {
		e.out.printf("bob: no problems\n")
	} else {
		word := "problems"
		if len(findings) == 1 {
			word = "problem"
		}
		e.out.printf("\nbob: %d %s (exit %d)\n", len(findings), word, exit)
	}
	return e.out.finish(exit, translate.Counts{}, nil)
}

// ignoreFindings is BOB-E-1013 for each path git does not ignore. A directory that is not a
// git work tree, or a host without git, has nothing to check.
func ignoreFindings(ctx context.Context, root string, paths []string) []*diag.Error {
	if _, err := os.Stat(filepath.Join(root, ".git")); err != nil {
		return nil
	}
	git, err := exec.LookPath("git")
	if err != nil {
		return nil
	}
	var out []*diag.Error
	for _, p := range paths {
		cmd := exec.CommandContext(ctx, git, "check-ignore", "-q", "--no-index", "--", p)
		cmd.Dir = root
		err := cmd.Run()
		var ee *exec.ExitError
		if errors.As(err, &ee) && ee.ExitCode() == 1 {
			out = append(out, diag.New(1013, "path", p))
		}
	}
	return out
}

// writeExclude replaces bob's block in .git/info/exclude with paths, keeping every other line.
func writeExclude(root string, paths []string) error {
	gitDir := filepath.Join(root, ".git")
	if fi, err := os.Stat(gitDir); err != nil || !fi.IsDir() {
		return nil
	}
	p := filepath.Join(gitDir, "info", "exclude")
	old, err := os.ReadFile(p)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return diag.New(7002, "reason", p+" cannot be read")
	}
	var keep []string
	in := false
	for _, l := range strings.Split(strings.TrimRight(string(old), "\n"), "\n") {
		switch {
		case l == excludeBegin:
			in = true
		case l == excludeEnd:
			in = false
		case !in && (l != "" || len(keep) > 0):
			keep = append(keep, l)
		}
	}
	sort.Strings(paths)
	var b bytes.Buffer
	for _, l := range keep {
		b.WriteString(l + "\n")
	}
	b.WriteString(excludeBegin + "\n")
	seen := map[string]bool{}
	for _, x := range paths {
		if x != "" && !seen[x] && !strings.ContainsAny(x, "\n\r") {
			seen[x] = true
			fmt.Fprintf(&b, "/%s\n", strings.TrimPrefix(x, "/"))
		}
	}
	b.WriteString(excludeEnd + "\n")
	if bytes.Equal(b.Bytes(), old) {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return diag.New(7002, "reason", p+" cannot be written")
	}
	tmp := p + ".bob-tmp"
	if err := os.WriteFile(tmp, b.Bytes(), 0o644); err != nil {
		return diag.New(7002, "reason", p+" cannot be written")
	}
	if err := os.Rename(tmp, p); err != nil {
		return diag.New(7002, "reason", p+" cannot be written")
	}
	return nil
}
