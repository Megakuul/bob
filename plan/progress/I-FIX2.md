# I-FIX2 progress

Scope: F3, F5, F6, F7, F11, F17, F18, noexit syscall.Exit (docs/review/m2-m4-reviewer.md).

## done
- F3: pkg/bob/node.go nixTokens (ids `nixpkgs:<attr>@<plat>+tool|lib|pkg[+cross][+n-<hash8>]`), test pkg/bob/nixid_test.go
- F5: store.ExportItem.WriteNAR + store.TreeItem, Store.Import streams via io.Pipe (exportPipe);
  fetch.importPath and srcs.Import stream large trees (srcs keeps NARs <= 4 MiB, 256 MiB total). Test pkg/engine/store/stream_test.go
- F6/F7: translate buildLog byte caps, run budget 64 MiB (boundLogs), hook logs only for the last hook;
  untrusted (no raw_msg) level-0 messages become claims settled at Finish. Test pkg/engine/translate/bounds_test.go

## next
F11, F17, F18, noexit, decisions rows

## DECISIONS

## NEEDS

## TEST PROBLEM
