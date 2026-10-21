//go:build bob

package app

import (
	"example.com/cc/libs/mathx"
	"example.com/cc/libs/pack"
	"example.com/cc/third_party/zstd"
	"github.com/megakuul/bob/pkg/bob"
	"github.com/megakuul/bob/pkg/cc"
)

// App is bin/app (label //app:app).
var App = bob.Define(func(p *bob.P) *cc.Binary {
	return cc.Binary(p, "app", cc.BinaryOpts{
		Srcs: bob.Glob("*.c"),
		Std:  cc.C17,
		Deps: []cc.Linkable{mathx.Lib(p), pack.Lib(p), zstd.Zstd(p)},
	})
})
