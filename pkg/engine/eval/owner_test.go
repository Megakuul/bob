package eval

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// copyTree copies dir to dst with fresh files, as a git checkout or an archive extraction of
// a committed directory produces them.
func copyTree(t *testing.T, dir, dst string) {
	t.Helper()
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		out := filepath.Join(dst, rel)
		switch {
		case d.IsDir():
			return os.MkdirAll(out, 0o755)
		case d.Type().IsRegular():
			b, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			fi, _ := d.Info()
			return os.WriteFile(out, b, fi.Mode().Perm())
		}
		return nil // symlinks: not needed here
	})
	if err != nil {
		t.Fatal(err)
	}
}

// TestClaimGenDir: state below .bob/eval is kept only when bob's own stamp matches; a copied
// (committed) directory, even with its stamp, is emptied; a committed lock symlink is
// replaced, never followed (A201).
func TestClaimGenDir(t *testing.T) {
	gen := filepath.Join(t.TempDir(), "repo", ".bob", "eval")
	if err := os.MkdirAll(filepath.Join(gen, "gocache", "up"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(gen, keyFile), []byte("committed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	claim := func(gen string) {
		t.Helper()
		lf, err := claimGenDir(gen)
		if err != nil {
			t.Fatal(err)
		}
		_ = lf.Close()
	}
	exists := func(p string) bool { _, err := os.Lstat(p); return err == nil }

	claim(gen)
	if exists(filepath.Join(gen, keyFile)) || exists(filepath.Join(gen, "gocache")) {
		t.Fatalf("state without bob's stamp was kept")
	}
	if err := os.WriteFile(filepath.Join(gen, keyFile), []byte("own\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	claim(gen)
	if !exists(filepath.Join(gen, keyFile)) {
		t.Fatalf("bob's own state was removed")
	}

	// The same directory committed and checked out elsewhere: the stamp is copied, the lock
	// file is new, so nothing of it is trusted.
	other := filepath.Join(t.TempDir(), "checkout", ".bob", "eval")
	copyTree(t, gen, other)
	claim(other)
	if exists(filepath.Join(other, keyFile)) {
		t.Errorf("a copied .bob/eval with a copied stamp was trusted")
	}

	// A committed lock symlink: replaced by a regular lock file, its target never created.
	third := filepath.Join(t.TempDir(), "evil", ".bob", "eval")
	if err := os.MkdirAll(third, 0o755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "outside")
	if err := os.Symlink(target, filepath.Join(third, lockFile)); err != nil {
		t.Fatal(err)
	}
	claim(third)
	if exists(target) {
		t.Errorf("the lock symlink's target was created")
	}
	if fi, err := os.Lstat(filepath.Join(third, lockFile)); err != nil || !fi.Mode().IsRegular() {
		t.Errorf("lock is not a regular file after the claim: %v", err)
	}
}

// TestEvaluateIgnoresCommittedBobDir: a repository that carries a committed .bob/ (evaluator
// binary, key, read-only GOCACHE entries of another user, the I-B26 worker failure) evaluates
// cold and correctly; the committed state is never used. The next evaluation reuses what
// bob wrote itself.
func TestEvaluateIgnoresCommittedBobDir(t *testing.T) {
	a := writeRepo(t, "core", coreRepo())
	res, err := evaluate(t, a)
	if err != nil {
		t.Fatal(err)
	}
	b := filepath.Join(t.TempDir(), "checkout")
	copyTree(t, a, b)
	// committed GOCACHE entries the evaluator sandbox cannot rewrite, and a planted file
	_ = filepath.WalkDir(filepath.Join(b, genDir, cacheSub), func(p string, d fs.DirEntry, err error) error {
		if err == nil && d.Type().IsRegular() {
			_ = os.Chmod(p, 0o444)
		}
		return nil
	})
	planted := filepath.Join(b, genDir, binSub, "planted")
	if err := os.WriteFile(planted, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := evaluate(t, b)
	if err != nil {
		t.Fatalf("evaluation of a checkout with a committed .bob/: %v", err)
	}
	if got.Cached {
		t.Errorf("the committed evaluator binary was reused (cached)")
	}
	if string(got.IR) != string(res.IR) {
		t.Errorf("IR differs from the original checkout's")
	}
	if _, err := os.Stat(planted); err == nil {
		t.Errorf("committed state below .bob/eval was kept")
	}
	again, err := evaluate(t, b)
	if err != nil {
		t.Fatal(err)
	}
	if !again.Cached {
		t.Errorf("bob's own evaluator state was not reused")
	}
	if strings.Contains(string(again.Stderr), "permission denied") {
		t.Errorf("evaluator stderr: %s", again.Stderr)
	}
}
