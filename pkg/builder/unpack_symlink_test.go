package builder

import (
	"archive/tar"
	"os"
	"path/filepath"
	"testing"

	"github.com/megakuul/bob/pkg/builder/bspec"
)

// TestUnpackRefusesSymlinkChains is the regression test of review finding F1 (R-NIX-016): a
// symlink whose target is lexically inside the tree but climbs out through another symlink of
// the archive fails the step, in either entry order; chains that stay inside are kept.
func TestUnpackRefusesSymlinkChains(t *testing.T) {
	dir := func(n string) tarEntry { return tarEntry{name: n, typ: tar.TypeDir} }
	sym := func(n, l string) tarEntry { return tarEntry{name: n, link: l, typ: tar.TypeSymlink} }
	for name, ents := range map[string][]tarEntry{
		"chain":       {dir("p/"), dir("p/d/"), sym("p/d/up", ".."), sym("p/x", "d/up/../outside/sentinel")},
		"chain first": {sym("p/x", "d/up/../outside/sentinel"), dir("p/d/"), sym("p/d/up", "..")},
		"repeated":    {dir("p/d/"), sym("p/d/up", ".."), sym("p/x", "d/up/d/up/../../etc/passwd")},
		"loop":        {sym("p/a", "b"), sym("p/b", "a")},
	} {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t, "out")
			job := bspec.Job{V: 1, Kind: "unpack", Label: "//t:u", Outputs: []string{"out"},
				Args: mustJSON(t, bspec.UnpackArgs{Archive: tarFile(t, ents...), To: e.outs["out"], Strip: 1})}
			if code, log := e.run(t, "unpack", job); code != ExitStep {
				t.Errorf("exit %d, want 1:\n%s", code, log)
			}
			e.contained(t)
		})
	}
	t.Run("inside", func(t *testing.T) {
		e := newEnv(t, "out")
		arch := tarFile(t, dir("p/d/"), dir("p/d/e/"), tarEntry{name: "p/d/e/f", typ: tar.TypeReg, body: "hi"},
			sym("p/lib", "d/e"), sym("p/y", "lib/../e/f"))
		job := bspec.Job{V: 1, Kind: "unpack", Label: "//t:u", Outputs: []string{"out"},
			Args: mustJSON(t, bspec.UnpackArgs{Archive: arch, To: e.outs["out"], Strip: 1})}
		if code, log := e.run(t, "unpack", job); code != 0 {
			t.Fatalf("exit %d:\n%s", code, log)
		}
		if b, err := os.ReadFile(filepath.Join(e.outs["out"], "y")); err != nil || string(b) != "hi" {
			t.Errorf("read y: %q %v", b, err)
		}
	})
}
