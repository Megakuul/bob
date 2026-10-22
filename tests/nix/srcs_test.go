package nix

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/megakuul/bob/pkg/engine/drv"
	"github.com/megakuul/bob/pkg/engine/srcs"
	"github.com/megakuul/bob/pkg/engine/store"
	"github.com/megakuul/bob/tests/harness"
	"github.com/megakuul/bob/tests/parity"
)

// randomTree writes a random source tree under root and returns the slash-separated paths of the
// files (and symlinks) a filter would keep: roughly two thirds of what was written.
func randomTree(t testing.TB, rng *rand.Rand, root string) (kept []string) {
	t.Helper()
	dirs := []string{""}
	for i := 0; i < 1+rng.Intn(5); i++ {
		dirs = append(dirs, filepath.Join(dirs[rng.Intn(len(dirs))], fmt.Sprintf("d%c%d", 'a'+rune(rng.Intn(26)), i)))
	}
	for i := 0; i < 3+rng.Intn(25); i++ {
		dir := dirs[rng.Intn(len(dirs))]
		name := filepath.Join(dir, fmt.Sprintf("f%d.%s", i, []string{"go", "c", "txt", "h", ""}[rng.Intn(5)]))
		full := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		switch rng.Intn(8) {
		case 0:
			if err := os.Symlink(fmt.Sprintf("target-%d", rng.Intn(100)), full); err != nil {
				t.Fatal(err)
			}
		default:
			data := make([]byte, rng.Intn(3000))
			rng.Read(data)
			mode := os.FileMode(0o644)
			if rng.Intn(4) == 0 {
				mode = 0o755
			}
			if err := os.WriteFile(full, data, mode); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(full, mode); err != nil {
				t.Fatal(err)
			}
		}
		if rng.Intn(3) != 0 {
			kept = append(kept, filepath.ToSlash(name))
		}
	}
	if len(kept) == 0 {
		kept = []string{"always.txt"}
		if err := os.WriteFile(filepath.Join(root, "always.txt"), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	sort.Strings(kept)
	return kept
}

// materialise copies exactly the kept files of root into dst (what `nix store add` must see).
func materialise(t testing.TB, root, dst string, kept []string) {
	t.Helper()
	for _, f := range kept {
		src, out := filepath.Join(root, filepath.FromSlash(f)), filepath.Join(dst, filepath.FromSlash(f))
		if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
			t.Fatal(err)
		}
		fi, err := os.Lstat(src)
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode()&os.ModeSymlink != 0 {
			l, _ := os.Readlink(src)
			if err := os.Symlink(l, out); err != nil {
				t.Fatal(err)
			}
			continue
		}
		b, _ := os.ReadFile(src)
		if err := os.WriteFile(out, b, fi.Mode().Perm()); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(out, fi.Mode().Perm()); err != nil {
			t.Fatal(err)
		}
	}
}

// TestRNIX009SourcePathEqualsNixStoreAdd is the first half of the acceptance: for 50 random
// trees the source path bob computes (NAR in Go, makeStorePath "source") equals
// `nix store add --mode nar --name <name>` of the same filtered tree.
func TestRNIX009SourcePathEqualsNixStoreAdd(t *testing.T) {
	parity.Covers(t, "R-NIX-009")
	o := newOracle(t)
	rng := rand.New(rand.NewSource(9))
	for i := 0; i < 50; i++ {
		root, filtered := t.TempDir(), t.TempDir()
		kept := randomTree(t, rng, root)
		materialise(t, root, filtered, kept)
		name := drv.Name("pkg", fmt.Sprint(i), "src")
		got, err := srcs.Path(name, srcs.Tree{Root: root, Files: kept})
		if notImplemented(t, err, "srcs.Path") {
			return
		}
		if err != nil {
			t.Fatalf("tree %d: srcs.Path: %v", i, err)
		}
		if want := o.addNar(filtered, name); got != want {
			t.Errorf("tree %d (%d files): srcs.Path = %s, nix store add says %s", i, len(kept), got, want)
		}
	}
}

// TestRNIX009ImportMissingOnly checks the import half: the sources become valid store paths
// equal to the computed ones, and a second import of the same sources is a no-op for Nix.
func TestRNIX009ImportMissingOnly(t *testing.T) {
	parity.Covers(t, "R-NIX-009")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	st, err := store.Open(ctx, store.Config{Home: filepath.Join(t.TempDir(), "bobhome")})
	if notImplemented(t, err, "store.Open") {
		return
	}
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	rng := rand.New(rand.NewSource(10))
	var in []srcs.Source
	var want []string
	for i := 0; i < 5; i++ {
		root := t.TempDir()
		kept := randomTree(t, rng, root)
		name := fmt.Sprintf("imp-%d-src", i)
		p, err := srcs.Path(name, srcs.Tree{Root: root, Files: kept})
		if err != nil {
			t.Fatalf("srcs.Path: %v", err)
		}
		in = append(in, srcs.Source{Name: name, Tree: srcs.Tree{Root: root, Files: kept}})
		want = append(want, p)
	}
	got, err := srcs.Import(ctx, st, in)
	if err != nil {
		t.Fatalf("srcs.Import: %v", err)
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("srcs.Import returned %v, want %v", got, want)
	}
	invalid, err := st.Invalid(ctx, want)
	if err != nil || len(invalid) != 0 {
		t.Errorf("imported paths not valid: %v (%v)", invalid, err)
	}
	// A second import must not need to import anything: the paths are valid, so the stream is empty.
	again, err := srcs.Import(ctx, st, in)
	if err != nil || strings.Join(again, "\n") != strings.Join(want, "\n") {
		t.Errorf("second srcs.Import = %v (%v), want the same paths", again, err)
	}
	// Budget B-076 (10,000 small source dirs in ≤ 5 s) is measured by the budgets stage.
}

// TestRNIX007ExportStreamGolden compares bob's export stream with `nix-store --export` of the
// same derivations (captured from Nix 2.34.8 into testdata/export).
func TestRNIX007ExportStreamGolden(t *testing.T) {
	parity.Covers(t, "R-NIX-007")
	cases, m, err := goldenCases()
	if notImplemented(t, err, "drv.Job.Derivation") {
		return
	}
	if err != nil {
		t.Fatalf("drv.Job.Derivation: %v", err)
	}
	dep, st := cases[depDrv], cases[stDrv]
	for _, d := range []*drv.Derivation{dep, st} {
		if err := d.Finalize(m); err != nil {
			if notImplemented(t, err, "drv.Finalize") {
				return
			}
			t.Fatalf("Finalize: %v", err)
		}
	}
	depItem, err := store.DrvExport(dep)
	if notImplemented(t, err, "store.DrvExport") {
		return
	}
	if err != nil {
		t.Fatalf("store.DrvExport: %v", err)
	}
	stItem, err := store.DrvExport(st)
	if err != nil {
		t.Fatalf("store.DrvExport: %v", err)
	}
	for _, c := range []struct {
		file  string
		items []store.ExportItem
	}{
		{"dep.export", []store.ExportItem{depItem}},
		{"dep-st.export", []store.ExportItem{depItem, stItem}},
	} {
		var buf bytes.Buffer
		if err := store.WriteExport(&buf, c.items); err != nil {
			t.Fatalf("WriteExport: %v", err)
		}
		if want := mustRead(t, filepath.Join("testdata", "export", c.file)); !bytes.Equal(buf.Bytes(), want) {
			t.Errorf("%s: export stream differs from nix-store --export (%d bytes, want %d)", c.file, buf.Len(), len(want))
		}
	}
}

// events parses an NDJSON stdout.
func events(t testing.TB, stdout string) []map[string]any {
	t.Helper()
	var out []map[string]any
	sc := bufio.NewScanner(strings.NewReader(stdout))
	sc.Buffer(make([]byte, 1<<20), 16<<20)
	for sc.Scan() {
		var m map[string]any
		if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
			t.Fatalf("--json line is not JSON: %q: %v", sc.Text(), err)
		}
		out = append(out, m)
	}
	return out
}

// drvImported returns the count of the `drv.imported` event of a --json run.
func drvImported(t testing.TB, r harness.Result) (int, bool) {
	t.Helper()
	for _, ev := range events(t, r.Stdout) {
		if ev["event"] == "drv.imported" {
			n, _ := ev["count"].(float64)
			return int(n), true
		}
	}
	return 0, false
}

// TestRNIX007SecondBuildImportsNothing: a second `bob build` of examples/polyglot imports 0 drvs
// (--json event drv.imported with count 0), and the drv cache exists.
func TestRNIX007SecondBuildImportsNothing(t *testing.T) {
	parity.Covers(t, "R-NIX-007")
	e := newBobEnv(t)
	first := e.run(t, "polyglot", "build", "--json", "//...")
	if harness.Red(t, first) {
		return
	}
	if first.Exit != 0 {
		t.Fatalf("first build exit %d:\n%s", first.Exit, first.Combined())
	}
	if n, ok := drvImported(t, first); !ok || n == 0 {
		t.Errorf("first build: drv.imported = %d (present %v), want > 0", n, ok)
	}
	second := e.run(t, "polyglot", "build", "--json", "//...")
	if second.Exit != 0 {
		t.Fatalf("second build exit %d:\n%s", second.Exit, second.Combined())
	}
	if n, ok := drvImported(t, second); !ok || n != 0 {
		t.Errorf("second build: drv.imported = %d (present %v), want 0", n, ok)
	}
	if ents, err := os.ReadDir(filepath.Join(e.Home, "cache", "drv")); err != nil || len(ents) == 0 {
		t.Errorf("<home>/cache/drv is empty or missing after a build (%v)", err)
	}
}
