package cc_test

import (
	"github.com/megakuul/bob/pkg/bob"
	"github.com/megakuul/bob/pkg/cc"
	"github.com/megakuul/bob/pkg/nixpkgs"
)

// A static library with a public include directory; consumers see include/ and link libfoo.a.
func ExampleStaticLibrary() {
	_ = bob.Define(func(p *bob.P) *cc.Library {
		return cc.StaticLibrary(p, "foo", cc.LibraryOpts{
			Srcs:    bob.Glob("src/*.cpp"),
			Hdrs:    bob.IncludeDir("include"),
			Std:     cc.CXX20,
			Defines: map[string]string{"FOO_VERSION": "\"1.2\""},
		})
	})
}

// A shared library linking openssl from nixpkgs; its headers reach consumers because the
// dependency is public (Deps), zlib stays private (PrivDeps).
func ExampleSharedLibrary() {
	_ = bob.Define(func(p *bob.P) *cc.Library {
		return cc.SharedLibrary(p, "crypt", cc.LibraryOpts{
			Srcs:     bob.Glob("src/*.c"),
			Hdrs:     bob.IncludeDir("include"),
			Std:      cc.C17,
			Deps:     []cc.Linkable{nixpkgs.Openssl.Lib(p)},
			PrivDeps: []cc.Linkable{nixpkgs.Zlib.Lib(p)},
		})
	})
}

// A program linking the library of another package: foo stands for that package's handle
// (foo.Lib(p) of libs/foo/bob.go); the program gets the library's headers only through it.
func ExampleNewBinary() {
	foo := bob.Define(func(p *bob.P) *cc.Library {
		return cc.StaticLibrary(p, "foo", cc.LibraryOpts{Srcs: bob.Glob("src/*.cpp"), Hdrs: bob.IncludeDir("include")})
	})
	_ = bob.Define(func(p *bob.P) *cc.Binary {
		return cc.NewBinary(p, "tool", cc.BinaryOpts{Srcs: bob.Files("tool.cpp"), Deps: []cc.Linkable{foo(p)}})
	})
}

// A test program: building //<dir>:foo_test runs it, exit status 0 passes.
func ExampleNewTest() {
	foo := bob.Define(func(p *bob.P) *cc.Library {
		return cc.StaticLibrary(p, "foo", cc.LibraryOpts{Srcs: bob.Glob("src/*.cpp"), Hdrs: bob.IncludeDir("include")})
	})
	_ = bob.Define(func(p *bob.P) *cc.Test {
		return cc.NewTest(p, "foo_test", cc.BinaryOpts{Srcs: bob.Files("foo_test.cpp"), Deps: []cc.Linkable{foo(p)}})
	})
}

// A third-party CMake project, pinned in bob.lock and linked like any other library.
func ExampleExternal() {
	zstd := bob.Define(func(p *bob.P) *cc.ExternalLib {
		return cc.External(p, "zstd", cc.ExternalOpts{
			URL:   "https://github.com/facebook/zstd/releases/download/v1.5.7/zstd-1.5.7.tar.gz",
			Build: cc.CMake,
			Args:  []string{"-S", "build/cmake", "-DZSTD_BUILD_PROGRAMS=OFF"},
		})
	})
	_ = bob.Define(func(p *bob.P) *cc.Binary {
		return cc.NewBinary(p, "pack", cc.BinaryOpts{Srcs: bob.Files("pack.c"), Deps: []cc.Linkable{zstd(p)}})
	})
}
