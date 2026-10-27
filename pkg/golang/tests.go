package golang

import (
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path"
	"sort"
	"strings"
)

// maxTestDirs bounds the directory walk of a recursive test set.
const maxTestDirs = 100_000

// testDirs returns the module-relative directories of the region of d (d and the directories
// below it without a bob.go of their own, R-FW-070) that hold _test.go files, sorted. The evaluator runs in the repository root, so the module's files are read
// relative to it; test units of dependency repositories are not created (their tests are not
// bob's to run). Directories the go command ignores are skipped: testdata, vendor, names
// starting with "." or "_", nested modules, and subpackages (a directory with its own bob.go
// declares its own tests).
//
// The listing reads the working tree, which the evaluator's cache key does not cover: a new
// test package shows up at the next evaluation that misses the key (decision D-M5a-3).
func (m *Module) testDirs(d string) []string {
	if m.Label().Module != "" {
		return nil
	}
	root := m.dir
	if root == "" {
		root = "."
	}
	start := path.Join(root, d)
	var out []string
	visited := 0
	_ = fs.WalkDir(os.DirFS("."), start, func(p string, e fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if !e.IsDir() {
			return nil
		}
		visited++
		if visited > maxTestDirs {
			return fs.SkipAll
		}
		name := e.Name()
		if p != start && (name == "testdata" || name == "vendor" || strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_")) {
			return fs.SkipDir
		}
		ents, err := fs.ReadDir(os.DirFS("."), p)
		if err != nil {
			return fs.SkipDir
		}
		if p != start {
			for _, f := range ents {
				if (f.Name() == "go.mod" || f.Name() == "bob.go") && f.Type().IsRegular() {
					return fs.SkipDir
				}
			}
		}
		for _, f := range ents {
			n := f.Name()
			if f.Type().IsRegular() && strings.HasSuffix(n, "_test.go") && !strings.HasPrefix(n, ".") && !strings.HasPrefix(n, "_") {
				rel := strings.TrimPrefix(strings.TrimPrefix(p, root), "/")
				if root == "." {
					rel = p
				}
				if rel == "" {
					rel = "."
				}
				out = append(out, path.Clean(rel))
				break
			}
		}
		return nil
	})
	sort.Strings(out)
	return out
}

// goPackage is the package name of the non-test Go files of the module-relative directory d
// ("" when it cannot be read: a dependency repository, or no Go files). bob.go files are left
// out: they carry the bob build tag and are not part of the Go package's build.
func (m *Module) goPackage(d string) string {
	if m.Label().Module != "" {
		return ""
	}
	dir := m.labelDir(d)
	if dir == "" {
		dir = "."
	}
	ents, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	fset := token.NewFileSet()
	for _, e := range ents {
		n := e.Name()
		if !e.Type().IsRegular() || !strings.HasSuffix(n, ".go") || strings.HasSuffix(n, "_test.go") ||
			n == "bob.go" || strings.HasPrefix(n, ".") || strings.HasPrefix(n, "_") {
			continue
		}
		f, err := parser.ParseFile(fset, path.Join(dir, n), nil, parser.PackageClauseOnly)
		if err != nil || f.Name == nil {
			continue
		}
		return f.Name.Name
	}
	return ""
}
