//go:build bob

package demo

import (
	"example.com/rustfix/corelib"
	"example.com/rustfix/mymacro"
	"github.com/megakuul/bob/pkg/bob"
	"github.com/megakuul/bob/pkg/rust"
)

// Demo is the S3 demo binary (label //demo:demo): serde, clap, tokio, regex, zstd (C via
// zstd-sys build script), libc, a local proc-macro and a local library.
var Demo = bob.Define(func(p *bob.P) *rust.Bin {
	return rust.Binary(p, rust.BinOpts{Deps: []rust.Dep{
		rust.Crate("serde", "1", rust.Features("derive")),
		rust.Crate("serde_json", "1"),
		rust.Crate("regex", "1"),
		rust.Crate("anyhow", "1"),
		rust.Crate("thiserror", "2"),
		rust.Crate("clap", "4", rust.Features("derive")),
		rust.Crate("tokio", "1", rust.Features("rt", "macros", "time")),
		rust.Crate("log", "0.4"),
		rust.Crate("zstd", "0.13"),
		rust.Crate("libc", "0.2"),
		mymacro.Macro(p),
		corelib.Lib(p),
	}})
})
