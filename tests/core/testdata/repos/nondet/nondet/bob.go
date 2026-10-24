//go:build bob

package nondet

import (
	"github.com/megakuul/bob/pkg/bob"
	"github.com/megakuul/bob/pkg/cc"
	"github.com/megakuul/bob/pkg/nixpkgs"
)

// libs has 8 keys; Go randomises map iteration order, so ranging over it is not
// deterministic (R-FW-008 (b)).
var libs = map[string]string{
	"zlib": "zlib", "openssl": "openssl", "bzip2": "bzip2", "xz": "xz",
	"zstd": "zstd", "lz4": "lz4", "libffi": "libffi", "sqlite": "sqlite",
}

// Lib is libnondet.a (label //nondet:nondet); its Deps order changes between runs.
var Lib = bob.Define(func(p *bob.P) *cc.Library {
	var deps []cc.Linkable
	for _, attr := range libs {
		deps = append(deps, nixpkgs.Lib(p, attr))
	}
	return cc.StaticLibrary(p, "nondet", cc.LibraryOpts{Srcs: bob.Glob("*.c"), Deps: deps})
})
