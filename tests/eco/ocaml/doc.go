// Package ocaml holds the acceptance tests of the OCaml ecosystem (SPEC §03.5): R-ML-001a,
// R-ML-001b and R-ML-002. They drive the bob binary against tests/fixtures/repos/ocaml and the
// offline opam mirror (tools/regmirror); they are red until the ecosystem is implemented. The
// oracle and validator of R-ML-002 is the pinned opam: tools/pins.json must pin the nixpkgs
// attribute as nix tool "opam" (not pinned yet).
package ocaml
