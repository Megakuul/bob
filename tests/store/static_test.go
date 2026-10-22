package store

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/megakuul/bob/tests/parity"
)

// shippedGo calls fn for every parsed non-test Go file of the shipped trees (pkg, internal,
// cmd). tools/ and tests/ are harness code: the rules judge shipped code only.
func shippedGo(t testing.TB, fn func(rel string, f *ast.File)) {
	t.Helper()
	root := repoRoot(t)
	for _, tree := range []string{"pkg", "internal", "cmd"} {
		err := filepath.WalkDir(filepath.Join(root, tree), func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				if os.IsNotExist(err) {
					return nil
				}
				return err
			}
			if d.IsDir() || !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
				return nil
			}
			f, err := parser.ParseFile(token.NewFileSet(), p, nil, parser.SkipObjectResolution)
			if err != nil {
				return err
			}
			rel, _ := filepath.Rel(root, p)
			fn(filepath.ToSlash(rel), f)
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}

// TestRSTORE100NoListOutsideSweep is the acceptance text "go run ./tools/ci fails if any
// package other than internal/store/sweep references ListObjectsV2, ListObjects,
// NewListObjectsV2Paginator or dynamitedb.Query", checked here directly on the source and,
// for the CI side, by requiring the analyzer in the lint set.
func TestRSTORE100NoListOutsideSweep(t *testing.T) {
	parity.Covers(t, "R-STORE-100")
	banned := map[string]bool{"ListObjects": true, "ListObjectsV2": true, "NewListObjectsV2Paginator": true}
	shippedGo(t, func(rel string, f *ast.File) {
		if strings.HasPrefix(rel, "internal/store/sweep/") {
			return
		}
		ast.Inspect(f, func(n ast.Node) bool {
			sel, ok := n.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			if banned[sel.Sel.Name] {
				t.Errorf("%s references %s outside internal/store/sweep", rel, sel.Sel.Name)
			}
			if id, ok := sel.X.(*ast.Ident); ok && id.Name == "dynamitedb" && sel.Sel.Name == "Query" {
				t.Errorf("%s references dynamitedb.Query outside internal/store/sweep", rel)
			}
			return true
		})
	})
	root := repoRoot(t)
	lint, err := os.ReadFile(filepath.Join(root, "tools", "lint", "lint.go"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(lint), "NoList") {
		t.Errorf("tools/lint does not run the NoList analyzer; `go run ./tools/ci` would not fail on a LIST")
	}
	nolist, err := os.ReadFile(filepath.Join(root, "tools", "lint", "nolist.go"))
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"ListObjectsV2", "ListObjects", "NewListObjectsV2Paginator", "Query"} {
		if !strings.Contains(string(nolist), `"`+name+`"`) {
			t.Errorf("tools/lint/nolist.go does not cover %s", name)
		}
	}
}

// TestRSTORE106OnlyStoreImportsDynamitedbAndS3: no package other than internal/store imports
// dynamitedb or service/s3; the exceptions are internal/logstore (lake) and internal/artifacts
// (plain S3 on the artifacts bucket); internal/store/sweep is part of the store.
func TestRSTORE106OnlyStoreImportsDynamitedbAndS3(t *testing.T) {
	parity.Covers(t, "R-STORE-106")
	allowed := func(rel string) bool {
		for _, p := range []string{"internal/store/", "internal/logstore/", "internal/artifacts/"} {
			if strings.HasPrefix(rel, p) {
				return true
			}
		}
		return false
	}
	shippedGo(t, func(rel string, f *ast.File) {
		for _, im := range f.Imports {
			path, _ := strconv.Unquote(im.Path.Value)
			if (path == "github.com/megakuul/dynamitedb" || strings.HasPrefix(path, "github.com/megakuul/dynamitedb/") ||
				path == "github.com/aws/aws-sdk-go-v2/service/s3" || strings.HasPrefix(path, "github.com/aws/aws-sdk-go-v2/service/s3/")) && !allowed(rel) {
				t.Errorf("%s imports %s; server packages reach S3 only through internal/store", rel, path)
			}
		}
	})
}
