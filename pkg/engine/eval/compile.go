package eval

import (
	"bufio"
	"bytes"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/megakuul/bob/pkg/engine/ir"
)

// Compiler messages of the evaluator build (R-FW-043.5) become definition errors at the
// position of the user's bob.go: the renamed copies of package main bob.go files and the copies
// of dependency bob repositories are mapped back to the files they came from.

// compileLineRe is one go build diagnostic: file:line[:col]: message.
var compileLineRe = sync.OnceValue(func() *regexp.Regexp { return regexp.MustCompile(`^(\S+?\.go):(\d+)(?::(\d+))?: (.*)$`) })

var bobmainRe = sync.OnceValue(func() *regexp.Regexp { return regexp.MustCompile(`\bbobmain[0-9]+\b`) })

// compileErrors maps the go build output of a failed evaluator build.
// root is the repository (for the source lines the nixpkgs hints read), fw the framework the
// evaluator was compiled against (its generated nixpkgs names).
func compileErrors(stderr []byte, g *generated, deps *depSet, root string, fw *framework) error {
	var errs ir.Errors
	var other []string
	sc := bufio.NewScanner(bytes.NewReader(stderr))
	sc.Buffer(make([]byte, 0, 4096), 1<<20)
	for sc.Scan() {
		line := sc.Text()
		switch {
		case strings.TrimSpace(line) == "", strings.HasPrefix(line, "# "):
			continue
		case strings.HasPrefix(line, "\t") && len(errs) > 0:
			last := errs[len(errs)-1]
			if len(last.Excerpt) < 12 {
				last.Excerpt = append(last.Excerpt, strings.TrimSpace(mapPaths(line, g, deps)))
			}
			continue
		}
		m := compileLineRe().FindStringSubmatch(line)
		if m == nil {
			other = append(other, line)
			continue
		}
		file, internal := mapFile(m[1], g, deps)
		if internal {
			other = append(other, line)
			continue
		}
		ln, _ := strconv.Atoi(m[2])
		col, _ := strconv.Atoi(m[3])
		msg := mapPaths(m[4], g, deps)
		e := ir.DefinitionError(file, ln, col, "", msg)
		if h := nixpkgsHint(msg, root, file, ln, fw); h != "" {
			// A typed-surface error is a coded error of its own (error[BOB-E-1020] head and a
			// hint), not a plain compiler line.
			e.Args["option"] = "nixpkgs package"
			withHint(e, h)
		}
		errs = append(errs, e)
	}
	if len(errs) == 0 {
		// Nothing points into a bob.go: the generated module or the framework broke.
		return ir.Errors{ir.EvaluatorBug("compiling the evaluator failed: %s", strings.Join(tail(other, 12), "\n"))}
	}
	// go build compiles packages in parallel: sort for a stable report.
	sort.SliceStable(errs, func(i, j int) bool {
		a, b := errs[i].Location, errs[j].Location
		if a.File != b.File {
			return a.File < b.File
		}
		if a.Line != b.Line {
			return a.Line < b.Line
		}
		return a.Col < b.Col
	})
	return errs
}

// mapFile maps a file name of the go build output to the user's path. internal is true for
// files that are not build definitions (the framework, the generated main).
func mapFile(f string, g *generated, deps *depSet) (string, bool) {
	f = strings.TrimPrefix(f, srcDir+"/")
	f = strings.TrimPrefix(f, "./")
	if orig, ok := g.renames[f]; ok {
		return orig, false
	}
	if rest, ok := strings.CutPrefix(f, genDir+"/mods/"); ok {
		for _, m := range deps.sorted() {
			if r, ok := strings.CutPrefix(rest, m.copyDir()+"/"); ok {
				return m.path + "@" + m.version + "/" + r, false
			}
		}
		return f, true
	}
	if strings.HasPrefix(f, "/") || strings.HasPrefix(f, genDir+"/") || strings.HasPrefix(f, mainDir+"/") ||
		strings.Contains(f, frameworkModule+"@") {
		return f, true
	}
	return path.Clean(f), false
}

// mapPaths rewrites generated names inside a message: renamed files and packages, copies of
// dependency repositories, sandbox paths.
func mapPaths(s string, g *generated, deps *depSet) string {
	for from, to := range g.renames {
		s = strings.ReplaceAll(s, srcDir+"/"+from, to)
		s = strings.ReplaceAll(s, from, to)
	}
	for _, m := range deps.sorted() {
		s = strings.ReplaceAll(s, srcDir+"/"+genDir+"/mods/"+m.copyDir(), m.path+"@"+m.version)
	}
	s = bobmainRe().ReplaceAllString(s, "main")
	return strings.ReplaceAll(s, srcDir+"/", "")
}

// Bob's block in .git/info/exclude keeps generated files out of git (SPEC §03.0.2).
const (
	excludeBegin = "# >>> bob generated files (do not edit) >>>"
	excludeEnd   = "# <<< bob <<<"
	// gitignoreBegin starts the same block in .gitignore (the CLI writes it, cmd_check.go).
	gitignoreBegin = "# >>> bob generated files, not committed (managed by bob; go.mod and go.sum are committed) >>>"
)

// excludeFromGit adds the repo-relative path rel to bob's block in .git/info/exclude. Without
// a .git directory there is nothing to do.
func excludeFromGit(root, rel string) error {
	if fi, err := os.Stat(filepath.Join(root, ".git")); err == nil && fi.IsDir() {
		if err := addToBlock(filepath.Join(root, ".git", "info", "exclude"), excludeBegin, excludeEnd, rel); err != nil {
			return err
		}
	}
	// bob init writes the same block to .gitignore, so a clone that has the file ignores it too (A550).
	return addToBlock(filepath.Join(root, ".gitignore"), gitignoreBegin, excludeEnd, rel)
}

// addToBlock adds the pattern /rel to the block delimited by begin and end in the file p
// (created when missing; the block is appended when absent).
func addToBlock(p, begin, end, rel string) error {
	old, err := os.ReadFile(p)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	entry := "/" + rel
	lines := strings.Split(strings.TrimRight(string(old), "\n"), "\n")
	if len(old) == 0 {
		lines = nil
	}
	bi, ei := -1, -1
	for i, l := range lines {
		switch l {
		case begin:
			bi = i
		case end:
			if bi >= 0 && ei < 0 {
				ei = i
			}
		}
	}
	if bi < 0 || ei < 0 {
		lines = append(lines, begin, entry, end)
	} else {
		if slices.Contains(lines[bi+1:ei], entry) {
			return nil
		}
		lines = append(lines[:ei], append([]string{entry}, lines[ei:]...)...)
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	return writeIfChanged(p, []byte(strings.Join(lines, "\n")+"\n"))
}
