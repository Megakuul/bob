package builder

import (
	"archive/tar"
	"os"
	"path/filepath"
	"testing"

	"github.com/megakuul/bob/pkg/builder/bspec"
)

// TestUnpackDuplicateFiles: a regular file may appear twice under equivalent names (npm's
// agent-base 7.1.4 has package/dist/index.js and package/./dist/index.js); the last entry
// wins, as with tar and npm. A file replacing a symlink, a symlink replacing anything and a
// directory replacing a file stay refused.
func TestUnpackDuplicateFiles(t *testing.T) {
	reg := func(n, body string) tarEntry { return tarEntry{name: n, typ: tar.TypeReg, body: body} }
	sym := func(n, l string) tarEntry { return tarEntry{name: n, link: l, typ: tar.TypeSymlink} }
	run := func(t *testing.T, ents ...tarEntry) (*env, int, string) {
		e := newEnv(t, "out")
		job := bspec.Job{V: 1, Kind: "unpack", Label: "//t:u", Outputs: []string{"out"},
			Args: mustJSON(t, bspec.UnpackArgs{Archive: tarFile(t, ents...), To: e.outs["out"], Strip: 1})}
		code, log := e.run(t, "unpack", job)
		return e, code, log
	}
	e, code, log := run(t, reg("package/dist/index.js", "one"), reg("package/./dist/index.js", "two"))
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, log)
	}
	if b, err := os.ReadFile(filepath.Join(e.outs["out"], "dist", "index.js")); err != nil || string(b) != "two" {
		t.Errorf("dist/index.js = %q, %v; want the last entry", b, err)
	}
	for name, ents := range map[string][]tarEntry{
		"file over symlink":    {reg("p/target", "x"), sym("p/f", "target"), reg("p/f", "y")},
		"symlink over file":    {reg("p/f", "x"), sym("p/f", "target")},
		"symlink over symlink": {sym("p/f", "a"), sym("p/f", "b")},
		"dir over file":        {reg("p/f", "x"), {name: "p/f/", typ: tar.TypeDir}},
	} {
		t.Run(name, func(t *testing.T) {
			e, code, log := run(t, ents...)
			if code != ExitStep {
				t.Errorf("exit %d, want 1:\n%s", code, log)
			}
			e.contained(t)
		})
	}
}
