package pcflags

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

	f, err := Resolve(Config{Roots: []string{dev}, Modules: []string{"libssl"}})
	if err != nil {
		t.Fatal(err)
	}
	wantC := []string{"-I" + dev + "/include", "-I" + z + "/inc dir"}
	wantL := []string{"-L" + out + "/lib", "-lssl", "-lcrypto", "-Wl,-rpath," + out + "/lib"}
	if !slices.Equal(f.Cflags, wantC) || !slices.Equal(f.Libs, wantL) {
		t.Errorf("libssl:\n cflags %q want %q\n libs %q want %q", f.Cflags, wantC, f.Libs, wantL)
	}

	all, err := Resolve(Config{Roots: []string{dev}})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(all.Libs, "-lssl") || !slices.Contains(all.Libs, "-lcrypto") || slices.Contains(all.Libs, "-lz") {
		t.Errorf("all modules of the roots (not the propagated ones): %q", all.Libs)
	}

	if _, err := Resolve(Config{Roots: []string{dev}, Modules: []string{"nope"}}); err == nil {
		t.Error("a missing module resolved")
	}
}

// TestPkgConfigFallback: without .pc files, the include dirs and every library of LibDir (as
// far as the program uses it).
func TestPkgConfigFallback(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "include/x.h"), "")
	write(t, filepath.Join(dir, "lib/libb.a"), "")
	write(t, filepath.Join(dir, "lib/liba.so"), "")
	write(t, filepath.Join(dir, "lib/liba.a"), "")
	f, err := Resolve(Config{Roots: []string{dir}, Include: []string{dir + "/include", dir + "/none"}, LibDir: dir + "/lib"})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(f.Cflags, []string{"-I" + dir + "/include"}) || !slices.Equal(f.Libs, []string{"-Wl,--push-state,--as-needed", "-L" + dir + "/lib", "-la", "-lb", "-Wl,--pop-state"}) {
		t.Errorf("fallback: %q %q", f.Cflags, f.Libs)
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

// TestPkgConfigThinPC: nixpkgs' boost.pc has no Cflags and no Libs (A564), yet its include
// directory and the libraries of its lib output must reach the compile and link steps.
func TestPkgConfigThinPC(t *testing.T) {
	dir := t.TempDir()
	dev, out := filepath.Join(dir, "boost-dev"), filepath.Join(dir, "boost")
	write(t, filepath.Join(dev, "include/boost/asio.hpp"), "")
	write(t, filepath.Join(dev, "lib/pkgconfig/boost.pc"), "includedir="+dev+"/include\nlibdir="+out+"/lib\n\nName: boost\nVersion: 1\n")
	write(t, filepath.Join(out, "lib/libboost_thread.so"), "")
	write(t, filepath.Join(out, "lib/libboost_thread.so.1"), "")
	f, err := Resolve(Config{Roots: []string{dev, out}, Include: []string{dev + "/include"}, LibDir: out + "/lib"})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(f.Cflags, []string{"-I" + dev + "/include"}) {
		t.Errorf("cflags %q", f.Cflags)
	}
	if !slices.Contains(f.Libs, "-lboost_thread") || !slices.Contains(f.Libs, "-Wl,--push-state,--as-needed") {
		t.Errorf("libs %q", f.Libs)
	}
}

// TestPkgConfigHeaderOnly: a library without .pc file and without library files provides its
// headers; the include directory of a propagated package comes with it; a package that
// provides nothing is an error.
func TestPkgConfigHeaderOnly(t *testing.T) {
	dir := t.TempDir()
	dev, dep := filepath.Join(dir, "eigen-dev"), filepath.Join(dir, "dep-dev")
	write(t, filepath.Join(dev, "include/eigen3/Eigen/Core"), "")
	write(t, filepath.Join(dev, "nix-support/propagated-build-inputs"), dep+"\n")
	write(t, filepath.Join(dep, "include/dep.h"), "")
	f, err := Resolve(Config{Roots: []string{dev}, Include: []string{dev + "/include"}, LibDir: dev + "/lib"})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(f.Cflags, []string{"-I" + dev + "/include", "-I" + dep + "/include"}) || len(f.Libs) != 0 {
		t.Errorf("header-only: %q %q", f.Cflags, f.Libs)
	}
	empty := filepath.Join(dir, "tool")
	write(t, filepath.Join(empty, "bin/prog"), "")
	if _, err := Resolve(Config{Roots: []string{empty}, Include: []string{empty + "/include"}, LibDir: empty + "/lib"}); err == nil {
		t.Error("a package without headers and libraries resolved")
	}
}

// TestPkgConfigStaticOrder: a module listed before the module that requires it is linked
// behind it (catch2.pc before catch2-with-main.pc: -lCatch2Main -lCatch2).
func TestPkgConfigStaticOrder(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "lib/pkgconfig/catch2.pc"), "Name: catch2\nLibs: -L/nix/store/x-c/lib -lCatch2\n")
	write(t, filepath.Join(dir, "lib/pkgconfig/catch2-with-main.pc"), "Name: m\nLibs: -L/nix/store/x-c/lib -lCatch2Main\nRequires: catch2\n")
	f, err := Resolve(Config{Roots: []string{dir}})
	if err != nil {
		t.Fatal(err)
	}
	i, j := slices.Index(f.Libs, "-lCatch2Main"), slices.Index(f.Libs, "-lCatch2")
	if i < 0 || j < 0 || i > j {
		t.Errorf("libs %q: -lCatch2Main must come before -lCatch2", f.Libs)
	}
}
