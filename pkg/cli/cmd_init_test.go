package cli

import (
	"bytes"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestInitTemplates: every --lang template has a bob.go with the build tag that parses as Go,
// and the names derived from the directory are valid (A474).
func TestInitTemplates(t *testing.T) {
	for lang, tmpl := range initTemplates {
		files := tmpl(initModulePath("hello-x"), initPackageName("hello-x"))
		src, ok := files["bob.go"]
		if !ok || !strings.HasPrefix(src, "//go:build bob\n") {
			t.Errorf("%s: no bob.go with the build tag", lang)
			continue
		}
		if _, err := parser.ParseFile(token.NewFileSet(), "bob.go", src, parser.AllErrors); err != nil {
			t.Errorf("%s: bob.go does not parse: %v", lang, err)
		}
		if len(files) < 2 {
			t.Errorf("%s: the template has no program", lang)
		}
		if initRunLabel[lang] == "" {
			t.Errorf("%s: no run label", lang)
		}
	}
	for in, want := range map[string]string{"hello-go": "hellogo", "testbober": "testbober", "03-x": "x03x", "func": "xfunc", "main": "xmain", "---": "x"} {
		if got := initPackageName(in); got != want {
			t.Errorf("initPackageName(%q) = %q, want %q", in, got, want)
		}
	}
	for in, want := range map[string]string{"hello-go": "example.com/hello-go", "My Project": "example.com/my-project", "...": "example.com/hello"} {
		if got := initModulePath(in); got != want {
			t.Errorf("initModulePath(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestInitRefusals: bob init never overwrites a project or a file of the template, and an
// unknown language is a usage error; both before bob's store is entered.
func TestInitRefusals(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "bob.go"), []byte("//go:build bob\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var so, se bytes.Buffer
	if got := Main([]string{"init", dir}, &so, &se); got != 2 || !strings.Contains(se.String(), "BOB-E-0004") || !strings.Contains(se.String(), "never overwrites") {
		t.Errorf("init over bob.go: exit %d\n%s", got, se.String())
	}
	other := filepath.Join(t.TempDir(), "p")
	if err := os.MkdirAll(filepath.Join(other, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(other, "src", "main.rs"), []byte("fn main() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	se.Reset()
	if got := Main([]string{"init", other, "--lang", "rust"}, &so, &se); got != 2 || !strings.Contains(se.String(), "src/main.rs") {
		t.Errorf("init over src/main.rs: exit %d\n%s", got, se.String())
	}
	if b, _ := os.ReadFile(filepath.Join(other, "src", "main.rs")); string(b) != "fn main() {}\n" {
		t.Error("bob init changed an existing file")
	}
	se.Reset()
	if got := Main([]string{"init", t.TempDir(), "--lang", "cobol"}, &so, &se); got != 2 || !strings.Contains(se.String(), "go, npm, ocaml, rust") {
		t.Errorf("--lang cobol: exit %d\n%s", got, se.String())
	}
}

// TestInitMatchesQuickstarts: the quickstarts show the files bob init writes (they run bob
// init and then explain its files), so every file of each template, for the directory name the
// page uses, appears verbatim on its page.
func TestInitMatchesQuickstarts(t *testing.T) {
	pages := map[string][2]string{
		"go": {"01-go.md", "hello-go"}, "cc": {"02-c-cpp.md", "hello-cc"}, "rust": {"03-rust.md", "hello-rust"},
		"npm": {"04-typescript.md", "hello-ts"}, "ocaml": {"05-ocaml.md", "hello-ocaml"},
	}
	for lang, tmpl := range initTemplates {
		pg, ok := pages[lang]
		if !ok {
			t.Errorf("%s: no quickstart page", lang)
			continue
		}
		b, err := os.ReadFile(filepath.Join("..", "..", "website", "content", "docs", "03-quickstart", pg[0]))
		if err != nil {
			t.Fatal(err)
		}
		for path, content := range tmpl(initModulePath(pg[1]), initPackageName(pg[1])) {
			if !strings.Contains(string(b), "\n"+content+"```") {
				t.Errorf("%s: %s of bob init --lang %s is not shown verbatim on the page", pg[0], path, lang)
			}
		}
		if !strings.Contains(string(b), "bob init") {
			t.Errorf("%s does not use bob init", pg[0])
		}
	}
}
