package srcs

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/megakuul/bob/pkg/engine/ir"
)

// TestResolveSymlinkChain is the regression test of review finding F1 (R-FW-021): a repo
// symlink whose target is lexically inside the repo but climbs out through another symlink is
// refused; chains that stay inside are kept.
func TestResolveSymlinkChain(t *testing.T) {
	outer := t.TempDir()
	repo := filepath.Join(outer, "repo")
	write(t, outer, map[string]string{"secret": "s", "repo/p/d/e/f.c": "f", "repo/p/a.c": "a"})
	link := func(target, at string) {
		t.Helper()
		if err := os.Symlink(target, filepath.Join(repo, filepath.FromSlash(at))); err != nil {
			t.Fatal(err)
		}
	}
	link("../..", "p/d/up") // the repo root: inside
	link("d/up/../secret", "p/x.c")
	_, err := Resolve(repo, ir.Srcs{Dir: "p", Patterns: []string{"x.c"}}, "p/bob.go:2", nil)
	code1025(t, err, "symlink")

	if err := os.Remove(filepath.Join(repo, "p/x.c")); err != nil {
		t.Fatal(err)
	}
	link("d/up/p/d/up/p/d/up/../secret", "p/y.c")
	_, err = Resolve(repo, ir.Srcs{Dir: "p", Patterns: []string{"y.c"}}, "p/bob.go:2", nil)
	code1025(t, err, "symlink")
	if err := os.Remove(filepath.Join(repo, "p/y.c")); err != nil {
		t.Fatal(err)
	}

	link("loop2.c", "p/loop1.c")
	link("loop1.c", "p/loop2.c")
	_, err = Resolve(repo, ir.Srcs{Dir: "p", Patterns: []string{"loop1.c"}}, "p/bob.go:2", nil)
	code1025(t, err, "symlink")

	link("d/e", "p/lib")
	link("lib/../e/f.c", "p/ok.c")
	link("d/up/p/a.c", "p/ok2.c")
	got, err := Resolve(repo, ir.Srcs{Dir: "p", Patterns: []string{"ok*.c"}}, "p/bob.go:2", nil)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"p/ok.c", "p/ok2.c"}; !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}
