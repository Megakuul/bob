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
		"docs/readme.txt":           "r",
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
	// The root package's region: nothing ignored, generated or bob-owned appears, and "**" does
	// not descend into libs/foo, which has its own bob.go (R-FW-070).
	all, err := Resolve(repo, ir.Srcs{Patterns: []string{"**"}}, "bob.go:1", func(rel string) bool { return rel == "libs/foo/src/b.cpp" })
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"docs/readme.txt"}; !reflect.DeepEqual(all, want) {
		t.Errorf("root region:\n got %v\nwant %v", all, want)
	}
	// libs/foo's own region, minus the skipped file.
	foo, err := Resolve(repo, ir.Srcs{Dir: "libs/foo", Patterns: []string{"**"}}, "libs/foo/bob.go:1", func(rel string) bool { return rel == "libs/foo/src/b.cpp" })
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"libs/foo/build/x.cpp", "libs/foo/include/d/e.h", "libs/foo/include/foo.h", "libs/foo/src/a.cpp",
		"libs/foo/src/keep.o", "libs/foo/src/legacy/c.cpp", "libs/foo/src/link.cpp"}
	if !reflect.DeepEqual(foo, want) {
		t.Errorf("libs/foo region:\n got %v\nwant %v", foo, want)
	}
}

// TestResolveLocality: ".." and files of a subpackage are BOB-E-1028 (R-FW-070), a pattern
// leaving the repository stays BOB-E-1025.
func TestResolveLocality(t *testing.T) {
	repo := t.TempDir()
	write(t, repo, map[string]string{"bob.go": "x", "app/bob.go": "x", "app/main.c": "m", "lib/bob.go": "x", "lib/l.c": "l"})
	for _, c := range []struct {
		dir, pattern string
		code         diag.Code
		msg          string
	}{
		{"app", "../lib/*.c", 1028, `app/bob.go:3: //app:app: "../lib/*.c" leaves the package directory app/`},
		{"", "app/*.c", 1028, `bob.go:3: //:all-c: "app/*.c" is inside app/, which has its own bob.go`},
		{"", "lib", 1028, `bob.go:3: //:all-c: "lib" is inside lib/, which has its own bob.go`},
		{"app", "../../x.c", 1025, ""},
	} {
		label := map[string]string{"app": "//app:app", "": "//:all-c"}[c.dir]
		_, err := ResolveFor(repo, ir.Srcs{Dir: c.dir, Patterns: []string{c.pattern}}, c.dir+map[bool]string{true: "/", false: ""}[c.dir != ""]+"bob.go:3", label, nil)
		var d *diag.Error
		if !errors.As(err, &d) || d.Code != c.code || c.msg != "" && d.Error() != c.msg {
			t.Errorf("%s %q: %v", c.dir, c.pattern, err)
		}
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
