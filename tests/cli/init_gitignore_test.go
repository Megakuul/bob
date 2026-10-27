package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/megakuul/bob/tests/harness"
)

// TestInitWritesGitignore: bob init writes a .gitignore block at once with every file bob
// generates and does not commit (go.mod and go.sum are not in it), per language (A550).
func TestInitWritesGitignore(t *testing.T) {
	for _, lang := range []string{"go", "cc", "rust", "npm", "ocaml"} {
		t.Run(lang, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "p")
			r := bob(t, t.TempDir(), home(t), "init", dir, "--lang", lang)
			if harness.Red(t, r) {
				return
			}
			if r.Exit != 0 {
				t.Fatalf("bob init --lang %s: exit %d\n%s", lang, r.Exit, r.Combined())
			}
			got, err := os.ReadFile(filepath.Join(dir, ".gitignore"))
			if err != nil {
				t.Fatal(err)
			}
			want, err := os.ReadFile(filepath.Join(harness.Root(t), "tests", "goldens", "cli", "init-gitignore", lang+".txt"))
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != string(want) {
				t.Errorf(".gitignore of bob init --lang %s:\n%s\nwant:\n%s", lang, got, want)
			}
		})
	}
}
