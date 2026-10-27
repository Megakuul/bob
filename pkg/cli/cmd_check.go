package cli

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"

	"github.com/megakuul/bob/pkg/bob/diag"
	"github.com/megakuul/bob/pkg/engine/build"
	"github.com/megakuul/bob/pkg/engine/translate"
)

// Bob's block in .git/info/exclude (SPEC §03.0.2).
const (
	excludeBegin = "# >>> bob generated files (do not edit) >>>"
	excludeEnd   = "# <<< bob <<<"
	// The block in .gitignore (A550); go.mod and go.sum are not in it: they are committed.
	gitignoreBegin = "# >>> bob generated files, not committed (managed by bob; go.mod and go.sum are committed) >>>"
	gitignoreEnd   = excludeEnd
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
		// What --fix is about to revert: the generated files edited by hand (1011).
		var edited []*diag.Error
		if pre, err := build.Check(e.ctx, o); err == nil {
			for _, f := range pre {
				if f.Code == 1011 {
					edited = append(edited, f)
				}
			}
		}
		res, err := build.Lock(e.ctx, o, build.LockOptions{})
		if err != nil {
			return e.out.fail(err)
		}
		paths, err := build.Gen(e.ctx, o, true)
		if err != nil {
			return e.out.fail(err)
		}
		if err := writeExclude(root, paths); err != nil {
			return e.out.fail(err)
		}
		if !e.out.json {
			reportFixed(e.out, res.Changes, edited)
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

// reportFixed tells what bob check --fix regenerated: the lock changes and every generated file
// that was edited by hand and is back as bob writes it (the edit is kept under .bob/gen/backup/).
func reportFixed(o *out, changes []build.LockChange, edited []*diag.Error) {
	if len(changes) > 0 {
		printChanges(o, changes)
	}
	for _, f := range edited {
		n := len(f.Excerpt)
		word := "differences"
		if n == 1 {
			word = "difference"
		}
		o.printf("fixed %s: restored what bob generates (%d %s; your copy is in .bob/gen/backup/)\n", f.Args["file"], n, word)
		for _, l := range f.Excerpt {
			o.printf("  %s\n", l)
		}
	}
	if len(changes) == 0 && len(edited) == 0 {
		o.printf("fix: nothing to regenerate\n")
	}
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
	if len(o.Patterns) == 1 && o.Patterns[0] == "//..." {
		// The whole repository is no patterns (build.Options): only then does bob gen prune
		// owned directories whose recipe was removed (R-FW-066 (d)).
		o.Patterns = nil
	}
	paths, err := build.Gen(e.ctx, o, e.a.bool("force"))
	if err != nil {
		return e.out.fail(err)
	}
	if err := writeExclude(root, paths); err != nil {
		return e.out.fail(err)
	}
	e.ensureShims(st, root)
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
		objs = append(objs, translate.NewErrorObject(f, findingHint(f)))
	}
	e.out.result(struct {
		Findings []translate.ErrorObject `json:"findings"`
	}{objs})
	for _, f := range findings {
		e.out.diagnose(f, findingHint(f))
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

// listOrderRe matches a BOB-E-1018 label whose differing field is a list element: only the
// order of a list differs, the symptom of ranging over a map.
var listOrderRe = sync.OnceValue(func() *regexp.Regexp { return regexp.MustCompile(`\(field [^)]*\]\)$`) })

// findingHint is f's hint; for BOB-E-1018 on a list element it is R-FW-008's specific hint.
func findingHint(f *diag.Error) string {
	if f.Code == 1018 && listOrderRe().MatchString(f.Args["label"]) {
		return "do not range over maps when building lists; sort the keys"
	}
	return f.Hint()
}

// writeExclude replaces bob's blocks in .git/info/exclude and .gitignore with .bob/ and paths,
// keeping every other line.
func writeExclude(root string, paths []string) error { return updateExclude(root, paths, true) }

// updateExclude writes bob's block in .git/info/exclude and in the repository's .gitignore:
// .bob/ and paths (the files bob generates and does not commit), keeping every other line.
// With replace the blocks hold exactly these; otherwise paths are added to them (a build that
// did not regenerate native files does not know every generated file). Both blocks hold the
// same list (A550): .git/info/exclude is local to a clone, .gitignore reaches everyone who
// clones the repository (bob init writes it at once).
func updateExclude(root string, paths []string, replace bool) error {
	if fi, err := os.Stat(filepath.Join(root, ".git")); err == nil && fi.IsDir() {
		if err := updateBlock(filepath.Join(root, ".git", "info", "exclude"), excludeBegin, excludeEnd, paths, replace); err != nil {
			return err
		}
	}
	return updateBlock(filepath.Join(root, ".gitignore"), gitignoreBegin, gitignoreEnd, paths, replace)
}

// updateBlock writes the block delimited by begin and end in the file p (created when missing):
// .bob/ and paths, one rooted pattern per line, sorted; every other line is kept.
func updateBlock(p, begin, end string, paths []string, replace bool) error {
	old, err := os.ReadFile(p)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return diag.New(7002, "reason", p+" cannot be read")
	}
	paths = append([]string{build.BobDir}, paths...) // a copy: the caller's slice stays
	var keep []string
	in := false
	for _, l := range strings.Split(strings.TrimRight(string(old), "\n"), "\n") {
		switch {
		case l == begin:
			in = true
		case l == end:
			in = false
		case in && (!replace || strings.HasSuffix(l, "/zz_bob_cgo.go")): // the evaluator owns the cgo stubs
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
	b.WriteString(begin + "\n")
	seen := map[string]bool{}
	for _, x := range paths {
		if x != "" && !seen[x] && !strings.ContainsAny(x, "\n\r") {
			seen[x] = true
			fmt.Fprintf(&b, "/%s\n", strings.TrimPrefix(x, "/"))
		}
	}
	b.WriteString(end + "\n")
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
