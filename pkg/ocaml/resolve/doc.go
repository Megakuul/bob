// Package resolve is bob's opam dependency resolver (SPEC §03.5.2, R-ML-002, ADR 0007): it
// reads opam files from a pinned opam-repository snapshot, evaluates their filters for
// linux/arm64|x86_64 with the toolchain packages fixed to the nixpkgs versions, and picks one
// version per package.
//
// The resolver reproduces opam's default solver criteria for a fresh install (fewest packages
// marked avoid-version, newest requested packages, smallest total version lag, fewest
// packages). It prunes versions that can never be installed (fixpoint), then runs a
// branch-and-bound search with constraint propagation whose first solution is the greedy
// newest-first one. Unsatisfiable requests return an *UnsatError naming the conflicting
// constraint chain. The package is pure: no I/O besides the fs.FS it is given, no globals.
//
// Every input (opam files, package and version names, formulas, user requirements) is
// bounded: file size, nesting depth, alternatives per formula, versions per package,
// packages read and search nodes.
package resolve
