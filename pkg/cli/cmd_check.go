package cli

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/megakuul/bob/pkg/bob/diag"
	"github.com/megakuul/bob/pkg/engine/build"
	"github.com/megakuul/bob/pkg/engine/translate"
)

// Bob's block in .git/info/exclude (SPEC §03.0.2).
const (
	excludeBegin = "# >>> bob generated files (do not edit) >>>"
	excludeEnd   = "# <<< bob <<<"
)

// runCheck is `bob check [--fix] [--determinism]` (SPEC R-CLI-009): definition errors of
// bob.go, stale or missing lock entries (1012), generated files edited by hand (1011) and
// paths git does not ignore (1013), and with --determinism a second evaluation (1018), without
// changing a file unless --fix. --fix runs bob lock, bob gen --force and writes bob's block in
// .git/info/exclude first.
func runCheck(e *env) int {
	root, err := e.root(false)
	if err != nil {
		return e.out.fail(err)
	}
	st, exit, done := e.enterStore()
	if done {
		return exit
	}
	o := build.Options{Root: root, Store: st, Offline: e.a.bool("offline")}
	if e.a.bool("fix") {
		if _, err := build.Lock(e.ctx, o, build.LockOptions{}); err != nil {
			return e.out.fail(err)
		}
		paths, err := build.Gen(e.ctx, o, true)
		if err != nil {
			return e.out.fail(err)
		}
		if err := writeExclude(root, paths); err != nil {
			return e.out.fail(err)
		}
	}
	findings, err := build.Check(e.ctx, o)
	if err != nil {
		findings = append(findings, diagErrors(err)...)
	} else if e.a.bool("determinism") {
		if err := build.CheckDeterminism(e.ctx, o); err != nil {
			findings = append(findings, diagErrors(err)...)
		}
	}
	return reportFindings(e, findings)
}

// runGen is `bob gen [--force]` (SPEC §03.0.2): write the generated native files and bob's
// block in .git/info/exclude. A file edited by hand fails with 1011 unless --force, which keeps
// a backup under .bob/gen/backup/.
func runGen(e *env) int {
	if e.a.bool("check") {
		return e.out.fail(diag.NotImplemented("bob gen --check"))
	}
	root, err := e.root(false)
	if err != nil {
		return e.out.fail(err)
	}
	st, exit, done := e.enterStore()
	if done {
		return exit
	}
	o := build.Options{Root: root, Patterns: e.patterns(root, e.a.pos), Store: st, Offline: e.a.bool("offline")}
	paths, err := build.Gen(e.ctx, o, e.a.bool("force"))
	if err != nil {
		return e.out.fail(err)
	}
	if err := writeExclude(root, paths); err != nil {
		return e.out.fail(err)
	}
	e.out.result(struct {
		Excluded []string `json:"excluded"`
	}{append([]string{}, paths...)})
	return e.out.finish(0, translate.Counts{}, nil)
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
	} else if !e.out.json {
		word := "problems"
		if len(findings) == 1 {
			word = "problem"
		}
		// after the blocks, on stderr
		fmt.Fprintf(e.out.errw, "\nbob: %d %s (exit %d)\n", len(findings), word, exit)
	}
	return e.out.finish(exit, translate.Counts{}, nil)
}

// writeExclude replaces bob's block in .git/info/exclude with .bob/ and paths, keeping every
// other line.
func writeExclude(root string, paths []string) error { return updateExclude(root, paths, true) }

// updateExclude writes bob's block in .git/info/exclude: .bob/ and paths, keeping every other
// line. With replace the block holds exactly these; otherwise paths are added to it (a build
// that did not regenerate native files does not know every generated file).
func updateExclude(root string, paths []string, replace bool) error {
	gitDir := filepath.Join(root, ".git")
	if fi, err := os.Stat(gitDir); err != nil || !fi.IsDir() {
		return nil
	}
	p := filepath.Join(gitDir, "info", "exclude")
	old, err := os.ReadFile(p)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return diag.New(7002, "reason", p+" cannot be read")
	}
	paths = append([]string{build.BobDir}, paths...) // a copy: the caller's slice stays
	var keep []string
	in := false
	for _, l := range strings.Split(strings.TrimRight(string(old), "\n"), "\n") {
		switch {
		case l == excludeBegin:
			in = true
		case l == excludeEnd:
			in = false
		case in && !replace:
			paths = append(paths, strings.TrimPrefix(l, "/"))
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
