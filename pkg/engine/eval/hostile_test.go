package eval

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/megakuul/bob/pkg/engine/ir"
)

// defErr asserts err is a BOB-E-1020 definition error whose text contains want.
func defErr(t *testing.T, err error, want string) {
	t.Helper()
	var errs ir.Errors
	if !errors.As(err, &errs) || len(errs) == 0 || errs[0].Code != 1020 {
		t.Fatalf("got %v, want BOB-E-1020", err)
	}
	if !strings.Contains(errs.Error(), want) {
		t.Errorf("%q lacks %q", errs.Error(), want)
	}
}

// hostileRepo writes a repository whose bob.go imports example.com/x, with the given go.mod
// require/replace lines, and a dependency bob repository at <repo>/evil.
func hostileRepo(t *testing.T, gomodTail string, imp ...string) (string, goMod, map[string]*bobFile) {
	t.Helper()
	if len(imp) == 0 {
		imp = []string{"example.com/x/lib"}
	}
	root := writeRepo(t, "core", map[string]string{
		"go.mod":          "module example.com/core\n\ngo 1.26.0\n" + gomodTail,
		"bob.go":          "//go:build bob\n\npackage core\n\nimport _ \"" + imp[0] + "\"\n",
		"evil/go.mod":     "module example.com/x\n\ngo 1.26.0\n",
		"evil/lib/bob.go": "//go:build bob\n\npackage lib\n",
	})
	b, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	src, err := os.ReadFile(filepath.Join(root, "bob.go"))
	if err != nil {
		t.Fatal(err)
	}
	return root, parseGoMod(b), map[string]*bobFile{".": parseBob(".", "bob.go", src)}
}

// TestHostileGoMod is the regression test of review finding F2: module paths and versions of
// go.mod lines are validated before they name a directory, replace targets of an untrusted
// repository stay inside it, and the generation never writes outside .bob/eval.
func TestHostileGoMod(t *testing.T) {
	ctx := context.Background()
	for name, c := range map[string]struct{ tail, want, imp string }{
		"version climbs":    {"require example.com/x v1.0.0/../../../../../../tmp/t\nreplace example.com/x => ./evil\n", "not a valid module version", ""},
		"bad path":          {"require example.com/x/.. v1.0.0\nreplace example.com/x/.. => ./evil\n", "invalid element", "example.com/x/../lib"},
		"upper-case domain": {"require Example.com/x v1.0.0\nreplace Example.com/x => ./evil\n", "domain name", "Example.com/x/lib"},
		"quoted newline":    {"require \"example.com/x\" \"v1.0.0\\nreplace y => /\"\nreplace example.com/x => ./evil\n", "not a valid module version", ""},
	} {
		t.Run(name, func(t *testing.T) {
			imp := []string{}
			if c.imp != "" {
				imp = append(imp, c.imp)
			}
			root, mod, own := hostileRepo(t, c.tail, imp...)
			_, err := resolveDeps(ctx, toolchain{}, root, mod, own, devVersion, false)
			defErr(t, err, c.want)
		})
	}
	t.Run("go line injection", func(t *testing.T) {
		root, mod, own := hostileRepo(t, "")
		mod.goDir = "1.26\nreplace example.com/y => /"
		_, err := resolveDeps(ctx, toolchain{}, root, mod, own, devVersion, false)
		defErr(t, err, "go version")
	})
	t.Run("untrusted replace outside", func(t *testing.T) {
		outside := t.TempDir()
		for _, target := range []string{"../" + filepath.Base(outside), outside, "/"} {
			root, mod, own := hostileRepo(t, "require example.com/x v1.0.0\nreplace example.com/x => "+target+"\n")
			_, err := resolveDeps(ctx, toolchain{}, root, mod, own, devVersion, true)
			defErr(t, err, "must be inside the repository")
		}
		// A symlink inside the repository that points outside is outside.
		root, mod, own := hostileRepo(t, "require example.com/x v1.0.0\nreplace example.com/x => ./ln\n")
		if err := os.Symlink(outside, filepath.Join(root, "ln")); err != nil {
			t.Fatal(err)
		}
		_, err := resolveDeps(ctx, toolchain{}, root, mod, own, devVersion, true)
		defErr(t, err, "must be inside the repository")
	})
	t.Run("untrusted replace inside", func(t *testing.T) {
		root, mod, own := hostileRepo(t, "require example.com/x v1.0.0\nreplace example.com/x => ./evil\n")
		deps, err := resolveDeps(ctx, toolchain{}, root, mod, own, devVersion, true)
		if err != nil {
			t.Fatal(err)
		}
		if m := deps.mods["example.com/x"]; m == nil || m.bob["lib"] == nil {
			t.Fatalf("dependency not loaded: %+v", deps.mods)
		}
	})
	for _, name := range []string{"../x", "mods/a/../../../x", "/etc/passwd", "a//b", ""} {
		if genName(name) == nil {
			t.Errorf("genName(%q) accepted", name)
		}
	}
	if err := genName("mods/example.com/x@v1.0.0/lib/bob.go"); err != nil {
		t.Error(err)
	}
}

// TestGenDirSymlinkRefused: a committed .bob symlink cannot redirect the generation's writes.
func TestGenDirSymlinkRefused(t *testing.T) {
	root := writeRepo(t, "core", map[string]string{"go.mod": "module example.com/core\n"})
	if err := os.Symlink(t.TempDir(), filepath.Join(root, ".bob")); err != nil {
		t.Fatal(err)
	}
	defErr(t, checkGenDir(root), "not a directory")
}

// TestScanCaps is the regression test of review finding F8: oversized files and too many
// bob.go files are definition errors, not allocations.
func TestScanCaps(t *testing.T) {
	root := writeRepo(t, "core", map[string]string{"go.mod": "module example.com/core\n", "bob.go": "package core\n"})
	if err := os.Truncate(filepath.Join(root, "bob.go"), maxBobGoBytes+1); err != nil {
		t.Fatal(err)
	}
	s, err := scanRepo(root)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.hashes(root, nil, false)
	defErr(t, err, "bob.go: invalid build definition: is larger than the limit")

	if err := os.Truncate(filepath.Join(root, "bob.go"), 10); err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(filepath.Join(root, "go.mod"), maxGoModBytes+1); err != nil {
		t.Fatal(err)
	}
	_, err = s.hashes(root, nil, false)
	defErr(t, err, "go.mod: invalid build definition: is larger than the limit")

	many := t.TempDir()
	for i := range maxBobFiles + 1 {
		d := filepath.Join(many, "d", strings.Repeat("x", 1+i%7)+"-"+strconv.Itoa(i))
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(d, "bob.go"), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	_, err = scanRepo(many)
	defErr(t, err, "more than")

	deep := t.TempDir()
	d := deep
	for range maxScanDepth + 2 {
		d = filepath.Join(d, "a")
	}
	if err := os.MkdirAll(d, 0o755); err != nil {
		t.Fatal(err)
	}
	_, err = scanRepo(deep)
	defErr(t, err, "nested deeper")
}

// TestEvalKeyCoversReplacedRepos is the regression test of review finding F9 (R-FW-043.1,
// A90): editing a bob.go or the go.mod of a dependency bob repository taken from a local
// replace directory changes the eval key.
func TestEvalKeyCoversReplacedRepos(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "repo")
	lib := filepath.Join(parent, "lib")
	for p, c := range map[string]string{
		filepath.Join(root, "go.mod"):       "module example.com/core\n\nrequire example.com/lib v1.0.0\n\nreplace example.com/lib => ../lib\n",
		filepath.Join(root, "bob.go"):       "package core\n",
		filepath.Join(lib, "go.mod"):        "module example.com/lib\n",
		filepath.Join(lib, "x", "bob.go"):   "package x\n",
		filepath.Join(lib, "x", "other.go"): "package x\n",
	} {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	key := func(prev map[string]indexEntry) (string, map[string]indexEntry) {
		t.Helper()
		s, err := scanRepo(root)
		if err != nil {
			t.Fatal(err)
		}
		idx, err := s.hashes(root, prev, false)
		if err != nil {
			t.Fatal(err)
		}
		if err := addReplaceKeys(root, prev, idx, false); err != nil {
			t.Fatal(err)
		}
		return evalKey(idx, "v1.0.0", "h1", "go1.26.0", "arm64"), idx
	}
	k0, idx := key(nil)
	if k, _ := key(idx); k != k0 {
		t.Fatal("key not stable")
	}
	if err := os.WriteFile(filepath.Join(lib, "x", "other.go"), []byte("package x // edited\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if k, _ := key(idx); k != k0 {
		t.Error("a non-bob.go file of the dependency changed the key")
	}
	if err := os.WriteFile(filepath.Join(lib, "x", "bob.go"), []byte("package x // edited\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	k1, idx := key(idx)
	if k1 == k0 {
		t.Error("editing the dependency's bob.go did not change the key")
	}
	if err := os.WriteFile(filepath.Join(lib, "go.mod"), []byte("module example.com/lib\n\ngo 1.26\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if k2, _ := key(idx); k2 == k1 {
		t.Error("editing the dependency's go.mod did not change the key")
	}
	// An untrusted evaluation ignores (and the generation refuses) the outside directory.
	s, _ := scanRepo(root)
	idx2, _ := s.hashes(root, nil, false)
	if err := addReplaceKeys(root, nil, idx2, true); err != nil {
		t.Fatal(err)
	}
	for n := range idx2 {
		if strings.HasPrefix(n, replacePrefix) {
			t.Errorf("untrusted key holds %s", n)
		}
	}
}
