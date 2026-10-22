package forge

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/megakuul/bob/tests/parity"
)

// TestAdapterSuitesExist is R-PIPE-007: the suites of ADR 0018 Decision 7 exist with the build
// tags HARNESS.md 8.2 gives them: forge-fixtures and forge-github-replay in the fast gate,
// forge-forgejo-live under `live`, forge-gitlab-live under `live && slow`.
func TestAdapterSuitesExist(t *testing.T) {
	parity.Covers(t, "R-PIPE-007")
	want := map[string]string{ // test name prefix -> build constraint of its file
		"TestFixtures":     "",
		"TestGitHubReplay": "",
		"TestForgejoLive":  "live",
		"TestGitLabLive":   "live && slow",
	}
	got := map[string]string{}
	files, _ := filepath.Glob("*_test.go")
	fset := token.NewFileSet()
	for _, f := range files {
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		constraint := ""
		for _, l := range strings.Split(string(src), "\n") {
			if c, ok := strings.CutPrefix(l, "//go:build "); ok {
				constraint = strings.TrimSpace(c)
			}
		}
		af, err := parser.ParseFile(fset, f, src, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range af.Decls {
			if fd, ok := d.(*ast.FuncDecl); ok && strings.HasPrefix(fd.Name.Name, "Test") {
				for prefix := range want {
					if strings.HasPrefix(fd.Name.Name, prefix) {
						got[prefix] = constraint
					}
				}
			}
		}
	}
	for prefix, c := range want {
		if g, ok := got[prefix]; !ok || g != c {
			t.Errorf("suite %s*: found=%v build constraint %q, want %q", prefix, ok, g, c)
		}
	}
}
