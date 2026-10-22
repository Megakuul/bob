package golang

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/megakuul/bob/tests/parity"
)

// R-GO-002a: granularity is hybrid. On the Go fixture a one-file change in cmd/api rebuilds
// exactly 2 derivations (package + link); a change in internal/msg rebuilds the package, its
// importer in the workspace (cmd/api) and the link, never a third-party module group or std.
func TestRGO002aFixtureGranularity(t *testing.T) {
	parity.Covers(t, "R-GO-002a")
	home := t.TempDir()
	dir, locked := lockedFixture(t, "go", home)
	if !locked {
		return
	}
	target := "//api/cmd/api:api"
	if r := bobIn(t, dir, home, "build", target); !ok(t, r) {
		return
	}
	cases := []struct {
		name, file string
		want       int
	}{
		{"main.go changes: package + link", "api/cmd/api/main.go", 2},
		{"msg.go changes: package + cmd/api + link", "api/internal/msg/msg.go", 3},
	}
	for _, c := range cases {
		path := filepath.Join(dir, filepath.FromSlash(c.file))
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("harness: %v", err)
		}
		writeFile(t, path, string(b)+"\n// edited by the granularity test\n")
		r := bobIn(t, dir, home, "build", target, "--json")
		if !ok(t, r) {
			return
		}
		if n := builtCount(t, r); n != c.want {
			t.Errorf("%s: %d derivations rebuilt, want exactly %d (labels: %v)", c.name, n, c.want, builtLabels(t, r))
		}
		for _, e := range eventsNamed(events(t, r), "upstream.start") {
			t.Errorf("%s: an upstream package was built (%v); std and third-party groups must not rebuild", c.name, e)
		}
	}
}

// R-GO-002a: on the synthetic 200-package workspace a bottom-layer change rebuilds 36 package
// derivations (the 35 transitive importers including the changed package, and main) + the link.
func TestRGO002aSyntheticBottomLayer(t *testing.T) {
	parity.Covers(t, "R-GO-002a")
	dir, imports := synRepo(t, 200, 20)
	if want := importersOf(imports, 0) + 1; want != 36 {
		t.Fatalf("harness: the synthetic workspace changed shape: %d packages rebuild, SPEC says 36", want)
	}
	home := t.TempDir()
	if r := bobEnv(t, dir, registryEnv(home), "lock"); !ok(t, r) {
		return
	}
	target := "//syn/cmd/syn:syn"
	if r := bobIn(t, dir, home, "build", target); !ok(t, r) {
		return
	}
	path := filepath.Join(dir, "syn", "lib", "p000", "p000.go")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("harness: %v", err)
	}
	writeFile(t, path, string(b)+"\n// bottom layer edit\n")
	r := bobIn(t, dir, home, "build", target, "--json")
	if !ok(t, r) {
		return
	}
	if n := builtCount(t, r); n != 37 {
		t.Errorf("bottom-layer change rebuilt %d derivations, want 37 (36 packages + link)", n)
	}
}

// R-GO-005: with the cutoff mode enabled (BOB_GO_CUTOFF=1, decision: the SPEC names no
// switch), a change of a non-inlinable function body in a bottom-layer package that keeps its
// export data byte-identical rebuilds 1 package derivation + the link, and the binary is
// bit-identical to the non-cutoff build of the same tree.
func TestRGO005CutoffEarlyCutoff(t *testing.T) {
	parity.Covers(t, "R-GO-005")
	dir, _ := synRepo(t, 200, 20)
	target := "//syn/cmd/syn:syn"
	homeCut, homePlain := t.TempDir(), t.TempDir()
	cutEnv := registryEnv(homeCut, "BOB_GO_CUTOFF=1")
	if r := bobEnv(t, dir, cutEnv, "lock"); !ok(t, r) {
		return
	}
	if r := bobEnv(t, dir, cutEnv, "build", target); !ok(t, r) {
		return
	}
	path := filepath.Join(dir, "syn", "lib", "p000", "p000.go")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("harness: %v", err)
	}
	// Name0 builds a string with a loop and strings.Builder: over the inlining budget, so the
	// body is not part of the export data. Only the case mapping changes.
	edited := replaceOnce(t, string(b), "return strings.ToUpper(b.String())", "return strings.ToLower(b.String())")
	writeFile(t, path, edited)
	r := bobEnv(t, dir, cutEnv, "build", target, "--json", "--print-paths")
	if !ok(t, r) {
		return
	}
	if n := builtCount(t, r); n != 2 {
		t.Errorf("with cutoff, a body-only change rebuilt %d derivations, want 2 (1 package + link): %v", n, builtLabels(t, r))
	}
	cut := finishPaths(t, r)[target]
	plain, built := outputPaths(t, dir, homePlain, target)
	if !built {
		return
	}
	if len(cut) == 0 || len(plain[target]) == 0 {
		t.Fatalf("no output path for %s (cutoff %v, plain %v)", target, cut, plain)
	}
	if treeDigest(t, cut[0]) != treeDigest(t, plain[target][0]) {
		t.Errorf("the binary of the cutoff build differs from the non-cutoff build of the same tree")
	}
}
