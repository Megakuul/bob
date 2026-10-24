package gen

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/megakuul/bob/pkg/engine"
	"github.com/megakuul/bob/pkg/engine/ir"
	"github.com/megakuul/bob/pkg/golang/resolve"
)

// TestFrameworkGo keeps frameworkGo equal to the language version of bob's own go.mod.
func TestFrameworkGo(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "..", "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	m, err := resolve.ParseModFile(b)
	if err != nil {
		t.Fatal(err)
	}
	if resolve.GoLang(m.Go) != frameworkGo {
		t.Errorf("frameworkGo = %s, bob's go.mod says go %s", frameworkGo, m.Go)
	}
}

// TestRootGoMod: the root go.mod requires the framework and every bob.Import (R-FW-022);
// a go.mod from go mod init is adopted, an added requirement is drift.
func TestRootGoMod(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		"bob.go":         "//go:build bob\n\npackage app\n\nimport \"github.com/megakuul/bob/pkg/bob\"\n\nvar _ = bob.Import(\"example.com/dep\", \"v1.0.0\")\n",
		"use/bob.go":     "//go:build bob\n\npackage use\n\nimport b \"github.com/megakuul/bob/pkg/bob\"\n\nvar _ = b.Import(\"example.com/dep\", \"v1.2.0\")\nvar _ = b.Import(\"example.com/other\", \"v0.1.0\")\n",
		"x/go.mod":       "module example.com/x\n",
		"x/bob.go":       "//go:build bob\n\npackage x\n\nimport \"github.com/megakuul/bob/pkg/bob\"\n\nvar _ = bob.Import(\"example.com/ignored\", \"v1.0.0\")\n",
		"gen/go.mod":     engine.GoModHeader + "\nmodule example.com/gen\n",
		"gen/bob.go":     "//go:build bob\n\npackage gen\n\nimport \"github.com/megakuul/bob/pkg/bob\"\n\nvar _ = bob.Import(\"example.com/third\", \"v2.0.0+incompatible\")\n",
		".hidden/bob.go": "//go:build bob\n\npackage h\n\nimport \"github.com/megakuul/bob/pkg/bob\"\n\nvar _ = bob.Import(\"example.com/hidden\", \"v1.0.0\")\n",
	})
	f, ok, err := RootGoMod(&ir.Graph{Module: "example.com/app", BobVersion: "v0.0.0-dev"}, root)
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	want := engine.GoModHeader + "\n// Edit bob.go and run bob lock.\nmodule example.com/app\n\ngo 1.26.0\n\nrequire (\n" +
		"\texample.com/dep v1.2.0\n\texample.com/other v0.1.0\n\texample.com/third v2.0.0+incompatible\n\tgithub.com/megakuul/bob v0.0.0-dev\n)\n"
	if string(f.Content) != want {
		t.Errorf("root go.mod:\n%s\nwant:\n%s", f.Content, want)
	}
	if !f.Committed || f.Path != "go.mod" || !strings.Contains(f.Owner, "//") {
		t.Errorf("file %+v", f)
	}
	if d := f.Explain([]byte("module example.com/app\n\ngo 1.26.0\n")); len(d) != 0 {
		t.Errorf("go mod init's go.mod is drift: %v", d)
	}
	if d := f.Explain([]byte("module example.com/app\n\ngo 1.26.0\n\nrequire golang.org/x/text v0.20.0\n")); len(d) != 1 || d[0].Op != "+" {
		t.Errorf("an added requirement: %v", d)
	}
	writeTree(t, root, map[string]string{"bad/bob.go": "//go:build bob\n\npackage bad\n\nimport \"github.com/megakuul/bob/pkg/bob\"\n\nvar _ = bob.Import(\"example.com/dep\", \"latest\")\n"})
	if _, _, err := RootGoMod(&ir.Graph{Module: "example.com/app"}, root); err == nil || !strings.Contains(err.Error(), "not a module version") {
		t.Errorf("invalid version: %v", err)
	}
}

// TestRootGoSum: the root go.sum holds the framework's lines for the version the root go.mod
// requires (I-M5b NEEDS 2); nothing for a graph without a module or a framework version.
func TestRootGoSum(t *testing.T) {
	sum := "github.com/megakuul/bob v0.0.0-dev h1:AAA=\ngithub.com/megakuul/bob v0.0.0-dev/go.mod h1:BBB=\n"
	f, ok := RootGoSum(&ir.Graph{Module: "example.com/app", BobVersion: "v0.0.0-dev"}, "", sum)
	if !ok || f.Path != "go.sum" || !f.Committed || string(f.Content) != sum {
		t.Fatalf("RootGoSum = %+v, %v", f, ok)
	}
	if d := f.Explain([]byte(sum)); len(d) != 0 {
		t.Errorf("unchanged go.sum is drift: %v", d)
	}
	if _, ok := RootGoSum(&ir.Graph{Module: "example.com/app", BobVersion: "v1.0.0"}, "", sum); ok {
		t.Error("lines of another framework version were used")
	}
	if _, ok := RootGoSum(&ir.Graph{BobVersion: "v0.0.0-dev"}, "", sum); ok {
		t.Error("a graph without a module got a go.sum")
	}
}

// TestRootGoSumImports: a bob.Import module the Go module cache holds gets its go.sum lines
// from the cache's download records (R-FW-022); one not downloaded gets none.
func TestRootGoSumImports(t *testing.T) {
	root, cache := t.TempDir(), t.TempDir()
	t.Setenv("GOMODCACHE", cache)
	writeTree(t, root, map[string]string{
		"go.mod": "module example.com/app\n\ngo 1.26.0\n",
		"bob.go": "//go:build bob\n\npackage app\n\nimport \"github.com/megakuul/bob/pkg/bob\"\n\nvar _ = bob.Import(\"example.com/Dep\", \"v1.0.0\")\n\nvar _ = bob.Import(\"example.com/missing\", \"v1.0.0\")\n",
	})
	gomod := []byte("module example.com/Dep\n")
	writeTree(t, cache, map[string]string{
		"cache/download/example.com/!dep/@v/v1.0.0.ziphash": "h1:ZZZ=\n",
		"cache/download/example.com/!dep/@v/v1.0.0.mod":     string(gomod),
	})
	f, ok := RootGoSum(&ir.Graph{Module: "example.com/app", BobVersion: "v1.0.0"}, root, "")
	want := "example.com/Dep v1.0.0 h1:ZZZ=\nexample.com/Dep v1.0.0/go.mod " + resolve.ModH1(gomod) + "\n"
	if !ok || string(f.Content) != want {
		t.Fatalf("RootGoSum = %q, %v; want %q", f.Content, ok, want)
	}
}

// TestRootGoSumKeepsImportLines: a bob.Import module the module cache does not hold keeps the
// lines the root go.sum already has (the file does not depend on one machine's cache).
func TestRootGoSumKeepsImportLines(t *testing.T) {
	root := t.TempDir()
	t.Setenv("GOMODCACHE", t.TempDir())
	writeTree(t, root, map[string]string{
		"go.mod": "module example.com/app\n\ngo 1.26.0\n",
		"go.sum": "example.com/dep v1.0.0 h1:AAA=\nexample.com/dep v1.0.0/go.mod h1:BBB=\nexample.com/old v1.0.0 h1:CCC=\n",
		"bob.go": "//go:build bob\n\npackage app\n\nimport \"github.com/megakuul/bob/pkg/bob\"\n\nvar _ = bob.Import(\"example.com/dep\", \"v1.0.0\")\n",
	})
	f, ok := RootGoSum(&ir.Graph{Module: "example.com/app", BobVersion: "v1.0.0"}, root, "")
	if want := "example.com/dep v1.0.0 h1:AAA=\nexample.com/dep v1.0.0/go.mod h1:BBB=\n"; !ok || string(f.Content) != want {
		t.Fatalf("RootGoSum = %q, %v; want %q", f.Content, ok, want)
	}
}
