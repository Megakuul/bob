package bop

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestPkgConfig: variables, Requires (public: Cflags and Libs; private: Cflags only),
// propagated packages, link order and RUNPATH entries for store directories.
func TestPkgConfig(t *testing.T) {
	dir := t.TempDir()
	dev, out, z := filepath.Join(dir, "ssl-dev"), "/nix/store/aaaa-ssl", filepath.Join(dir, "zlib-dev")
	write(t, filepath.Join(dev, "lib/pkgconfig/libcrypto.pc"), "prefix="+out+"\nlibdir=${prefix}/lib\nincludedir="+dev+"/include\n\nName: libcrypto\nLibs: -L${libdir} -lcrypto\nLibs.private: -ldl\nCflags: -I${includedir}\nRequires.private: zlib >= 1.2\n")
	write(t, filepath.Join(dev, "lib/pkgconfig/libssl.pc"), "libdir="+out+"/lib\nName: libssl\nLibs: -L${libdir} -lssl\nCflags: -I"+dev+"/include\nRequires: libcrypto\n")
	write(t, filepath.Join(dev, "nix-support/propagated-build-inputs"), z+"\n")
	write(t, filepath.Join(z, "share/pkgconfig/zlib.pc"), "Name: zlib\nLibs: -L/nix/store/bbbb-zlib/lib -lz\nCflags: -I'"+z+"/inc dir'\n")

	f, err := resolve(PkgConfig{Roots: []string{dev}, Modules: []string{"libssl"}})
	if err != nil {
		t.Fatal(err)
	}
	wantC := []string{"-I" + dev + "/include", "-I" + z + "/inc dir"}
	wantL := []string{"-L" + out + "/lib", "-lssl", "-lcrypto", "-Wl,-rpath," + out + "/lib"}
	if !slices.Equal(f.cflags, wantC) || !slices.Equal(f.libs, wantL) {
		t.Errorf("libssl:\n cflags %q want %q\n libs %q want %q", f.cflags, wantC, f.libs, wantL)
	}

	all, err := resolve(PkgConfig{Roots: []string{dev}})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(all.libs, "-lssl") || !slices.Contains(all.libs, "-lcrypto") || slices.Contains(all.libs, "-lz") {
		t.Errorf("all modules of the roots (not the propagated ones): %q", all.libs)
	}

	if _, err := resolve(PkgConfig{Roots: []string{dev}, Modules: []string{"nope"}}); err == nil {
		t.Error("a missing module resolved")
	}
}

// TestPkgConfigFallback: without .pc files, the include dirs and every library of LibDir.
func TestPkgConfigFallback(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "include/x.h"), "")
	write(t, filepath.Join(dir, "lib/libb.a"), "")
	write(t, filepath.Join(dir, "lib/liba.so"), "")
	write(t, filepath.Join(dir, "lib/liba.a"), "")
	f, err := resolve(PkgConfig{Roots: []string{dir}, Include: []string{dir + "/include", dir + "/none"}, LibDir: dir + "/lib"})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(f.cflags, []string{"-I" + dir + "/include"}) || !slices.Equal(f.libs, []string{"-L" + dir + "/lib", "-la", "-lb"}) {
		t.Errorf("fallback: %q %q", f.cflags, f.libs)
	}
}

func TestSplitArgs(t *testing.T) {
	for in, want := range map[string][]string{
		`-I/a  -DX="a b" 'c d'\ e`: {"-I/a", "-DX=a b", "c d e"},
		``:                         nil,
		`-l\"x`:                    {`-l"x`},
	} {
		if got := splitArgs(in); !slices.Equal(got, want) {
			t.Errorf("splitArgs(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestRequires(t *testing.T) {
	got := requires("a >= 1.2, b,c = 3 d")
	if !slices.Equal(got, []string{"a", "b", "c", "d"}) {
		t.Errorf("requires: %q", got)
	}
}
