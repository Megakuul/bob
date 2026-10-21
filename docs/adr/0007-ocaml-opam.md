# ADR 0007 — OCaml: opam packages resolved by bob, one derivation per opam package, own code compiled by bob

- **Status:** Accepted (locked) — owner at Gate S, 2026-10-21
- **Requirements:** R-ML-001, §7 ecosystem rules (bob replaces the native build system, generates
  tooling files), R-FW-006, R-NIX-010…012, R-NFR-001/008
- **Evidence:** `docs/spikes/s05-ocaml.md`, code in `spikes/s05-ocaml/`.

## Context

OCaml projects normally use opam (package manager + solver) and dune (build system). bob must
build OCaml libraries, executables and tests, use opam packages declared in `bob.go` as
fine-grained packages, and keep ocaml-lsp working. Backend B (ADR 0001) computes derivations in
Go; ADR 0005 set the pattern for Rust (bob's own resolver, one derivation per unit).

## Decision (proposed)

1. **Package source is the opam repository, not nixpkgs `ocamlPackages`.** bob pins one
   opam-repository commit in `bob.lock` and resolves in Go. `ocamlPackages` overlaps only 22 %
   of opam packages (12 % at the same version) and offers one version per package; in the
   spike graphs 16–28 % of the needed packages were missing from it.
2. **Toolchain from nixpkgs, fixed during resolution.** `ocaml`, `dune` and `ocamlfind` (plus
   the `base-*` and `ocaml-*` compiler meta packages) are "provided" at the versions of the
   pinned nixpkgs (5.4.1 / 3.21.1 / 1.9.8). The resolver only picks versions compatible with
   them. Changing the OCaml version = choosing another nixpkgs `ocaml-ng` set (deferred).
3. **bob's resolver:** parse opam files (opam 2.0 grammar, filters, `%{var}%`), apply the flag
   filters (`build`, `post` = true; `with-test`, `with-doc`, `dev` = false), evaluate
   `available` and `conflicts` for linux/arm64|x86_64, prune versions that can never be
   installed against the provided toolchain (fixpoint), then depth-first search newest-first
   with backtracking. `depopts` present in the solution become build-order edges and set
   `%{pkg:installed}%`. opam itself is never run.
4. **One derivation per opam package, running the package's own opam `build:`/`install:`
   commands** (rendered by bob in Go, variables substituted, filters applied) followed by
   `opam-installer` on `<name>.install`. This covers dune, topkg, Makefile and ocamlbuild
   packages alike. Inputs: the source FOD (`builtin:fetchurl`, flat sha512/sha256 straight from
   the opam file's `checksum`), the toolchain, the closure of dependency outputs on `OCAMLPATH`.
5. **conf-* packages map to nixpkgs** through a small curated table (`conf-gmp → gmp`, …); their
   own system checks are not run. Unmapped `conf-*` is a bob error naming the table.
6. **bob's own code is compiled without dune** by default (`ocamldep -sort` + `ocamlfind
   ocamlopt`, dune-compatible module wrapping `Lib__Mod`, C stubs via `ocamlfind ocamlopt -c` +
   `ar`, link flags from bob C libraries). One derivation per library and per executable. A
   `dune` mode (generated dune files, `dune build` inside the derivation) is kept as the
   fallback for features bob does not implement (ppx rewriters with complex rules, menhir…);
   both modes were verified to produce the same working executable.
7. **Editors:** `bob dev` writes `.merlin` files (no dune build needed; own libraries' `.cmi`
   from bob's store outputs) and `.bob/bin/{ocamllsp,ocamlmerlin,dune,ocamlfind,…}` shims with
   `OCAMLPATH` pointing at the store. Generated `dune-project`/`dune` files are the alternative
   when the user wants in-tree `dune build @check` (ocaml-lsp then needs that build).
8. **Native executables have build-time references scrubbed** (OCaml stdlib path, gcc,
   glibc-dev): runtime closure 693 MB → 55 MB in the spike.

## Consequences

- bob owns an opam-file parser and a resolver (≈ 1 200 lines in the spike). The resolver is
  greedy newest-first, not opam's global optimisation: 6 of 8 test graphs were identical to
  opam 2.5.1; 2 differed in valid but different choices. A regression corpus against opam is
  needed.
- opam build commands run arbitrary scripts inside the sandbox (no network). Packages that
  download in `build:` or need `patches`/`extra-source` (not implemented in the spike) need
  work in production.
- No dune early cutoff between packages: a change in a library rebuilds that library and its
  dependents' derivations (measured ≈ 2 s for lib + exe).

## Owner amendments

- **A21** (S5 1a, 2a): Decisions 1 and 6 confirmed (opam repository + bob resolver; bob
  compiles own code directly, dune mode opt-in).
- Changed (Decision 3): production uses a conflict-driven (PubGrub-style) search after the
  pruning fixpoint, not the spike's greedy newest-first search (risk recorded in S5);
  acceptance R-ML-002 (valid always, = opam on ≥ 70 % of a 20-graph corpus).
- **D59**: ocaml-lsp runs through the `.bob/bin` shims and the §02 LSP path proxy.
- **S13**: source and repository-snapshot FODs are realised by bob's fetcher.
- Schemas `lock.OpamRepository`/`lock.OpamPackage`, layout, budgets: SPEC §03.5, §03.8.
