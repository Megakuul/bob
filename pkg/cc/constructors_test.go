package cc

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// The fixtures and SPEC §03.1 call only functions pkg/cc declares (A108: programs and tests are
// made with cc.NewBinary / cc.NewTest; Binary and Test are the types), and SPEC.md carries
// §03.1.1 exactly as spec/03-ecosystems.md does.
func TestConstructorsExist(t *testing.T) {
	funcs := map[string]bool{}
	fset := token.NewFileSet()
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		af, err := parser.ParseFile(fset, f, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range af.Decls {
			if fd, ok := d.(*ast.FuncDecl); ok && fd.Recv == nil && fd.Name.IsExported() {
				funcs[fd.Name.Name] = true
			}
		}
	}
	for _, want := range []string{"NewBinary", "NewTest"} {
		if !funcs[want] {
			t.Errorf("pkg/cc has no function %s", want)
		}
	}

	// Calls cc.X(...) in the fixture repositories' bob.go files.
	repos := filepath.Join("..", "..", "tests", "fixtures", "repos")
	var bad []string
	err = filepath.WalkDir(repos, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || d.Name() != "bob.go" {
			return err
		}
		af, err := parser.ParseFile(fset, p, nil, 0)
		if err != nil {
			return err
		}
		ast.Inspect(af, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
				if x, ok := sel.X.(*ast.Ident); ok && x.Name == "cc" && !funcs[sel.Sel.Name] {
					bad = append(bad, fset.Position(call.Pos()).String()+": cc."+sel.Sel.Name)
				}
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	// Calls in SPEC §03.1 (C / C++), in both copies of the SPEC.
	section := func(file string) string {
		b, err := os.ReadFile(filepath.Join("..", "..", file))
		if err != nil {
			t.Fatal(err)
		}
		s := string(b)
		i := strings.Index(s, "### §03.1.1 API")
		j := strings.Index(s, "### §03.1.2 ")
		if i < 0 || j < i {
			t.Fatalf("%s: §03.1.1 not found", file)
		}
		return s[i:j]
	}
	split, whole := section("spec/03-ecosystems.md"), section("SPEC.md")
	if split != whole {
		t.Errorf("§03.1.1 differs between spec/03-ecosystems.md and SPEC.md")
	}
	callRe := regexp.MustCompile(`\bcc\.([A-Z][A-Za-z0-9]*)\(`)
	for _, m := range callRe.FindAllStringSubmatch(split, -1) {
		if !funcs[m[1]] {
			bad = append(bad, "SPEC §03.1.1: cc."+m[1])
		}
	}
	sort.Strings(bad)
	for _, b := range bad {
		t.Errorf("call of a function pkg/cc does not declare: %s", b)
	}
}
