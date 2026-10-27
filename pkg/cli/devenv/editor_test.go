package devenv

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestEditorNvimHelix: bob editor nvim and helix own a whole file and start the language
// servers of the project from .bob/bin; gopls always (bob.go is Go) with the bob build tag
// (A550). A file of another content is not overwritten.
func TestEditorNvimHelix(t *testing.T) {
	nv, err := EditorSettings("nvim", []string{"cc"})
	if err != nil || nv.File != ".nvim.lua" {
		t.Fatal(nv, err)
	}
	for _, want := range []string{"/.bob/bin/gopls", "-tags=bob", "/.bob/bin/clangd", "vim.lsp.config"} {
		if !strings.Contains(nv.Text, want) {
			t.Errorf(".nvim.lua lacks %q:\n%s", want, nv.Text)
		}
	}
	if strings.Contains(nv.Text, "rust") {
		t.Errorf(".nvim.lua names a server of an ecosystem not in use:\n%s", nv.Text)
	}
	hx, err := EditorSettings("helix", nil)
	if err != nil || hx.File != ".helix/languages.toml" || !strings.Contains(hx.Text, `command = ".bob/bin/gopls"`) {
		t.Fatal(hx, err)
	}
	root := t.TempDir()
	if changed, err := nv.Merge(root); err != nil || !changed {
		t.Fatal(changed, err)
	}
	if changed, err := nv.Merge(root); err != nil || changed {
		t.Errorf("second write: %v %v", changed, err)
	}
	if err := os.WriteFile(filepath.Join(root, ".nvim.lua"), []byte("-- mine\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := nv.Merge(root); err == nil {
		t.Error("a file of the user was overwritten")
	}
}
