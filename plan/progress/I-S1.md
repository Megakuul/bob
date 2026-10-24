# I-S1 progress (error codes, events schema, usage rendering, cc Stale)

## done
1. New codes (SPEC §02.3 table in spec/02-cli-dev.md and SPEC.md, pkg/bob/diag/codes.go, codes.lock appended by `go test ./pkg/bob/diag -update`):
   - 0010 usage `unknown platform "{platform}" (known: {known})` (R-PLAT-001; was code 1)
   - 0011 usage `{label}: {ecosystem} packages cannot be cross-compiled yet (requested {platform} on a {host} host)`, hint "build for the host platform, or use an ecosystem that supports cross-compilation" (R-PLAT-005; was code 6 + Cause)
   - 1026 definition `nixpkgs mismatch: {what}`, hint `{hint}` (R-PLAT-007; 1021 was already "toolchain mismatch", kept). The §02.3 mapping rows (1020 no longer lists OCaml cross; 1021 no longer lists nixpkgs target-library mismatch) updated.
   - Uses switched: pkg/engine/toolchain.go (mismatch, CrossRefused), pkg/cli/cmd_build.go (checkPlatforms, runRun), toolchain_test.go.
   - Golden diff (tests/goldens/diag/codes.golden, via -update): exactly three added entries: BOB-E-0010 (usage, exit 2, no hint), BOB-E-0011 (usage, exit 2, hint above), BOB-E-1026 (definition, exit 3, message `nixpkgs mismatch: {what}`, hint `{hint}`), no other line changed.
2. schema/events-v1.json: `note` event (type, message required; code, label, attr, platform, field optional), test.result status `skipped` and `runner`, `reason` (additive). SPEC §02.4 table (both files) updated.
3. Usage errors as `✗ <message>`: translate.definitionLine now also covers 8, 10, 11, 1026 (with 6, 1020). Not 0001/0003/0004/0009: tests/cli, tests/install, tests/devenv require "BOB-E-000x" in the output for those (the `✗` line shows no code), so they keep the `error[...]` block. Faults goldens do not contain usage codes (unchanged).
4. pkg/cc/gen Stale: a graph holding only part of the repository no longer reports BOB-E-1012; only declarations present are compared with their entries (missing / URL / Strip). Consequence: an External removed from bob.go leaves an unused lock entry that is no longer reported stale (cannot be told from a partial graph). Test: pkg/cc/gen/stale_test.go.
framework.zip regenerated (go generate ./pkg/engine/eval); pkg/engine/eval TestFrameworkZip green.

## tests
Green: pkg/bob/..., pkg/cc/..., pkg/engine, pkg/engine/eval, pkg/cli, pkg (layering). gofmt clean on my files.

## NEEDS / observed in other units' WIP (not mine)
- pkg/engine/build does not compile: pipeline.go:161 r.materialise undefined; materialise.go:124 type error (another unit). My build-side usages need no edit there, but its tests could not run.
- pkg/engine/translate TestReplayCatalogue/TestReplayBothNixVersions fail: BOB-E-1008 "template argument ... missing" (go-too-new replay uses 1008 without the "requirement" variant args; caused by the 1008 variant added by another unit).
