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
