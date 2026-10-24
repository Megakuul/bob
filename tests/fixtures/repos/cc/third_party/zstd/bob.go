//go:build bob

package zstd

import (
	"github.com/megakuul/bob/pkg/bob"
	"github.com/megakuul/bob/pkg/cc"
)

// Zstd is upstream zstd built with its own Makefile (label //third_party/zstd:zstd).
var Zstd = bob.Define(func(p *bob.P) *cc.ExternalLib {
	return cc.External(p, "zstd", cc.ExternalOpts{
		URL:   "https://github.com/facebook/zstd/releases/download/v1.5.7/zstd-1.5.7.tar.gz",
		Build: cc.Make,
		Args:  []string{"-C", "lib"}, // make PREFIX=$out install -C lib: library, headers, .pc only
		Libs:  []string{"zstd"},
	})
})
