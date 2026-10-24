package errorsuite

import (
	"bufio"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// MarkerPos finds the comment "//bob:fault <id>" in a bob.go below dir and returns the position
// of the first call expression after it as "<rel>/bob.go:<line>:<col>", rel relative to dir.
// That is the constructor call bob reports as "package defined at" (HARNESS.md §5.2 check 2).
func MarkerPos(dir, id string) (string, error) {
	want := "//bob:fault " + id
	var found string
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || found != "" {
			return err
		}
		if d.IsDir() || d.Name() != "bob.go" {
			return nil
		}
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, path, nil, parser.ParseComments|parser.SkipObjectResolution)
		if err != nil {
			return fmt.Errorf("marker: %w", err)
		}
		for _, cg := range f.Comments {
			for _, c := range cg.List {
				if strings.TrimSpace(c.Text) != want {
					continue
				}
				after := c.End()
				var call *ast.CallExpr
				ast.Inspect(f, func(n ast.Node) bool {
					if ce, ok := n.(*ast.CallExpr); ok && ce.Pos() > after && (call == nil || ce.Pos() < call.Pos()) {
						call = ce
					}
					return true
				})
				if call == nil {
					return fmt.Errorf("marker %q in %s: no call follows it", want, path)
				}
				rel, err := filepath.Rel(dir, path)
				if err != nil {
					return fmt.Errorf("marker: %w", err)
				}
				p := fset.Position(call.Pos())
				found = fmt.Sprintf("%s:%d:%d", filepath.ToSlash(rel), p.Line, p.Column)
				return nil
			}
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	if found == "" {
		return "", fmt.Errorf("marker %q not found in any bob.go below %s", want, dir)
	}
	return found, nil
}

var siteMarker = regexp.MustCompile(`bob:fault-site\b`)

// SitePos finds the line of the patch target that carries a "bob:fault-site" marker and returns
// "<file>:<line>". The column is not part of the marker: compilers report different columns, so
// the check compares file and line only.
func SitePos(dir, file string) (string, error) {
	f, err := os.Open(filepath.Join(dir, file))
	if err != nil {
		return "", fmt.Errorf("site marker: %w", err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for n := 1; sc.Scan(); n++ {
		if siteMarker.MatchString(sc.Text()) {
			return fmt.Sprintf("%s:%d", filepath.ToSlash(file), n), nil
		}
	}
	if err := sc.Err(); err != nil {
		return "", fmt.Errorf("site marker: %w", err)
	}
	return "", fmt.Errorf("no bob:fault-site marker in %s", file)
}
