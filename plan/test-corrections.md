# Protected-test corrections (owner OK given 2026-10-22: "i give the onetime ok for test-corrections")

Applied by the coordinator 2026-10-22: items 1-7. Item 8 (A75) was NOT applied: the layering test
explicitly forbids pkg/rust importing pkg/builder/bspec ("API into builder"), which encodes SPEC
R-ARCH-003; A75 is revoked. Generators describe jobs through E0 (`pkg/engine/drv`); if the job
types are needed there, the M5 owner moves the bspec types into an E0 package.

The permission system blocks agents and the coordinator from editing protected tests. Each item
is a defect found while implementing, resolved with AGENTS.md section 2 (rulings A65-A67 in
plan/decisions.md). Fixing them is not loosening: each makes the test match the SPEC. Needed edits:

1. A65 `tests/nix/drv_test.go` TestRNIX006ATermEscapes: swap the two inputDrv entries in the
   expected ATerm (sorted by path, as SPEC R-NIX-006 and real Nix; evidence plan/progress/I-M2a.md).
2. A66 `bob.NamedVariant` is both a type and a function in SPEC section 1.9, which Go forbids.
   Ruling: type `bob.Named`, function `bob.NamedVariant(name, opts...) Named`. Edits:
   `tests/fixtures/repos/polyglot/bob.go` lines 17-18 and `tests/core/platform_test.go` lines 40-43:
   `[]bob.NamedVariant{` becomes `[]bob.Named{` (calls stay `bob.NamedVariant(...)`). SPEC 1.9 text
   updated by the coordinator.
3. A67 `tests/core/ir_test.go` helper irDoc: dependencies are written as labels while ids are
   "a:one"; SPEC says dependencies are ids. Fix the helper to emit ids; then remove the
   label-to-id leniency from `ir.Parse` (implementation, coordinator task).
4. A68 `TestRFW046ProjectionIgnoresUndeclaredAxes` vs `TestRFW047IRValidation`: `ir.Parse` must
   reject an unregistered kind (R-FW-047); the toy-kind test must therefore link the toy
   ecosystem: blank-import `github.com/megakuul/bob/tests/core/toyeco` in tests/core (non-test
   file or helpers_test.go), registering the toy kinds in the test binary.
5. A70 `tests/sandbox` layer-removal build `-tags bob_noseccomp`, case `sockets`: `AF_VSOCK socket`
   leaks with seccomp removed. No other layer can refuse socket families (Landlock has no socket
   family control, checked in kernel headers; vsock is loaded on the host); S13 never ran this case
   without seccomp. Ruling: seccomp is the only layer for socket families, so the layer-removal
   expectation for that one check must be "leaks when seccomp is removed" (the other layers still
   hold for the rest of the case). Evidence: plan/progress/I-M3b.md TEST PROBLEM.
6. A72 `tests/nix/builder_test.go:61` TestRNIX016FuzzedJobsStayContained builds raw JSON by string
   concatenation; the fuzz tokens `a\..\b` and `\x00` make the JSON invalid for every
   implementation (seed 16). Fix: build the job with `raw(t, map[string]string{"path": path()})`
   (proper JSON encoding). Evidence: plan/progress/I-M4a.md.
7. A73 `pkg/layering_test.go` mayImport: E3 (`pkg/engine/translate` produces `pkg/bob/diag` errors)
   may import L0: `mayImport[e3] = {e0, e1, e2, l0}`. SPEC R-ARCH-003 E3 row already updated.
8. A75 `pkg/layering_test.go`: E2 ecosystem generators may import `pkg/builder/bspec` (the job
   format is the contract between generators and bob-builder; it has no behaviour, only types).
   Both I-M4a and I-M4c need it. SPEC R-ARCH-003 gets one sentence; test edit pending.
   (REVOKED, see A75b: the layering test forbids it.)

## Wave 2 (pending owner OK: the permission system blocked these edits)
9. A83 `tests/eco/ocaml/corpus_test.go` opam oracle (evidence plan/progress/I-R-opam.md):
   (a) `installLine` regexp `\binstall\s+(\S+)\s+(\S+)` also matches opam's header `=== install N
   packages`; anchor it: `(?m)^\s*\S\s+install\s+(\S+)\s+(\S+)`;
   (b) `solve` does not pin the toolchain, so opam upgrades dune to 3.24.2 in 18 of 20 graphs while
   bob keeps dune fixed (SPEC 03.5.2): append `dune.3.21.1`, `ocamlfind.<v>` and
   `ocaml-base-compiler.<v>` to the specs (the S5 request);
   (c) add `--assume-depexts` to the dry-run (opam stops at the system-package prompt for conf-gmp:
   cohttp, dream).
   Without these the required 14/20 equality cannot hold for any resolver.
10. A86 `tests/eco/npm/resolve_test.go` `npmResolution` reads only `dependencies` and
   `optionalDependencies` of an importer's npm lock entry, but SPEC 03.4.2 puts Dev() dependencies
   and peers in `deps`: read `devDependencies` and `peerDependencies` too (8 of 10 graphs report
   false "edge bob has and npm lacks" otherwise; with them there are zero differences; evidence
   plan/progress/I-R-npm.md).
11. A87 `tests/ui` harness does not follow `proto/` and the `/api/` mount of SPEC 6.2 (evidence
   plan/progress/I-UI.md TEST PROBLEM 1-7; a verification copy that fixes only these mismatches
   passes everything: out/uicheck, gitignored): (1) `tests/ui/seed.json` does not decode against the
   proto (20 of 23 responses); replacement ready at out/uicheck/seed.json; (2) the mock must answer
   `/api/bob.*`; (3) page RPC lists: ListJobs for packages, ResolveRelease by exact version, no
   artifact size on GetBuild, install line `bob install <project>/<release>@<version>`; (4) seed the
   calls WatchBuild, GetBuildGraph, GetPath, ResolveRelease, ListRoleBindings, ListMySessions,
   BuildHistory, RecentFailures; (5) serve TLS + HTTP/2 (six connections per host otherwise);
   (6) fontconfig generic aliases in the harness; (7) goldens do not exist yet (owner approves).
   Needs owner OK like the others (tests/ui is protected).
14. A97 (evidence plan/progress/I-B2.md TEST PROBLEM): (a) `TestRFLEET003Reslot`: `newLab` builds the
   Queue without a clock, so Enqueue stamps wall-clock time while the reconciler runs on the test's
   fixed clock; add `Now: l.clk.Now` in `newLab` (the optional `Queue.Now` exists); (b)
   `TestRFLEET005Planner` "within 25 % of the S14 model" rows compare absolute times of the SPEC
   formula (S14 5.1: +0.14 s per drv) with simulation numbers: go200 closure 39.0 s vs 30.0 s,
   wide2000 closure 329.9 s vs 223.6 s, split 6.5 s vs 16.6 s, while every split/no-split decision
   is right. Ruling: the SPEC formula is the predictor; compare the predicted GAIN ratio and the
   decision against the simulation (gain within 25 %), not absolute makespans; if the gains also
   differ by more than 25 %, the acceptance sentence must be restated to "decisions equal"
   (coordinator verifies with the formula before editing).
13. A95 (evidence plan/progress/I-M4d.md TEST PROBLEM 3, 4): (a) `TestRFW051ToyEcosystem` leak-checks
   the `--print-paths` output whose `paths[]` must hold store paths (tests/cli already excludes
   `paths`): exclude `paths` there too; (b) `tests/cli/events_test.go` `knownEvents()` must list the
   stage events (namespace, scan, ir, lock, index, sources, plan, fetch, drv.imported, run.start,
   package.finish, run.finish ...) that SPEC R-FW-049 and 02.4 name and that
   schema/events-v1.json now accepts.
12. A88 (evidence plan/progress/I-B3.md): (a) the R-API-001 generated-trees subtest runs `go tool
   buf` outside the module (needs the module dir / GOWORK); (b) `srvtest.Call` posts without the
   `/api/` prefix of SPEC 6.2; (c) `srvtest` reports a "not implemented" answer from `bob login` as
   a real failure instead of red; (e) `tests/store/env/api.go` RPC posts to `<api_url>/<method>`
   without the `/api/` prefix (same bug as (b), another helper: TestRFLEET001ZeroSetupRegistration
   fails on it; by hand with /api/ 20 workers were ACTIVE in 1.97-2.19 s, budget 5 s); (d) `tests/harness` leaves a 53 MB `bob-harness-bin-*` directory
   per test process in /tmp (580 across the agents, /tmp hit 100 % twice): the harness must remove
   it (TestMain cleanup or a shared cache dir keyed by source hash).
