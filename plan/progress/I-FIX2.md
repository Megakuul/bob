# I-FIX2 progress

Scope: F3, F5, F6, F7, F11, F17, F18, noexit syscall.Exit (docs/review/m2-m4-reviewer.md).

## done
- F3: pkg/bob/node.go nixTokens (ids `nixpkgs:<attr>@<plat>+tool|lib|pkg[+cross][+n-<hash8>]`), test pkg/bob/nixid_test.go
- F5: store.ExportItem.WriteNAR + store.TreeItem, Store.Import streams via io.Pipe (exportPipe);
  fetch.importPath and srcs.Import stream large trees (srcs keeps NARs <= 4 MiB, 256 MiB total). Test pkg/engine/store/stream_test.go
- F6/F7: translate buildLog byte caps, run budget 64 MiB (boundLogs), hook logs only for the last hook;
  untrusted (no raw_msg) level-0 messages become claims settled at Finish. Test pkg/engine/translate/bounds_test.go

- F11: bspec.TestRunArgs.TimeoutS (default 600 s, DefaultTestTimeoutS), builder kills the test, 2007 diag.
  SPEC §03.0.5 amended in spec/03-ecosystems.md and SPEC.md. Test pkg/builder/testrun_timeout_test.go
- F18: genbuilders -check (+ -base), CI stage tools/ci/stage_builder.go ("builder"), test
  cmd/bob-builder/genbuilders/check_test.go. BuilderVersion 2, builders regenerated (16:27).
- noexit: tools/lint/noexit.go flags syscall.Exit/unix.Exit; allow-list ns.go/stage.go; test
  tools/lint/noexit_syscall_test.go + testdata/noexitsys
- decisions.md rows A91, A91b, A91c, A92, A92b, A93a..A93g (A94-A96 were taken by others)
- store: sqliteBusy now matches Nix 2.34's "SQLite database '<db>' is busy" (was never retried)

## status: DONE (16:46)
CI -only fmt,vet,lint,layering,builder: builder ok; fmt/vet/lint/layering fail only on other units' files
(internal/logstore fmt, out/uicheck, internal/worker S1016, pkg/engine/build mid-edit by I-M4d).
Pre-existing: srcs TestImportManyTiming / tests/nix TestRNIX009Import10000SourceDirs ~9-10 s vs 5 s budget
(A/B: old buffered import 10.0-10.7 s, streamed 8.9-9.2 s, same host, load ~5).

## DECISIONS

## NEEDS
- Ecosystem owners: expose `Timeout time.Duration` in test options -> TestRunArgs.TimeoutS (A92).
- Anyone changing pkg/builder after the next commit: bump bspec.BuilderVersion (CI stage "builder").

## TEST PROBLEM
