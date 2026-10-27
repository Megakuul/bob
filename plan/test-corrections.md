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
9. APPLIED 2026-10-22 I-TC (A112). A83 `tests/eco/ocaml/corpus_test.go` opam oracle (evidence plan/progress/I-R-opam.md):
   (a) `installLine` regexp `\binstall\s+(\S+)\s+(\S+)` also matches opam's header `=== install N
   packages`; anchor it: `(?m)^\s*\S\s+install\s+(\S+)\s+(\S+)`;
   (b) `solve` does not pin the toolchain, so opam upgrades dune to 3.24.2 in 18 of 20 graphs while
   bob keeps dune fixed (SPEC 03.5.2): append `dune.3.21.1`, `ocamlfind.<v>` and
   `ocaml-base-compiler.<v>` to the specs (the S5 request);
   (c) add `--assume-depexts` to the dry-run (opam stops at the system-package prompt for conf-gmp:
   cohttp, dream).
   Without these the required 14/20 equality cannot hold for any resolver.
10. APPLIED 2026-10-22 I-TC (A113). A86 `tests/eco/npm/resolve_test.go` `npmResolution` reads only `dependencies` and
   `optionalDependencies` of an importer's npm lock entry, but SPEC 03.4.2 puts Dev() dependencies
   and peers in `deps`: read `devDependencies` and `peerDependencies` too (8 of 10 graphs report
   false "edge bob has and npm lacks" otherwise; with them there are zero differences; evidence
   plan/progress/I-R-npm.md).
11. APPLIED 2026-10-22 I-TC (A114), (7) goldens stay with the owner. A87 `tests/ui` harness does not follow `proto/` and the `/api/` mount of SPEC 6.2 (evidence
   plan/progress/I-UI.md TEST PROBLEM 1-7; a verification copy that fixes only these mismatches
   passes everything: out/uicheck, gitignored): (1) `tests/ui/seed.json` does not decode against the
   proto (20 of 23 responses); replacement ready at out/uicheck/seed.json; (2) the mock must answer
   `/api/bob.*`; (3) page RPC lists: ListJobs for packages, ResolveRelease by exact version, no
   artifact size on GetBuild, install line `bob install <project>/<release>@<version>`; (4) seed the
   calls WatchBuild, GetBuildGraph, GetPath, ResolveRelease, ListRoleBindings, ListMySessions,
   BuildHistory, RecentFailures; (5) serve TLS + HTTP/2 (six connections per host otherwise);
   (6) fontconfig generic aliases in the harness; (7) goldens do not exist yet (owner approves).
   Needs owner OK like the others (tests/ui is protected).
18. APPLIED earlier by I-P1 (verified 2026-10-22 I-TC, A121). A108 (evidence plan/progress/I-M7a.md TEST PROBLEM): five fixtures (cc/app, cc/libs/mathx,
   cpp-lib, rust/cmain, polyglot/libs/foo) call `cc.Binary(p, ...)` / `cc.Test(p, ...)`: rename the
   calls to `cc.NewBinary` / `cc.NewTest` (Go cannot have a function and a type named alike;
   the types stay `cc.Binary` / `cc.Test`); the tests/eco/cc server uses plain http without pin for
   `bob lock` (BOB-E-3002): bobtest builds allow http to loopback (A108), no test change needed
   once the fetcher honours that (implementation task of unit I-P1).
17. APPLIED 2026-10-22 I-TC (A120). A104 forge test defects (evidence plan/progress/I-B10.md, problems 1-6): (1) `adapters_test.go`
   looks up GitHub fixtures as `"push.branch.json"` but `forgefix.Load` names them without `.json`
   (every GitHub row and TestFixturesRefused stop there); (2) the "github push" row's fixture has
   `created: true` but the expected result says false; (3) the Forgejo seed must make bobbot a
   repository admin (Forgejo lets only admins manage hooks, 403 otherwise; blocks real hooks for the
   live R-PIPE-003/004/005 tests); (4a) the test's status read-back struct never decodes
   `target_url`; (4b) cancelled expects "error" but SPEC 6.9.2 says `warning` (SPEC wins);
   (5) a job token on the other private repository gets 404 on this Forgejo: R-FLEET-006 /
   G3 must accept a forge-decided denial (403 or 404, never 2xx) since the forge chooses the
   status; (6) GitLab rejects the test's 34-byte signing key (422): use 32 bytes like the spike and
   the recorded fixture.
16. APPLIED 2026-10-22 I-TC (A119). A102 `TestRGO004aToolchainPerModule` (evidence plan/progress/I-M5a.md): `tools/cmd/stamp`
   (Go 1.26) imports packages of `lib` (Go 1.25); Go refuses to compile against an archive built by
   another toolchain (go1.26.9 rejects a go1.25.10 archive), so lib's packages MUST be compiled
   again with 1.26 inside stamp's closure, which the test flags. The test must allow a package of
   another module to be compiled with the importing closure's toolchain (and its own module's
   -lang), as SPEC R-GO-004a "toolchain per module" implies, while still asserting that each
   module's own binaries use that module's toolchain.
15. APPLIED 2026-10-22 I-TC (A118). A101 `tests/cache/big_slow_test.go` `assertNoDanglingReferences` requires a bob narinfo for every
   reference, but references to skipped cache.nixos.org paths (R-REG-005) have none by design:
   restrict the check to references that are not skipped nixpkgs paths (or to a fixture whose
   closure has no nixpkgs references). Evidence plan/progress/I-B9.md.
14. APPLIED 2026-10-22 I-TC (A117; (b) gains differ > 25 %, acceptance restated). A97 (evidence plan/progress/I-B2.md TEST PROBLEM): (a) `TestRFLEET003Reslot`: `newLab` builds the
   Queue without a clock, so Enqueue stamps wall-clock time while the reconciler runs on the test's
   fixed clock; add `Now: l.clk.Now` in `newLab` (the optional `Queue.Now` exists); (b)
   `TestRFLEET005Planner` "within 25 % of the S14 model" rows compare absolute times of the SPEC
   formula (S14 5.1: +0.14 s per drv) with simulation numbers: go200 closure 39.0 s vs 30.0 s,
   wide2000 closure 329.9 s vs 223.6 s, split 6.5 s vs 16.6 s, while every split/no-split decision
   is right. Ruling: the SPEC formula is the predictor; compare the predicted GAIN ratio and the
   decision against the simulation (gain within 25 %), not absolute makespans; if the gains also
   differ by more than 25 %, the acceptance sentence must be restated to "decisions equal"
   (coordinator verifies with the formula before editing).
13. APPLIED 2026-10-22 I-TC (A116). A95 (evidence plan/progress/I-M4d.md TEST PROBLEM 3, 4): (a) `TestRFW051ToyEcosystem` leak-checks
   the `--print-paths` output whose `paths[]` must hold store paths (tests/cli already excludes
   `paths`): exclude `paths` there too; (b) `tests/cli/events_test.go` `knownEvents()` must list the
   stage events (namespace, scan, ir, lock, index, sources, plan, fetch, drv.imported, run.start,
   package.finish, run.finish ...) that SPEC R-FW-049 and 02.4 name and that
   schema/events-v1.json now accepts.
12. APPLIED 2026-10-22 I-TC (A115). A88 (evidence plan/progress/I-B3.md): (a) the R-API-001 generated-trees subtest runs `go tool
   buf` outside the module (needs the module dir / GOWORK); (b) `srvtest.Call` posts without the
   `/api/` prefix of SPEC 6.2; (c) `srvtest` reports a "not implemented" answer from `bob login` as
   a real failure instead of red; (e) `tests/store/env/api.go` RPC posts to `<api_url>/<method>`
   without the `/api/` prefix (same bug as (b), another helper: TestRFLEET001ZeroSetupRegistration
   fails on it; by hand with /api/ 20 workers were ACTIVE in 1.97-2.19 s, budget 5 s); (d) `tests/harness` leaves a 53 MB `bob-harness-bin-*` directory
   per test process in /tmp (580 across the agents, /tmp hit 100 % twice): the harness must remove
   it (TestMain cleanup or a shared cache dir keyed by source hash).
19. APPLIED earlier (verified 2026-10-22 I-TC, A121): `tests/platforms/platform_test.go`
   restrictedAppGo calls `cc.NewBinary` (evidence plan/progress/I-M7b.md, I-P1.md TEST PROBLEM 1).

## Wave 3 (applied 2026-10-22 by I-TC under the owner's approval of A111)
Notes on wave 2: item 11 kept the SPEC 6.10.3 image ref in the build-page artifacts check (the
image is derivable from RegistryService ListTags tag `b-<build>`; the seed's repository is
`api-server`), and the release page checks the install line `bob install api/api@1.4.2` in
the DOM (new Page.Text: derived text is in no single API response) while its seeded id is
the version; the seeded release points at the seeded build (the mock answers GetBuild with
one build only). Item 12 also fixed `tests/devenv` rpc/buildState and srvtest FollowLog
(same missing /api/); 12(d): the harness keeps its binaries under $TMPDIR/bob-harness-bin/run-*
with a pid file and removes the directories of exited test processes. Item 13 also added the
stage events to tests/errors' event set (I-M4e TEST PROBLEM 3). Item 14(b): formula gains vs
simulation: go200 24.9 s vs 13.6 s, rust129 107.0 vs 109.6, wide2000 323.4 vs 207.0, so SPEC
R-FLEET-005's closeness line is restated as "decisions equal" (spec/05 and SPEC.md) and the
test compares the planner's decision with the rule applied to the S14 times. Items 16 and 17(5)
amended SPEC R-GO-004a / R-FLEET-006 text accordingly.
20. APPLIED 2026-10-22 I-TC (A122). I-M8a TEST PROBLEM 1: fixtures rust/corelib and
   polyglot/crates/rcore declare `*rust.TestSet` (func `rust.Test` returns it; Go cannot name a
   type and a func alike); SPEC 03.3.1 updated.
21. APPLIED 2026-10-22 I-TC (A123). npm: fixtures npm/native, npm/ts-lib, codegen/web and
   tests/eco/npm (build_test, repos_test, graphs_test) call `npm.NewApp` / `npm.NewTest`
   (A35, pkg/npm API; evidence: I-M9a private fixture copy /var/tmp/i-m9a/fx2); SPEC 03.4 updated.
22. APPLIED 2026-10-22 I-TC (A124). I-M8a TEST PROBLEM 5: `serverenv.NixTool` returned the first
   printed output on a cold cache (rustc's -man); it now returns the out link, like the warm path.
23. APPLIED 2026-10-22 I-TC (A125). I-M7b TEST PROBLEM 2: TestRPLAT004 checks the host system on
   bob-generated derivations (builder bob-builder) only, as SPEC R-PLAT-004 says; nixpkgs target
   libraries carry the target system (ADR 0009, R-FW-032).
24. APPLIED 2026-10-22 I-TC (A126). I-M7b TEST PROBLEM 3: tests/core TestRFW038 / TestRFW033 /
   R-FW-032 lookups use the §03.1.3 cc names (`cc-foo-<platform>-address`, `cc-mathx`,
   `cc-pack`) per ruling G14 (A54).
25. APPLIED 2026-10-22 I-S2 (A130, owner OK). Root package name in temp-dir repos: SPEC R2 names the
   root package after the directory. tests/eco/ocaml/corpus_test.go `writeRepo` wrote `package root`
   into the bare t.TempDir(); the repo root is now the subdirectory `<TempDir>/root` (sanitised
   name `root`), every assertion kept. Audit of tests/eco/*, tests/core, tests/store, tests/server:
   npm (repos, graphs), golang (resolve corproot, fixture synroot), rust (rsroot), core steps
   (chain) and srvtest (srv) already put the repo in a directory named like the package; no other
   change needed.

Not applied (need an owner/SPEC decision, not a test defect): I-M5b 1 (go fixture's
cross-module import vs `go list -m all`), I-C1 2 (remote builds without a project), I-M8a 4
(`rust_1_94` not in the pin: SPEC R-RS-004a [open] second toolchain), I-M8a 2 and the npm/ocaml
fixtures' root package names (pkg/engine/eval expectedPkg uses the module path's last element
for the root, SPEC R2 says the directory name: implementation vs SPEC, owner), ocaml fixture
`ocaml.Test` func+type clash (I-M11a in progress; SPEC 03.5 has the same clash).

## Wave 4 (applied 2026-10-22 by I-M8b under the owner's approval of evidence-based corrections, A111)
25. APPLIED (M8b-5). I-M8a TEST PROBLEM 3: `tests/core/api_test.go` TestAPISurface allowed no two
   constructors of one package to share a result type, but the SPEC API itself declares
   `cc.StaticLibrary`/`cc.SharedLibrary` → `*Library` (§03.1.1), `rust.Library`/`rust.ProcMacro`
   → `*Lib` and `rust.StaticLib`/`rust.CDylib` → `*CLib` (§03.3.1). The test now allows exactly
   these SPEC-listed sets (`specSharedResults`); any other constructor returning one of those
   types, and every other shared result (e.g. `ext.New`/`ext.NewIn`), still fails.
26. APPLIED (M8b-3). I-M8a TEST PROBLEM 4: `tests/eco/rust/build_test.go` TestRRS004a* used
   `nixpkgs.Tool(p, "rust_1_94")`, an attribute that does not exist in the pin (nix eval at
   cd4ea8d5 and da6dc0e7: `rust_1_95`, `rust_1_97`, `rust_1_98`; `rust_1_9x` are package sets,
   not tools). Now `rust_1_97.packages.stable.rustc` (rustc-wrapper-1.97.1, on cache.nixos.org)
   and the job regexp `1\.97`; SPEC R-RS-004a [open] decided the same way (spec/03 and SPEC.md).
   The golden name rcore-alt-toolchain.json is unchanged (no golden yet: owner approval).
27. APPLIED (M8b-6). I-M8a TEST PROBLEM 7: TestRRS001a ran outputs straight from
   `$BOB_HOME/nix/store` on the host; their interpreter/RUNPATH are `/nix/store/...` paths that
   exist only inside bob's namespace (cmain's librdyn.so: "cannot open shared object file").
   SPEC §02 `--print-paths`: "binaries there run only through bob run/bob exec". The host
   platform's outputs now run through `bob run <label>` with the harness's minimal environment
   (PATH=/usr/bin:/bin, HOME, TZ, LC_ALL, BOB_HOME; no LD_LIBRARY_PATH or toolchain variables);
   the foreign platform keeps qemu-user with the bob home as guest root (unchanged).
28. APPLIED (M8b-7). TestRRS005: the cold run used a fresh BOB_HOME in a user+net namespace
   (ruling G24). A fresh home cannot plan offline at all: the nixpkgs pin (tarball) and the
   evaluator's Go (PATH=/usr/bin:/bin has none) come from the network, which has nothing to do
   with crates. The cold home now first gets nixpkgs from `bob build --dry-run` with network
   (plan only), the test asserts that this put no crate source (`crate-*`) into the store, then
   runs the offline dry run in it and compares with the warm home's (which has all crate
   sources from bob lock). This keeps the SPEC statement ("network disabled and an empty crate
   cache"); verified by hand on arm64: identical 89 derivation paths warm, cold-online and
   cold-offline (`unshare -n`).

## I-M5c (2026-10-22, owner-approved class of evidence-based corrections, A111)
26. APPLIED (A133). tests/eco/golang/resolve_test.go TestRGO002bBuildListEqualsGoCommand: a
   `go list -m all` line `<path> v0.0.0 => <dir>` is accepted when <path> is another workspace
   module of the entry (e.mods) and <dir> points at its directory; every other line is checked as
   before. Evidence: the go fixture's tools and api import example.com/lib; the go command finds a
   workspace module only through such a replace (go.work would print several main modules and the
   union build list). SPEC §03.2.5 / R-GO-002b amended accordingly.
27. NOTE (A131): under the coordinator ruling A131 a root bob.go without non-test Go files may use
   any package name, so tests with fixed root package names (`package root`, `corproot`,
   `rsroot`, `synroot`, `srv`, `synth`, vite-react's `web`, ...) are valid as they are. The
   directory renames I-M5c had made for the dir-name rule (seen by I-S2 in item 25: npm repos/
   graphs, golang corproot/synroot, rust rsroot, srvtest srv, tests/nix probe dir, devenv
   vite-react package, ocaml corpus) were reverted to their original form; no test change remains
   from that rule.

## I-B15 (2026-10-22, owner-approved evidence-based corrections, A111: store re-based on dynamitedb)
Evidence: dynamitedb v0.6.1 keys a record by its `pk`/`sk` struct tags on `KeyField` fields
(dynamitedb.go `retrievePartKey`: "partition key ... is unset" for a plain string field) and
reads/writes data only through `DataField` operators (data.go: `Value()` of an update operator
panics, `set()` of a fetched value panics). tests/store/doc.go names rec_test.go as the file that
changes when dynamitedb joins go.mod. Every assertion is kept; only record construction, field
access and the primitive constructors change.
29. APPLIED. tests/store/rec_test.go: `rec` and `slot` are dynamitedb models (`dynamitedb.KeyField`
   with `pk:"trec"` / `pk:"tqs"` + `sk:"s"`, `ETagField`, `DataField` with the same CBOR names);
   new test document kinds tail (tqt), head (tqh), page (tbpage + sk p), shard (tjact) and the
   builder helpers newSeqLog / newPagedIndex / newShardSet (the primitives take the record kind
   as builder functions, SPEC 05.3 as amended); `act` moved here from TestRSTORE106ShardSet.
30. APPLIED. tests/store/primitives_test.go: `store.NewSeqLog[slot](b, "tqs", "tqt", "tqh", name)`
   → `newSeqLog(b, name)` (same keys tqs/<name>/s/<seq20>, tqt/<name>, tqh/<name>);
   `store.NewPagedIndex[entry](b, "tbpage", "p1", 50)` → `newPagedIndex(b, "p1", 50)`;
   `store.NewShardSet[act](b, "tjact", 4)` → `newShardSet(b, 4)`; `store.NewLease(b,
   "ctl/job-reconciler", o, ttl)` → `store.NewControllerLease(b, "job-reconciler", o, ttl)` (same
   key ctl/job-reconciler). Appended values carry no key (`ID: "a%d-%d"` was ignored before too:
   the slot key is the log's); the test's own bookkeeping of who got which slot uses a plain
   struct `appended` instead of `slot` values; reads use `.Value()`.
31. APPLIED. tests/store/records_test.go and panic_test.go: literals `&rec{ID: "c1", N: 1}` →
   `&rec{ID: store.ID("c1"), N: store.Set[int64](1)}`; reads `got.N` → `got.N.Value()`, `got.ETag
   == ""` → `store.ETagOf(got.ETag) == ""`; the "cas loss" closure starts its count from
   `cur.N.Value()` (a fresh `rec{ID: ...}` has no N to read). Same assertions, same counts (the
   "two uncontended CAS cost 2 PUTs" check holds: one Create, one PUT If-Match).
32. APPLIED. tests/server/logs_forged_test.go: `&store.JobRecord{ID: j.ID}` →
   `&store.JobRecord{ID: store.ID(j.ID)}`; `victim.ID` / `victim.Epoch` / `victim.Owner` →
   `.Value()`.
33. APPLIED 2026-10-22 I-P3 (owner approval relayed by the coordinator; A137). I-M6a TEST
   PROBLEM 1: tests/devenv/shim_test.go TestRDEV008ExecAndShims expected `.bob/bin/go run ./x`
   (program `os.Exit(5)`) to exit 5. `go run` itself exits 1 and prints "exit status 5"
   (verified 2026-10-22 with the pinned /nix/store/x1qs…-go-1.26.8/bin/go and go 1.26.9, no
   shim involved). The test now expects go's own 1 plus "exit status 5" on stderr, and keeps the
   pass-through assertion with a tool whose status is not 1: `.bob/bin/jq -n '"" |
   halt_error(5)'` must exit 5 directly and via `sh -c '…; echo $?'` (jq 1.8.1/1.8.2 verified
   to exit 5). SPEC R-DEV-008 acceptance text updated alike (spec/02-cli-dev.md + SPEC.md).
34. APPLIED 2026-10-22 I-P3 (A137). I-M6a TEST PROBLEM 2: tests/devenv/shim_test.go `findTool`
   indexed `strings.Fields(c + " ")[0]`, which panics for every process with an empty
   /proc/<pid>/cmdline (kernel threads, zombies); such processes are now skipped.
29. APPLIED (I-M8b). `tests/eco/rust/helpers_test.go` noLeak leak-checked `--print-paths --json`
   output including package.finish `paths`, which are host store paths by contract (§02.4);
   every outputPaths call failed ("shows raw Nix text: store path"). Same correction as A116 for
   TestRFW051: the package.finish `paths` field is removed before the leak check; everything else
   (stderr, other events, other fields) is still checked.
30. APPLIED 2026-10-22 I-S3 (A123 completion, owner OK for evidence-based corrections). Item 21
   / A123 were recorded as applied but six constructor calls were still `npm.App(p, …)` /
   `npm.Test(p, …)` (evidence I-M9b TEST PROBLEM 1; pkg/npm/api.go has `func NewApp`,
   `func NewTest`, types `App`, `Test`; SPEC §03.4 uses `npm.NewApp`/`npm.NewTest`): changed to
   `npm.NewApp` / `npm.NewTest` in tests/fixtures/repos/npm/native/bob.go:14,
   tests/fixtures/repos/npm/ts-lib/bob.go:24, tests/fixtures/repos/codegen/web/bob.go:16,
   tests/eco/npm/repos_test.go:34, build_test.go:151, graphs_test.go:110 (generated bob.go).
   Types `*npm.App` / `*npm.Test` unchanged. Grep of tests/ for the same clash in cc/rust/ocaml:
   every qualified call (cc.NewBinary/NewTest, rust.Test → *rust.TestSet, ocaml.NewTest) resolves
   to a func and every `*pkg.T` to a type; nothing else to change.
31. APPLIED 2026-10-22 I-S3 (A141). tests/errors/errors_test.go buildBins built bob and
   bob-server without `-tags bobtest`, so the BOB_TEST_NIX_REPLAY hook was not compiled in and
   every replay entry of the S15 fault catalogue failed. It now passes
   `serverenv.BobtestTagsFlag()` (bobtest merged with the caller's GOFLAGS -tags, A109), as
   tests/harness and tests/serverenv/stack do. Assertions unchanged.
32. APPLIED 2026-10-22 I-S3 (A141). tests/eco/internal/ecotest: the registry manifests are found by
   `RegistryManifests` ($BOB_REGISTRY_MANIFESTS, else tests/fixtures/registry when it has
   index.json, else $XDG_CACHE_HOME/bob-test/registry-manifests); `Registry` and `OpamCommit`
   use it. Needed because the npm snapshot is larger than the 150 MB the repository may hold.

## I-M12a (2026-10-22, owner-approved evidence-based corrections; evidence plan/progress/I-M12a.md)
35. APPLIED. tests/outputs/oci_test.go TestROUT009LayerAlgorithm: "v2 differs from v1 in exactly
   one layer blob" cannot hold together with R-OUT-008/R-OUT-012: the customisation layer (last)
   carries /.bob/licences.json, which lists every closure path including the root, whose store
   path changes with the source. Measured on a scratch repo (Go program + coreutils, arm64): after
   one string change exactly one new oci-layer derivation and two new layer blobs (the program's
   layer and the customisation layer). The count now leaves the last layer out; the ≤ 16 % byte
   check still counts every new blob. SPEC R-OUT-009 acceptance text needs the same words.
36. APPLIED. tests/outputs/oci_test.go TestROUT011ImageLayoutInTheStore: `skopeo copy` with a
   fresh HOME fails before copying on hosts without /etc/containers/policy.json ("Error loading
   trust policy: no policy.json file found", skopeo 1.22.2 on this host); `--insecure-policy`
   added (a local layout-to-layout copy verifies no signatures). With it the copy of a bob image
   succeeds and keeps the index digest.
37. APPLIED. tests/outputs/release_test.go TestROUT023ReleaseManifest: invalid versions are
   BOB-E-1020 (SPEC §02.3/§4.11), whose template is `{label}: invalid {option}: {reason}`; the
   text `✗ invalid release version "<v>"` (no label) cannot be produced by that code. The version
   comes from the command line, so the label is the flag: the test now expects
   `✗ --version: invalid release version "<v>"` (exit 3 unchanged).
38. APPLIED (I-M10a). proto.Library: the fixtures call `proto.Library(p, …)` returning
   `*proto.Library`; a Go function and a type cannot share a name (same class as A66/A108/A123).
   The constructor is `proto.NewLibrary`; fixtures codegen/proto/{greet,common}/v1, go/proto/msg/v1,
   polyglot/proto/greeting/v1 and SPEC §03.6.1 (spec/03 + SPEC.md) changed.
39. APPLIED (I-M10a). tests/core/api_test.go specSharedResults: SPEC §03.6.1 declares proto.Lint
   and proto.Breaking both returning `*proto.Check` (two check packages of one kind), the class the
   table exists for: `"pkg/proto": {"*Check": {"Breaking", "Lint"}}`.
40. APPLIED (I-M10a). tests/eco/gen TestRGEN001aProtoSyntaxError: buf 1.73.0 (the pin's, the same
   as S6) prints for the test's injection ("message GreetRequest {}", "", "}") `9:1:encountered
   unmatched `}` delimiter` + `9:1:unexpected `}` in file scope`; S6's text came from fault F1
   (the `;` of `string name = 1;` removed) and uses backticks. Excerpt lines start with `|`
   outside a terminal (§02.5 rule 2). The test now injects F1 and expects
   ``| proto/greet/v1/greet.proto:9:1:unexpected `}` after definition`` (measured with the pinned
   buf through bob); SPEC R-GEN-001a acceptance text amended. The same `│` problem exists in
   TestRGEN002aUndefinedTypeInSchema (`│ failed to load schema…`, I-M10b's area; not changed).
33. APPLIED 2026-10-22 I-S3 (A141). tests/eco/npm/graphs_test.go `writeGraph` (two defects, both
   seen as `bob lock` exit 3 in TestRNPM001aMatchesNpm on 8 of 10 graphs): (a) importer bob.go files
   used `package g<i>` in directories named web/lib/api/…; SPEC R2 (A131): the package of a
   directory without Go files is its name (`BOB-E-1024 web/bob.go: package g0, but … package
   web`); now `package <base of the importer dir>`. (b) the root bob.go imported pkg/npm even when
   no importer is the root, which Go refuses (`"github.com/megakuul/bob/pkg/npm" imported and not
   used`); the import is now written only when the root declares an importer. I-S2's audit (item 25)
   had only checked the root package. After the fix all 10 graphs, TestRNPM001aPeerSplit and the
   hostile-document tests pass against the recorded snapshot.

## I-FIN1 (2026-10-22, owner-approved evidence-based corrections, A143)
41. APPLIED 2026-10-22 I-FIN1. tests/install/auth_test.go checkNo401: the substring check for
   "401" failed on bob's own `BOB-E-4010`, which TestRINST003PrivateNeedsLogin requires in the
   same output, so the test could not pass for any implementation (evidence plan/progress/I-C2.md
   TEST PROBLEM 1). The HTTP status is now matched as a word of its own
   (`(?:^|[^\w-])401(?:[^\w]|$)`: `HTTP 401`, `401 Unauthorized`, ` 401 `); "Unauthorized",
   "HTTP error", "unable to download" and "warning: error:" are still checked as substrings.
42. APPLIED 2026-10-22 I-FIN1. tests/core/api_test.go specSharedResults: SPEC §1.5 declares the
   extension API's node constructors `ext.New`, `ext.NewIn` and `ext.NewAt`, all returning
   `bob.Node` (one constructor family differing only in label directory / definedAt; SPEC §1.5
   listing completed by I-FIN1, A144): `"pkg/bob/ext": {"bob.Node": {"New", "NewAt", "NewIn"}}`.
   `Here` takes no *P and `Elsewhere` returns *bob.P, so they are not in the set. TestAPISurface
   now reports only the (pre-existing) missing Example tests.
43. APPLIED 2026-10-22 I-FIN1. tests/eco/gen/generate_test.go TestRGEN002aUndefinedTypeInSchema:
   the expected excerpt lines started with `│`, which bob prints only on a terminal; the test
   captures stderr (no TTY), where §02.5 rule 2 prints `|` (same fix as item 40 for
   TestRGEN001aProtoSyntaxError). Expects `| failed to load schema: …` and `| Unknown type: "Nope".`;
   SPEC R-GEN-002a acceptance amended (spec/03 + SPEC.md).
44. APPLIED 2026-10-22 I-B19 (recorded by I-FIN1; A141 second entry). tests/store/fleet/lease_test.go
   chaos(): `l.q.Now = l.clk.Now` after `l.clk = realClock()`. newLab binds the queue to the
   injected clock, so every enqueued job got qa = the lab's fixed start time while the reconcilers
   ran on the wall clock: each queued job was re-slotted once (passes of 12-17 s spent in re-slots).
   Assertions unchanged.
45. APPLIED 2026-10-22 I-B19 (recorded by I-FIN1; A141 second entry). tests/store/fleet/e2e_test.go
   TestRFLEET001ZeroSetupRegistration: `harness.Bin(t, "bob-server")` once before the timing loop.
   The first Bin call built three binaries inside the first measurement (first samples 7.9 s and
   17.4 s, the build accounting for all but ~3 s; other samples 1.5-6 s). Assertions unchanged.

## I-B22 (2026-10-23, owner-approved evidence-based fixture correction, A146)
46. APPLIED 2026-10-23 I-B22. tests/fixtures/repos/acme/deploy/bob.go api(): declares the cgo
   package first with the same constructor call as `//cmd/api:FFI`
   (`acme.Module(p).Cgo("./cmd/api", golang.CgoOpts{Deps: []cc.Linkable{foo.Lib(p), rcore.Crate(p)}})`,
   imports crates/rcore, libs/foo, pkg/cc), then the unchanged Binary call. A Go binary picks up
   the module's cgo packages declared so far (pkg/golang Module.Cgo doc), so a worker evaluating
   only //deploy:api declared the api binary without libfoo/rcore and its build failed with
   `BOB-E-2003 //cmd/api:api: header foo.h not found` (evidence plan/progress/I-B20.md, devstack
   run 1, NEEDS 2). Identical calls give identical nodes (pkg/bob node.go sameAs), so //deploy:api
   and //cmd/api:api stay one node with one label; cmd/api/bob.go and every label unchanged.
RE-APPLIED 2026-10-23 (coordinator, A147): item 24/A126 (tests/core/platform_test.go cc derivation names cc-foo, cc-mathx, cc-pack) and part of item 11/A114 (tests/ui/seed.json seeded release buildId 01jbq7k2p4m8x0c3v5n7r9t1y6): both had been reverted by a tar restore (incident A147).

## I-B23 (2026-10-23, evidence-based fixture correction)
47. APPLIED 2026-10-23 I-B23. tests/fixtures/repos/polyglot/bob.go: the release pipeline was
   registered as `bob.Pipeline("release", bob.PipelineOpts{})` (comment: "SPEC §06 does not define
   the fields of bob.PipelineOpts, so the pipeline is registered by name only"; outdated: the
   fields exist and the acme fixture uses them). With no trigger and no release, the tag push of
   TestRREG003PullRegistry matched no pipeline: the devstack showed the tag build
   b-01M5K7NJR9WT35T6GPX991N03E (refs/tags/v1.0.0) as BUILD_STATE_SKIPPED, nothing was built and
   the test failed after 20 min with `release pub/polyglot@1.0.0 not published within 20m0s`.
   Now `Triggers: OnTag("v*"), Concurrency: bob.Queue, Releases: {"polyglot"}`, exactly what the
   fixture's own comment specifies. No golden for the polyglot IR exists (tests/goldens/ir/).
48. APPLIED 2026-10-23 I-B23. tests/fixtures/repos/polyglot/bob.go API(): calls `FFI(p)` before
   the unchanged `Module(p).Binary("./cmd/api", …)` (same defect and fix as item 46 for acme). With
   item 47 in place the tag build ran and failed on the worker: orchestrator log `build: job failed
   … status=failed error="BOB-E-2003 //cmd/api:api: header foo.h not found"` (build
   b-01M5K8ASQY8D6GZ4CT5BF5DVB8): a worker evaluating //deploy:polyglot declared the api binary
   before the module's cgo package //:FFI. Labels and nodes unchanged (FFI is the same node).
49. APPLIED 2026-10-23 I-B23. tests/serverenv/forgejo/forgejo.go seed: bobbot is collaborator
   `admin` (was `read`) on alice/other. TestRREG003PullRegistry links project priv to alice/other
   and publishes a release from it by tag push, but with read access the bot cannot manage the
   repository's hook: orchestrator log `forge: webhook not ensured; the repository is linked but
   pushes trigger nothing … repository=alice/other err="forgejo: list hooks: … (HTTP 403): user
   should be an owner or a collaborator with admin write of a repository"`, so the priv tag push
   never triggered a build and `e.Publish("priv", "other", …)` could only time out. No test relies
   on the bot lacking admin on alice/other: the job-token scoping checks of
   tests/forge/forgejo_live_test.go use the repository-scoped job token, not the bot's rights.

## I-B26 (2026-10-23, evidence-based harness correction)
50. APPLIED 2026-10-23 I-B26. tests/serverenv/forgejo/forgejo.go app.ini: `[queue] TYPE = channel`
   (was `level`). TestPIPE003PipelinesRunAndReport failed `project acme has fewer than 1 builds
   after 500ms`. Measured on the devstack (private copy, intake instrumented): Forgejo POSTed the
   push hook 3.7 s after `git push` returned (orchestrator `intake start` 02:56:20.909, push
   returned 02:56:17.183); the intake then created the build in 0.34 s. Forgejo's level queue
   polls with a growing backoff (push_update and webhook queues chained), so the delay is the
   test Forgejo's queue configuration. SPEC/budgets B-071 count the budget "after receipt" and
   name the forge's delivery latency as excluded. With the in-memory channel queue the hook
   arrives ~0.2 s after the push and the build is listed 0.55-0.68 s after the push returns.
   No assertion, timeout or budget changed.

## I-B27 (2026-10-23, evidence-based test correction; NOT applied: protected path, coordinator applies)
51. APPLIED 2026-10-23 I-B31 (proposed by I-B27; owner pre-approved evidence-based corrections). tests/core TestRFW047FixtureIRIsCanonicalAndValid panics for every
   fixture (nil pointer at ir_test.go:544). Cause, measured: the IR is valid; the test binary does
   not link the ecosystems. `ir.Parse` rejects kinds this binary cannot plan (SPEC §1.6 "unknown
   kinds rejected"; `nodes[].kind` = "registered kind name"; R-FW-051: `engine.Register` in each
   `pkg/<eco>/gen` init feeds `ir.KnownKind`). The bob binary links all of them through
   `pkg/cli/link.go`; the tests/core test binary links only pkg/golang/gen and pkg/nixpkgs
   (`go list -test -deps ./tests/core`). Probe (scratch package, deleted) calling eval.Evaluate +
   ir.Parse on the nine fixtures: with only golang/gen+nixpkgs linked every fixture fails, e.g.
   acme `node //crates/rcore:rcore@linux-amd64 has the unknown kind "rust.crate"`, polyglot/go
   `"connect.go"`, rust/cpp-lib `"cc.library"`, npm `"npm.app"`, ocaml `"opam.executable"`, codegen
   `"proto.library"`; with `_ "github.com/megakuul/bob/pkg/cli"` linked all nine parse with err nil.
   Second defect: line 543-544 evaluates `len(g.Nodes)` when err != nil (g is nil), turning the
   error into a panic that hides the message. Same class as item 4 (A68: toy kinds linked by a
   blank import in tests/core). Edits:
   a) tests/core/helpers_test.go imports, after `pkg/bob/diag`:
      `_ "github.com/megakuul/bob/pkg/cli" // links the kinds and ecosystems the bob binary links (R-FW-051)`
   b) tests/core/ir_test.go lines 543-545 become
      `if err != nil { t.Fatalf("ir.Parse of the evaluator's own IR: %v", err) }`
      `if len(g.Nodes) == 0 { t.Errorf("ir.Parse of the evaluator's own IR: 0 nodes") }`
   Verified on a private copy (/var/tmp/I-B27/repo, gofmt clean): TestRFW047FixtureIRIsCanonicalAndValid
   PASS for all nine fixtures; TestRFW047IRValidation (incl. unknown_kind "nope.x"), TestRFW044*,
   TestRFW047SchemaFile unchanged PASS; TestRFW046* and TestRFW047GoldenIR fail identically with
   and without the edit (other causes, plan/progress/I-B27.md). No assertion loosened.
   Edit (a) also fixes the same in-process `unknown kind` failures of TestRFW016DefinedAtIsConstructorLine
   (label_test.go:210 `ir.Parse: … node //crates/rcore:rcore@linux-amd64 has the unknown kind
   "rust.crate"`) and TestRFW021IgnoredFilesDoNotChangeSources / ChangeOneFileChangesOnePackage /
   ModesAndMtimes (`build.SourcePaths: … unknown kind "rust.crate"`), seen in the I-B27 run 11:44.

## I-B26 (continued, 2026-10-23, evidence-based test correction; 51 is I-B27's)
52. APPLIED 2026-10-23 I-B26. tests/forge/pipelines_live_test.go TestPIPE005 (1): the cancelled
   status of the superseded build is looked for with Forgejo state `warning` (was `error`). SPEC
   §6.9.2 maps bob's `cancelled` to Forgejo `warning` (GitHub `error`+"cancelled", GitLab
   `canceled`), R-PIPE-005 (1) asks for "forge status `cancelled`", and the protected
   TestForgejoLive asserts `forge.StateCancelled: "warning"` for the same adapter
   (forgejo_live_test.go:198, "SPEC 6.9.2 table"). The two tests could not both pass; the
   description check (`superseded by #<n>`) and the timeout are unchanged.

## I-B27 (continued; 52 is I-B26's)
53. APPLIED 2026-10-23 I-B31 (proposed by I-B27; owner pre-approved evidence-based corrections). tests/serverenv/workerclient/workerclient.go Attach (harness client):
   TestRegisterAndAttach failed once in the CI unit stage (host load 17) with `token with a foreign
   PoP: workerclient: send hello: unknown: write envelope: EOF, want unauthenticated: proof of
   possession invalid`; 5 isolated runs passed. Cause: the server refuses the stream before the
   client's Send completes; connect-go documents (connectrpc.com/connect client_stream.go:304,
   BidiStreamForClient.Send) "If the server returns an error, Send returns an error that wraps
   io.EOF" and the error itself comes from Receive. The harness returns the Send error. Edit: in
   Attach, when `errors.Is(err, io.EOF)` after `bs.Send(hello)`, call `bs.Receive()` and return
   `fmt.Errorf("workerclient: attach: %w", rerr)` if it fails (imports errors, io). The assertion
   (error contains "proof of possession") is unchanged. The same defect in the real worker
   (internal/worker/attach.go) was fixed in place by I-B27 (A170).

## I-B25 (2026-10-23, evidence-based fixture correction)
55. APPLIED 2026-10-23 I-B25. tests/cli/testdata/repos/pipe: libs/bob.go removed, its three
   declarations moved unchanged in substance into the root bob.go (Module `example.com/pipe` Go
   1.26, the binary of `./app` with CGoOff, Tests("./...")). SPEC §03.2.1: NewModule's root is
   the directory of the calling bob.go and Binary's dir is module-relative, so libs/bob.go
   declared a module rooted at libs/ whose binary was libs/app, a directory the fixture does not
   have (go.mod and app/ are at the repository root, and TestRCLI012PipelineRun writes
   app/bad_test.go and app/main.go). Evidence: `bob pipeline run ci` on the unchanged fixture:
   `error[BOB-E-1015]: cannot resolve Go dependencies for //libs/app:app: example.com/pipe/app:
   cannot find package "." in: libs/app` (package defined at libs/bob.go:17); the fixture's own
   comment named a label //libs/greet:greet that no declaration produces. With the correction
   the test is green; the test file is unchanged.

## I-B24 (2026-10-23, evidence-based test correction)
54. APPLIED 2026-10-23 I-B24. tests/oci/oci_test.go TestRREG003PullRegistry, the `norm` helper of
   the "other-project pull must fail like a missing tag" comparison: it replaced only the full
   reference and the host, but podman's error also names the bare tag ("reading manifest <tag>
   in <host>/<repo>"), so the two outputs can never be equal for different tags. Evidence (devstack
   run 2026-10-23 04:16, registry fixed by I-B24/A151): denied = `...initializing source
   docker://HOST/priv/polyglot:1.0.0: reading manifest 1.0.0 in HOST/priv/polyglot: unauthorized`,
   missing = `...docker://HOST/priv/polyglot:no-such-tag: reading manifest no-such-tag in
   HOST/priv/polyglot: unauthorized` — identical except the tag inside podman's own message.
   Edit: norm also replaces `manifest <tag> in` by `manifest TAG in`. The assertion (both fail,
   same normalised text) is unchanged.

56. tests/serverenv/serverenv.go, tests/golden/golden.go: repo-root detection matched go.mod only if it started with `module github.com/megakuul/bob`; once bob owns go.mod its first line is the generated header (SPEC §03.0.2). Now matches `(?m)^module github\.com/megakuul/bob\s*$` as tests/core/layout_test.go does. APPLIED (coordinator, evidence: I-B25 incident 1).

## I-B27 (continued, 2026-10-23; numbers 51, 53, 57, 58 are I-B27's)
57. APPLIED 2026-10-23 I-B31 (proposed by I-B27; owner pre-approved evidence-based corrections). tests/cli/events_test.go knownEvents lacks the `note` event that
   SPEC §02.4 lists (event table, `note` **new**: `type`, `code` (string), `message`, optional
   `label`, `attr`, `platform`, `field`; SPEC §03 R-cross text line ~5272 shows
   `{"type":"note","code":"uncached-target-lib",...}`) and that tests/core/platform_test.go:664
   requires. TestRCLI002JSONStream (build //... --json, --json-log, --print-paths; test //... --json)
   fails `line 6: unknown event "note"` (I-B27 acceptance run, 05:16). pkg/cli/cmd_build.go:305
   emits exactly the SPEC fields. Edit: add
   `"note": {"type": "s", "code": "s", "message": "s"},` to knownEvents (optional fields are not
   listed, as for the other events). No assertion is removed.
58. APPLIED 2026-10-23 I-B31 (proposed by I-B27; owner pre-approved evidence-based corrections). tests/eco/golang/helpers_test.go:52 and tests/eco/cc/helpers_test.go:52
   noLeak leak-check `r.Combined()` including the package.finish `paths` of `--print-paths --json`,
   which are host store paths by contract (SPEC §02.4 `paths[]` "host paths, only with
   --print-paths"; §02.6). Same defect and fix as item 29 (tests/eco/rust, I-M8b) and item 13a
   (TestRFW051, A116); tests/eco/internal/ecotest Bob already strips them. Every outputPaths call
   fails `shows raw Nix text: store path: "{...\"event\":\"package.finish\",...\"paths\":[\".../nix/store/<hash>-..."]}"`:
   TestRGO001aBuildTestCrossAndReproducible (both platforms), TestRGO005CutoffEarlyCutoff,
   TestRCC001aBuildTestCrossAndReproducible (both), TestRCC005aExternalPinnedAndVerified (I-B27
   acceptance run 08:19-08:58). Edit: copy tests/eco/rust's `withoutFinishPaths` into both
   packages and check `withoutFinishPaths(r.Stdout) + r.Stderr`; stderr, other events and other
   fields stay checked. Same defect in tests/core/ir_test.go:83 TestRFW046ProjectionIgnoresUndeclaredAxes
   (`noLeak(t, r)` on `build //toy:b@linux-arm64 --print-paths --json`): apply the same filter there
   (TestRFW046 also has a real failure after the leak lines: //toy:a has no paths[] in the +debug
   run, see plan/progress/I-B27.md).
   Applied as written in tests/eco/golang and tests/eco/cc. In tests/core/ir_test.go the filter is
   the package's existing `withoutPaths` helper (`noLeak(t, withoutPaths(r))`, the form TestRFW051
   in the same file already uses, item 13a) instead of a second copy of `withoutFinishPaths`.
   Items 51 (both edits), 53 (Receive before cancel, imports io) and 57 applied as written. I-B31.

## I-B32 (2026-10-23, evidence-based test correction)
59. APPLIED 2026-10-23 I-B32. tests/nix/closure_test.go `buildPath` (TestRNIX030ClosureBudgets
   cross-native-libs, cross-Cross()): it looked up the `package.finish` event whose label starts
   with the configured label `//cmd/api:api@linux-amd64`. bob's events name packages by their
   plain label for every configuration (SPEC §02.4 defines `label` and shows only plain labels;
   no SPEC text puts a configured label into events), and five other protected tests require
   exactly that for cross builds of the same kind: tests/core/platform_test.go:159-163
   (TestRFW030, `printPaths(…"//cmd/api:api@"+other)` then `paths["//cmd/api:api"]`), :588, :707
   (same with `+asan`), tests/platforms/platform_test.go:60-64 (`--platform other` →
   `paths["//cmd/api:api"]`), tests/eco/golang/build_test.go:43-47 (and the same lookup in
   tests/eco/rust and tests/eco/cc cross subtests). Emitting configured labels would break all of
   those; the two expectations cannot both hold. Edit: `buildPath` matches the label's plain part
   (`strings.Cut(label, "@")`) by prefix, so it also accepts a configured label should SPEC ever
   add one. The budgets and the closure measurement are unchanged.

## I-B33 (2026-10-23, evidence-based test correction)
60. APPLIED 2026-10-23 I-B33. tests/outputs/appimage_test.go TestROUT007Pruning builds
   `//ocitest:pruned` (the OCI half of "pruning a library the binary needs is a build error ...
   also for images"), but the test's world is `appWorld`, which overlays only `apptest/bob.go`;
   `ocitest/bob.go` (overlays_test.go `ociOverlay`, the package that declares `Pruned`) is written
   only by the OCI tests' world. Evidence (I-B33 run 13:30): `bob build //ocitest:pruned` → exit 2
   `error[BOB-E-0003]: pattern //ocitest:pruned matches no package`. Edit: one line,
   `w.overlay(ociOverlay())` after `appWorld(t)` in TestROUT007Pruning. The expected exit code and
   message, the size budget and every other assertion are unchanged. With the overlay a private
   build of the same declaration prints exactly the expected
   `✗ //ocitest:pruned: prune pattern "/nix/store/*-openssl-*/lib/libssl.so*" removes libssl.so.3, which api needs`.

## I-B34 (2026-10-23, evidence-based test and fixture corrections; numbers 61-65)
61. APPLIED 2026-10-23 I-B34 (A250). tests/cache/cache_test.go TestRREG154DownloadTokenScope and
   tests/serverenv/regenv/regenv.go (new `CLIDownloadToken`). The test called WhoAmI with a
   service-account token of scope download (`e.Principal`, a `bob_sa_` secret) and wanted
   token_scope "download". SPEC R-REG-154 is about `bob_dt_` tokens ("`bob_dt_` tokens are accepted
   by WhoAmI and download endpoints only"); R-AUTH-005 says an SA download token "gets
   Unauthenticated on /api/", §6.3.1 lists SA tokens as `bob_sa_` and the CLI session's download
   token as `bob_dt_`, and the protected TestRAUTH005ServiceAccounts calls WhoAmI with exactly
   such an SA token and wants Unauthenticated "token not valid for the API". The two tests could
   not both pass; the server follows SPEC (internal/auth/identity.go refuses SA download tokens on
   every /api/ method, D-IB4-2). Edit: `CLIDownloadToken(login)` creates a local user, logs in
   (LoginPassword -> bob_session), runs the device flow of §6.3.2 (/oauth/device/code, POST
   /device allow, /oauth/token device grant) and returns `bob_download_token` (checked to start
   with `bob_dt_`); the test uses it instead of the SA token. All three assertions (WhoAmI scope,
   ListProjects Unauthenticated "token not valid for the API", nix-cache-info 200) are unchanged.
   Result: PASS (18.9 s, I-B34 run 13:33).
62. APPLIED 2026-10-23 I-B34 (A251). tests/cache/helpers_test.go `copyFrom` and `closure`
   (used by TestRREG001, RREG150, RREG152TwoOrchestrators, RREG153, the slow RREG151/152 tests).
   `copyFrom` ran `nix copy --from <bob>/nix` with bob's keys only, and the tests then verified
   the whole closure with bob's key alone. The acme release closure holds nixpkgs paths
   (x86_64 glibc, gcc-lib, openssl, …); cache.nixos.org serves them (HEAD
   https://cache.nixos.org/54cd56yscrlh84g0fhn67gjpl9cd1w22.narinfo = 200 for the
   `x86_64-unknown-linux-gnu-gcc-15.3.0-lib` the test failed on), so R-REG-005 forbids bob to
   store or serve them (the protected TestRREG005SkippedPathsAreNotServed asserts the 404). A
   bob-only copy of such a closure can never succeed. SPEC R-REG-001 (1) says "embedded Nix with
   bob's config", and §6.6.5 bob's client config is `substituters = https://cache.nixos.org
   https://<server>/nix` with both keys. Edits: (a) `copyFrom` substitutes with that config
   (`nix build --no-link --store <local> --max-jobs 0`, substituters `<bob> https://cache.nixos.org`,
   trusted keys = the given keys + cache.nixos.org-1, netrc unchanged); (b) it also sets
   `narinfo-cache-positive-ttl 0` (it had only the negative one): every local store of a test
   shares one client home, and with (a) the second step of TestRREG001 found the root's narinfo in
   Nix's disk cache from step (1) (fetched with the p1 token) and tried the NAR anonymously
   (`copying path ...cc-foo-linux-amd64 from 'http://127.0.0.1:…/nix'`, NAR 404) — the server
   answered correctly, the client cache made it look like a leak; (c) `closure` returns the
   bob-built part (every closure path without a cache.nixos.org-1 signature) and verifies the
   cache.nixos.org part against the cache.nixos.org key alone. Every bob path must still come
   from bob (it is in no other cache) and still verifies against bob's key alone; the
   anonymous, other-project and wrong-token assertions are unchanged.
   Result: TestRREG001NixSubstitution PASS (493 s, run 14:03).
63. APPLIED 2026-10-23 I-B34 (A253). tests/cli/testdata/repos/exits/tools/bob.go and the labels
   in tests/cli/output_test.go (TestRCLI002JSONStream, TestRCLI002VerboseAddsLines,
   TestRCLI007HumanOutput, TestRCLI007Colour) and tests/cli/exitcodes_test.go
   (TestRCLI006ProgramExit). The fixture declares the module in tools/bob.go (module root = tools/,
   SPEC §03.2.1 NewModule) and `Binary("./tools/exit3")`, i.e. the directory tools/tools/exit3,
   which does not exist; the evaluated labels are //tools/tools/{broken,exit3,hello,selfkill}
   (probe, `bob graph --json`). The tests wanted //tools:exit3, which no declaration can give:
   Binary's label is `//<module dir>/<dir>:<Name>` (§03.2.1), and four main packages need four
   directories. Same defect class as item 55 (pipe fixture). Edits: Binary dirs `./exit3`,
   `./selfkill`, `./hello`, `./broken` (module-relative; label comments updated); the tests use
   //tools/exit3:exit3, //tools/selfkill:selfkill, //tools/hello:hello, //tools/broken:broken.
   `package defined at tools/bob.go:`, exit codes, `//tools:nosuch` (still no such package) and
   every other assertion are unchanged. Probe on a private copy: `bob run //tools/exit3:exit3`
   exits 3, `bob run //tools/hello:hello -- a b` prints `hello a b`, `bob build
   //tools/broken:broken //tools/hello:hello` = FAILED //tools/broken:broken, BOB-E-2001, exit 1.
   Result (run 15:30): TestRCLI006ProgramExit PASS; TestRCLI002JSONStream exits subtests PASS
   (its `--json-log` subtest fails: no log event on the fully cached second build of acme); the
   label, ordering, error-block and `package defined at tools/bob.go:` checks of
   TestRCLI007HumanOutput pass, it still fails on the summary line (`bob: 1 failed (exit 1)`), the
   missing golden and the success summary; TestRCLI007Colour fails on NO_COLOR under a pty;
   TestRCLI002VerboseAddsLines fails because the -v run follows the default run in the same store
   (`built` vs `cached`) and on a 4-minute harness timeout of `test //...` under load. None of
   these involve the labels.
64. APPLIED 2026-10-23 I-B34 (A254). tests/platforms/platform_test.go restrictedAppGo:
   `package restrictedapp` -> `package app`. R-FW-002 R2: a directory without Go files takes the
   directory name (apps/app holds only main.c), so the overlay failed with BOB-E-1024 `package
   restrictedapp, but the Go code in this directory is package app` before reaching the platform
   check. Result: TestRPLAT006PerPackagePlatformRestriction PASS (258 s).
65. APPLIED 2026-10-23 I-B34 (A255). tests/faults: patch.diff for 49, 51, 58-66, 69, 70 (they had
   none, so the live runner ran the unmodified fixture: exit 0 or another error), fault.json and
   expect.json of those entries aligned with SPEC, and the runner's per-invocation timeout.
   HARNESS.md §5.1 says the added faults run live from `patch.diff`; tests/faults/README says
   49-81 have no capture by design. Each patch was applied to a scratch copy of its fixture and
   run with bob (I-B34, 13:30-14:00); output as SPEC's code table and R-FW-002 texts say:
   - 58-63 (acme, `bob check`): R1 first line removed; R2 `package greeting`; R3 import of a new
     main package tools/version's bob.go; R4 `var Hello` (greet.go declares Hello); R6
     internal/greet/bob.go imports a new internal/hello whose Go code imports greet; R7 import of
     github.com/google/uuid. Each prints exactly its R-FW-002 text as BOB-E-1024, exit 3.
     fault.json: position marker -> none and hint true -> false: the 1024 row of the code table
     (SPEC §02) has no hint ("—"), and the rule texts carry a file (R4 file:line), not a package,
     so no `package defined at` line exists. expect.json gains the rule text as `messages`.
   - 64/65 (acme): Srcs `bob.Glob("../../../outside/*.cpp")` / `bob.Glob("src/*.cc")` ->
     BOB-E-1025 `<pos>: source pattern … leaves the repository` / `bob.Glob(…) matches no files`,
     exit 3; position none and hint false (1025 row: no hint, the position is inside the
     message); messages added.
   - 66 (acme): Defines from time.Now(). command `build //...` -> `check --determinism`: R-FW-008
     detects non-determinism only by `bob check --determinism` (two evaluations); a build
     evaluates once. Output BOB-E-1018 `evaluating bob.go is not deterministic: //libs/foo:foo
     differs between two runs (field nodes[…].spec.defines.BUILD_TIME)` + hint, exit 3; position
     none (the message names the label); message added.
   - 49 (go): `require golang.org/x/text v0.20.0` appended to api/go.mod -> BOB-E-1011 `generated
     file api/go.mod was edited by hand` + hint, exit 3 (R-FW-061); position none.
   - 51 (polyglot): bob.lock with <<<<<<< / ======= / >>>>>>> -> BOB-E-1019 `bob.lock cannot be
     read: merge conflict marker <<<<<<< (line 1)` + hint, exit 3 (R-FW-048); position none.
   - 69 (codegen): svc/bob.go requires github.com/99designs/gqlgen v0.17.89 (SPEC §03.6.4 / R-GEN-006
     F4), marker on the graphql.Gqlgen call -> `//svc:gen-graph: generator gqlgen 0.17.90 (bob's
     nixpkgs pin) does not match its runtime: … but svc/bob.go requires v0.17.89`, exit 3. Entry
     kept as marker/hint; messages added. Still FAILS on bob: the line is printed as `✗ …`
     without `error[BOB-E-1021]`, and `package defined at svc/bob.go:24` has no column (FEAT).
   - 70 (codegen, `bob gen`): hand-written svc/gen/rest/version.go -> BOB-E-1016 `svc/gen/rest/
     version.go conflicts with generated output of //svc:gen-rest: bob did not write it` + hint,
     exit 3; position none.
   tests/errors/run.go: default per-invocation timeout 2 min -> 10 min. A live run starts with an
   empty HOME; measured cold `bob build //...` of fixture acme with patch 64: 2m13s, cold `bob
   check` with patch 58: 59 s (8-core host, load ~15); with 2 min every evaluating entry was
   "killed or timed out" (I-B34 run 14:45, entry 49). A hang still fails the entry.
   tests/errors/run.go `events`: adds `note` (SPEC §02.4 event table, `note` **new**; the same
   gap as item 57 in tests/cli). 64/65 failed only on `--json: line 6: unknown event "note"`
   (bob's `note: no version given; using 0.0.0-dev`).
   Result (TestFaults filtered to these 13 entries, run 14:47-15:25, then 64/65 again): 49, 51,
   58-63, 64, 65 (after the `note` fix), 66, 70 PASS (12 of 13); 69 FAIL on bob (FEAT, above).
   TestCatalogue, TestRunnerClassifies, TestCheckJSON, TestCapturedGoldensDoNotLeak, TestMarkerPos PASS.
66. APPLIED 2026-10-23 I-B34 (A257). tests/cache/promote_test.go TestRREG005SkippedPathsAreNotServed:
   `e.DelayStaging(20 * time.Second)` -> `5 * time.Second` (comment added). The delay is only
   there so that awaitStaged (200 ms polls) sees COMPLETE.json before the promotion, but every
   staged GET of the promotion pays it, and the test then waits ≤ 5 min (waitPromotion) for
   `done`. Measured (run 17:00-17:10, with A252): the acme release job stages 29 paths; COMPLETE
   at 17:05:22, `promoted 29` at 17:09:26 (SPEC §6.6.4 step 3 runs 4 paths at a time per level,
   so ≥ ceil(29/4) x 20 s = 160 s for the NAR GETs alone), then step 4 reads the image index,
   manifests and blobs from staging and step 5 the assets, each GET +20 s: `done` cannot arrive
   within 5 minutes. With 5 s the same test PASSES (549 s, I-B34 cache run 6); the assertions (skipped
   non-empty, promotion `done`, every skipped path 404) are unchanged. The tampered/forged-claim
   tests keep 20 s (they end at the first failing path and pass).
67. APPLIED 2026-10-23 I-B34 (A258). tests/cache/visibility_test.go `servedPaths`
   (TestRREG150VisibilityDocuments): it took the 10 distinct paths of R-REG-150 from the
   bob-built part of the release closure only, which for fixture acme holds 2 paths (the cgo
   binary and libfoo; C++ and Rust are linked in): `closure … has 2 bob-served paths, the
   acceptance needs 10 distinct` (I-B34 cache run 4). R-REG-150 asks for "10 distinct paths", not paths of
   one closure. Edit: servedPaths still substitutes the root (same check as before) and returns
   the store hashes of the closure's bob paths plus every other promoted narinfo (S3 listing of
   nix/narinfo/ on the backend, not through the counting proxy) that answers 200 for the token.
   The counting (≤ 20 GETs on nix/vis/, 0 LIST) and the private-within-31 s assertions are
   unchanged. Result: PASS (373 s, I-B34 cache run 5).
68. APPLIED 2026-10-23 I-B34 (A259). tests/fixtures/repos/nondet/bob.go (fixture of
   TestRREG152NonReproduciblePathKeepsFirstPublication): `bob.Pipeline("ci", bob.PipelineOpts{})`
   -> `Triggers: OnPush("*"), Targets: //:nonce`. Its comment said "SPEC §06 does not define the
   fields of bob.PipelineOpts, so the pipeline is registered by name only"; SPEC §1.4.4 now
   defines them ("Triggers: none = manual runs only", "Targets: packages to build"), and the
   pipeline matcher (internal/build/jobspec Match) runs no pipeline on a push without a matching
   trigger, so both pushes built nothing (staging held only eval.json; I-B34 cache run 7). With the
   correction both builds build //:nonce.

## I-B35 (2026-10-23, evidence-based test correction)

69. APPLIED 2026-10-23 I-B35 (A276). tests/core/label_test.go TestRFW016FailingBuildShowsDefinedAt:
   `want := "(defined at libs/foo/bob.go:<line>)"` (Contains) -> the line
   `package defined at libs/foo/bob.go:<line>[:<col>]` (regexp, whole line).
   SPEC conflicts with itself: R-FW-016's acceptance sentence writes `(defined at
   libs/foo/bob.go:13)`, while §02.5 rule 2 fixes the block line `package defined at
   <file>:<line>:<col>` and R-CLI-007 requires the S15 goldens byte for byte (tests/faults/*/golden.txt,
   e.g. 06-cxx-compile: `package defined at fixture/libfoo/bob.go:8:11`, 58 occurrences). bob
   prints the §02.5 line (`package defined at libs/foo/bob.go:13`; this definedAt has no column). The
   position asserted (the constructor line, the R-FW-016 point) is unchanged; only the wrapper text
   follows the format rule with goldens. Result: PASS (I-B35 run 4). NEEDS: owner/coordinator to amend
   the R-FW-016 sentence.

## I-B36 (2026-10-23, evidence-based test correction)
70. APPLIED 2026-10-23 I-B36 (A285). tests/core/native_test.go TestRFW067GoplsResolvesGeneratedCode:
   `execShim(t, filepath.Join(dir, "svc"), nil)` -> `execShim(t, dir, nil)` (the next line keeps
   `cmd.Dir = svc`, so gopls still starts in the svc module). execShim runs `<arg>/.bob/bin/gopls`;
   SPEC puts the shims at the project root only: §02.7.2 `PATH=<root>/.bob/bin:$PATH`, §02.7.3
   `.bob/bin/<tool>` with `exec bob exec --project '<root>' -- gopls`, the editor table
   (`${workspaceFolder}/.bob/bin/gopls`), and R-FW-067 "through the shim". The root of the codegen
   fixture is codegen/ (bob.lock), so `bob env` writes codegen/.bob/bin/gopls; codegen/svc/.bob/bin
   never exists (I-B31 T5: the test looked the shim up at codegen/svc/.bob/bin/gopls and could not
   start gopls). Same pattern as the protected TestRFW001GoplsSeesBobGo (api_test.go:494,
   `execShim(t, dir, env)` at the root). The assertions (0 gopls errors in cmd/server/main.go,
   definition of greetv1.GreetRequest in gen/proto/greet/v1) are unchanged.

## I-B38 (2026-10-23, evidence-based harness test correction and generated budgets file)
71. APPLIED 2026-10-23 I-B38 (A302). tools/ci/stage_budgets_test.go TestBudgetStagesPass. Its
   comment states its premise: "With no benchmarks registered every row is "no benchmark yet",
   so both the gate stage and the slow bench stage pass". The budgets file's own note (and
   HARNESS §12) says each task registers its benchmark in tools/internal/budgets/benches.go and
   the row then counts; I-B38 registered 27 rows. From then on the test ran every real benchmark
   twice inside the unit stage (`go test ./tools/ci`): evidence (I-B38 run 22:10): with
   -test.timeout 140s it was still in `budgets.Run` -> `budgets.execCommand` running
   `go run ./tools/budgetbench noop-rust` (the 6th of 15 commands) when it panicked
   "test timed out after 2m20s"; the full set takes about 12 minutes per pass on this host (fleet
   75 s, idle fleet 90 s, dev stack 30 s, fixtures), fails whenever a row is over its limit on a
   loaded host, and writes tests/budgets/baseline-8c15g.json from a unit test. Edit: the test
   checks the same two things without running benchmarks: budgets.Check("../..") (file current)
   and budgets.Run for slow=false and slow=true with an Exec stub that answers every registered
   command with each of its rows at exactly its limit, in a scratch root (baseline written there,
   not in the repository). The real measurements stay in the budgets/bench stages. Result: PASS.
   Also: tests/budgets/budgets.json regenerated with budgets.Write (the gen stage's function) after
   the registration; the diff is the "bench" field of 27 rows only (checked: every other field of
   all 97 rows and the header identical).

## I-B37 (2026-10-23, evidence-based test correction)
72. APPLIED 2026-10-23 I-B37 (A294). tests/server/log_commit_test.go TestRLOG001SizeTriggeredCommit
   and TestRLOG001KillLosesOnlyUncommitted: new helper `warmWorker` (one `//warm:t` build of a
   trivial package, waited for with WaitBuild 5 min) called right after srvtest.Start, before the
   test's own context and submissions. Both tests start their 60 s / 90 s windows at the first
   submission on a fresh stack, so the window held the cold start of a fresh worker, which is not
   what R-LOG-001 measures (commit triggers, loss on SIGKILL). Evidence (I-B37 probes, 8 cores,
   load 10-16, `/bin/bob job-run` process tree sampled every second): claim 6 ms after "build
   planned"; then nixpkgs channel tarball download and unpack (~21 s), `nix-store --import` of it
   (~22 s), go toolchain realise (~4 s), the evaluator's standard-library seed (~38 s), evaluator
   compile, build: first output line 83-125 s after the submission (I-B37 cold1/cold2/cold3,
   rlog4: the 90 s window missed by 3 s). With the fixes A290 and A292 a warm worker shows the
   first line 4.2 s after the submission (cold3 round 1, was 88 s before them). The assertions
   (>= 10,000 / >= 2,000 lines readable within 60 s with a 10 min flush interval; gap-free attempt
   1 prefix, no committed line lost, attempt 2 continues at the next seq) are unchanged. Result
   (shared tree, with A290-A293): both PASS (rlog5: kill test; rlog6: size test).
73. APPLIED 2026-10-24 I-B37 (A300). tests/store/fleet/e2e_test.go TestRFLEET008BuildsAreIsolated, the
   final "persistent store is unchanged" check: `exec.Command(nix, …, "store", "verify",
   "--check-contents", "--all", "--store", "local?root="+store)` -> `exec.Command(filepath.Join(nix,
   "bin", "nix"), …, "store", "verify", "--all", "--no-trust", "--store",
   "local?root="+filepath.Dir(store))`. Three defects, each shown by a run (I-B37 fl2/fl3/fl4):
   (1) serverenv.NixTool returns the package's store path (its doc: "returns its store path"; every
   other caller joins bin/<tool>, e.g. tests/auth/api_test.go:192, tests/platforms/helpers_test.go:198),
   so the test exec'ed a directory: `fork/exec /nix/store/…-nix-2.34.9: permission denied` (I-B27
   row and I-B37 triage). (2) The pinned Nix 2.34.9 has no `--check-contents` for `nix store
   verify` (`error: unrecognised flag '--check-contents'`); its help: contents are verified by
   default, `--no-contents` disables it. (3) `local?root=R` opens R/nix/store; the worker's store
   directory <state>/worker/nix is what job containers mount at /nix (internal/worker process.go
   bind {Host: p.store, Path: "/nix"}), so R is its parent; with the old root the verify opened an
   empty store below the worker's store. With (1)-(3) the verify exits 2 with 13 "is untrusted"
   lines and no corrupted path: bob's own imported sources and derivations carry no signature.
   SPEC §05.8 R-FLEET-008 asks for "persistent store is unchanged (`nix store verify
   --check-contents` = 0 errors)", i.e. contents, hence `--no-trust`. The hostile-package part of the
   test is unchanged (still red: `bob test //lib:tests` runs no test, engine NEEDS in I-B37.md).

## I-B40 (2026-10-24, evidence-based test corrections T1-T13 of I-B31, 58b, RCC005a; ruling A310)
Evidence for each defect: plan/progress/I-B31.md "TEST PROBLEM" (T-numbers), I-B35.md (RCC005a);
results: plan/progress/I-B40.md. Assertions are unchanged unless stated.
74. APPLIED 2026-10-24 I-B40 (A320, T12). tests/core/helpers_test.go: new `testHomeEnv(t)` = one
   HOME and BOB_HOME (`<t.TempDir>/testhome`, `…/bob`) per test (per *testing.T, created on first
   use); `bobRunEnv` prepends it to the caller's env, so a test's own `BOB_HOME=`/`HOME=` still wins
   (later entries win in os/exec). harness.Run gives every call a fresh HOME, so before this each bob
   call of a test started with an empty store and caches (I-B31: the second `bob test` rebuilt 13
   drvs and downloaded 569 MB; the chain's warm-up ran in another store). Also tests/core/native_test.go
   TestRFW066KillDuringWrite and TestRFW067DevProtoEditReachesTree (exec.Command with os.Environ(),
   i.e. the host user's HOME) now add testHomeEnv(t), the home their `bob gen` used; and
   bobNoNetworkHome uses the test's HOME when a warm BOB_HOME is given (its doc says "caches warmed
   by an earlier run"; the Go proxy cache is $HOME/.cache/bob/goproxy, SPEC §03.2.5; evidence: the
   warm no-network `bob lock` of TestRFW005LockOnDemand failed BOB-E-1015 "lookup proxy.golang.org …
   network is unreachable" with a fresh HOME, passes with the test's HOME). Result:
   TestRFW024PassingTestsAreCached PASS, TestRFW020ChainOf100Steps PASS (release+debug),
   TestRFW066ManifestLossAdoptsFiles PASS, TestRFW066KillDuringWrite PASS (40 kill points; before it
   needed ~25 GB of temp space for 80 fresh stores).
75. APPLIED 2026-10-24 I-B40 (A321, T1). tests/core/helpers_test.go bobNoNetworkHome (new
   `noNetworkAttr`) and tests/eco/rust/helpers_test.go noNetwork: when the test process is root, the
   new user namespace maps 65534->65534 (uid and gid) next to 0->0, with setgroups allowed (root
   writes the maps of its child). evalbox (rootIDs/asNobody) refuses root in a namespace without
   65534 and runs the sandbox as 65534 with it, exactly as for a run as host root (store.Enter maps
   65534 next to 0 the same way). CLONE_NEWNET is unchanged, so "no network" is proved as before.
   Result: no more BOB-E-9005 "refusing to evaluate as root"; TestRFW062StaleSectionPerEcosystem/npm
   PASS; RFW005LockOnDemand and RFW062/go,rust now reach bob's 1012 and fail on implementation
   wording/detection (I-B40 table).
76. APPLIED 2026-10-24 I-B40 (A322, T2). tests/core/lock_test.go TestRFW005RemovalAndPins: with the
   uuid Require removed, also remove cmd/api/main.go's `"github.com/google/uuid"` import and its
   `_ = uuid.NewString()` (restored before the `build --lock` step). Before, bob lock correctly
   failed BOB-E-1015 (cmd/api still imports uuid). Result: PASS.
77. APPLIED 2026-10-24 I-B40 (A323, T3). tests/core/native_test.go TestRFW066ConflictAndForce,
   OtherRootsProceed, TrackedFileIsNeverOverwritten: the foreign/tracked file goes into an owned dir
   (the edited file's dir / the dir of the first generated file), not svc/gen/proto itself; SPEC
   §03.6.1 GoOpts.Dir default "gen/proto/<proto dir relative to Root>" = one owned dir per proto dir
   (svc/gen/proto/common/v1, svc/gen/proto/greet/v1). The --force backup check lists files below
   .bob/gen/backup recursively (the old glob matched the directory "common"). The BOB-E-1016 message
   check (`svc/gen/proto`), exit codes and the untouched-root check are unchanged. Result:
   OtherRootsProceed PASS, TrackedFileIsNeverOverwritten PASS; ConflictAndForce passes every
   assertion except the missing golden tests/goldens/native/codegen/conflict.txt (owner).
78. APPLIED 2026-10-24 I-B40 (A324, T4). tests/core/native_test.go TestRFW066RemovedRecipeRemovesRoot
   also removes the then unused `"github.com/megakuul/bob/pkg/openapi"` import from svc/bob.go (before:
   `bob gen` exit 3 "imported and not used"). Result: gen exits 0; the test still fails on a real
   finding: svc/gen/rest remains after its recipe is removed (materialise, FEAT).
79. APPLIED 2026-10-24 I-B40 (A325, T6). tests/core/native_test.go
   TestRFW067TypeScriptResolvesGeneratedCode stats greet_pb.ts instead of greet_pb.js (SPEC §03.6.1
   connect.TS = protoc-gen-es target=ts, import_extension=js; R-FW-067's `./gen/proto/greet/v1/greet_pb.js`
   is the import spelling, which tsserver resolves to the .ts file). The tsc --noEmit check is
   unchanged. Result: the stat passes; the test then fails at `bob exec -- tsc` with BOB-E-7006 (no
   .bob/env.json after `bob gen`), open (needs a ruling, I-B40 table).
80. APPLIED 2026-10-24 I-B40 (A326, T8 + T8b). tests/core/helpers_test.go bobRunEnv skips the leak
   check for `-vv` as for `-v` (as tests/eco/* noLeak; raw Nix text is allowed from -v on).
   T8b, found after T8 in the same test: tests/core/pipeline_test.go TestRFW063NoNixBuildActivityForSources
   adds the hosts of bob.lock urls as `://<host>`: the nixpkgs pin's url is
   https://github.com/NixOS/nixpkgs/archive/… (pkg/engine/lock/nixpkgs.go), so plain "github.com"
   matched the Go module path in every Go module derivation (`Nix ran a build of
   …-go-github.com-google-uuid-v1.6.0.drv that fetches from github.com`). A fetch names the URL. The
   built-in host list is unchanged. Result: PASS (go, rust, npm, ocaml, cc).
81. APPLIED 2026-10-24 I-B40 (A327, T10). tests/core/platform_test.go TestRFW034RustTargetStd: a
   derivation that names static.rust-lang.org fails the test when it is a bob derivation or names
   rust-std; nixpkgs' own rustc-src and bootstrap fetches (closure of
   legacyPackages.<sys>.rustc.unwrapped, which R-FW-034 prescribes) no longer do. Result: PASS.
82. APPLIED 2026-10-24 I-B40 (A328, T11). tests/core/platform_test.go TestRFW038DerivationNames expects
   `cc-foo-<other>-dbg-address-undefined`: polyglot's named variant asan is
   WithSanitize(ASan, UBSan)+WithProfile(Debug) (fixture bob.go:18), and R-FW-038 orders the tags
   `dbg`, `address`, `undefined`. Result: PASS.
83. APPLIED 2026-10-24 I-B40 (A329, T13). tests/cli/output_test.go TestRCLI002JSONStream: the
   `build //... --json --json-log` run comes first (the plain `--json` build ran first with the same
   home, so everything was cached and no `log` event could appear). Result: PASS (12/12 subtests).
84. APPLIED 2026-10-24 I-B40 (A330, 58b). tests/core/helpers_test.go bobRunEnv: for runs with
   `--print-paths --json` the leak check reads withoutPaths(r) (package.finish paths[] are host
   store paths by SPEC §02.6; same as correction 58 and ir_test.go). Covers printPaths (platform) and
   outputPaths (steps). Result: TestRFW030CrossBuildKeepsHostSystem, TestRFW023StepEnvironment,
   DeclaredOutputs, SameOutputOnTwoMachines, TestRFW037CrossSanitiserRunpath PASS.
85. APPLIED 2026-10-24 I-B40 (A331, I-B35 T-RCC005a). tests/eco/cc/external_test.go
   TestRCC005aExternalPinnedAndVerified (c): after `bob env --format json` (bob exec needs
   .bob/env.json, else BOB-E-7006, R-DEV-008; first attempt without it failed so), the app runs
   through `bob exec -- env -i /nix/store/<out>/bin/app` (same bob home) instead of executing the host path
   `<home>/nix/store/<out>/bin/app` directly. SPEC §02.6: --print-paths host paths "run only through
   bob run/bob exec"; on the host /nix/store is the system store, where libpack.so does not exist.
   env -i keeps the empty environment, so the External's library is still found by RUNPATH only.
   Result: PASS. Also T1 in tests/eco/rust (75): TestRRS005LockCarriesCrateGraphFacts PASS.
86. APPLIED 2026-10-24 I-B41 (A333 coordinator ruling, A340). tests/core/native_test.go
   TestRFW067TypeScriptResolvesGeneratedCode: `bob env --format json` runs before
   `bob exec -- tsc --noEmit -p web` (bob exec stays strict: BOB-E-7006 without .bob/env.json,
   R-DEV-008), as TestRFW067GoplsResolvesGeneratedCode and RCC005a (85) do. Before: exit 7
   BOB-E-7006 right after `bob gen` (I-B40 table, correction 79). The stat of greet_pb.ts and the
   tsc checks are unchanged. Result: PASS (I-B41 run, 198 s).
Not applied (evidence not clear, need a decision): T7 (RFW063 flat-hash check: the Go part is not
explained), T9 (polyglot has no nixpkgs.Tool("protobuf"): fixture decision), RFW038ProjectionTable
"fetch / unpack" row (needs a store-path check design). RFW010 stays as is (A279).

## I-B42 (2026-10-24, evidence-based test corrections; evidence plan/progress/I-B41.md TEST PROBLEM 1/2, I-B42.md)
87. APPLIED 2026-10-24 I-B42 (A353). tests/core/native_test.go TestRFW067DevProtoEditReachesTree:
   `bob dev --servers-only --no-servers` starts with the environment its `bob gen` (bobRun ->
   harness.Run) used: `PATH=/usr/bin:/bin`, `TZ=UTC`, `LC_ALL=C` plus testHomeEnv(t), instead of
   os.Environ() + testHomeEnv. Evidence (I-B41, A343): with the host PATH (/root/.nix-profile/bin/go,
   go1.26.9) bob dev's evaluator key differs from bob gen's (no go on the minimal PATH -> nixpkgs go
   1.26.8; R-FW-043.5: go on PATH wins and is part of the key), so bob dev evaluated cold and the
   first codegen pass took 15-34 s; with one environment 0.23 s. A developer runs both with one
   PATH. The 2.5 s / 10 s assertions are unchanged. Result: PASS (I-B42 run, 163 s incl. bob gen).
88. APPLIED 2026-10-24 I-B42 (A354). tests/cli/helpers_test.go tree (the tree hash of
   TestRCLI009Check, called at commands_test.go:60): a symbolic link is hashed by its path and
   target (os.Readlink) instead of os.ReadFile, which follows the link and failed with `read
   …/web/node_modules/@sveltejs/vite-plugin-svelte: is a directory` (I-B41 table; the materialised
   node_modules holds links to directories). WalkDir does not follow links, so before a link to a
   directory could not be hashed at all; a changed target still changes the hash. Assertions are
   unchanged. Result: the hash works; TestRCLI009Check now reaches its assertions and fails on
   commands_test.go:85 (bob check rewrites .bob/eval after a go.mod edit; TEST PROBLEM 1 in
   plan/progress/I-B42.md, needs a ruling, not applied).

## I-B45 (2026-10-24, evidence-based test, harness and fixture corrections; evidence plan/progress/I-B45.md)
89. APPLIED 2026-10-24 I-B45 (A381). tests/nix/layer_test.go TestRNIX002NixpkgsStaysUsable: with the
   bob.go edit (Go "1.26" -> "1.25") the test also edits the fixture's committed root go.mod
   `go 1.26.0` -> `go 1.25.0` (fails as a harness error if the line is missing). Before: the only
   error event was BOB-E-1011 "generated file go.mod was edited by hand" (`~ go 1.25.0 → 1.26.0`):
   a fresh copy has no .bob/state/native.json record that bob wrote go.mod, so the stale go line
   is drift by R-FW-061 ("any difference fails"); deleting go.mod instead does not work (the
   evaluator needs the root go.mod: BOB-E-9005 "the repository has no go.mod"). The commit that
   lowers Go in bob.go carries the regenerated go.mod. The assertion (only 1008/2006 may fail
   against nixos-25.11) is unchanged. Result: PASS (278 s).
90. APPLIED 2026-10-24 I-B45 (A382). tests/nix/env_test.go recomputeClosure (TestRNIX006RecomputeHello):
   Finalize gets a memoising resolver (new memoClosure: each derivation's hashDerivationModulo
   computed once, as bob's own resolvers do, pkg/engine/build resolver.memo). parsedClosure
   alone re-hashed every input once per path leading to it, exponential in the depth of the
   stdenv bootstrap: the test ran > 75 min and hit the package timeout (I-B27, I-B44). The
   comparison of every recomputed output path and drv path with the recorded ones is unchanged.
   Result: PASS (156 s incl. the nixpkgs fetch; 228 derivations).
91. APPLIED 2026-10-24 I-B45 (A383). tests/eco/golang helpers_test.go runGuest (+ build_test.go
   callers, new dir argument; runEnvI removed, it had no other caller): the host-platform binary
   runs through `bob env --format json` + `bob exec -- env -i /nix/store/<out>/bin/<prog>` in the
   fixture with the same bob home, as correction 85 (RCC005a). SPEC §02.6: --print-paths host
   paths `<home>/nix/store/…` run only through bob run/bob exec; executed directly the loader
   looks in the host's /nix/store: `api: error while loading shared libraries: libfoo.so`
   (TestRGO001a/linux-arm64 since I-B27). The other platform keeps qemu-user -L <home>. Result:
   TestRGO001aBuildTestCrossAndReproducible PASS (both platforms).
92. APPLIED 2026-10-24 I-B45 (A384). tests/eco/golang/resolve_test.go TestRGO002bBuildListEqualsGoCommand:
   "bob.lock has no build list for workspace module" fires only when bob.lock has no entry for
   the module; an empty build list is valid (the fixture's example.com/lib "has no third-party
   requirements" per its bob.go; pkg/golang/gen/lock.go writes `{"go":"1.26.0","modules":[]}`;
   `go list -m all` prints only the main module there). Result: the lib check passes; fixture-go
   still fails on the cgo package (FEAT/RULE row in I-B45.md).
93. APPLIED 2026-10-24 I-B45 (A385). tests/eco/golang/toolchain_test.go TestRGO004aToolchainPerModule:
   (a) with lib/bob.go's Go lowered to 1.25 the test also edits lib/go.mod `go 1.26.0` -> `go 1.25.0`
   (same cause as 89: BOB-E-1011 `~ go 1.25.0 → 1.26.0 (set Go in lib/bob.go)` before any build);
   (b) a compile job is identified by `-p <pkg>` or `importPath <pkg>`: main packages are compiled
   with `-p main` (as the go command does; measured: the job of example.com/api/cmd/api has
   `"flags":["-p","main","-lang=go1.26"]` and `"importPath":"example.com/api/cmd/api"`), so
   "no compile job found for example.com/api/cmd/api" was a matching error (stamp only matched
   through its go-test job). Toolchain/-lang/std assertions unchanged. Result: PASS (162 s).
94. APPLIED 2026-10-24 I-B45 (A386). tests/errors runner (catalogue.go, run.go) and fault 71:
   (a) inject kind `env` (HARNESS §5.1) is implemented: fault.json `inject.env` (K=V list) is added
   to bob's environment in live runs (a note when the list is empty); fault 71 gets
   `"env": ["BOB_TEST_CLONE_ERRNO=EPERM"]` (the bobtest hook of R-NIX-022, as tests/rootless uses it).
   Before, the runner ignored the kind and the build simply succeeded (exit 0).
   (b) SPEC writes some codes as a `✗ <message>` line without an `error[CODE]:` head: 7005
   (R-NIX-022 block, which tests/rootless checks line by line), 2013/2014 (§03.4.4), 2015
   (R-OUT-007). For these the runner counts the code when a default-output line starts with
   `✗ ` (+ for 7005 the exact head) and the --json stream of the same run has an error event
   with that code (`markLines`, `jsonReports`); every other code still needs `error[CODE]`.
   (c) expect.json 71 messages: the R-NIX-022 layout `\n  cause: ` and
   `\n  fix (run once as an administrator):\n    <exact EPERM/container fix of the SPEC table>\n`
   instead of `fix (run once as an administrator): ` (a trailing space SPEC's layout never prints);
   fault.json 71 hint false (the R-NIX-022 block has the fix line in place of a hint line).
   Result: TestRNIX022UserNamespacesBlockedFault PASS; tests/errors TestCatalogue,
   TestRunnerClassifies, TestCheckJSON, TestMissingLine PASS.
95. APPLIED 2026-10-24 I-B45 (A387). tests/errors/run.go live mode: one warm-up run of the fault's
   command (crash-checked) before the three measured runs (default, --json, -v), so they start
   from the same store. Before, the -v run (third) showed `cached` for packages the first run
   printed as `built`, and "-v output lacks the default line \"built   //native:native\"" failed every
   live fault with a successful sibling package (seen on 52, 53, 54). Checks unchanged.
96. APPLIED 2026-10-24 I-B45 (A388). tests/faults/54-prune-needed-file/patch.diff authored (none
   existed; I-B34 table): npm fixture native/bob.go gets an AppImage of //native:native with
   `Prune: {"/nix/store/*-gcc-*-lib/lib/libstdc++.so*"}` and the `//bob:fault 54-prune-needed-file`
   marker before `appimage.NewBundle` (the position bob reports). An OCI image was tried first: its
   default platforms include x86_64, which an npm native addon cannot be cross-built for
   (BOB-E-1010), so the AppImage (host platform) is used. Measured output: `✗ //native:native-appimage:
   prune pattern "…libstdc++.so*" removes libstdc++.so.6, which libmerve.so.1.2.2 needs`, defined-at
   with column, hint, exit 1. Result: TestRNIX017BuilderExitCodesInRealRuns/54 PASS (with 94b, 95).
Not applied: fault 74 capture (needs an owner-approved golden; candidate in
out/fault-candidates/74-unknown-nix-message, see I-B45.md); faults 52/53 (fixture design, I-B45.md).
97. APPLIED 2026-10-24 N2 (A451). tests/docs/docs_test.go TestSnippetsTypeCheck: a page whose
   project has bob.go files but no `file=go.mod` block is type-checked with the module path bob
   gives a new repository (helper newRepoModule: an import "<module>/<dir>" of another bob.go
   directory, else any path) instead of failing "no file=go.mod block with a module line". The
   test encoded the documented workaround of the first-run blocker "a new repository needs a
   hand-written go.mod" (W2 status page; SPEC R-FW-002 "the bob-generated root go.mod",
   §03.2.5, R-FW-022, §02 open item 11: bob writes the root go.mod). N2 fixed the blocker
   (pkg/engine/eval: no root go.mod → evaluation with the inferred module, an overlay go.mod for
   go build -modfile) and removed the hand-written go.mod blocks from the quickstarts and
   guides 03/06. A page that has a go.mod block is checked as before (a block without a module
   line still fails). Result: tests/docs PASS.
98. APPLIED 2026-10-24 Q1 (A480), owner approved all, chat 2026-10-24 (D71/D72, ADR 0020).
   tests/fixtures/repos/* migrated to the typed surface and per-package modules:
   `Nixpkgs: bob.NixOS2605` in every root; nixpkgs handles (`nixpkgs.Openssl.Lib(p, …)`,
   `nixpkgs.Jq.Tool(p)`, `nixpkgs.Bash/Coreutils/Zlib…`); `Hdrs`/`Headers: bob.IncludeDir("include")`;
   Go declarations moved into the Go package's own bob.go without directory strings
   (acme/cmd/api, polyglot/cmd/api (new), bigclosure/cmd/big (new), go/api/cmd/api (new),
   go/api/internal/ffi (new), go/tools/cmd/stamp (new), codegen/svc/cmd/{server,client} (new));
   module roots use `Tests(golang.TestOpts{Recursive: true})`; main-package binaries reach
   deploy/ through `bob.Slot` (acme.API, polyglot.API, bigclosure.Big) so every label and release
   name stays; cpp-lib's binary moved to cpp-lib/tool/bob.go (label //tool:ringtool, was
   //:ringtool); npm native: `npm.AllowScripts(sqlite, esbuild)` with handles; pipelines
   `Releases: []bob.Pattern{"//deploy:api"}` / `{"//deploy:polyglot"}`. acme LSP script: steps
   `complete libs/foo/bob.go "nixpkgs.Op"` and `hover libs/foo/bob.go "nixpkgs.Openssl"`.
   Result: TestRFW070FixtureLocality PASS; fixtures evaluate again once Q2/Q3 land (red until then).
99. APPLIED 2026-10-24 Q1 (A481), owner approved all, chat 2026-10-24. Test repositories and
   embedded bob.go sources migrated the same way: tests/core/testdata/repos (svc: cmd/sleeper/bob.go
   + slot `svc.Sleeper`, `postgres.Options{Server: nixpkgs.Postgresql17}`; typecheck/linkable without
   the dir string; nixtool keeps its intentional type error as `nixpkgs.Openssl.Tool(p)`),
   tests/cli/testdata/repos (pipe/app/bob.go, exits/tools/<prog>/bob.go), tests/devenv/testdata/repos,
   and the bob.go strings in tests/eco/{golang,rust,npm,cc,ocaml}, tests/core/steps_test.go,
   tests/nix/{closure,layer,namespace}_test.go, tests/rootless, tests/outputs/overlays_test.go
   (`nixpkgs.Curl.Exe(p, nixpkgs.MainProgram("bin/curl"))`), tests/server/srvtest,
   tests/fixtures/evil. tests/eco/rust uses `nixpkgs.Rust1_97PackagesStableRustc` (include.json
   entry, ADR 0020 §2); tests/eco/npm/scripts_test.go expects the handle wording of 2013;
   tests/nix/layer_test.go TestRNIX005 writes `Nixpkgs: bob.NixOS2605, NixpkgsRev: "<rev>"`.
100. APPLIED 2026-10-24 Q1 (A482), owner approved all, chat 2026-10-24. Fault patches refreshed
   for the migrated fixtures (context lines only, same faults): 54, 60, 61, 62, 63, 64, 65, 66
   (`acme.Module(p).Tests(golang.TestOpts{…})`, `bob.IncludeDir`, `nixpkgs.Openssl.Lib`,
   `npm.AllowScripts(sqlite, esbuild)`). All 14 patch faults checked with `git apply --check`
   against a private copy of the fixture.
101. APPLIED 2026-10-24 Q1 (A483), owner approved all, chat 2026-10-24. New faults 82–92
   (fault.json, patch.diff or env hook, expect.json rows; expect.json now sorted by key):
   82 source outside package (1028), 83 root lists subpackage sources (1028), 84 two cc units in
   one package (1029), 85 `-I` of another package (1020), 86 string nixpkgs attribute (1020 +
   typed hint), 87 misspelt handle (1020 + did-you-mean), 88 nixext import (1024 R7), 89 assignment
   to a framework value (1024 R8), 90 handle missing at the pin (1001 `pin`, hook
   `BOB_TEST_NIXPKGS_MISSING`), 91 public headers outside the package (1028), 92 unbound slot (1020).
   tests/faults/README.md range updated. Red until Q2/Q3 implement the checks.
102. APPLIED 2026-10-24 Q1 (A484), owner approved all, chat 2026-10-24. Code table (SPEC §02.3):
   1001 variant `pin`, 1024 variant `R8`, new 1028 (`outside`, `subpackage`) and 1029 (1027 stays
   reserved for the I-B32 proposal); 2013/2014 hints name handles instead of strings.
   pkg/bob/diag/codes.go (data only), codes.lock (5 lines appended), tests/goldens/diag/codes.golden
   regenerated (`go test ./pkg/bob/diag -update`), website 05-cli/02-errors.md regenerated
   (`BOB_DOCS_UPDATE=1`). Diffs in out/golden-candidates/q1/.
103. APPLIED 2026-10-24 Q1 (A485), owner approved all, chat 2026-10-24. New guards:
   tests/core/typesafe_test.go (TestRFW072TypedSurface with the protected allowlist and violation
   list, TestRNIX031GeneratedSurface, TestRFW070FixtureLocality, TestBoblintCatchesViolations,
   TestRFW070GlobStopsAtSubpackage, TestRFW071Slots), tests/harness/boblint (shared go/ast
   linter), tests/docs/locality_test.go (TestDocsPackageLocality, waits for the surface),
   tests/core/bobgo_test.go (R-FW-002 cases r7nixext and r8, LSP nixpkgs completion/hover
   checks), test repositories define/slot, define/slot2, rules/r7nixext, rules/r8.
104. APPLIED 2026-10-24 Q1 (A486), owner approved all, chat 2026-10-24. Budgets B-098…B-103 for
   the generated surface (SPEC Appendix B, status proposal, no benchmark yet);
   tests/budgets/budgets.json regenerated by tools/internal/budgets (`go test -update`), whose
   row count check and file note moved from 97 to 103.
105. APPLIED 2026-10-24 Q1 (A487), owner approved all, chat 2026-10-24. tests/nix/embedded_test.go
   TestRNIX025SystemNixCoexistence builds `//tool:ringtool` (cpp-lib's binary is its own package
   since correction 98; it was `//:ringtool`). Nothing else changes.
106. APPLIED 2026-10-24 Q1 (A488), owner approved all, chat 2026-10-24. tests/core/api_test.go
   TestRFW010NoLabelStringLiterals skips the elements of `[]bob.Pattern` literals: pipeline label
   patterns are the CLI form SPEC R-FW-010 (amended, ADR 0020) allows, and pipelines now name
   releases by pattern (`Releases: []bob.Pattern{"//deploy:api"}`). This also resolves the
   pre-existing failure on nondet's `Targets: []bob.Pattern{"//:nonce"}`. Every other label
   literal still fails.

107. APPLIED 2026-10-24 Q2 (A507), owner approved ADR 0020 test changes, chat 2026-10-24.
   tests/budgets/budgets.json regenerated (`go test ./tools/internal/budgets -update`): rows
   B-098…B-103 get their benchmark command `go run ./tools/budgetbench nixpkgs` (new group,
   tools/budgetbench/nixpkgs.go). Budgets, limits and statistics unchanged; measured values in
   plan/progress/Q2.md.
108. APPLIED 2026-10-24 Q2 (TEST PROBLEM 1), owner approved ADR 0020 test changes, chat
   2026-10-24. tests/faults/83-source-in-subpackage/patch.diff: the injected binary is named
   "central" instead of "all". "all" is reserved for the pattern //dir:all (R-FW-015), so the
   fault produced `package name "all" is reserved` (BOB-E-1020) instead of the region error it
   is about. Same line, same marker, same expectation (BOB-E-1028 `"app/*.c" is inside app/`).
109. APPLIED 2026-10-24 Q2 (TEST PROBLEM 2), owner approved ADR 0020 test changes, chat
   2026-10-24. tests/errors/run.go markLines gains "BOB-E-1020": the human output writes every
   BOB-E-1020 as a `✗ <message>` line without an `error[BOB-E-1020]:` head
   (pkg/engine/translate/render.go definitionLine, case 1020, as SPEC writes definition errors,
   R-FW-043 (c), R-FW-011, R-FW-015), so the runner counted 0 for faults 85, 86, 87 and 92 although
   bob reported the code (the --json stream carries it). Same mechanism as 2013/2014/2015/7005:
   counted once when a `✗ ` line is printed and the JSON stream reports the code.

## Q3 (2026-10-24, evidence-based test and fixture corrections; numbers 130+, 107–129 left to Q2;
## evidence plan/progress/Q3.md TEST PROBLEM)
130. APPLIED 2026-10-24 Q3 (TP1), owner approved all ADR 0020 test changes, chat 2026-10-24.
   tests/eco/rust/resolve_test.go TestRRS002aCfgTablesEqualRustc imports `bobrust` from
   github.com/megakuul/bob/pkg/rust/resolve instead of pkg/rust: TestRFW072TypedSurface (protected
   violation V20) requires `rust.CfgTable` to leave the build-definition API, and
   `resolve.CfgTable` has the same signature and reads the same cfgtab data. The comparison with
   `rustc --print cfg` is unchanged.
131. APPLIED 2026-10-24 Q3 (TP2, A521), owner approved all, chat 2026-10-24.
   tests/fixtures/repos/polyglot/cmd/api/bob.go: `Tests(golang.TestOpts{})` instead of
   `TestOpts{Name: "api_tests"}`, so the test unit is //cmd/api:go_test as the fixture's own
   comment says and tests/core/platform_test.go TestRFW039 runs it. A one-package test set is
   one unit labelled by Name (tests/core/testdata/repos/svc/it needs //it:it, //it:it2 and
   //it:never as three units in one directory, tests/core/steps_test.go:368, :398).
132. APPLIED 2026-10-24 Q3 (TP4), owner approved all, chat 2026-10-24.
   tests/outputs/overlays_test.go: the apptest overlay bob.go calls `polyglot.API.Get(p)` (9
   places) instead of `polyglot.API(p)`: polyglot.API is the typed slot of correction 98, so the
   overlay did not compile (`cannot call polyglot.API (variable of type *bob.Slot[*golang.Binary])`,
   tests/outputs TestROUT007/015/016/019 exit 3). Same programs, same assertions.
133. APPLIED 2026-10-24 Q3 (A523), owner approved all golden changes, chat 2026-10-24.
   New goldens tests/goldens/ir/polyglot.json and acme.json (TestRFW047GoldenIR; never committed
   before, Q1 README): written as candidates to out/golden-candidates/q3/ and copied into place.
134. APPLIED 2026-10-24 Q3 (A523), owner approved all golden changes, chat 2026-10-24.
   New goldens tests/goldens/eco/cc/jobs/{app,pack,mathx,mathx_test}.json (TestRCC002a) and
   tests/goldens/eco/rust/jobs/rcore-alt-toolchain.json (TestRRS004a), candidates in
   out/golden-candidates/q3/ (README there), copied into place.

## Q5 (2026-10-24, evidence-based test corrections; evidence plan/progress/Q5.md)
135. APPLIED 2026-10-24 Q5 (task 5), owner approved ADR 0020 test changes, chat 2026-10-24.
   pkg/engine/translate/npm_test.go TestProgramNpmFailures expects the runtime lines of SPEC
   §03.4.4 as amended by ADR 0020 (options take handles): `Add npm.AllowScripts(<handle of
   better-sqlite3>) to //native:native`, `Add npm.Extend(<handle of P>, npm.Peer(<handle of X>))`,
   and the BOB-E-2013 hint of the code table (`declare better-sqlite3 (npm.Dep or
   npm.Indirect("better-sqlite3")) and add npm.AllowScripts(<its handle>) to //native:native`).
   It still asserted the string forms (`npm.AllowScripts("better-sqlite3")`), which the API no
   longer accepts; tests/eco/npm/scripts_test.go denyWording (correction 99) already expects the
   handle wording. Same cases, same assertions otherwise.
136. APPLIED 2026-10-24 Q5 (task 6), owner approved ADR 0020 test changes, chat 2026-10-24.
   tests/core/api_test.go TestRFW010TypedHandles: `bob gen` and `bob env` run while
   lnk/bob.go is set aside, the ill-typed file comes back before `bob exec -- go vet -tags=bob
   ./...`. Evidence: every evaluating command reports a bob.go that does not compile as a
   definition error with exit 3 (SPEC R-FW-043 (c), §02 exit classes; measured: `bob gen` →
   `✗ lnk/bob.go:16:23: cannot use mod(p).Binary(…) … (missing method LinkInfo)`, exit 3), so the
   test never reached its go vet step (known since I-B27, A279). Measured after the change:
   go vet exits 1 with `lnk/bob.go:16:23: … *golang.Binary does not implement bob.Linkable
   (missing method LinkInfo)`. The R-FW-010 acceptance (go vet fails with that text) is unchanged.
137. APPLIED 2026-10-24 Q5 (task 7), owner instruction (Q5 brief): SPEC.md Appendix B rows
   B-098…B-103 source cells filled from Q2's measurements (plan/progress/Q2.md); tests/budgets/
   budgets.json regenerated by `go test ./tools/internal/budgets -update`: only the six rows'
   `source` text and `busy` flag change; budgets, limits, statistics and benches unchanged.
138. APPLIED 2026-10-24 Q5 (ROUT004), owner approved ADR 0020 test changes, chat 2026-10-24.
   tests/fixtures/repos/go: tools/bob.go declares the slot `var Stamp = bob.NewSlot[*golang.Binary]()`,
   tools/cmd/stamp/bob.go binds it (`var _ = tools.Stamp.Bind(Stamp)`); tests/outputs/overlays_test.go
   stamptarGo uses `tools.Stamp.Get(p)` instead of `tools.Stamp(p)`. Correction 98 moved the stamp
   binary into its main package's bob.go without the slot it gave acme/polyglot/bigclosure, so the
   overlay did not compile (`tools/tar/bob.go:12:68: undefined: tools.Stamp`, TestROUT004 exit 3).
   Same program, label //tools/cmd/stamp:stamp unchanged, same assertions.
139. APPLIED 2026-10-25 Q5, owner approved ADR 0020 test changes, chat 2026-10-24.
   tests/core/testdata/repos/nondet/nondet/bob.go: the map of attribute names becomes a map of
   typed handles (`map[string]nixpkgs.Attr{"zlib": nixpkgs.Zlib, …}`, `attr.Lib(p)`); correction 99
   missed this repository, so it failed to compile (`nondet/bob.go:22:31: invalid nixpkgs package:
   undefined: nixpkgs.Lib`, BOB-E-1020) instead of producing BOB-E-1018. Same 8 libraries, same
   randomised map iteration, same assertion. TestRFW008NondeterministicBobGo PASS.
140. APPLIED 2026-10-25 Q5, owner approved ADR 0020 test changes, chat 2026-10-24.
   tests/core/platform_test.go TestRFW032ToolIsNotALinkable: the regexp expects the typed call
   `nixpkgs\.\w+\.Tool` (the repository says `nixpkgs.Openssl.Tool(p)` since correction 99) instead of
   the removed `nixpkgs.Tool`. bob's output was already right: `✗ nt/bob.go:15:23: cannot use
   nixpkgs.Openssl.Tool(p) (value of type *nixext.ToolPackage) as cc.Linkable value …`. PASS.

141. APPLIED 2026-10-27 E1, owner request (chat 2026-10-27: bob init's go.mod pointed at the non-existent
   v0.0.0-dev, so gopls without bob's shim resolved nothing; "this is a clear issue of the init command"),
   covered by the owner's blanket approval of ADR 0020-era test/fixture/golden rewrites ("by force I approve
   everything"). SPEC §01 R-FW-043.4 and §02.7.6 amended (A550). Test changes:
   pkg/golang/gen/root_test.go TestRootGoMod: the root go.mod now ends with
   `replace github.com/megakuul/bob => ./.bob/framework`; new TestFrameworkTree.
   pkg/engine/build native_root_test.go TestRootGoSumHasFramework: the root files include the framework
   tree below .bob/framework/ (ignored when counting go.mod/go.sum). No golden changed.

142. APPLIED 2026-10-27 E2, owner bug report (chat 2026-10-27: `bob dev` on NixOS, "/run/current-system/sw/bin/fish is not runnable"),
   covered by the owner's blanket approval of golden rewrites. New diagnostic BOB-E-7011 ("{program} cannot be started in bob's
   dev environment: {reason}", hint "{hint}", class store, exit 7): tests/goldens/diag/codes.golden gains the entry
   (go test ./pkg/bob/diag -update; pkg/bob/diag/codes.lock +1 line), the generated error table of
   website/content/docs/05-cli/02-errors.md gains the row (BOB_DOCS_UPDATE=1). BOB-E-0005 keeps its meaning (bob run of a package
   that is not a binary); the old misleading use for dev start failures is gone. No existing golden line changed.


142. APPLIED 2026-10-27 E1, owner additions (chat 2026-10-27): (a) bob init writes a .gitignore block of the
   not-committed generated files (kept current by bob lock/gen/check --fix), same list as .git/info/exclude;
   (b) BOB-E-1011 hint for edits bob.go cannot express, and bob check --fix prints what it restored.
   SPEC §03 "Not-committed files …", O-03-1, §02.3 rows 1011 and 1013 amended (A550). Changes:
   pkg/bob/diag/codes.go 1011 gains the hint variant "{hint}" (before the default); codes.lock line
   `1011 … hint=hint`; tests/goldens/diag/codes.golden regenerated with go test -update (it also
   carries another unit's 7011). New: pkg/cli/cli_test.go TestGitignoreBlock,
   pkg/golang/gen/gomod_explain_test.go TestExplainFrameworkEdit, tests/cli/init_gitignore_test.go with
   tests/goldens/cli/init-gitignore/{go,cc,rust,npm,ocaml}.txt.
143. APPLIED 2026-10-27 E3, owner bug report (chat 2026-10-27: `bob dev` shell is root): SPEC §02.7.7 step 6 gains a
    paragraph (A561): programs of the user's session run as the user, not as the namespace's root. No test, golden
    or protected file changed; new tests pkg/engine/store/hostview_test.go TestHostStoreView (uid/gid/file owner in the
    session) and TestExecRunsAsUser.
144. APPLIED 2026-10-27 CI1, owner request (chat 2026-10-27: pipelines fail): BuilderVersion 18 -> 19 (A562): the four
    tests/goldens/eco/cc/jobs/*.json hold the builder store path (aarch64 5lpg8a0v... -> 9mzv0wvh...); no test logic changed.
    New tests: cmd/bob-builder/genbuilders/scrub_test.go, tools/ci/progress_test.go.
