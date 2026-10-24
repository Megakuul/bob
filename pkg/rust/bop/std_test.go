package bop

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// R-FW-034: rust-std holds exactly the files of the target rustc's manifest (written with
// sysroot-relative paths) and refuses entries outside lib/rustlib/<triple>/.
func TestWriteStd(t *testing.T) {
	from := filepath.Join(t.TempDir(), "rustc-1.95.0")
	const triple = "x86_64-unknown-linux-gnu"
	lib := filepath.Join(from, "lib", "rustlib", triple, "lib")
	if err := os.MkdirAll(lib, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"libstd-1.rlib", "libcore-2.rlib", "not-listed.rlib"} {
		if err := os.WriteFile(filepath.Join(lib, f), []byte(f), 0o444); err != nil {
			t.Fatal(err)
		}
	}
	manifest := "file:" + filepath.Join(lib, "libstd-1.rlib") + "\nfile:lib/rustlib/" + triple + "/lib/libcore-2.rlib\n"
	mpath := filepath.Join(from, "lib", "rustlib", "manifest-rust-std-"+triple)
	if err := os.WriteFile(mpath, []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	o := filepath.Join(t.TempDir(), "out")
	if err := writeStd(from, triple, o); err != nil {
		t.Fatal(err)
	}
	var files []string
	_ = filepath.WalkDir(o, func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			rel, _ := filepath.Rel(o, p)
			files = append(files, rel)
		}
		return nil
	})
	want := "lib/rustlib/manifest-rust-std-" + triple + " lib/rustlib/" + triple + "/lib/libcore-2.rlib lib/rustlib/" + triple + "/lib/libstd-1.rlib"
	if strings.Join(files, " ") != want {
		t.Errorf("files %v\nwant %s", files, want)
	}
	m, _ := os.ReadFile(filepath.Join(o, "lib", "rustlib", "manifest-rust-std-"+triple))
	if string(m) != "file:lib/rustlib/"+triple+"/lib/libstd-1.rlib\nfile:lib/rustlib/"+triple+"/lib/libcore-2.rlib\n" {
		t.Errorf("manifest %q", m)
	}
	for _, bad := range []string{"file:/etc/passwd\n", "file:lib/rustlib/" + triple + "/../../../x\n", "dir:lib\n"} {
		if err := os.WriteFile(mpath, []byte(bad), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := writeStd(from, triple, filepath.Join(t.TempDir(), "o")); err == nil {
			t.Errorf("manifest %q accepted", bad)
		}
	}
}
