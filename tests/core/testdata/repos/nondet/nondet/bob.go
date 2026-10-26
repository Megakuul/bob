//go:build bob

package nondet

import (
	"github.com/megakuul/bob/pkg/bob"
	"github.com/megakuul/bob/pkg/cc"
	"github.com/megakuul/bob/pkg/nixpkgs"
)

// libs has 8 keys; Go randomises map iteration order, so ranging over it is not
// deterministic (R-FW-008 (b)).
var libs = map[string]nixpkgs.Attr{
	"zlib": nixpkgs.Zlib, "openssl": nixpkgs.Openssl, "bzip2": nixpkgs.Bzip2, "xz": nixpkgs.Xz,
	"zstd": nixpkgs.Zstd, "lz4": nixpkgs.Lz4, "libffi": nixpkgs.Libffi, "sqlite": nixpkgs.Sqlite,
}

// Lib is libnondet.a (label //nondet:nondet); its Deps order changes between runs.
var Lib = bob.Define(func(p *bob.P) *cc.Library {
	var deps []cc.Linkable
	for _, attr := range libs {
		deps = append(deps, attr.Lib(p))
	}
	return cc.StaticLibrary(p, "nondet", cc.LibraryOpts{Srcs: bob.Glob("*.c"), Deps: deps})
})
