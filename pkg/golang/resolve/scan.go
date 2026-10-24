package resolve

import (
	"bytes"
	"fmt"
	"go/build/constraint"
	"go/parser"
	"go/token"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// Package imports are read the way cmd/go computes the "all" pattern for go.mod: every build
// tag counts as both set and unset (all platforms, cgo on and off), except "ignore" and "bob"
// (build definitions, ADR 0003), which are never set.

const maxGoFileRead = 1 << 20 // bytes of one .go file read for its imports

// pkgImports are the imports of one package directory.
type pkgImports struct {
	imports []string // non-test files
	tests   []string // _test.go files (in-package and external)
	goFiles bool     // the directory holds .go files at all
}

// readImports parses the import blocks of the given .go files (base name → opener).
func readImports(files map[string]func() (io.ReadCloser, error)) (pkgImports, error) {
	var out pkgImports
	imp, tst := map[string]bool{}, map[string]bool{}
	names := make([]string, 0, len(files))
	for n := range files {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, name := range names {
		if !strings.HasSuffix(name, ".go") {
			continue
		}
		out.goFiles = true
		if strings.HasPrefix(name, "_") || strings.HasPrefix(name, ".") {
			continue
		}
		rc, err := files[name]()
		if err != nil {
			return out, err
		}
		src, err := io.ReadAll(io.LimitReader(rc, maxGoFileRead))
		rc.Close()
		if err != nil {
			return out, err
		}
		paths, ok, err := fileImports(name, src)
		if err != nil {
			return out, err
		}
		if !ok {
			continue
		}
		dst := imp
		if strings.HasSuffix(name, "_test.go") {
			dst = tst
		}
		for _, p := range paths {
			dst[p] = true
		}
	}
	out.imports, out.tests = sortedSet(imp), sortedSet(tst)
	return out, nil
}

// fileImports returns the imports of one file; ok is false when its build constraint
// excludes it under the any-tag rule. Files that do not parse are skipped like cmd/go skips
// them for module loading (the compiler reports them later).
func fileImports(name string, src []byte) ([]string, bool, error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, name, src, parser.ImportsOnly|parser.ParseComments)
	if f == nil {
		return nil, false, nil
	}
	_ = err
	if !anyTagsMatch(src, f.Package, fset) {
		return nil, false, nil
	}
	var out []string
	for _, is := range f.Imports {
		p, err := strconv.Unquote(is.Path.Value)
		if err != nil || p == "" {
			continue
		}
		out = append(out, p)
	}
	return out, true, nil
}

// anyTagsMatch evaluates the file's build constraint (//go:build, else // +build lines) with
// every tag both set and unset except "ignore" and "bob".
func anyTagsMatch(src []byte, pkgPos token.Pos, fset *token.FileSet) bool {
	head := src
	if off := fset.Position(pkgPos).Offset; off > 0 && off <= len(src) {
		head = src[:off]
	}
	var goBuild constraint.Expr
	var plus []constraint.Expr
	for _, line := range bytes.Split(head, []byte("\n")) {
		l := strings.TrimSpace(string(line))
		if !constraint.IsGoBuild(l) && !constraint.IsPlusBuild(l) {
			continue
		}
		x, err := constraint.Parse(l)
		if err != nil {
			continue
		}
		if constraint.IsGoBuild(l) {
			goBuild = x
		} else {
			plus = append(plus, x)
		}
	}
	if goBuild != nil {
		return evalAny(goBuild, true)
	}
	for _, x := range plus {
		if !evalAny(x, true) {
			return false
		}
	}
	return true
}

// evalAny is cmd/go's imports.eval with the "*" tag set: a tag other than ignore/bob is
// whatever the context prefers, so both it and its negation hold.
func evalAny(x constraint.Expr, prefer bool) bool {
	switch x := x.(type) {
	case *constraint.TagExpr:
		if x.Tag == "ignore" || x.Tag == "bob" || x.Tag == "" {
			return false
		}
		return prefer
	case *constraint.NotExpr:
		return !evalAny(x.X, !prefer)
	case *constraint.AndExpr:
		return evalAny(x.X, prefer) && evalAny(x.Y, prefer)
	case *constraint.OrExpr:
		return evalAny(x.X, prefer) || evalAny(x.Y, prefer)
	}
	return false
}

// diskDir lists the .go files of a directory of the working tree.
func diskDir(dir string) map[string]func() (io.ReadCloser, error) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	out := map[string]func() (io.ReadCloser, error){}
	for _, e := range ents {
		n := e.Name()
		if !e.Type().IsRegular() || !strings.HasSuffix(n, ".go") || n == "bob.go" {
			continue
		}
		p := filepath.Join(dir, n)
		out[n] = func() (io.ReadCloser, error) { return os.Open(p) }
	}
	return out
}

// workspaceDirs lists the package directories of a workspace module rooted at root
// (module-relative, "" = root): directories below it without their own go.mod, skipping
// testdata, vendor and names starting with "." or "_" (cmd/go's ./... rules).
func workspaceDirs(root string) ([]string, error) {
	var out []string
	walked := 0
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			if p == root {
				return err
			}
			return nil
		}
		if !d.IsDir() {
			return nil
		}
		walked++
		if walked > 100_000 {
			return fmt.Errorf("%s: more than 100000 directories", root)
		}
		rel, _ := filepath.Rel(root, p)
		rel = filepath.ToSlash(rel)
		if rel == "." {
			rel = ""
		} else {
			n := d.Name()
			if n == "testdata" || n == "vendor" || strings.HasPrefix(n, ".") || strings.HasPrefix(n, "_") {
				return filepath.SkipDir
			}
			if _, err := os.Lstat(filepath.Join(p, "go.mod")); err == nil {
				return filepath.SkipDir // another module
			}
		}
		out = append(out, rel)
		return nil
	})
	return out, err
}

func sortedSet(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// isStd reports whether an import path belongs to the standard library (cmd/go: the first
// element has no dot); "C" is cgo.
func isStd(p string) bool {
	first, _, _ := strings.Cut(p, "/")
	return !strings.Contains(first, ".")
}
