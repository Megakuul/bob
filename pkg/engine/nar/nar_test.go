package nar

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func write(t *testing.T, p string, data string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(data), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(p, mode); err != nil {
		t.Fatal(err)
	}
}

func tree(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	write(t, filepath.Join(dir, "b", "tool"), "#!/x\n", 0o755)
	write(t, filepath.Join(dir, "a.txt"), "1234567", 0o644)
	write(t, filepath.Join(dir, "B", "eight888"), "12345678", 0o644)
	write(t, filepath.Join(dir, "skip", "x"), "", 0o644)
	if err := os.Symlink("../a.txt", filepath.Join(dir, "b", "link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "empty"), 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

// nixDump is the NAR the host Nix writes for dir (the reference implementation).
func nixDump(t *testing.T, dir string) []byte {
	t.Helper()
	if _, err := exec.LookPath("nix-store"); err != nil {
		t.Skip("no host nix-store to compare with")
	}
	out, err := exec.Command("nix-store", "--dump", dir).Output()
	if err != nil {
		t.Fatalf("nix-store --dump: %v", err)
	}
	return out
}

func TestWriteMatchesNix(t *testing.T) {
	dir := tree(t)
	var got bytes.Buffer
	if err := Write(&got, dir, nil); err != nil {
		t.Fatal(err)
	}
	if want := nixDump(t, dir); !bytes.Equal(got.Bytes(), want) {
		t.Fatalf("NAR differs from nix-store --dump (%d vs %d bytes)", got.Len(), len(want))
	}
	file := filepath.Join(dir, "b", "tool")
	got.Reset()
	if err := Write(&got, file, nil); err != nil {
		t.Fatal(err)
	}
	if want := nixDump(t, file); !bytes.Equal(got.Bytes(), want) {
		t.Fatalf("single-file NAR differs from nix-store --dump")
	}
}

func TestFilter(t *testing.T) {
	dir := tree(t)
	sum, size, err := Hash(dir, []string{"b/tool", "a.txt", "b/link", "B", "empty"})
	if err != nil {
		t.Fatal(err)
	}
	// The same tree without skip/ must give the same NAR.
	if err := os.RemoveAll(filepath.Join(dir, "skip")); err != nil {
		t.Fatal(err)
	}
	want, wsize, err := Hash(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	if sum != want || size != wsize {
		t.Errorf("filtered NAR differs from the NAR of the filtered tree")
	}
	for _, bad := range [][]string{{"../x"}, {"/abs"}, {"a.txt/x"}, {"missing"}, {""}} {
		if _, _, err := Hash(dir, bad); err == nil {
			t.Errorf("files %q accepted", bad)
		}
	}
}
