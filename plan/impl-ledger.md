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
