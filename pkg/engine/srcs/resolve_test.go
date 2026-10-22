package srcs

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/megakuul/bob/pkg/bob/diag"
	"github.com/megakuul/bob/pkg/engine/ir"
)

func write(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for p, c := range files {
		full := filepath.Join(root, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestResolve(t *testing.T) {
	repo := t.TempDir()
	write(t, repo, map[string]string{
		".gitignore":                "*.o\n/build/\n# comment\n!keep.o\n",
		".git/info/exclude":         "secret.txt\n",
		".git/config":               "x",
		".bob/bin/tool":             "x",
		"bob.go":                    "x",
		"libs/foo/bob.go":           "x",
		"libs/foo/zz_bob_cgo.go":    "x",
		"libs/foo/.gitignore":       "gen/\nlocal_*\n",
		"libs/foo/src/a.cpp":        "a",
		"libs/foo/src/b.cpp":        "b",
		"libs/foo/src/a.o":          "o",
		"libs/foo/src/keep.o":       "k",
		"libs/foo/src/secret.txt":   "s",
		"libs/foo/src/local_x.cpp":  "l",
		"libs/foo/src/legacy/c.cpp": "c",
		"libs/foo/gen/g.cpp":        "g",
		"libs/foo/include/foo.h":    "h",
		"libs/foo/include/d/e.h":    "e",
		"libs/foo/build/x.cpp":      "not ignored: /build/ is anchored at the root",
		"build/y.cpp":               "y",
	})
	if err := os.Symlink("a.cpp", filepath.Join(repo, "libs/foo/src/link.cpp")); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		patterns []string
		want     []string
	}{
		{[]string{"src/**/*.cpp", "!src/legacy/**"}, []string{"libs/foo/src/a.cpp", "libs/foo/src/b.cpp", "libs/foo/src/link.cpp"}},
		{[]string{"src/*.cpp"}, []string{"libs/foo/src/a.cpp", "libs/foo/src/b.cpp", "libs/foo/src/link.cpp"}},
		{[]string{"src/*.o"}, []string{"libs/foo/src/keep.o"}},
		{[]string{"include"}, []string{"libs/foo/include/d/e.h", "libs/foo/include/foo.h"}},
		{[]string{"include/**"}, []string{"libs/foo/include/d/e.h", "libs/foo/include/foo.h"}},
		{[]string{"build/x.cpp", "src/?.cpp"}, []string{"libs/foo/build/x.cpp", "libs/foo/src/a.cpp", "libs/foo/src/b.cpp"}},
	}
	for _, c := range cases {
		got, err := Resolve(repo, ir.Srcs{Dir: "libs/foo", Patterns: c.patterns}, "libs/foo/bob.go:3", nil)
		if err != nil {
			t.Errorf("%v: %v", c.patterns, err)
			continue
		}
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("%v:\n got %v\nwant %v", c.patterns, got, c.want)
		}
	}
	// The whole repository: nothing ignored, generated or bob-owned appears.
	all, err := Resolve(repo, ir.Srcs{Patterns: []string{"**"}}, "bob.go:1", func(rel string) bool { return rel == "libs/foo/src/b.cpp" })
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"libs/foo/build/x.cpp", "libs/foo/include/d/e.h", "libs/foo/include/foo.h", "libs/foo/src/a.cpp",
		"libs/foo/src/keep.o", "libs/foo/src/legacy/c.cpp", "libs/foo/src/link.cpp"}
	if !reflect.DeepEqual(all, want) {
		t.Errorf("whole repo:\n got %v\nwant %v", all, want)
	}
}

func code1025(t *testing.T, err error, variant string) {
	t.Helper()
	var d *diag.Error
	if !errors.As(err, &d) || d.Code != 1025 || d.Args["variant"] != variant {
		t.Errorf("got %v, want BOB-E-1025 %s", err, variant)
	}
}

func TestResolveErrors(t *testing.T) {
	repo := t.TempDir()
	write(t, repo, map[string]string{"p/a.c": "a"})
	_, err := Resolve(repo, ir.Srcs{Dir: "p", Patterns: []string{"../../etc/passwd"}}, "p/bob.go:2", nil)
	code1025(t, err, "outside")
	_, err = Resolve(repo, ir.Srcs{Dir: "p", Patterns: []string{"/etc/passwd"}}, "p/bob.go:2", nil)
	code1025(t, err, "outside")
	_, err = Resolve(repo, ir.Srcs{Dir: "p", Patterns: []string{"*.cpp"}}, "p/bob.go:2", nil)
	code1025(t, err, "noglob")
	if err := os.Symlink("../../outside", filepath.Join(repo, "p", "bad.c")); err != nil {
		t.Fatal(err)
	}
	_, err = Resolve(repo, ir.Srcs{Dir: "p", Patterns: []string{"*.c"}}, "p/bob.go:2", nil)
	code1025(t, err, "symlink")
	if d := (&diag.Error{}); errors.As(err, &d) && d.Args["file"] != "p/bad.c" {
		t.Errorf("symlink error names %q", d.Args["file"])
	}
}

// TestSourcePathStability is R-FW-021 (c): mtimes and modes other than the executable bit do
// not change the path; the executable bit and contents do.
func TestSourcePathStability(t *testing.T) {
	repo := t.TempDir()
	write(t, repo, map[string]string{"a.c": "a", "b.c": "b"})
	tree := Tree{Root: repo, Files: []string{"a.c", "b.c"}}
	p0, err := Path("x-src", tree)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(repo, "a.c"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(filepath.Join(repo, "b.c"), time0, time0); err != nil {
		t.Fatal(err)
	}
	if p1, _ := Path("x-src", tree); p1 != p0 {
		t.Errorf("mode/mtime change changed the path")
	}
	if err := os.Chmod(filepath.Join(repo, "a.c"), 0o755); err != nil {
		t.Fatal(err)
	}
	if p2, _ := Path("x-src", tree); p2 == p0 {
		t.Errorf("the executable bit did not change the path")
	}
}

var time0 = time.Unix(1, 0)
