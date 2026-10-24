// Package cc is the C and C++ ecosystem API (SPEC §03.1): hermetic static and shared libraries,
// programs and tests compiled with nixpkgs' gcc by bob-builder, and third-party projects built
// with their own build system from a source pinned in bob.lock (External). Typed options map to
// exact compiler flags; every C/C++ package is a Linkable for C, C++, cgo, Rust and OCaml
// consumers.
package cc
