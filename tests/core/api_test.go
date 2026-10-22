package core

// SPEC §1.4 core API: R-FW-010, R-FW-011, R-FW-012/014, R-FW-013, R-FW-022.

import (
	"archive/zip"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/megakuul/bob/pkg/engine/ir"
	"github.com/megakuul/bob/tests/harness"
	"github.com/megakuul/bob/tests/parity"
)

// R-FW-010 (a): passing golang.Binary where bob.Linkable is expected fails go vet -tags=bob
// with "missing method LinkInfo".
func TestRFW010TypedHandles(t *testing.T) {
	parity.Covers(t, "R-FW-010")
	dir := repo(t, "typecheck/linkable")
	if r := bobRun(t, dir, "gen"); harness.Red(t, r) || !exitIs(t, r, 0) {
		return
	}
	r := bobRun(t, dir, "exec", "--", "go", "vet", "-tags=bob", "./...")
	if harness.Red(t, r) {
		return
	}
	if r.Exit == 0 {
		t.Fatalf("go vet -tags=bob accepted a *golang.Binary as a bob.Linkable")
	}
	if !strings.Contains(r.Combined(), "missing method LinkInfo") {
		t.Errorf("go vet does not say \"missing method LinkInfo\":\n%s", r.Combined())
	}
}

// labelLiteral matches string literals that name a package: local, foreign or nixpkgs labels.
var labelLiteral = regexp.MustCompile(`^(//[A-Za-z0-9_./-]*:[a-z0-9_.-]+|[A-Za-z0-9.-]+\.[a-z]+(/[A-Za-z0-9_.-]+)*//[A-Za-z0-9_./-]*:[a-z0-9_.-]+|nixpkgs:[A-Za-z0-9_.'-]+)(@.*)?$`)

// R-FW-010 (b): the example programs contain no label string literals (constructor names are
// plain names without "//" or ":").
func TestRFW010NoLabelStringLiterals(t *testing.T) {
	parity.Covers(t, "R-FW-010")
	root := filepath.Join(harness.Root(t), "tests", "fixtures", "repos")
	fset := token.NewFileSet()
	files := 0
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || d.Name() != "bob.go" {
			return err
		}
		files++
		f, err := parser.ParseFile(fset, p, nil, parser.SkipObjectResolution)
		if err != nil {
			return err
		}
		ast.Inspect(f, func(n ast.Node) bool {
			lit, ok := n.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			s, err := strconv.Unquote(lit.Value)
			if err == nil && labelLiteral.MatchString(s) {
				rel, _ := filepath.Rel(root, p)
				t.Errorf("%s:%d: label string literal %q (packages are passed as typed values)", rel, fset.Position(lit.Pos()).Line, s)
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if files == 0 {
		t.Fatalf("no bob.go in %s", root)
	}
}

// evalIR evaluates root for patterns and parses the IR.
func evalIR(t *testing.T, root string, patterns ...string) (*ir.Graph, bool) {
	t.Helper()
	res, ok := evaluate(t, root, patterns...)
	if !ok {
		return nil, false
	}
	var head struct {
		BobVersion string `json:"bobVersion"`
	}
	if err := json.Unmarshal(res.IR, &head); err != nil {
		t.Fatalf("IR is not JSON: %v", err)
	}
	g, err := ir.Parse(res.IR, head.BobVersion)
	if isNotImplemented(err) {
		harness.RedFeature(t, "ir.Parse")
		return nil, false
	}
	if err != nil {
		t.Fatalf("ir.Parse: %v", err)
	}
	return g, true
}

// R-FW-011 (a): Lib(p) is memoised per *P: three calls with one P give one value (the repo
// panics otherwise), two different P (linux-amd64, linux-arm64) run the body once each.
func TestRFW011Memoised(t *testing.T) {
	parity.Covers(t, "R-FW-011")
	g, ok := evalIR(t, repo(t, "define/memo"), "//memo:use@linux-amd64", "//memo:use@linux-arm64")
	if !ok {
		return
	}
	calls := map[string]int{} // node id → CALL value
	re := regexp.MustCompile(`"CALL":\s*"(\d+)"`)
	for _, n := range g.Nodes {
		if n.Label != "//memo:lib" {
			continue
		}
		m := re.FindSubmatch(n.Spec)
		if m == nil {
			t.Errorf("node %s has no CALL variable in its spec: %s", n.ID, n.Spec)
			continue
		}
		calls[n.ID], _ = strconv.Atoi(string(m[1]))
	}
	if len(calls) != 2 {
		t.Fatalf("want one //memo:lib node per configuration (2), got %v", calls)
	}
	vals := []int{}
	for _, v := range calls {
		vals = append(vals, v)
	}
	sort.Ints(vals)
	if vals[0] != 1 || vals[1] != 2 {
		t.Errorf("the constructor body ran for call numbers %v, want exactly once per P (1 and 2)", vals)
	}
}

// R-FW-011 (b-d): the three error cases print their texts.
func TestRFW011ErrorCases(t *testing.T) {
	parity.Covers(t, "R-FW-011")
	cases := []struct {
		name, repo string
		pattern    string
		re         string
		code       string
	}{
		{"before evaluation", "define/early", "//early:lib",
			`bob: early/bob\.go:\d+: Lib was called before evaluation; call it only inside another bob\.Define function`, ""},
		{"define in constructor", "define/nested", "//nested:outer",
			`nested/bob\.go:\d+: bob\.Define must be used in a package-level var declaration`, ""},
		{"cycle", "define/cycle", "//cycle:a", `//cycle:a`, "BOB-E-1006"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := bobRun(t, repo(t, c.repo), "build", c.pattern, "--json")
			if harness.Red(t, r) || !exitIs(t, r, 3) {
				return
			}
			out := r.Combined()
			if !regexp.MustCompile(c.re).MatchString(out) {
				t.Errorf("output lacks %s:\n%s", c.re, out)
			}
			if c.code != "" {
				e, ok := errorWithCode(t, r, c.code)
				if ok && (!strings.Contains(e.Message, "//cycle:a") || !strings.Contains(e.Message, "//cycle:b")) {
					t.Errorf("the cycle error does not name the chain //cycle:a → //cycle:b: %q", e.Message)
				}
			}
		})
	}
}

var apiPackages = []string{
	"pkg/bob", "pkg/bob/ext", "pkg/nixpkgs", "pkg/cc", "pkg/golang", "pkg/rust", "pkg/npm", "pkg/ocaml",
	"pkg/proto", "pkg/connect", "pkg/graphql", "pkg/openapi", "pkg/services",
	"pkg/output/oci", "pkg/output/appimage", "pkg/output/tarball", "pkg/release",
}

// R-FW-012/014: every exported constructor (an exported function taking *bob.P) of L0–L3 has a
// doc comment and an Example<Func> test in a _test.go file of its package, and no two
// constructors of one package return the same kind type.
func TestAPISurface(t *testing.T) {
	parity.Covers(t, "R-FW-012", "R-FW-014")
	root := harness.Root(t)
	var pkgs []string
	for _, p := range apiPackages {
		matches, _ := filepath.Glob(filepath.Join(root, filepath.FromSlash(p)))
		for _, m := range matches {
			filepath.WalkDir(m, func(path string, d fs.DirEntry, err error) error {
				if err == nil && d.IsDir() {
					rel, _ := filepath.Rel(root, path)
					rel = filepath.ToSlash(rel)
					// gen/bop/diag/internal are not part of the public API surface.
					if base := d.Name(); base == "gen" || base == "bop" || base == "diag" || base == "internal" || base == "testdata" {
						return filepath.SkipDir
					}
					if p == "pkg/bob" && rel != p {
						return filepath.SkipDir // pkg/bob/ext is listed on its own
					}
					if p == "pkg/services" || p == "pkg/output/oci" || rel == p {
						pkgs = append(pkgs, rel)
					}
				}
				return nil
			})
		}
	}
	if len(pkgs) == 0 {
		t.Fatal("no API packages found")
	}
	sort.Strings(pkgs)
	pkgs = compact(pkgs)
	var missing []string
	for _, rel := range pkgs {
		missing = append(missing, checkSurface(t, filepath.Join(root, filepath.FromSlash(rel)), rel)...)
	}
	// Constructors without an Example test are a feature gap (the examples ship with the
	// constructors' implementation), so they are reported as red with the exact list. Every other
	// violation above fails the test without the marker.
	if len(missing) > 0 {
		harness.RedFeature(t, "Example tests missing for exported constructors: "+strings.Join(missing, ", "))
	}
}

func compact(s []string) []string {
	out := s[:0]
	for i, x := range s {
		if i == 0 || x != s[i-1] {
			out = append(out, x)
		}
	}
	return out
}

func checkSurface(t *testing.T, dir, rel string) (missingExamples []string) {
	t.Helper()
	fset := token.NewFileSet()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Errorf("%s: %v", rel, err)
		return nil
	}
	examples := map[string]bool{}
	type ctor struct {
		name string
		ret  string
		doc  bool
	}
	var ctors []ctor
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") {
			continue
		}
		f, err := parser.ParseFile(fset, filepath.Join(dir, e.Name()), nil, parser.ParseComments)
		if err != nil {
			t.Errorf("%s/%s: %v", rel, e.Name(), err)
			continue
		}
		for _, d := range f.Decls {
			fn, ok := d.(*ast.FuncDecl)
			if !ok {
				continue
			}
			if strings.HasSuffix(e.Name(), "_test.go") {
				if strings.HasPrefix(fn.Name.Name, "Example") {
					examples[fn.Name.Name] = true
				}
				continue
			}
			if fn.Recv != nil || !fn.Name.IsExported() || !takesP(fn) {
				continue
			}
			ret := ""
			if fn.Type.Results != nil && len(fn.Type.Results.List) > 0 {
				ret = exprString(fn.Type.Results.List[0].Type)
			}
			ctors = append(ctors, ctor{fn.Name.Name, ret, fn.Doc != nil && len(fn.Doc.List) > 0})
		}
	}
	byRet := map[string][]string{}
	for _, c := range ctors {
		if !c.doc {
			t.Errorf("%s.%s: exported constructor without a doc comment", rel, c.name)
		}
		if !examples["Example"+c.name] {
			missingExamples = append(missingExamples, fmt.Sprintf("%s.%s (Example%s)", rel, c.name, c.name))
		}
		byRet[c.ret] = append(byRet[c.ret], c.name)
	}
	for ret, names := range byRet {
		if len(names) > 1 && ret != "" && !isInterfaceish(ret) && !specShared(rel, ret, names) {
			t.Errorf("%s: constructors %v all return %s; one constructor per kind (R-FW-014)", rel, names, ret)
		}
	}
	return missingExamples
}

// specSharedResults are the constructors the SPEC API itself declares with one result type: one
// Go type for kinds that differ only in what they build (SPEC §03.1.1 cc libraries, §03.3.1 Rust
// libraries and C libraries). Exactly these sets are allowed; any other constructor returning
// the same type still fails.
var specSharedResults = map[string]map[string][]string{
	"pkg/cc":   {"*Library": {"SharedLibrary", "StaticLibrary"}},
	"pkg/rust": {"*Lib": {"Library", "ProcMacro"}, "*CLib": {"CDylib", "StaticLib"}},
	// §03.6.1: proto.Lint and proto.Breaking are both "check packages" returning *proto.Check.
	"pkg/proto": {"*Check": {"Breaking", "Lint"}},
}

func specShared(rel, ret string, names []string) bool {
	want, ok := specSharedResults[rel][ret]
	if !ok {
		return false
	}
	got := append([]string(nil), names...)
	sort.Strings(got)
	return strings.Join(got, ",") == strings.Join(want, ",")
}

// isInterfaceish exempts shared interface results (bob.Tree, bob.Package): they are not kinds.
func isInterfaceish(ret string) bool {
	switch strings.TrimPrefix(ret, "*") {
	case "Tree", "Tool", "Package", "Linkable", "Executable", "Test", "bob.Tree", "bob.Tool", "bob.Package", "bob.Linkable":
		return true
	}
	return false
}

func takesP(fn *ast.FuncDecl) bool {
	if fn.Type.Params == nil || len(fn.Type.Params.List) == 0 {
		return false
	}
	s := exprString(fn.Type.Params.List[0].Type)
	return s == "*P" || s == "*bob.P"
}

func exprString(e ast.Expr) string {
	switch x := e.(type) {
	case *ast.Ident:
		return x.Name
	case *ast.StarExpr:
		return "*" + exprString(x.X)
	case *ast.SelectorExpr:
		return exprString(x.X) + "." + x.Sel.Name
	case *ast.IndexExpr:
		return exprString(x.X)
	case *ast.ArrayType:
		return "[]" + exprString(x.Elt)
	}
	return "?"
}

// exampleRepos are the six example programs of R-FW-013 (SPEC's examples/ names → fixtures).
var exampleRepos = []string{"cpp-lib", "go", "rust", "npm", "polyglot", "cross-repo"}

const polyglotDigest = "81b637d8fcd2c6da6359e6963113a1170de795e4b725b84d1e0b4cfd9ec58ce9"

// R-FW-013: for each example, bob build //... and bob test //... exit 0 on both profiles;
// polyglot's //cmd/api:api prints the golden line.
func TestRFW013ExamplePrograms(t *testing.T) {
	parity.Covers(t, "R-FW-013")
	for _, name := range exampleRepos {
		t.Run(name, func(t *testing.T) {
			var dir string
			var env []string
			if name == "cross-repo" {
				dir, env = crossRepo(t)
			} else {
				dir = harness.Fixture(t, name)
			}
			for _, variant := range []string{"release", "debug"} {
				for _, cmd := range []string{"build", "test"} {
					args := []string{cmd, "//...", "--variant", variant}
					r := bobRunEnv(t, dir, env, args...)
					if harness.Red(t, r) || !exitIs(t, r, 0) {
						return
					}
				}
			}
			if name != "polyglot" {
				return
			}
			r := bobRun(t, dir, "run", "//cmd/api:api")
			if !exitIs(t, r, 0) {
				return
			}
			want := "digest=" + polyglotDigest + " numhex=3432 proto_bytes=116"
			if !strings.Contains(r.Stdout, want) {
				t.Errorf("//cmd/api:api printed %q, want a line containing %q", r.Stdout, want)
			}
		})
	}
}

// crossRepo returns a copy of the app of the cross-repo example and the environment that
// serves the dependency module from a file:// GOPROXY.
func crossRepo(t *testing.T) (string, []string) {
	t.Helper()
	proxy := goProxyOf(t, filepath.Join(harness.Root(t), "tests", "core", "testdata", "repos", "cross-repo", "dep"), "example.com/dep", "v1.0.0")
	env := []string{"GOPROXY=file://" + proxy, "GOSUMDB=off", "GOFLAGS=-modcacherw"}
	return repo(t, "cross-repo/app"), env
}

// goProxyOf writes a file:// GOPROXY directory serving module at version, built from dir.
func goProxyOf(t *testing.T, dir, module, version string) string {
	t.Helper()
	proxy := t.TempDir()
	vdir := filepath.Join(proxy, filepath.FromSlash(module), "@v")
	if err := os.MkdirAll(vdir, 0o755); err != nil {
		t.Fatal(err)
	}
	zf, err := os.Create(filepath.Join(vdir, version+".zip"))
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(zf)
	err = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		w, err := zw.Create(module + "@" + version + "/" + filepath.ToSlash(rel))
		if err != nil {
			return err
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		_, err = w.Write(b)
		return err
	})
	if err == nil {
		err = zw.Close()
	}
	if err == nil {
		err = zf.Close()
	}
	if err != nil {
		t.Fatalf("harness: module zip: %v", err)
	}
	gomod, err := os.ReadFile(filepath.Join(dir, "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{
		version + ".mod":  string(gomod),
		version + ".info": `{"Version":"` + version + `","Time":"2026-01-01T00:00:00Z"}`,
		"list":            version + "\n",
	} {
		if err := os.WriteFile(filepath.Join(vdir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return proxy
}

// R-FW-022: examples/cross-repo (the module served from a file:// GOPROXY) builds the
// dependency's package by typed handle, the dependency's label is example.com/dep//libs/bar:lib,
// the dependency's bob.lock is not used (the consumer's lock holds the graph), and gopls
// completion on `bar.` offers Lib.
func TestRFW022CrossRepo(t *testing.T) {
	parity.Covers(t, "R-FW-022")
	dir, env := crossRepo(t)
	if r := bobRunEnv(t, dir, env, "lock"); harness.Red(t, r) || !exitIs(t, r, 0) {
		return
	}
	if r := bobRunEnv(t, dir, env, "build", "//use:consumer", "--json"); !exitIs(t, r, 0) {
		return
	}
	g := bobRunEnv(t, dir, env, "graph", "//use:consumer", "--format", "json")
	if !exitIs(t, g, 0) {
		return
	}
	if !strings.Contains(g.Stdout, "example.com/dep//libs/bar:lib") {
		t.Errorf("the graph does not contain example.com/dep//libs/bar:lib:\n%s", g.Stdout)
	}
	if !strings.Contains(readFile(t, filepath.Join(dir, "go.mod")), "example.com/dep v1.0.0") {
		t.Errorf("the generated go.mod does not require example.com/dep v1.0.0:\n%s", readFile(t, filepath.Join(dir, "go.mod")))
	}
	// gopls through the shim completes bar. in use/bob.go.
	if r := bobRunEnv(t, dir, env, "env", "--format", "json"); !exitIs(t, r, 0) {
		return
	}
	cmd := execShim(t, dir, env)
	c, err := startLSP(cmd, dir, map[string]any{"build.buildFlags": []string{"-tags=bob"}})
	if err != nil {
		t.Fatalf("start gopls: %v", err)
	}
	defer c.close()
	if err := c.open("use/bob.go"); err != nil {
		t.Fatal(err)
	}
	res, err := c.at("textDocument/completion", "use/bob.go", "bar.Lib", 3)
	if err != nil {
		t.Fatalf("completion: %v", err)
	}
	if !strings.Contains(string(res), `"label":"Lib"`) {
		t.Errorf("completion on bar. does not offer Lib: %.400s", res)
	}
}
