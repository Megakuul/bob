# I-M5c progress (eval: R2, bob.Import, framework go.sum; diag 1008 variant; R-GO-005 cutoff; go fixture replace)

Owner of: pkg/engine/eval, pkg/golang (+ gen, bop, resolve). Small additive edits in
pkg/engine/build (plan.go RealiseNow, pipeline.go planBuilt/options, native.go root go.sum,
native_root_test.go), pkg/engine/planstore.go (PlanRealiser), pkg/bob/diag (1008 variant).

## done
1. R2 root (superseded by coordinator ruling A131): a root bob.go without non-test Go files
   accepts any package name; non-root dirs without Go code use the sanitised dir name; a root
   with Go code must match it. rules.go (goPkg.nonTest), SPEC R2 text (spec/01-core.md +
   SPEC.md). Test: eval TestRuleR2Root. Directory renames made for the dir-name rule reverted.
2. bob.Import honoured before compiling (eval/imports.go addBobImports; patch at
   /var/tmp/i-m5b was gone, rewritten). Tests: TestAddBobImports, TestEvaluateCrossRepo (file://
   GOPROXY, fresh consumer). Manual: cross-repo app lock + build //use:consumer + graph + gen +
   check pass.
3. eval.FrameworkSum + golang/gen.RootGoSum; build/native.go writes root go.sum with the
   framework lines. Tests: TestRootGoSum, build TestRootGoSumHasFramework.
4. BOB-E-1008 variant `requirement` (A134), codes.lock appended, codes golden -update; lock.go
   uses it. Test: gen TestLockErrors (message carries the §03.2.4 text).
5. R-GO-005 (A136): engine.PlanRealiser / planStore.RealiseNow; Go planner cutoff.go. Tests:
   bop TestLinkobjSplit, gen TestPlanCutoff (export by content, unchanged importer, fallback =
   plain plan). Manual e2e: built 2 after a body-only change, binary identical to plain build;
   compile error with cutoff → fallback, translated BOB-E-2001.
6. Go fixture TEST PROBLEM (A133): require v0.0.0 + replace for imported workspace modules
   (resolve.WorkspaceDeps, gen Native). Test: gen TestNativeLocalModules (real go command:
   list -m all, build, vet offline). Test correction 26 (resolve_test.go); SPEC §03.2.5 and
   R-GO-002b amended.
- framework.zip regenerated (go generate ./pkg/engine/eval).
- Green: go test ./pkg/golang/... ./pkg/bob/... ./pkg/engine ./pkg/engine/eval/...
  ./pkg/engine/build; staticcheck clean on touched packages; gofmt clean on my files.
  tools/ci -only fmt,vet,lint,layering fails only on other units' files (pkg/engine/build/dev.go
  gofmt, out/uicheck R-ARCH-001, pkg/rust/gen and internal/store lint).

## still red (not mine)
- tests/eco/golang go-fixture tests (TestRGO004aRequirementNeedsNewerGo, R-GO-002b fixture-go):
  the fixture's proto/msg/v1/bob.go needs proto.Library (pkg/proto not implemented).
- tests/core TestRFW022CrossRepo: `bob lock` took 4m53s on this loaded host (fresh nixpkgs pin)
  → harness 5 min timeout (exit -1); and the test expects the root go.mod written after
  `bob lock` + `bob build //use:consumer`, but bob writes native files only on whole-repo runs
  and bob gen (NEEDS 1).
- TestRGO005CutoffEarlyCutoff not run (200-package repo with two fresh homes exceeds the 5 min
  harness timeouts on this host); same flow verified by hand on a 3-package repo.

## DECISIONS
A131 (coordinator), A133–A136 in plan/decisions.md.

## NEEDS
1. build/CLI owner: TestRFW022CrossRepo reads the root go.mod after `bob lock` and a targeted
   `bob build`: write the root go.mod/go.sum (or all native files) at bob lock, or on every
   build (the root go.mod depends only on bob.go files).
2. Root go.sum lacks lines for bob.Import'ed dependency repos (A135): bob.lock should record
   their h1/modH1 (or bob lock fills them from the module cache) so `go list`/gopls work in the
   root module.
3. Cutoff: no substitution from bob's caches while planning (narinfo batch as in
   runner.substitute could be reused by planStore.RealiseNow).
4. Once seen (not reproducible): `bob build //use:consumer` exit 70 right after `bob gen` wrote
   the root go.mod with the dependency requirement; a rebuilt binary built fine. Another unit
   was editing pkg/engine/build at the same time.
