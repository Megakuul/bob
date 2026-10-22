package store_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

// TestDraftCarriesEveryField: CAS writes the fields a CAS func left alone through the record
// type's Draft (store.writeValue), so a Draft that misses a field would drop it on every CAS.
// Every record type of the server (a struct with a pk-tagged field) must have a Draft whose
// composite literal names every field of the struct (test-only kinds may have none).
func TestDraftCarriesEveryField(t *testing.T) {
	root := filepath.Join("..", "..")
	checked := 0
	for _, dir := range []string{"internal", "tests/store"} {
		err := filepath.WalkDir(filepath.Join(root, dir), func(path string, d os.DirEntry, err error) error {
			if err != nil || !d.IsDir() {
				return err
			}
			if strings.Contains(path, "/gen/") {
				return filepath.SkipDir
			}
			checked += checkDir(t, path)
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if checked < 40 {
		t.Errorf("only %d record types found; the walk is broken", checked)
	}
}

func checkDir(t *testing.T, dir string) int {
	t.Helper()
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, dir, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, pkg := range pkgs {
		records := map[string]*ast.StructType{}
		inTest := map[string]bool{}
		drafts := map[string]*ast.CompositeLit{}
		for fname, f := range pkg.Files {
			ast.Inspect(f, func(node ast.Node) bool {
				switch x := node.(type) {
				case *ast.TypeSpec:
					if st, ok := x.Type.(*ast.StructType); ok && isRecord(st) {
						records[x.Name.Name] = st
						inTest[x.Name.Name] = strings.HasSuffix(fname, "_test.go")
					}
				case *ast.FuncDecl:
					if x.Name.Name != "Draft" || x.Recv == nil || len(x.Recv.List) != 1 {
						return true
					}
					star, ok := x.Recv.List[0].Type.(*ast.StarExpr)
					if !ok {
						return true
					}
					recv, _ := star.X.(*ast.Ident)
					ast.Inspect(x.Body, func(m ast.Node) bool {
						if cl, ok := m.(*ast.CompositeLit); ok && recv != nil {
							drafts[recv.Name] = cl
							return false
						}
						return true
					})
				}
				return true
			})
		}
		for name, st := range records {
			n++
			cl, ok := drafts[name]
			if !ok {
				// Test-only kinds that are written only through builders need no Draft.
				if !inTest[name] {
					t.Errorf("%s: record type %s has no Draft method", dir, name)
				}
				continue
			}
			named := map[string]bool{}
			for _, e := range cl.Elts {
				if kv, ok := e.(*ast.KeyValueExpr); ok {
					if id, ok := kv.Key.(*ast.Ident); ok {
						named[id.Name] = true
					}
				}
			}
			for _, f := range st.Fields.List {
				for _, fn := range f.Names {
					if !named[fn.Name] {
						t.Errorf("%s: %s.Draft does not carry field %s", dir, name, fn.Name)
					}
				}
			}
		}
	}
	return n
}

func isRecord(st *ast.StructType) bool {
	for _, f := range st.Fields.List {
		if f.Tag == nil {
			continue
		}
		tag, _ := strconv.Unquote(f.Tag.Value)
		if _, ok := reflect.StructTag(tag).Lookup("pk"); ok {
			return true
		}
	}
	return false
}
