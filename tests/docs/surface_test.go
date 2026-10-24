package docs

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// cliCommand is one entry of the command table of pkg/cli (SPEC §02.1).
type cliCommand struct {
	name  string
	flags []string // long names without dashes; short flags as their letter
}

// cliSurface reads the command table (func commands in pkg/cli/cli.go) and the global flags
// (var globalFlags in pkg/cli/args.go) from the source. The table is data written as
// composite literals; a refactor that changes its shape makes this test fail loudly, never
// pass silently (zero commands is an error).
func cliSurface(t testing.TB, root string) (cmds []cliCommand, global []string) {
	t.Helper()
	fset := token.NewFileSet()
	cliFile, err := parser.ParseFile(fset, filepath.Join(root, "pkg/cli/cli.go"), nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	argsFile, err := parser.ParseFile(fset, filepath.Join(root, "pkg/cli/args.go"), nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range cliFile.Decls {
		fd, ok := d.(*ast.FuncDecl)
		if !ok || fd.Name.Name != "commands" || fd.Body == nil {
			continue
		}
		ast.Inspect(fd.Body, func(n ast.Node) bool {
			ret, ok := n.(*ast.ReturnStmt)
			if !ok || len(ret.Results) != 1 {
				return true
			}
			lit, ok := ret.Results[0].(*ast.CompositeLit)
			if !ok {
				return true
			}
			for _, e := range lit.Elts {
				c, ok := e.(*ast.CompositeLit)
				if !ok || len(c.Elts) < 4 {
					t.Fatalf("pkg/cli/cli.go: an entry of the command table is not {name, synopsis, summary, flags, run}")
				}
				name := stringLit(t, c.Elts[0])
				cmds = append(cmds, cliCommand{name: name, flags: flagNames(t, c.Elts[3])})
			}
			return false
		})
	}
	for _, d := range argsFile.Decls {
		gd, ok := d.(*ast.GenDecl)
		if !ok {
			continue
		}
		for _, s := range gd.Specs {
			vs, ok := s.(*ast.ValueSpec)
			if !ok || len(vs.Names) != 1 || vs.Names[0].Name != "globalFlags" || len(vs.Values) != 1 {
				continue
			}
			global = flagNames(t, vs.Values[0])
		}
	}
	if len(cmds) < 10 || len(global) < 5 {
		t.Fatalf("read %d commands and %d global flags from pkg/cli: the command table changed shape; update tests/docs/surface_test.go", len(cmds), len(global))
	}
	return cmds, global
}

func stringLit(t testing.TB, e ast.Expr) string {
	t.Helper()
	bl, ok := e.(*ast.BasicLit)
	if !ok || bl.Kind != token.STRING {
		t.Fatalf("pkg/cli: expected a string literal, found %T", e)
	}
	s, err := strconv.Unquote(bl.Value)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// flagNames reads []flag{{name: "x", …}, …} (nil for no flags).
func flagNames(t testing.TB, e ast.Expr) []string {
	t.Helper()
	if id, ok := e.(*ast.Ident); ok && id.Name == "nil" {
		return nil
	}
	lit, ok := e.(*ast.CompositeLit)
	if !ok {
		t.Fatalf("pkg/cli: a flag list is not a composite literal (%T)", e)
	}
	var out []string
	for _, fe := range lit.Elts {
		fl, ok := fe.(*ast.CompositeLit)
		if !ok {
			t.Fatalf("pkg/cli: a flag is not a composite literal")
		}
		for _, kv := range fl.Elts {
			if kv, ok := kv.(*ast.KeyValueExpr); ok {
				if k, ok := kv.Key.(*ast.Ident); ok && k.Name == "name" {
					out = append(out, stringLit(t, kv.Value))
				}
			}
		}
	}
	return out
}

// apiPackages are the public bob.go API packages (SPEC R-ARCH-003 layers L0-L3) whose
// exported functions the guides must mention. pkg/bob/ext is the ecosystem extension API, not
// for bob.go files, and is left out.
var apiPackages = []string{
	"pkg/bob", "pkg/nixpkgs",
	"pkg/golang", "pkg/cc", "pkg/rust", "pkg/npm", "pkg/ocaml",
	"pkg/proto", "pkg/connect", "pkg/graphql", "pkg/openapi",
	"pkg/services/postgres",
	"pkg/output/oci", "pkg/output/tarball", "pkg/output/appimage", "pkg/release",
}

// apiFunc is one exported function or method of a public package.
type apiFunc struct {
	pkg  string // package name ("golang")
	recv string // receiver type name, "" for a function
	name string
	sig  string // rendered declaration without body and comment
}

// apiType is one exported type with its first doc sentence.
type apiType struct {
	name, doc string
}

// apiSurface returns the exported functions, methods and types of dir (non-test files).
func apiSurface(t testing.TB, root, dir string) (pkgName string, funcs []apiFunc, types []apiType) {
	t.Helper()
	fset := token.NewFileSet()
	files, err := filepath.Glob(filepath.Join(root, dir, "*.go"))
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(files)
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		af, err := parser.ParseFile(fset, f, nil, parser.ParseComments)
		if err != nil {
			t.Fatal(err)
		}
		if buildIgnored(af) {
			continue
		}
		pkgName = af.Name.Name
		for _, d := range af.Decls {
			switch d := d.(type) {
			case *ast.FuncDecl:
				if !d.Name.IsExported() {
					continue
				}
				recv := ""
				if d.Recv != nil && len(d.Recv.List) > 0 {
					recv = recvName(d.Recv.List[0].Type)
					if !ast.IsExported(recv) {
						continue
					}
				}
				decl := *d
				decl.Doc, decl.Body = nil, nil
				var b bytes.Buffer
				if err := printer.Fprint(&b, token.NewFileSet(), &decl); err != nil {
					t.Fatal(err)
				}
				funcs = append(funcs, apiFunc{pkg: pkgName, recv: recv, name: d.Name.Name, sig: strings.Join(strings.Fields(b.String()), " ")})
			case *ast.GenDecl:
				if d.Tok != token.TYPE {
					continue
				}
				for _, s := range d.Specs {
					ts := s.(*ast.TypeSpec)
					if !ts.Name.IsExported() {
						continue
					}
					doc := ts.Doc
					if doc == nil {
						doc = d.Doc
					}
					types = append(types, apiType{name: ts.Name.Name, doc: firstSentence(doc.Text())})
				}
			}
		}
	}
	if pkgName == "" {
		t.Fatalf("%s: no Go files (update apiPackages in tests/docs/surface_test.go)", dir)
	}
	sort.Slice(funcs, func(i, j int) bool {
		if funcs[i].recv != funcs[j].recv {
			return funcs[i].recv < funcs[j].recv
		}
		return funcs[i].name < funcs[j].name
	})
	sort.Slice(types, func(i, j int) bool { return types[i].name < types[j].name })
	return pkgName, funcs, types
}

// buildIgnored reports a file with a build constraint that excludes it from the default
// build (a bobtest-only file).
func buildIgnored(f *ast.File) bool {
	for _, cg := range f.Comments {
		if cg.Pos() > f.Package {
			break
		}
		for _, c := range cg.List {
			if strings.HasPrefix(c.Text, "//go:build") && strings.Contains(c.Text, "bobtest") && !strings.Contains(c.Text, "!bobtest") {
				return true
			}
		}
	}
	return false
}

func recvName(e ast.Expr) string {
	switch e := e.(type) {
	case *ast.StarExpr:
		return recvName(e.X)
	case *ast.IndexExpr:
		return recvName(e.X)
	case *ast.IndexListExpr:
		return recvName(e.X)
	case *ast.Ident:
		return e.Name
	}
	return ""
}

func firstSentence(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if i := strings.Index(s, ". "); i >= 0 {
		s = s[:i+1]
	}
	return s
}

// envLiteral is a string literal that names a BOB_* environment variable.
var envLiteral = regexp.MustCompile(`^BOB_[A-Z0-9_]+$`)

// envVars returns every BOB_* string literal (also in the form "BOB_X=", the start of an
// environment entry) in the non-test, non-bobtest Go files below dirs, with one file that
// uses it. BOB_TEST_* names are harness hooks and are left out.
func envVars(t testing.TB, root string, dirs ...string) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, dir := range dirs {
		err := filepath.WalkDir(filepath.Join(root, dir), func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				if n := d.Name(); n == "testdata" || n == "node_modules" || strings.HasPrefix(n, ".") {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			fset := token.NewFileSet()
			af, err := parser.ParseFile(fset, path, nil, parser.ParseComments)
			if err != nil {
				return fmt.Errorf("%s: %w", path, err)
			}
			if buildIgnored(af) {
				return nil
			}
			rel, _ := filepath.Rel(root, path)
			ast.Inspect(af, func(n ast.Node) bool {
				bl, ok := n.(*ast.BasicLit)
				if !ok || bl.Kind != token.STRING {
					return true
				}
				s, err := strconv.Unquote(bl.Value)
				s = strings.TrimSuffix(s, "=") // "BOB_DEV_SHELL="+name sets a variable too
				if err != nil || !envLiteral.MatchString(s) || strings.HasPrefix(s, "BOB_TEST_") {
					return true
				}
				if _, ok := out[s]; !ok {
					out[s] = filepath.ToSlash(rel)
				}
				return true
			})
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	return out
}
