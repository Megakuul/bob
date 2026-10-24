# I-R-opam — M11.1 opam resolver (pure library, A78)

Owns: `pkg/ocaml/resolve` only.

## Done (M11.1 resolver core complete, 2026-10-22)
- Package `pkg/ocaml/resolve` (L2, std-lib only): parse.go (opam 2.x grammar, bounds: 1 MiB,
  depth 64, 128k tokens), filter.go (filters, %{}%, formulas → CNF ≤ 256 alternatives, version
  constraints, user requirement parsing), version.go (Debian/opam order), repo.go (fs.FS
  snapshot index, lazy parse, sha256 of opam files, name/version validation, no symlinks,
  ≤ 4096 versions/package, ≤ 50 000 files), solve.go/search.go (prune fixpoint + branch and bound
  with propagation and strong branching over opam's criteria, 200 000-step budget, ctx),
  result.go (`Locked` = lock.OpamPackage + name, SRI sha512/sha256, checksum rejection,
  conflict-chain explanations).
- Oracle: opam 2.5.1 (`/nix/store/j7jf…-opam-2.5.1`, builtin-mccs+glpk, default criteria) over the
  spike snapshot af5ddb0…, toolchain fake-installed, S5 request; recorded in
  `pkg/ocaml/resolve/testdata/opam-corpus.json` (scratch state in /var/tmp/iropam).
- Result (aarch64 8 vCPU host, warm repo cache): **20/20 corpus graphs identical to opam**, all
  8 S5 graphs included, each proven optimal, ≤ 13 000 steps, ≤ 0.34 s (dream, 90 packages).
  Every solution passes an opam-file-level validator (requests, availability, depends,
  conflicts, conflict classes, closedness). All 19 414 opam files of the snapshot parse.
  SPEC's unsat example reproduced exactly on the snapshot.
- Tests: `go test ./pkg/ocaml/resolve` (corpus/snapshot tests need
  `BOB_OPAM_REPO=<unpacked snapshot>`, e.g. spikes/s05-ocaml/tmp/repo; they skip otherwise);
  FuzzParse 45 s and FuzzSolve 60 s clean. gofmt, GOWORK=off vet, staticcheck, boblint
  (-nopanic -noexit -nolist -safego, -importboundary), TestLayering clean for the package.
  `go run ./tools/ci -only fmt,vet,lint,layering` fails only on other units' code
  (pkg/npm/resolve does not compile, pkg/engine/build/plan.go unformatted).

## Next (integration, M11.2 — not this unit)
- gen/lock writer: map `Locked` → lock.OpamPackage; repository snapshot fetch + narHash;
  conf-* table check; CLI rendering of `*UnsatError` as BOB-E-1015.

## DECISIONS
- D-opam-1: Objective = opam 2.5.1's default install criteria (`opam config report`):
  `-removed,-count[avoid-version,changed],-count[version-lag,request],-count[version-lag,changed],
  -count[missing-depexts,changed],-changed`. version-lag = number of newer versions whose
  `available` holds on the platform (measured: lsp graph only matches with lag counted over
  versions not installable with the fixed dune). `deprecated` counts as avoid-version.
  Measured, not assumed: 19/20 equal (the 20th see D-opam-2).
- D-opam-2: The corpus oracle request is the S5 one (`<roots> dune.3.21.1 ocamlfind.1.9.8
  ocaml-base-compiler.5.4.1`). With only `<roots> dune.3.21.1`, opam 2.5.1 returns 91 packages
  for dream including `multipart-form-data.0.3.0`, which nothing requires (no "required by");
  with the S5 request it returns 90. That is a mccs/glpk artefact; bob's 90-package answer is
  strictly better under opam's own criteria.
- D-opam-3: Search = static prune fixpoint, then depth-first branch and bound with unit
  propagation, conflict/conflict-class propagation, a lower bound that counts each forced
  package's cheapest version once (plus packages every remaining version needs), and strong
  branching (probe every version of every forced package, branch on the one whose best version
  raises the bound most). ADR 0007's amendment asks for "conflict-driven (PubGrub-style)"
  search instead of the spike's greedy one; the motive (robustness, conflict chains in errors)
  is met — propagation-driven search with conflict explanations, optimal under opam's criteria —
  but it is not literally PubGrub. Coordinator: flag to architect if the wording must match.
- D-opam-4: `Locked.Deps` (build-order edges) leave out `{post}` dependencies (installed after
  the package, not before; otherwise cycles like ocaml-lsp-server → ocaml-index can arise).
  Post dependencies are still in the solution.
- D-opam-5: A source URL that is not http(s) (git+https, file) is rejected with the same
  `has no sha256/sha512 checksum; bob cannot pin it` error (cannot be a flat fixed-output fetch).
- D-opam-6: Search budget 200 000 steps (nodes + probes); the corpus needs ≤ 13 000. Over budget
  with a solution: the best one is returned with `Optimal=false`; without: `ErrTooComplex`.

## NEEDS
- Integrator (M11.2, pkg/ocaml/gen): the diag template for 1015 is
  `cannot resolve {ecosystem} dependencies for {root}: {reason}`, but SPEC §03.5.2 / the test
  want `✗ cannot resolve opam packages: <reason>`. `UnsatError.Error()` gives exactly
  `cannot resolve opam packages: <reason>`; the CLI rendering must use it (or the template needs
  an opam variant — pkg/bob/diag is not mine).

## TEST PROBLEM
- tests/eco/ocaml/corpus_test.go `opamOracle` (protected): (1) it does not pin dune in the
  request (S5 did): opam 2.5.1 then upgrades dune to 3.24.2 in 18/20 graphs (measured), so its
  answers use dune-configurator/stdune 3.24.x which bob cannot choose (dune is fixed by the
  toolchain, SPEC §03.5.2) — the ≥ 14/20 equality cannot be met with that oracle. (2) under the test's minimal env opam prints `- install <name> <version>` lines (verified with
  env -i), which the regexp `\binstall\s+(\S+)\s+(\S+)` reads, but it also matches the header
  `=== install 11 packages` (→ "11":"packages"), so `same()` can never hold. (3) opam stops at the depext prompt for graphs
  needing conf-gmp etc. (cohttp, dream) unless `--assume-depexts`/`depext=false`. Evidence:
  /var/tmp/iropam/out/*.plain (scratch). Not touched.
