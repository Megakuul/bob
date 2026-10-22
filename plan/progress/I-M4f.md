# I-M4f progress (engine NEEDS of the CLI unit + CLI wiring)

Owner of: pkg/engine/build (not testrun), pkg/cli, cmd/bob; coordinated, additive edits in
pkg/engine/engine.go, pkg/engine/ir (Pos.Col + schema), pkg/bob (column.go, env.go bobSite,
eval.go fields, irout.go), tools/ci/stage_faults.go (protected, D68). Not touched: pkg/golang,
pkg/engine/eco (I-M5a).

## done
- build.Package.Drv (first configuration's derivation); CLI `.bob/last-build.json` records it,
  so `bob log` works for failed packages (D-M4e-5 resolved).
- build.CheckDeterminism(ctx, o): eval.CheckDeterminism with Evaluate's Go resolution.
- build.Check(ctx, o) ([]*diag.Error, error): 1012 (nixpkgs pin missing/stale, fetch entries
  missing/changed/unused, sections missing/unused, stale sections via engine.LockFreshness),
  1011 (generated files edited by hand), 1013 (.bob/ and not-committed generated files git does
  not ignore; build.IgnoreFindings, moved from pkg/cli). No writes; no downloads except the
  evaluator's Go toolchain (offlineFetcher for everything else).
- build.Gen(ctx, o, force) ([]string, error): writes engine.Ecosystem.Native files 0444 (temp +
  rename), .bob/state/native.json {"version":1,"files":{path:{sha256,owner}}}, backup of edited
  files to .bob/gen/backup/<20060102T150405Z>/ with force, orphans removed when unchanged;
  returns the not-committed paths.
- Run: drift check + (re)write of native files inside the index stage, whole-repo host runs
  only; Result.Generated. lockStage: stale ecosystem sections → 1012 before any download
  (unless --lock), missing sections locked on demand (offline: 1012).
- pkg/engine: GeneratedFile.Committed, GeneratedFile.Explain + DriftLine (SPEC §03.0.1),
  optional interface LockFreshness{Stale(ctx, LockRequest) (what, ir.Pos, error)}.
- ir.Pos.Col (`col`, omitempty; String() stays file:line for error goldens); pkg/bob records
  the called function per bob.go position and writes the column of that call into the IR
  (pkg/bob/column.go); framework.zip regenerated; pkg/bob/testdata/ir.golden.json updated with
  -update (package-local golden, not under tests/; only the new "col" fields changed).
- CLI: `bob check` = build.Check (+ --determinism via build.CheckDeterminism; --fix = bob lock +
  bob gen --force + exclude block); `bob gen [--force]` (result.data.excluded; --check →
  BOB-E-9008 not implemented); bob build/test add Result.Generated to the exclude block
  (add-only); `bob graph` prints file:line:col.
- tools/ci stage `errors`: `GOFLAGS=<existing> -tags=bobtest` in the go test environment.
- Stage events: Run already emits scan (eval.Result.Scan) and eval durations; the CLI writes
  them (verified on the nondet fixture: namespace, scan, eval(cached), ir, lock, index, …).
- Tests (new): pkg/engine/build/native_test.go (TestGenerateNativeFiles, TestLockFindings,
  TestNativePathOK, TestRunDrvCheckGen), pkg/bob/column_test.go. Green: pkg/engine/build,
  pkg/engine, pkg/engine/ir, pkg/engine/eval TestFrameworkZip, pkg/bob/..., pkg/cli.
- Smoke (nondet fixture, real bob binary): check → 1012 nixpkgs + 1013 .bob/; check --fix →
  no problems; graph shows bob.go:21:9 / 22:23 / 22:48; build --json stage events; bob log
  reads the .drv log.

## Coordinator items (17:15, from I-M5a NEEDS) — done
1. plan.go: PlanRequest.Root (o.Root) and PlanRequest.Store (planStore: HostPath, ImportSources
   = srcs.Import, Ensure = the run's fetcher); fields on planner, no package global.
2. pkg/cli/link.go blank-imports pkg/golang/gen.
3. eval nested go.mod (A103): patch reviewed and applied by hand (scan.go bobGoMod/bobMods,
   gen.go overlay hides them); SPEC R-FW-002 scan text amended identically in spec/01-core.md
   and SPEC.md; test pkg/engine/eval/scan_nested_test.go.
4. counts.built (run.finish) = bob derivations Nix built (helpers included); the human summary
   keeps counting packages (Report.BuiltPackages, json:"-") so the protected fault goldens hold.
5. realise.go: helper drvs attributed first to packages this run realises, deterministically
   (sorted drv paths); role "unpack" only for bob-builder unpack jobs, else "helper"; a
   --rerun unit drv replaces its planned drv in the translator context (no 9002).
6. Human error/warning blocks and the summary after them go to stderr (translate.HumanTo,
   out.diagnose, refusal, check, test); package/PASS lines and clean summaries stay on stdout;
   stdout+stderr reads as before. Overturns D-M4e-3; pkg/cli unit tests TestMainUsage /
   TestOutsideRepository now read stderr. Builder diagnostics' detail lines (a failed
   service's log tail) keep 20 lines (R-FW-025), the only exception to the 12-line excerpt.
7. builder test-run `copy` (TestRunArgs.Copy, before services; Cwd into the copy);
   BuilderVersion 4; builders.go regenerated; `tools/ci -only builder` ok; test
   pkg/builder/testrun_copy_test.go.
- NEEDS for I-M5a: set TestRunArgs.Copy {from: <package source path>, to: <build dir>/…} and
  Cwd for Go test units.
- Host: / is 100 % full (151G, other units' data); evaluator-based tests (pkg/engine/eval,
  evalbox, pkg/engine/build Run tests, tests/errors) fail with ENOSPC until space is freed.
  I removed my own test leftovers (~4 GB) — still 0 bytes available.

## DECISIONS
- D-M4f-1: drift (1011) = working copy differs from what bob would write AND from the sha256
  bob recorded in native.json (no record: any difference). A copy bob wrote from older bob.go
  declarations is not "edited by hand": build/gen rewrite it silently (like a missing file).
  bob check does not report such outdated-but-untouched files.
- D-M4f-2: native files come from the whole repository's graph in the host configuration
  (they describe whole modules). bob gen/check always evaluate //...; bob build/test check and
  rewrite native files only for whole-repo host runs (a partial graph would generate partial
  go.mod files and report false drift). R-FW-061's "before every build" holds for //... runs.
- D-M4f-3: bob build/test also write missing/outdated native files (R-FW-061 "bob rewrites it
  silently") and add them to the exclude block; a hand edit stops the run before Nix.
- D-M4f-4: ecosystem lock freshness is an optional interface (engine.LockFreshness) so the
  Ecosystem interface of SPEC §03.0.1 and the ecosystems in progress keep compiling. Without
  it an existing section is never stale. A missing known section is locked on demand by
  build/test (as missing pins, D-M4d-1) and reported by bob check as "<eco> is not locked".
- D-M4f-5: column = 1-based byte column of the call of the function the bob.go frame called
  (qualifier included: "cc.SharedLibrary" starts at "cc"); several calls on one line → the one
  whose text holds the package name as a string literal; unknown → 0 (omitted). Only the
  repo's own bob.go files; Position (public, SPEC R-FW-016 signature) is unchanged.
- D-M4f-6: bob check downloads nothing except, when no go is on PATH and the store lacks it,
  the evaluator's Go (nixpkgs' go) — evaluation needs it. bob gen may fetch the pinned nixpkgs
  source (for the index Native sees) unless --offline.

## NEEDS
- I-M5a (pkg/golang) and every ecosystem: implement engine.LockFreshness (R-FW-062: compare
  the section's `inputs` with the declarations, return the bob.go position) and set
  GeneratedFile.Committed for go.mod/go.sum (R-FW-022), Explain for the R-FW-061 template.
  Native gets Graph, Nodes, Lock section and Nixpkgs (nil when unavailable offline); no
  Inputs/Sources/Resolver.
- The R-FW-061 Go template ("✗ api/go.mod or go.sum was changed outside bob …" with a
  Go-specific hint) is not the generic 1011 block; it needs either an ecosystem-provided
  message/hint in GeneratedFile (SPEC has only Explain) or a 1011 variant (owner/SPEC).
- framework.zip must be regenerated after any pkg/bob change (another unit edited pkg/bob in
  parallel; the zip was regenerated at 16:52 from the tree then).

## TEST PROBLEM
- (none new)
