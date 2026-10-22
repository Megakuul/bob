// Package bop implements the Go builder operations: the bob-builder job kinds go-std,
// go-tools, go-compile, go-cgo, go-link and go-testmain (SPEC §03.0.4, §03.2.3). They call the
// toolchain's compile, cgo, asm, pack and link directly with an importcfg, as cmd/go does,
// without loading packages through cmd/go (ADR 0004).
package bop
