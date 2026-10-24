package eval

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/megakuul/bob/pkg/engine/ir"
)

// writeRepo writes files (repo-relative path → content) below a new directory named name.
func writeRepo(t *testing.T, name string, files map[string]string) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), name)
	for p, c := range files {
		full := filepath.Join(root, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func evaluate(t *testing.T, root string, patterns ...string) (*Result, error) {
	t.Helper()
	if testing.Short() {
		t.Skip("evaluations need the sandbox and a Go toolchain")
	}
	if len(patterns) == 0 {
		patterns = []string{"//..."}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	return Evaluate(ctx, Request{Root: root, Patterns: patterns})
}

// coreRepo uses only pkg/bob: a root package, a library directory with ordinary Go code next
// to bob.go, and a main package directory.
func coreRepo() map[string]string {
	return map[string]string{
		"go.mod":   "module example.com/core\n\ngo 1.26.0\n",
		"bob.lock": "{\n  \"lockVersion\": 1\n}\n",
		"bob.go": `//go:build bob

package core

import "github.com/megakuul/bob/pkg/bob"

var _ = bob.Repo(bob.RepoOpts{Nixpkgs: "nixos-26.05"})
`,
		"lib/lib.go": "// Package lib is ordinary Go code.\npackage lib\n\n// Answer is 42.\nconst Answer = 42\n",
		"lib/bob.go": `//go:build bob

package lib

import "github.com/megakuul/bob/pkg/bob"

// Gen is //lib:gen.
var Gen = bob.Define(func(p *bob.P) *bob.Step {
	return bob.Run(p, "gen", bob.RunOpts{Cmd: []string{"true"}, Outputs: []string{"out"}})
})
`,
		"cmd/app/main.go": "// Command app does nothing.\npackage main\n\nfunc main() {}\n",
		"cmd/app/bob.go": `//go:build bob

package main

import (
	"example.com/core/lib"
	"github.com/megakuul/bob/pkg/bob"
)

// App is //cmd/app:app.
var App = bob.Define(func(p *bob.P) *bob.Step {
	return bob.Run(p, "app", bob.RunOpts{Cmd: []string{"true"}, Inputs: map[string]bob.Tree{"gen": lib.Gen(p)}})
})
`,
	}
}

func TestEvaluateCoreRepo(t *testing.T) {
	a := writeRepo(t, "core", coreRepo())
	res, err := evaluate(t, a)
	if err != nil {
		t.Fatal(err)
	}
	if res.Cached {
		t.Errorf("first evaluation reports cached")
	}
	var doc struct {
		BobVersion string `json:"bobVersion"`
		Nodes      []struct {
			Label     string `json:"label"`
			DefinedAt struct {
				File string `json:"file"`
				Line int    `json:"line"`
			} `json:"definedAt"`
		} `json:"nodes"`
	}
	if err := json.Unmarshal(res.IR, &doc); err != nil {
		t.Fatalf("IR: %v\n%s", err, res.IR)
	}
	if doc.BobVersion != BobVersion() {
		t.Errorf("bobVersion %q, want %q", doc.BobVersion, BobVersion())
	}
	at := map[string]string{}
	for _, n := range doc.Nodes {
		at[n.Label] = n.DefinedAt.File + ":" + itoa(n.DefinedAt.Line)
	}
	// The main package's bob.go is evaluated through its renamed copy, at its own position.
	if at["//cmd/app:app"] != "cmd/app/bob.go:12" || at["//lib:gen"] != "lib/bob.go:9" {
		t.Errorf("labels and positions: %v", at)
	}

	// Unchanged repository: the evaluator binary is reused.
	again, err := evaluate(t, a)
	if err != nil {
		t.Fatal(err)
	}
	if !again.Cached || !bytes.Equal(again.IR, res.IR) {
		t.Errorf("second evaluation: cached=%v, same IR=%v", again.Cached, bytes.Equal(again.IR, res.IR))
	}

	// Another checkout path, a broken Go file next to a bob.go: same IR (R-FW-043 a, b).
	files := coreRepo()
	files["lib/broken.go"] = "package lib\n\nvar X = generatedByBob\n"
	b := writeRepo(t, "elsewhere", files)
	other, err := evaluate(t, b)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(other.IR, res.IR) {
		t.Errorf("IR differs between two checkouts:\n%s\n%s", res.IR, other.IR)
	}

	// A body change rebuilds.
	p := filepath.Join(a, "lib", "bob.go")
	src, _ := os.ReadFile(p)
	if err := os.WriteFile(p, bytes.Replace(src, []byte(`"out"`), []byte(`"out2"`), 1), 0o644); err != nil {
		t.Fatal(err)
	}
	changed, err := evaluate(t, a)
	if err != nil {
		t.Fatal(err)
	}
	if changed.Cached || bytes.Equal(changed.IR, res.IR) {
		t.Errorf("a changed bob.go: cached=%v, IR unchanged=%v", changed.Cached, bytes.Equal(changed.IR, res.IR))
	}
}

func TestEvaluateTypeErrorInMainPackage(t *testing.T) {
	files := coreRepo()
	src := files["cmd/app/bob.go"]
	line := strings.Count(src, "\n") + 2
	files["cmd/app/bob.go"] = src + "\nvar _ int = \"not an int\"\n"
	_, err := evaluate(t, writeRepo(t, "core", files))
	var errs ir.Errors
	if !errors.As(err, &errs) {
		t.Fatalf("err = %v, want ir.Errors", err)
	}
	text := err.Error()
	if !regexp.MustCompile(`cmd/app/bob\.go:` + itoa(line) + `:13: `).MatchString(text) {
		t.Errorf("type error not at cmd/app/bob.go:%d:13:\n%s", line, text)
	}
	if strings.Contains(text, "bobmain") || strings.Contains(text, ".bob/eval") || strings.Contains(text, "/src/") {
		t.Errorf("message names generated files:\n%s", text)
	}
	if errs[0].Code != 1020 || errs[0].Location == nil || errs[0].Location.File != "cmd/app/bob.go" {
		t.Errorf("error %+v", errs[0])
	}
}

func TestEvaluateEvaluatorDiagnostics(t *testing.T) {
	files := coreRepo()
	files["lib/bob.go"] += `
// Dup has the label of Gen.
var Dup = bob.Define(func(p *bob.P) *bob.Step {
	return bob.Run(p, "gen", bob.RunOpts{Cmd: []string{"false"}, Outputs: []string{"out"}})
})
`
	_, err := evaluate(t, writeRepo(t, "core", files))
	var errs ir.Errors
	if !errors.As(err, &errs) || len(errs) == 0 {
		t.Fatalf("err = %v, want ir.Errors", err)
	}
	if !strings.Contains(err.Error(), "lib/bob.go") || strings.Contains(err.Error(), "bob::") {
		t.Errorf("duplicate label error: %v", err)
	}
}

// The rule fixtures of tests/core (read-only use).
func rulesRepo(t *testing.T, rule string) string {
	t.Helper()
	src := filepath.Join("..", "..", "..", "tests", "core", "testdata", "repos", "rules", rule)
	dst := filepath.Join(t.TempDir(), rule)
	if err := os.CopyFS(dst, os.DirFS(src)); err != nil {
		t.Fatal(err)
	}
	return dst
}

func TestRules(t *testing.T) {
	cases := map[string][]string{
		"r1": {`lib/bob.go: the first line must be "//go:build bob" so that go build, go vet and go test ignore it`},
		"r2": {`lib/bob.go: package other, but the Go code in this directory is package lib; bob.go must use package lib`},
		"r3": {`lib/bob.go: imports the build definitions of cmd/app/, which is a main package and cannot be imported; define the shared package in a bob.go of a non-main directory`},
		"r4": {`lib/bob.go:8: Files is also declared at lib/lib.go:5; bob.go shares its package with that code, rename one of them`},
		"r6": {`b/bob.go imports a, whose Go code imports b (cycle exists only for build definitions); move the definition`},
		"r7": {`lib/bob.go: imports github.com/megakuul/bob/pkg/engine/ir, which build definitions cannot use; bob.go may import the bob framework, the standard library and other bob.go packages`},
		"all": {
			`a/bob.go: the first line must be "//go:build bob" so that go build, go vet and go test ignore it`,
			`b/bob.go: imports github.com/megakuul/bob/pkg/engine/ir, which build definitions cannot use; bob.go may import the bob framework, the standard library and other bob.go packages`,
		},
	}
	for rule, want := range cases {
		t.Run(rule, func(t *testing.T) {
			_, err := evaluate(t, rulesRepo(t, rule))
			var errs ir.Errors
			if !errors.As(err, &errs) {
				t.Fatalf("err = %v, want rule violations", err)
			}
			var got []string
			for _, e := range errs {
				if e.Code != 1024 {
					t.Errorf("%s: code %s, want BOB-E-1024", e.Error(), e.Code.ID())
				}
				got = append(got, e.Error())
			}
			if strings.Join(got, "\n") != strings.Join(want, "\n") {
				t.Errorf("violations:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
			}
		})
	}
}

// TestRuleR2Root (A131): a root bob.go without non-test Go code next to it may use any package
// name (the checkout directory's name differs between machines); a root with Go code and a
// non-root directory without Go code still follow R2.
func TestRuleR2Root(t *testing.T) {
	cases := []struct{ dir, want string }{
		{"npm", "npm"}, {"cpp-lib", "cpplib"}, {"My.Repo", "myrepo"}, {"9lives", "x9lives"}, {"---", "x"},
	}
	for _, c := range cases {
		if got := expectedPkg(path.Join("a", c.dir)); got != c.want {
			t.Errorf("expectedPkg(%q) = %q, want %q", c.dir, got, c.want)
		}
	}
	// Any name at the root (coreRepo's root is package core in a directory named src).
	if _, err := evaluate(t, writeRepo(t, "src", coreRepo())); err != nil {
		t.Errorf("root package core in directory src: %v", err)
	}
	// A root with Go code must use its package.
	files := coreRepo()
	files["root.go"] = "package other\n"
	_, err := evaluate(t, writeRepo(t, "core", files))
	want := `bob.go: package core, but the Go code in this directory is package other; bob.go must use package other`
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Errorf("root with Go code: err = %v, want %q", err, want)
	}
	// A non-root directory without Go code uses its name.
	files = coreRepo()
	files["tools/bob.go"] = "//go:build bob\n\npackage wrong\n"
	_, err = evaluate(t, writeRepo(t, "core", files))
	want = `tools/bob.go: package wrong, but the Go code in this directory is package tools; bob.go must use package tools`
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Errorf("non-root dir: err = %v, want %q", err, want)
	}
}

func TestRuleR5Stub(t *testing.T) {
	root := rulesRepo(t, "r5")
	if out, err := exec.Command("git", "-C", root, "init", "-q").CombinedOutput(); err != nil {
		t.Skipf("git: %v %s", err, out)
	}
	scan, err := scanRepo(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := scan.load(root); err != nil {
		t.Fatal(err)
	}
	own := map[string]*bobFile{}
	for _, d := range scan.bobDirs {
		own[d] = parseBob(d, bobPath(d), scan.content[bobPath(d)])
	}
	rc := &ruleCheck{root: root, module: "example.com/rules/r5", scan: scan, own: own, deps: &depSet{mods: map[string]*depModule{}},
		fwPkgs: map[string]bool{frameworkModule + "/pkg/bob": true, frameworkModule + "/pkg/cc": true}}
	errs, stubs := rc.check()
	if len(errs) > 0 || len(stubs) != 1 || stubs[0] != "native" {
		t.Fatalf("violations %v, stubs %v", errs, stubs)
	}
	if err := writeCgoStub(root, "native", "native"); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(root, "native", "zz_bob_cgo.go"))
	if string(b) != "//go:build bob && cgo\n\npackage native\n\nimport \"C\"\n" {
		t.Errorf("stub %q", b)
	}
	if out, err := exec.Command("git", "-C", root, "check-ignore", "-q", "native/zz_bob_cgo.go").CombinedOutput(); err != nil {
		t.Errorf("stub not ignored by git: %v %s", err, out)
	}
	// Idempotent.
	if err := writeCgoStub(root, "native", "native"); err != nil {
		t.Fatal(err)
	}
	ex, _ := os.ReadFile(filepath.Join(root, ".git", "info", "exclude"))
	if strings.Count(string(ex), "/native/zz_bob_cgo.go") != 1 {
		t.Errorf("exclude block:\n%s", ex)
	}
}

func TestDependencyBobRepo(t *testing.T) {
	base := t.TempDir()
	dep := filepath.Join(base, "dep")
	for p, c := range map[string]string{
		"go.mod": "module example.com/dep\n\ngo 1.26.0\n",
		"lib/bob.go": `//go:build bob

package lib

import "github.com/megakuul/bob/pkg/bob"

// Lib is example.com/dep//lib:lib.
var Lib = bob.Define(func(p *bob.P) *bob.Step {
	return bob.Run(p, "lib", bob.RunOpts{Cmd: []string{"true"}, Outputs: []string{"o"}})
})
`,
		"lib/lib.go": "package lib\n\nvar Broken = undefinedThing\n",
	} {
		full := filepath.Join(dep, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	files := coreRepo()
	files["go.mod"] = "module example.com/core\n\ngo 1.26.0\n\nrequire example.com/dep v1.0.0\n\nreplace example.com/dep => " + dep + "\n"
	files["lib/bob.go"] = strings.Replace(files["lib/bob.go"], `import "github.com/megakuul/bob/pkg/bob"`,
		"import (\n\tdeplib \"example.com/dep/lib\"\n\t\"github.com/megakuul/bob/pkg/bob\"\n)", 1)
	files["lib/bob.go"] = strings.Replace(files["lib/bob.go"], `Outputs: []string{"out"}`,
		`Outputs: []string{"out"}, Inputs: map[string]bob.Tree{"dep": deplib.Lib(p)}`, 1)
	res, err := evaluate(t, writeRepo(t, "core", files))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(res.IR, []byte(`"label":"example.com/dep//lib:lib"`)) {
		t.Errorf("IR lacks the dependency's package:\n%s", res.IR)
	}
}

func TestCompare(t *testing.T) {
	a := []byte(`{"irVersion":1,"nodes":[{"deps":["x","y"],"id":"//n:n","label":"//n:n"}]}`)
	b := []byte(`{"irVersion":1,"nodes":[{"deps":["y","x"],"id":"//n:n","label":"//n:n"}]}`)
	if err := Compare(a, a); err != nil {
		t.Errorf("identical: %v", err)
	}
	err := Compare(a, b)
	var errs ir.Errors
	if !errors.As(err, &errs) || errs[0].Code != 1018 {
		t.Fatalf("err = %v", err)
	}
	if got := errs[0].Error(); got != "evaluating bob.go is not deterministic: //n:n differs between two runs (field nodes[0].deps[0])" {
		t.Errorf("message %q", got)
	}
}

func TestParseGoMod(t *testing.T) {
	m := parseGoMod([]byte("// x\nmodule \"example.com/a\" // c\n\ngo 1.26.0\n\nrequire (\n\texample.com/b v1.2.0\n\texample.com/c v0.1.0 // indirect\n)\nrequire example.com/d v2.0.0\nreplace example.com/b => ../b\nreplace example.com/c v0.1.0 => example.com/c2 v0.1.1\n"))
	if m.module != "example.com/a" || m.goDir != "1.26.0" || len(m.require) != 3 || m.require["example.com/c"] != "v0.1.0" {
		t.Errorf("%+v", m)
	}
	if len(m.replace) != 1 || m.replace["example.com/b"] != "../b" {
		t.Errorf("replace %v", m.replace)
	}
}

func TestSemverLess(t *testing.T) {
	for _, c := range []struct {
		a, b string
		less bool
	}{{"v1.2.3", "v1.10.0", true}, {"v1.2.3", "v1.2.3", false}, {"v1.2.3-rc1", "v1.2.3", true}, {"v2.0.0", "v1.9.9", false}} {
		if got := semverLess(c.a, c.b); got != c.less {
			t.Errorf("semverLess(%s, %s) = %v", c.a, c.b, got)
		}
	}
}

func itoa(n int) string { return strconv.Itoa(n) }

func TestEvaluateConcurrently(t *testing.T) {
	if testing.Short() {
		t.Skip("evaluations need the sandbox and a Go toolchain")
	}
	root := writeRepo(t, "core", coreRepo())
	errs := make(chan error, 3)
	irs := make(chan []byte, 3)
	for i := 0; i < 3; i++ {
		go func() {
			res, err := evaluate(t, root)
			errs <- err
			if err == nil {
				irs <- res.IR
			}
		}()
	}
	for i := 0; i < 3; i++ {
		if err := <-errs; err != nil {
			t.Errorf("concurrent evaluation: %v", err)
		}
	}
	close(irs)
	var first []byte
	for b := range irs {
		if first == nil {
			first = b
		} else if !bytes.Equal(first, b) {
			t.Errorf("concurrent evaluations differ")
		}
	}
}

// rootMainRepo is a one-program repository: the root holds the main package and its bob.go
// (the quickstart layout, A450).
func rootMainRepo() map[string]string {
	return map[string]string{
		"go.mod":   "module example.com/hello\n\ngo 1.26.0\n",
		"bob.lock": "{\n  \"lockVersion\": 1\n}\n",
		"main.go":  "// Command hello does nothing.\npackage main\n\nfunc main() {}\n",
		"bob.go": `//go:build bob

package main

import "github.com/megakuul/bob/pkg/bob"

// Hello is //:hello.
var Hello = bob.Define(func(p *bob.P) *bob.Step {
	return bob.Run(p, "hello", bob.RunOpts{Cmd: []string{"true"}, Outputs: []string{"out"}})
})
`,
	}
}

// TestEvaluateRootMainPackage: a root bob.go in package main is evaluated at its own position
// (before A450 its copy was named <module>/.bob/eval/rename/0.go in the evaluator's frames:
// "ext.New(...) was called from , which is not a bob.go file of this build").
func TestEvaluateRootMainPackage(t *testing.T) {
	res, err := evaluate(t, writeRepo(t, "hello", rootMainRepo()))
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Nodes []struct {
			Label     string `json:"label"`
			DefinedAt struct {
				File string `json:"file"`
				Line int    `json:"line"`
			} `json:"definedAt"`
		} `json:"nodes"`
	}
	if err := json.Unmarshal(res.IR, &doc); err != nil {
		t.Fatalf("IR: %v\n%s", err, res.IR)
	}
	found := false
	for _, n := range doc.Nodes {
		if n.Label == "//:hello" {
			found = true
			if got := n.DefinedAt.File + ":" + itoa(n.DefinedAt.Line); got != "bob.go:9" {
				t.Errorf("//:hello defined at %s, want bob.go:9", got)
			}
		}
	}
	if !found {
		t.Errorf("no //:hello in the IR:\n%s", res.IR)
	}

	// A type error in the root main bob.go names bob.go, not the evaluator's copy.
	files := rootMainRepo()
	line := strings.Count(files["bob.go"], "\n") + 2
	files["bob.go"] += "\nvar _ int = \"not an int\"\n"
	_, err = evaluate(t, writeRepo(t, "hello", files))
	if err == nil || !regexp.MustCompile(`(^|\s)bob\.go:`+itoa(line)+`:13: `).MatchString(err.Error()) {
		t.Fatalf("type error: err = %v, want bob.go:%d:13", err, line)
	}
	if text := err.Error(); strings.Contains(text, "bobmain") || strings.Contains(text, renameDir) || strings.Contains(text, ".bob/eval") {
		t.Errorf("message names generated files:\n%s", text)
	}
}

// TestEvaluateWithoutGoMod: a new repository has no root go.mod; bob writes it after the first
// evaluation (A451). Before, Evaluate stopped with "the repository has no go.mod" (BOB-E-9005).
func TestEvaluateWithoutGoMod(t *testing.T) {
	files := coreRepo()
	delete(files, "go.mod")
	res, err := evaluate(t, writeRepo(t, "My_Repo", files))
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Module string `json:"module"`
	}
	if err := json.Unmarshal(res.IR, &doc); err != nil {
		t.Fatal(err)
	}
	// cmd/app/bob.go imports example.com/core/lib: the module path the bob.go files use.
	if doc.Module != "example.com/core" {
		t.Errorf("module %q, want example.com/core (from the bob.go imports)", doc.Module)
	}
	// Without such an import: the directory name.
	files = rootMainRepo()
	delete(files, "go.mod")
	res, err = evaluate(t, writeRepo(t, "My_Repo", files))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(res.IR, &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Module != "my_repo" {
		t.Errorf("module %q, want my_repo (the directory name)", doc.Module)
	}
}
