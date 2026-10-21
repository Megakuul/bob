//go:build ignore

// Illustrative bob.go (S15).
package rcore

import "github.com/megakuul/bob/pkg/rust"

var (
	itoa = rust.Crate("itoa", "1.0.18")
	hex  = rust.Crate("hex", "0.4.3", rust.Features("std", "alloc"))
)

var Rcore = rust.StaticLib("rcore", rust.Opts{
	Edition: "2021",
	Deps:    rust.Deps{itoa, hex},
	Hdrs:    "include",
})
