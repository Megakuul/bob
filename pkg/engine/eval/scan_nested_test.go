package eval

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// A103: a directory with a bob-generated go.mod belongs to the repository's definitions; one
// with a foreign go.mod is another module.
func TestScanBobGeneratedGoMod(t *testing.T) {
	root := t.TempDir()
	files := map[string]string{
		"bob.go":     "//go:build bob\n",
		"api/go.mod": generatedGoMod + "\n// Edit api/bob.go and run bob lock.\nmodule example.com/api\n",
		"api/go.sum": "",
		"api/bob.go": "//go:build bob\n",
		"ext/go.mod": "module example.com/ext\n",
		"ext/bob.go": "//go:build bob\n",
	}
	for p, c := range files {
		p = filepath.Join(root, p)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	s, err := scanRepo(root)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(s.bobDirs, []string{".", "api"}) {
		t.Errorf("bobDirs %v, want [. api]", s.bobDirs)
	}
	if !slices.Equal(s.bobMods, []string{"api/go.mod", "api/go.sum"}) {
		t.Errorf("bobMods %v", s.bobMods)
	}
}

// R-FW-002 scan: a directory with its own bob.lock is a nested repository; the parent's scan
// stops there although bob generated its root go.mod too (R-DEV-003 nested projects).
func TestScanStopsAtNestedRepo(t *testing.T) {
	root := t.TempDir()
	files := map[string]string{
		"bob.lock":       "",
		"bob.go":         "//go:build bob\n",
		"lib/bob.go":     "//go:build bob\n",
		"sub/bob.lock":   "",
		"sub/go.mod":     generatedGoMod + "\n// Edit bob.go and run bob lock.\nmodule sub\n",
		"sub/bob.go":     "//go:build bob\n",
		"sub/api/bob.go": "//go:build bob\n",
	}
	for p, c := range files {
		p = filepath.Join(root, p)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	s, err := scanRepo(root)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(s.bobDirs, []string{".", "lib"}) {
		t.Errorf("bobDirs %v, want [. lib]", s.bobDirs)
	}
	if len(s.bobMods) != 0 {
		t.Errorf("bobMods %v, want none (sub is its own repository)", s.bobMods)
	}
}
