# Harness self-validation (AGENTS.md §6.10, HARNESS.md §10), M1.10

Date 2026-10-22. Machine: aarch64 Linux, 8 cores. Branch bob-v1, uncommitted working tree.
Method: `cp -a /root/bob /root/scratch-m110`, one deliberate fault at a time, run the CI stage
named below, restore with `git checkout` (the repository itself was never touched).
Env: `CGO_ENABLED=0 GOCACHE=/root/.cache/go-build-m110 XDG_CACHE_HOME=/root/.cache/bobxdg-m110`.
All commands run in the scratch copy. "CI" below is `go run ./tools/ci -only <stage>`; it exits 1
when the stage fails.

`go run ./tools/ci selfcheck` (HARNESS.md §10) is **not built**. These runs are manual. No helper
was added. Every fault was planted by hand; the patches are described, not stored as files.

States: **caught** = planted fault made the named stage fail, demonstrated now.
**caught (partly)** = see note. **deferred** = needs product code that does not exist yet; the
procedure is written, nothing is claimed. **gap** = the harness does not catch it.

## 0. Baseline facts that qualify every result

1. Stages `errors layering licence protected lint parity golden budgets fmt` are green on the
   unmodified tree (9 stages, 0 failed).
2. **Stage `acceptance` is red on the unmodified tree for non-red reasons.**
   `go run ./tools/ci -only acceptance` prints `acceptance: 40 packages, 290 tests pass, 861 red
   (not implemented), 8 fail` and exits 1. The 8 failures carry no red marker on every failure
   line: cli `TestRCLI008ServerLog`, core `TestAPISurface` (8 missing Example tests), devenv
   `TestRDEV005RemoteBuildsInsteadOfPushes`, nix `TestRNIX021NamespaceProbes`, policy
   `TestRSEC102PositivePathAndRefusal`, rbac `TestRAUTH006EveryRPCHasRule/proto` (2 proto
   problems), ui `TestFirstLoadBudget`, ui `TestServedHeaders`. Consequence: a fault found only
   by an acceptance suite cannot be shown to flip CI from green to red today; it can only be
   shown to add failures (baseline 8). Rows below say which stage and which counts. The 8 need
   an owner or coordinator decision (fix the tests' markers/stubs, or accept). Not touched here.
3. Stage `protected` never fails on changes (owner decision D68: "reported, not blocked").
4. Logs: `/root/.cache/m110-*.txt`.

## 1. Result table

| # | Fault | How planted (scratch copy) | Caught by (stage / test) | State | Evidence |
|---|---|---|---|---|---|
| A | Raw Nix error leaked (HARNESS #2) | `pkg/bob/diag/diag.go`: `NotImplemented` appends `"\nerror: builder for '/nix/store/<32 chars>-x.drv' failed"` to the message, so bob prints raw Nix text for every catalogue entry (replay mode, `BOB_TEST_NIX_REPLAY`, runs inside `tests/errors`) | stage `errors`, `TestFaults/<every entry>`, leakcheck | caught | `go run ./tools/ci -only errors` exit 1, `2 stages, 1 failed: errors`; `errors_test.go:104: replay 2.34.9: raw Nix text in 'bob build //libfoo:foo': store path: "error: builder for '/nix/store/0123456789abcdfghijklmnpqrsvwxyz-x.drv' failed is not implemented yet"` (also patterns derivation file, builder for, raw error line). Unmodified: `89 fault entries: 0 pass, 89 red, 0 fail`, ok. Note: this plants the leak at the output edge. It proves leakcheck, not the translator (M4.2). |
| B | Worker can read control bucket (HARNESS #6), live | `deploy/policies/worker.json`: delete `DenyControlAlways`, add Allow `s3:GetObject,s3:ListBucket` on `bob-control`; **and** `control-bucket.json`: add `worker-bob` to the `aws:username` exception. RustFS applies both (`tests/serverenv/stack/policies.go`) | stage `acceptance` (tests/policy): `TestRSEC002PolicyMatrix`, `TestRSEC002PrefixTable`, `TestRSEC002ShippedPolicyFiles` | caught | `go test -count=1 ./tests/policy/...`: `matrix_test.go:141: 8 of 324 cells deviate from the expected table`; `matrix_test.go:182: worker GET on bob-control/fleet-cfg = 404, want 403`; `sec_test.go:99: worker.json: GetObject on bob-control is true, the SPEC table says false`. `-only acceptance`: `acceptance: ... 15 fail` (baseline 8), the three policy tests above newly listed, exit 1. I did not diff the other 4 additional entries; they were not examined (likely flaky or shared-stack effects, unverified). |
| B2 | Same, one layer only | Only `worker.json` widened as in B, bucket policy untouched | `TestRSEC002ShippedPolicyFiles` only (static model of the file) | caught (partly) | Live `TestRSEC002PolicyMatrix` stayed **PASS**: the bucket policy still denies the worker, so RustFS enforces the deny. This is defence in depth, not a harness hole, but the live matrix alone does not detect a widened identity policy. |
| C | Budget regression (HARNESS #26 analogue) | `tools/internal/budgets/benches.go`: `Benches = {"B-009": "echo BUDGET B-009 20"}` then `go run ./tools/ci -update -only gen` to regenerate `tests/budgets/budgets.json` (limit 10 ms, +10 %) | stage `budgets` | caught | `B-009 namespace entry: median 20 ms > limit 10 ms (+10% tolerance)`, `FAIL budgets`, exit 1. Control with `echo BUDGET B-009 5`: `budget B-009 fast ok`, exit 0. Also: changing Benches without regenerating makes stage `gen` fail (`budgets.json was stale and has been regenerated`) and `budgets` fail (`stale against SPEC.md Appendix B`). |
| D | Import `internal/` from `pkg/` (#12) | new `pkg/bob/zzplant/p.go` with `import _ ".../internal/store"` | stage `layering`, `TestLayering` | caught | `layering_test.go:181: ... (L0 core API) imports server code github.com/megakuul/bob/internal/store (R-ARCH-002)`; `FAIL layering`, exit 1 |
| E | `panic` / `os.Exit` in `internal/` (#10) | new `internal/store/zzplant/p.go` with `panic("boom")`; second run with `os.Exit(1)` | stage `lint` (boblint nopanic, noexit) | caught | `p.go:3:12: panic outside package main: return an error instead (AGENTS.md §5.3)`; `p.go:5:28: os.Exit outside func main of cmd/*/main.go`; exit 1 |
| F | LIST call in `internal/store` outside `sweep` (#8) | `c.ListObjectsV2(ctx, &s3.ListObjectsV2Input{})` in `internal/store/zzplant` | stage `lint` (nolist) | caught | `ListObjectsV2 outside github.com/megakuul/bob/internal/store/sweep: control and fleet state is GET-only (D60, R-STORE-100)`; exit 1 |
| G | Bare `go` statement without recover (#11) | `func F() { go func() {}() }` in `internal/store/zzplant` | would be stage `lint` (`safego`) | **gap** | `go run ./tools/ci -only lint` exit **0**. boblint runs only `-nopanic -noexit -nolist` (`tools/ci/stage_lint.go`); no `safego` analyzer exists in `tools/lint`. Needs an analyzer; owner of tools/lint (M0.2 follow-up). Not claimed. |
| H | Golden mismatch, harness goldens (#22 partly) | append a line to `tests/goldens/harness/normalise.txt` | stage `golden`, `TestApprovedGolden` | caught | `golden_test.go:57: golden: tests/goldens/harness/normalise.txt differs from the output (70 bytes vs 78 golden bytes)`; `FAIL golden`, exit 1 |
| I | Error template/code table changed without golden update (#21/#22) | append a line to `tests/goldens/diag/codes.golden` (equivalent: reword a template) | stage `unit`, `pkg/bob/diag` `TestCodeTableGolden` | caught | `go test ./pkg/bob/diag/`: `diag_test.go:143: code table differs from .../codes.golden (rewording a template needs a golden update)`; FAIL. Run directly, not through the full `unit` stage. |
| J | Parity floor lowered coverage (#32 analogue: a covering test disappears) | `echo 1 > tests/parity/floor` (count is 0, so any covered requirement lost would show the same) | stage `parity` | caught (partly) | `parity: 0 requirements pass, below the floor of 1`; `FAIL parity`, exit 1. The floor is 0 today, so deleting a test cannot lower anything yet; this proves the comparison, not the deletion path. Floor starts to mean something at the first green feature. |
| K | GPL dependency (#30) | `web/package-lock.json`: add package `node_modules/zz-gpl-planted` with `"license": "GPL-3.0-only"` | stage `licence` | caught | `npm:zz-gpl-planted@1.0.0: not allowed: GPL-3.0-only`; `FAIL licence`, exit 1. Go-module path not exercised (no GPL module available offline). |
| L | Golden edited without manifest update (#27) | edit `tests/goldens/diag/codes.golden` | stage `protected` | **not blocking by design** | `protected: changed tests/goldens/diag/codes.golden (vs manifest)`, `657 changes to protected paths (reported, not blocked: D68)`, exit **0**. The fault is reported, not failed. Gate H row 27 as written in HARNESS §10 ("must be caught by `protected`") does not hold under D68; HARNESS.md needs the sentence changed, or the owner reverses D68. |
| M | Red tolerance must not hide a non-red failure | new `tests/errors/zz_m110_plant_test.go` with four tests: plain `t.Fatalf("expected 3, got 4")`; one red line plus one real line (`t.Errorf` x2); red only; red log then `panic("boom")` | stage `acceptance` | caught | `go run ./tools/ci -only acceptance`: `861 red, 8 fail` became `862 red, 11 fail`. The three non-red plants are listed under `--- FAIL ...TestM110PlantPlainFailure`, `...MixedRedAndReal`, `...Panic` (`panic: boom`); the red-only plant is counted as red (+1). Exit 1. Baseline already fails (see §0.2), so the proof is the count and the listing, not the exit code. Unit tests in `tools/ci/stage_acceptance_test.go` exist and were not re-run here. |

## 2. Deferred (needs product code), never claimed as passed

Each procedure: plant in the scratch copy, run the named command, expect the named tests red
for the stated reason (not with the BOB-E-9008 marker). Until the milestone lands, these tests
are red for the stub reason, so they prove nothing.

| # | Fault | Planted-fault procedure | Expected failing stage / test | State | First milestone |
|---|---|---|---|---|---|
| 1 | Drop a dependency edge (HARNESS #1) | in the IR to drv generator (`pkg/engine/drv`), skip the edge for one input (e.g. `cc.Lib` header dependency of the polyglot fixture) | `golden` (drv goldens), fixture build fails with missing header (`tests/fixtures`, `tests/eco/cc`) | deferred | M2.1 (`engine/drv` goldens); end-to-end part M7.1 |
| 2b | Raw Nix leak through the real translator (HARNESS #2 proper) | in `engine/translate`, pass one Nix message through unchanged for fault 01 | `errors` `TestFaults/01-unknown-attr`: leakcheck plus code and position | deferred (A proves leakcheck only) | M4.2 |
| 3 | Break drift detection (HARNESS #3) | ignore a hand-edited `go.mod` in the generated-file check (drift code BOB-E-1011) | `errors` `TestFaults/49-gomod-drift` (and `75-drift-all-unknown`) | deferred | M5.1 |
| 4 | Skip a permission check in one RPC (HARNESS #4) | in the RBAC interceptor, return early for one method | `tests/rbac` `TestRBACMatrix` (generated matrix), `TestRAUTH006EveryRPCHasRule` | deferred | M15.4 |
| 5 | Remove the rule option from one RPC (#5) | delete the `(bob.rbac...)` option on one RPC in `proto/` | `TestRAUTH006EveryRPCHasRule`, stage `proto` | deferred (baseline already reports 2 proto problems for Update{Project,Release}) | M15.1 / M15.4 |
| 6b | Worker reads control bucket, product side (R-SEC-102 self-check) | in the orchestrator start-up self-check, skip the worker-credential probe | `tests/store` `TestRSEC102OrchestratorRefusesReadableControlBucket`, `tests/policy` `TestRSEC102PositivePathAndRefusal` (expect exit 78 and "control bucket is readable with the worker credential") | deferred (B covers the policy files, not the self-check) | M13.2 |
| 7 | Lose a log flush on shutdown (HARNESS #7) | in `internal/logstore`, drop the last batch when the writer stops | `tests/server` `TestRLOG001SizeTriggeredCommit`, `TestRLOG001KillLosesOnlyUncommitted`, `TestRLOG101MatrixLive/Process` | deferred | M14.1 (live part M14.2) |
| 9, 13-19, 23-25, 28, 29, 31 | other HARNESS §10 rows (counting proxy, epoch fence, CAS, sandbox, determinism, ui theme, goleak, ...) | not attempted: each needs the named product component | see HARNESS.md §10 table | deferred | first milestone implementing the component; not scheduled by M1.10 |

## 3. Findings to resolve before Gate H

1. Acceptance is red at baseline for 8 non-red reasons (§0.2). CI cannot go green, and a new
   acceptance fault cannot flip it, until those are fixed or declared.
2. No `safego` analyzer (row G).
3. `protected` does not fail (row L, D68) against HARNESS §10 row 27.
4. A single widened identity policy is caught statically, not by the live RustFS matrix (B2).
5. `go run ./tools/ci selfcheck` does not exist, so none of this is repeatable by one command.

## 4. Documentation guardrail (stage `docs`, HARNESS.md §17), W2 2026-10-24

Run on a private copy (`/var/tmp/W2/repo`, deleted afterwards), one mutation at a time, each
reverted before the next. Command: `go test ./tests/docs -run <test>` for the content checks;
`go run ./tools/ci -only docs` in a git repository made of the copy for the coupling rule.

| # | Planted fault | Caught by | Result |
|---|---|---|---|
| D1 | remove the `--rerun` row from `05-cli/01-commands.md` | `TestCLIReference` | caught: `flag --rerun of bob test is not in the section's flags table` |
| D2 | add command `frobnicate` to the command table | `TestCLIReference` | caught: `command "frobnicate" (pkg/cli/cli.go) has no ### bob frobnicate section` |
| D3 | add flag `--deep` to `bob doctor` | `TestCLIReference` | caught |
| D4 | add global flag `--profile` | `TestCLIReference` | caught |
| D5 | add exported `golang.Replace` | `TestAPIReference` | caught twice: not mentioned in prose; API index out of date |
| D6 | add error code 0012 | `TestErrorTable` | caught: generated page out of date |
| D7 | server reads new `BOB_SERVER_PUBLIC_URL` | `TestEnvironmentVariables` | caught |
| D8 | CLI reads new `BOB_SERVER_URL` | `TestEnvironmentVariables` | caught |
| D9 | config page renames `BOB_WORKER_SLOTS` to `BOB_WORKER_THREADS` | `TestEnvironmentVariables` | caught twice (missing and stale) |
| D10 | link to a missing anchor | `TestLinks` | caught |
| D11 | link to a missing page | `TestLinks` | caught |
| D12 | unknown field in a quickstart `bob.go` | `TestSnippetsTypeCheck` | caught: `vet: ./bob.go:17:59: unknown field Nme` |
| D13 | type error in a `verify` block | `TestSnippetsTypeCheck` | caught |
| D14 | misspelled frontmatter key | `TestFrontmatter` | caught |
| D15 | hand edit of the generated API index | `TestAPIReference` | caught |
| D16 | new bob-server mode `replay` in the usage text | `TestServerModes` | caught |
| D17 | comment change in `pkg/golang/golang.go`, no docs | coupling rule | caught: `the change touches the public surface but not website/content/docs/` |
| D18 | D17 plus `docs-exempt: <reason>` in an untracked progress file | coupling rule | passes with the exemption printed |
| D19 | D17 committed with `docs-exempt:` in the message | coupling rule | passes with the exemption printed |
| D20 | new `"BOB_…` literal line in `internal/server/config.go`, no docs | coupling rule | caught: `internal/server/config.go (BOB_* variable)` |
| D21 | D20 plus a docs page change | coupling rule | passes |
| D22 | no `.git` | coupling rule | skipped with notice `not a git work tree` |

On the shared tree the guardrail found a real gap on its first run: `BOB_SERVER_OUTBOUND_ALLOW`,
added by another unit the same day, was undocumented (fixed in `06-server/03-configuration.md`).
Unit tests of the rule: `tools/ci/stage_docs_test.go`.

## 5. Typed surface and per-package modules (HARNESS.md §18, ADR 0020), Q1 2026-10-24

Mutations applied to a private copy of the tree (`/var/tmp/Q1/repo`, aarch64 host, busy;
`GOWORK=off CGO_ENABLED=0 go test ./tests/core -run <test>`), each reverted before the next.
Baseline: `TestRFW070FixtureLocality`, `TestBoblintCatchesViolations` pass;
`TestRFW072TypedSurface` and `TestRNIX031GeneratedSurface` are red (audited violations remain,
no generated surface yet).

| # | Mutation | Guard | Result |
|---|---|---|---|
| T1 | `func ByName(p *bob.P, attr string) *Package` added to `pkg/nixpkgs` | `TestRFW072TypedSurface` | caught (hard failure, not red): `pkg/nixpkgs.ByName(attr) takes a raw string; … classify the parameter in tests/core/typesafe_test.go (owner-protected, R-FW-072)` |
| T1b | `nixpkgs.Lib(p, "zlib")` in `cc/libs/pack/bob.go` | `TestRFW070FixtureLocality` | caught: `[R-NIX-031] nixpkgs.Lib names the attribute "zlib" as a string` |
| T2 | violation row moved into the allowlist | `protected` | by construction: `tests/core/typesafe_test.go` is a protected path (hash report); not run |
| T3 | `bob.Glob("*.c", "../libs/mathx/src/*.c")` in `cc/app/bob.go` | `TestRFW070FixtureLocality` | caught: `[R-FW-070] … leaves the package directory` |
| T4 | root `cc/bob.go` step over `libs/mathx/src/*.c` | `TestRFW070FixtureLocality` | caught: `[R-FW-070] … is inside libs/mathx/, which has its own bob.go` |
| T5 | second `cc.StaticLibrary` in `libs/mathx/bob.go` | `TestRFW070FixtureLocality` | caught: `[R-CC-006] 2 cc build units in one package directory` |
| T6 | relative `-I` accepted by `pkg/cc` | fault 85 | deferred: the fault runs live once Q3 implements R-CC-006 (today `-I../…` is accepted: audit C03, so fault 85 is red) |
| T7 | hand edit of a doc comment in `attrs_gen.go` | `TestRNIX031GeneratedSurface` (`nixpkgsgen -check`) | deferred to Q2 (no generated file yet) |
| T8 | `python3Packages` exclusion dropped | `TestRNIX031GeneratedSurface` | deferred to Q2 |
| T9 | `**` descends into subpackages | `TestRFW070GlobStopsAtSubpackage` | deferred to Q2/Q3 (the test is red until `bob.Run` sources and the new API evaluate) |
| T10 | string nixpkgs attribute in a documented `bob.go` | `TestDocsPackageLocality` | deferred: the test skips until `pkg/nixpkgs/attrs_gen.go` exists (docs migrate in the same change) |

The linter's own negative and positive cases are the unit test `TestBoblintCatchesViolations`
(8 violation classes reported, a valid repository not reported).
