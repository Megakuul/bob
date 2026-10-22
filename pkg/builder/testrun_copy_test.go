package builder

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/megakuul/bob/pkg/builder/bspec"
)

// test-run Copy: the package tree is copied into the build dir before the test, which runs
// there (Go tests read testdata/ relative to their package directory).
func TestTestRunCopy(t *testing.T) {
	e := newEnv(t, "out")
	src := filepath.Join(e.root, "outside", "pkg")
	if err := os.MkdirAll(filepath.Join(src, "testdata"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "testdata", "in.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	pkg := filepath.Join(e.build, "src", "pkg")
	job := testRunJob(t, bspec.TestRunArgs{Argv: []string{"sh", "-c", "test -f testdata/in.txt && touch testdata/written"},
		Path: []string{"/bin", "/usr/bin"}, Copy: []bspec.CopyArgs{{From: src, To: pkg}}, Cwd: pkg})
	if code, log := e.run(t, "test-run", job); code != 0 {
		t.Fatalf("exit %d:\n%s", code, log)
	}
	if _, err := os.Stat(filepath.Join(pkg, "testdata", "written")); err != nil {
		t.Errorf("the test did not run in the copied, writable tree: %v", err)
	}
	// A copy outside the build dir and the outputs is refused.
	job = testRunJob(t, bspec.TestRunArgs{Argv: []string{"true"}, Path: []string{"/bin", "/usr/bin"},
		Copy: []bspec.CopyArgs{{From: src, To: filepath.Join(e.root, "outside", "x")}}})
	if code, _ := newEnv(t, "out").run(t, "test-run", job); code != ExitInvalid {
		t.Errorf("copy outside the build dir: exit %d, want %d", code, ExitInvalid)
	}
}
