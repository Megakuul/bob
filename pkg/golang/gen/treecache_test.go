package gen

import (
	"crypto/sha256"
	"encoding/hex"
	"go/build"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/megakuul/bob/pkg/engine"
)

func writeFiles(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for name, body := range files {
		p := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// TestTreePackagesCache: the package list of a store path is computed once and then read from
// the user cache; damaged, foreign or non-store entries are recomputed (B-049).
func TestTreePackagesCache(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	const sp, mod = "/nix/store/0123456789abcdfghijklmnpqrsvwxyz-gomod-example.com-m-v1.0.0", "example.com/m"
	home := t.TempDir()
	tree := filepath.Join(home, sp) // the host path of sp in a store below home
	writeFiles(t, tree, map[string]string{"a.go": "package a\n", "b/b.go": "package b\n", "testdata/x.go": "package x\n", "c/README": ""})
	want, err := modulePackages(tree, mod)
	if err != nil {
		t.Fatal(err)
	}
	got, err := treePackages(sp, tree, mod)
	if err != nil || !slices.Equal(got, want) {
		t.Fatalf("first treePackages = %v, %v; want %v", got, err, want)
	}
	sum := sha256.Sum256([]byte(tree))
	file := treeCacheFile("go-packages", hex.EncodeToString(sum[:]))
	if _, err := os.Stat(file); err != nil {
		t.Fatalf("no cache entry: %v", err)
	}
	// A store path never changes: the second call must not walk (a new package stays unseen).
	writeFiles(t, tree, map[string]string{"d/d.go": "package d\n"})
	if got, _ := treePackages(sp, tree, mod); !slices.Equal(got, want) {
		t.Fatalf("cached treePackages = %v, want %v", got, want)
	}
	walked, _ := modulePackages(tree, mod)
	for name, entry := range map[string]string{
		"damaged":  "garbage",
		"foreign":  treeCacheVersion + "\nexample.com/other\nexample.com/other\n",
		"unsorted": treeCacheVersion + "\n" + mod + "\nexample.com/m/b\nexample.com/m\n",
		"outside":  treeCacheVersion + "\n" + mod + "\nexample.com/mx\n",
		"old":      "bob-go-tree-cache-v0\n" + mod + "\nexample.com/m\n",
	} {
		if err := os.WriteFile(file, []byte(entry), 0o644); err != nil {
			t.Fatal(err)
		}
		if got, _ := treePackages(sp, tree, mod); !slices.Equal(got, walked) {
			t.Errorf("%s entry: treePackages = %v, want the walk %v", name, got, walked)
		}
	}
	// A tree without packages caches the empty answer.
	const sp2 = "/nix/store/0123456789abcdfghijklmnpqrsvwxyz-gomod-example.com-e-v1.0.0"
	empty := filepath.Join(home, sp2)
	if err := os.MkdirAll(empty, 0o755); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if got, err := treePackages(sp2, empty, "example.com/e"); err != nil || got != nil {
			t.Errorf("empty tree, call %d: %v, %v", i, got, err)
		}
	}
	// Not a store path, or a host path that is not the store path's: never cached.
	for _, p := range []string{"/tmp/x", "/nix/store/0123456789abcdfghijklmnpqrsvwxyz-other"} {
		if got, _ := treePackages(p, tree, mod); !slices.Equal(got, walked) {
			t.Errorf("%s: %v", p, got)
		}
	}
}

// TestImportDirCache: a package directory of a store path is read once per configuration; the
// cached package equals the slimmed go/build result, and every field the planner reads is kept.
func TestImportDirCache(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{
		"a.go":        "// Package a.\npackage a\n\nimport (\n\t_ \"embed\"\n\t\"fmt\"\n\t\"example.com/x\"\n)\n\n//go:embed data.txt\nvar s string\n\nvar _ = fmt.Sprint\nvar _ = x.Y\n",
		"a_linux.go":  "package a\n",
		"a_darwin.go": "package a\n",
		"a_test.go":   "package a\n\nimport \"testing\"\n\nfunc TestA(t *testing.T) {}\n",
		"x_test.go":   "package a_test\n\nimport \"example.com/a\"\n\nvar _ = a.S\n",
		"asm_arm64.s": "",
		"data.txt":    "hi",
	})
	c := buildCtx{goos: "linux", goarch: "arm64", tc: &toolchain{minor: 26}}
	bc := c.goContext()
	fresh, err := bc.ImportDir(dir, build.ImportComment)
	if err != nil {
		t.Fatal(err)
	}
	want := slimPackage(fresh)

	pl := newPlanner(t.Context(), engine.PlanRequest{})
	first, err := pl.importDir(c, &bc, dir, true)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first, want) {
		t.Fatalf("first read:\n%+v\nwant\n%+v", first, want)
	}
	if _, err := os.Stat(buildCacheFile(buildKey(&bc, dir))); err != nil {
		t.Fatalf("no cache entry: %v", err)
	}
	writeFiles(t, dir, map[string]string{"new.go": "package a\n"}) // unseen: the entry answers
	second, err := newPlanner(t.Context(), engine.PlanRequest{}).importDir(c, &bc, dir, true)
	if err != nil || !reflect.DeepEqual(second, want) {
		t.Fatalf("cached read = %+v, %v; want %+v", second, err, want)
	}
	// Another configuration is another entry.
	c2 := buildCtx{goos: "darwin", goarch: "arm64", tc: &toolchain{minor: 26}}
	bc2 := c2.goContext()
	other, err := newPlanner(t.Context(), engine.PlanRequest{}).importDir(c2, &bc2, dir, true)
	if err != nil || !slices.Contains(other.GoFiles, "a_darwin.go") || slices.Contains(other.GoFiles, "a_linux.go") {
		t.Fatalf("darwin read = %v, %v", other, err)
	}
	// The planner's fields survive slimming.
	for name, ok := range map[string]bool{
		"Name":          want.Name == "a",
		"GoFiles":       slices.Contains(want.GoFiles, "a_linux.go"),
		"Imports":       slices.Equal(want.Imports, []string{"embed", "example.com/x", "fmt"}),
		"TestImports":   slices.Equal(want.TestImports, []string{"testing"}),
		"XTestImports":  slices.Equal(want.XTestImports, []string{"example.com/a"}),
		"EmbedPatterns": slices.Equal(want.EmbedPatterns, []string{"data.txt"}),
		"SFiles":        slices.Equal(want.SFiles, []string{"asm_arm64.s"}),
		"XTestGoFiles":  slices.Equal(want.XTestGoFiles, []string{"x_test.go"}),
		"Dir":           want.Dir == dir,
	} {
		if !ok {
			t.Errorf("slimPackage lost %s: %+v", name, want)
		}
	}
	// Workspace directories are never cached.
	ws := t.TempDir()
	writeFiles(t, ws, map[string]string{"w.go": "package w\n"})
	if _, err := newPlanner(t.Context(), engine.PlanRequest{}).importDir(c, &bc, ws, false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(buildCacheFile(buildKey(&bc, ws))); err == nil {
		t.Error("a workspace directory was cached")
	}
}

// TestBuildCacheExact: a package that does not survive the encoding exactly is not cached,
// and an entry for another key is a miss.
func TestBuildCacheExact(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	file := buildCacheFile("k")
	if err := writeBuildCache(file, "k", &build.Package{Dir: "/d", GoFiles: []string{"a\xff.go"}}); err == nil {
		t.Error("invalid UTF-8 was cached")
	}
	bp := &build.Package{Dir: "/d", Name: "d", GoFiles: []string{"a.go"}, Imports: []string{}}
	if err := writeBuildCache(file, "k", bp); err != nil {
		t.Fatal(err)
	}
	if got, ok := readBuildCache(file, "k"); !ok || !reflect.DeepEqual(got, bp) {
		t.Errorf("readBuildCache = %+v, %v", got, ok)
	}
	if _, ok := readBuildCache(file, "other"); ok {
		t.Error("an entry of another key was used")
	}
}

// TestThirdOf: the prefix walk finds the module the old scan over the build list found (the
// longest selected module path that the import path is within).
func TestThirdOf(t *testing.T) {
	pl := newPlanner(t.Context(), engine.PlanRequest{})
	pl.third = map[string]*thirdParty{}
	pl.selected = map[string]map[string]string{"main": {}}
	for _, mv := range []string{"example.com/a@v1", "example.com/a/b@v2", "example.com/ab@v1", "golang.org/x/net@v0", "example.com/nopkgs@v1"} {
		p, v, _ := strings.Cut(mv, "@")
		pl.selected["main"][p] = v
		if p != "example.com/nopkgs" {
			pl.third[mv] = &thirdParty{}
			pl.third[mv].Path, pl.third[mv].Version = p, v
		}
	}
	old := func(ip string) *thirdParty {
		var best *thirdParty
		for mp, v := range pl.selected["main"] {
			if t := pl.third[mp+"@"+v]; t != nil && (ip == mp || strings.HasPrefix(ip, mp+"/")) && (best == nil || len(mp) > len(best.Path)) {
				best = t
			}
		}
		return best
	}
	for _, ip := range []string{"example.com/a", "example.com/a/x", "example.com/a/b", "example.com/a/b/c", "example.com/ab",
		"example.com/abc", "example.com", "golang.org/x/net/http2", "golang.org/x/netx", "example.com/nopkgs/p", "fmt", ""} {
		if got, want := pl.thirdOf("main", ip), old(ip); got != want {
			t.Errorf("thirdOf(%q) = %v, want %v", ip, got, want)
		}
		if pl.thirdOf("other", ip) != nil {
			t.Errorf("thirdOf(other, %q) found a module", ip)
		}
	}
}
