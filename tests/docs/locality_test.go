package docs

import (
	"os"
	"path"
	"path/filepath"
	"testing"

	"github.com/megakuul/bob/tests/harness/boblint"
)

// TestDocsPackageLocality applies the per-package module and typed-surface rules (SPEC
// R-FW-070, R-FW-071, R-CC-006, R-NIX-031; ADR 0020) to every bob.go the documentation shows:
// the file=<dir>/bob.go blocks of a page form one repository, each verify block is a bob.go of
// its own. A documented project must be built the way bob requires: no source outside its
// package, no central source list, no directory string naming another Go package, no nixpkgs
// package named by a string.
//
// The documentation moves to the typed surface in the same change as the framework (AGENTS.md
// §5.9): until pkg/nixpkgs/attrs_gen.go exists (unit Q2), the snippets still compile only against
// the old API (TestSnippetsTypeCheck), so this test waits for it instead of failing the docs stage.
func TestDocsPackageLocality(t *testing.T) {
	root := repoRoot(t)
	if _, err := os.Stat(filepath.Join(root, "pkg", "nixpkgs", "attrs_gen.go")); os.IsNotExist(err) {
		t.Skip("waiting for the typed nixpkgs surface (R-NIX-031, unit Q2): the documentation is migrated in the same change")
	}
	pages := loadPages(t, root)
	checked := 0
	for _, p := range pages {
		repo := boblint.Repo{}
		for _, b := range p.blocks {
			if name, ok := b.attrs["file"]; ok && path.Base(name) == "bob.go" {
				repo[name] = b.content
			}
			if _, ok := b.attrs["verify"]; ok && b.lang == "go" {
				for _, pr := range boblint.Check(boblint.Repo{"bob.go": b.content}) {
					t.Errorf("%s:%d (verify block): %s", p.rel, b.line, pr)
				}
				checked++
			}
		}
		if len(repo) == 0 {
			continue
		}
		checked += len(repo)
		for _, pr := range boblint.Check(repo) {
			t.Errorf("%s: %s", p.rel, pr)
		}
	}
	if checked == 0 {
		t.Fatal("harness: no bob.go block found in the documentation")
	}
}
