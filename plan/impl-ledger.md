# Implementation ledger (coordinator-owned), Phase 3.2

Resume after a limit: read this file, `git status`, `plan/progress/*.md`; relaunch every unit not
`done` from its progress file. Commit per task: `M<m>.<n>: <title>` with explicit paths.
Gate H: tag gate-h exists; `go run ./tools/ci protect -write` (hash manifest) pending, owner action.

| Unit | Tasks (execution-plan) | Owns | Model | State |
|---|---|---|---|---|
| I-M2a | M2.1 drv/nar, then M2.4 fetch | pkg/engine/{drv,nar,fetch} | opus | launched |
| I-M2b | M2.2 store, then M2.5 srcs, M2.3 import | pkg/engine/{store,srcs}, scripts/fetch-nix.sh | opus | launched |
| I-M3a | M3.1 labels, M3.2 core API, M3.3 ext/IR | pkg/bob (not diag), pkg/bob/ext, pkg/engine/ir | opus | launched |
| I-M3b | M3.5 evalbox sandbox | pkg/engine/eval (sandbox), internal sandbox helpers | opus | launched |
| I-M2a | M2.1, M2.4 | done, committed | | |
| I-M2b | M2.2, M2.5, M2.3 | done, committed | | |
| I-M3a | M3.1-M3.3 | done, committed | | |
| I-M3b | M3.5 | done, committed | | |
| I-M4a | M4.1, M4.4 (builder part) | done, committed | | |
| I-M4b | M4.2 translate | done, committed | | |
| I-M4c | M4.5 (postgres service uncommitted: needs I-M3c nixpkgs) | done, committed | | |
| I-M3c | M3.6 lock, M3.7, M4.3 nixpkgs | opus | running |
| I-M3d | M3.4 evaluator | opus | running |
| I-B1 | M13.1-M13.3 internal/store | opus | running (A77 early start) |
| I-R-npm / I-R-opam / I-R-crates | pure resolvers M9.1 / M11.1 / M8.1 | opus | running (A78) |
| I-UI | M18 web UI vs mock | opus | running (A78) |
Next after I-M3c+I-M3d: M4.6 build.Run + CLI core, then M5.x; pending owner OK: plan/test-corrections.md edits.
