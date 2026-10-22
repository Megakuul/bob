package ocaml

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/megakuul/bob/tests/eco/internal/ecotest"
	"github.com/megakuul/bob/tests/parity"
)

// R-ML-001b: after bob gen, ocamllsp through the shim answers hover and go-to-definition for an
// identifier of an opam package (Lwt_main.run) and one of a local library (Geom.Vec.hash), and
// no _build directory exists in the tree.
func TestRML001bLSPWithoutInTreeBuild(t *testing.T) {
	parity.Covers(t, "R-ML-001b")
	home := t.TempDir()
	dir, env, ok := lockedFixture(t, home)
	if !ok {
		return
	}
	if r := ecotest.BobEnv(t, dir, env, "gen"); !ecotest.OK(t, r) {
		return
	}
	if r := ecotest.BobEnv(t, dir, env, "env", "--format", "json"); !ecotest.OK(t, r) {
		return
	}
	shim := filepath.Join(dir, ".bob", "bin", "ocamllsp")
	if _, err := os.Stat(shim); err != nil {
		t.Fatalf("the ocamllsp shim is missing after bob gen: %v", err)
	}
	c := ecotest.StartLSP(t, dir, shim, env)
	uri, text := c.DidOpen(filepath.Join(dir, "app", "main.ml"), "ocaml")

	cases := []struct {
		name, needle string
		delta        int
		wantFile     string // suffix of the definition location
	}{
		{"opam package identifier", "Lwt_main.run", len("Lwt_main."), "lwt_main.ml"},
		{"local library identifier", "Geom.Vec.hash", len("Geom.Vec."), "geom/vec.ml"},
	}
	for _, tc := range cases {
		pos, err := ecotest.Position(text, tc.needle, tc.delta)
		if err != nil {
			t.Fatalf("harness: %v", err)
		}
		if h, err := c.Hover(uri, pos); err != nil {
			t.Errorf("%s: hover: %v\nstderr: %s", tc.name, err, c.Stderr())
		} else if !strings.Contains(h, "contents") {
			t.Errorf("%s: hover answer has no contents: %s", tc.name, h)
		}
		locs, err := c.Definition(uri, pos)
		if err != nil {
			t.Errorf("%s: definition: %v\nstderr: %s", tc.name, err, c.Stderr())
			continue
		}
		found := false
		for _, l := range locs {
			found = found || strings.HasSuffix(strings.TrimSuffix(l, "i"), tc.wantFile) // .ml or .mli
		}
		if !found {
			t.Errorf("%s: definition %v does not point into %s(i)", tc.name, locs, tc.wantFile)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "_build")); err == nil {
		t.Errorf("a _build directory exists in the tree: the language server needs no in-tree build")
	}
}
