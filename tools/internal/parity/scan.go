package parity

import (
	"context"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// skipDirs are never scanned: build output, dependencies, throwaway spikes.
var skipDirs = map[string]bool{
	".git": true, "out": true, "node_modules": true, "vendor": true, "testdata": true, "spikes": true,
}

// Scan finds every parity.Covers call in the _test.go files below root. It
// returns test key ("<dir>.<TestFunc>", dir relative to root) → ids.
// Ids must be string literals; anything else is an error, because the
// catalogue could not see it.
func Scan(ctx context.Context, root string) (map[string][]string, error) {
	out := map[string][]string{}
	fset := token.NewFileSet()
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if d.IsDir() {
			if path != root && (skipDirs[d.Name()] || strings.HasPrefix(d.Name(), ".")) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, "_test.go") {
			return nil
		}
		f, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if err != nil {
			return fmt.Errorf("parity scan: %w", err)
		}
		rel, err := filepath.Rel(root, filepath.Dir(path))
		if err != nil {
			return fmt.Errorf("parity scan: %w", err)
		}
		return scanFile(fset, f, filepath.ToSlash(rel), out)
	})
	if err != nil {
		return nil, fmt.Errorf("parity scan: %w", err)
	}
	for k, ids := range out {
		sort.Strings(ids)
		out[k] = dedup(ids)
	}
	return out, nil
}

func scanFile(fset *token.FileSet, f *ast.File, dir string, out map[string][]string) error {
	name := ""
	for _, imp := range f.Imports {
		if p, _ := strconv.Unquote(imp.Path.Value); p == CoversPkg {
			name = "parity"
			if imp.Name != nil {
				name = imp.Name.Name
			}
		}
	}
	if name == "" || name == "_" {
		return nil
	}
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}
		key := dir + "." + fn.Name.Name
		var err error
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || err != nil {
				return err == nil
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "Covers" {
				return true
			}
			if x, ok := sel.X.(*ast.Ident); !ok || x.Name != name {
				return true
			}
			for _, arg := range call.Args[min(1, len(call.Args)):] {
				lit, ok := arg.(*ast.BasicLit)
				id := ""
				if ok && lit.Kind == token.STRING {
					id, _ = strconv.Unquote(lit.Value)
				}
				if !idPattern.MatchString(id) {
					err = fmt.Errorf("%s: parity.Covers wants requirement id literals like \"R-FW-001\"", fset.Position(arg.Pos()))
					return false
				}
				out[key] = append(out[key], id)
			}
			return true
		})
		if err != nil {
			return err
		}
	}
	return nil
}

func dedup(s []string) []string {
	out := s[:0]
	for i, v := range s {
		if i == 0 || v != s[i-1] {
			out = append(out, v)
		}
	}
	return out
}
