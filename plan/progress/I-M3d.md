# I-M3d progress (M3.4 bob.go rules, evaluator generation, version lock, eval key, determinism)

Owner of: pkg/engine/eval (incl. two small evalbox API additions), pkg/engine/ir/evalout.go.

## done (M3.4 engine side complete, 15:10)
- framework.go: framework module = L0–L3 non-test Go files + minimal go.mod (module, go
  directive; L0–L3 import only std). Embedded `framework.zip` (deterministic; regenerate with
  `go generate ./pkg/engine/eval`, TestFrameworkZip fails when stale). Source builds use the live
  tree (runtime.Caller), -trimpath builds the zip. Installed per repo in `.bob/eval/gomod` in the
  go command's offline layout (+ `.mod`, `.ziphash`), go.sum lines computed (h1, dirhash).
  `eval.BobVersion()`: module version of an installed bob, else `v0.0.0-dev`.
- scan.go: one ReadDir walk (R-FW-002 skip rules), size+mtime index `.bob/eval/index.json`,
  eval key (keyVersion, bob version, framework h1, Go version, GOARCH, sorted path+sha256 of every
  bob.go, go.mod, go.sum). Key hit reuses `.bob/eval/bin/bob-eval`.
- rules.go: R1–R7 with the exact SPEC texts (BOB-E-1024), sorted by path; R5 writes
  `<dir>/zz_bob_cgo.go` and adds it to bob's block in `.git/info/exclude`.
- deps.go: dependency bob repos from go.mod requires (directory replaces, host module cache, or
  `go mod download` with the user's Go settings), transitive; copies go.mod + bob.go files to
  `.bob/eval/mods/<escaped>@<v>/`, replace lines; newer-framework check (BOB-E-1021).
- gen.go: overlay hides compilable files of bob.go dirs, package main bob.go → `.bob/eval/rename/
  <n>.go` with `bobmain<n>` on the same line, `_bobeval/main.go` (sorted blank imports), go.mod
  with framework + deps, go.sum. All paths sandbox paths (/src) → checkout independent.
- compile.go: go build messages → BOB-E-1020 at `<bob.go>:<line>:<col>` (renamed copies and dep
  copies mapped back, `bobmain<n>` → `main`), sorted; nothing user-positioned → 9004 internal.
- eval.go: two sandbox runs. Compile (key miss only): toolchain, repo, gomod read-only, bin dir
  writable, seed lower + persistent per-repo upper `.bob/eval/gocache`; go build runs no user
  code. Run: toolchain + repo read-only, binary dir exec, S13 §5.2 env contract. No IR →
  `ir.Diagnostics` of the bob:: lines. bobVersion of the IR checked (R-FW-044). flock on
  `.bob/eval/lock` (exclusive for generate/compile, shared for run).
- determinism.go: `Compare(a, b)` → BOB-E-1018 "<label> differs between two runs (field
  nodes[i].deps[j])"; `CheckDeterminism(ctx, req)` evaluates twice.
- evalbox: `Bind.Writable`, `Spec.CacheDir` (persistent overlay upper of /work/gocache).
- ir/evalout.go: `ir.Errors` (Unwrap []error), `Diagnostics` (bob:: lines; code templates
  reversed to args; unknown codes never trusted), `DefinitionError` (1020), `RuleError` (1024),
  `Nondeterministic` (1018), `ToolchainMismatch`/`FrameworkTooOld` (1021), `EvaluatorBug` (9004).

## tests
- green: `go test ./pkg/engine/eval/... ./pkg/engine/ir/... ./tests/sandbox` (root; also
  `-tags bob_nolandlock`); eval tests also as bobtest (embedded-zip path, non-root overlay upper).
  Unit tests cover: core-repo IR, key-hit reuse, path independence, hidden broken Go file,
  main-package rename positions, type error at `cmd/app/bob.go:L:13` without generated names,
  evaluator diagnostics, rules r1–r7/all (tests/core fixtures), R5 stub + git exclude,
  dependency repo via replace, Compare, concurrent evaluations.
- CI `-only fmt,vet,lint,layering` green.
- tests/core: 13 pass / 76 fail before and after (no PASS→FAIL). The R-FW-043/047/016 fixture
  tests now fail on missing L2 APIs (golang.Module, cc.Library, rust.CLib, …), not on the
  evaluator; the CLI ones are red on bob build/graph/check (M4.6).
- Budgets (TestEvalBudgets, BOB_EVAL_BUDGETS=1, 1,000-package wide graph, 8c/15G, host load 7–13
  from other units): B-001 key hit 0.074 s (≤ 0.1), B-002 one change 0.93 s (≤ 1.5), B-003 cold
  (no per-repo cache, seed present) 5.3 s (≤ 15).

## IR candidates
- Not producible yet: `out/ir-candidates/{acme,polyglot}.error.txt` hold the evaluator's answer
  (BOB-E-1020 "undefined: golang.Module / cc.Library / rust.CLib …"): pkg/golang, pkg/cc,
  pkg/rust, pkg/npm, pkg/connect, pkg/proto have no API yet. polyglot also needs A66
  (`[]bob.Named{`). Rerun when L2 exists: evaluate a copy with eval.Evaluate and write res.IR.

## DECISIONS
- D-M3d-1 Framework source: live tree when the binary's source is on disk, embedded zip
  otherwise; freshness enforced by TestFrameworkZip (go generate ./pkg/engine/eval).
- D-M3d-2 Version: `BobVersion()` = installed module version (semver, no +dirty), else
  `v0.0.0-dev` (stable for goldens); the framework's content hash is in the eval key and the
  module-cache stamp, so a source build with the same version string never reuses stale code.
- D-M3d-3 Evaluator go.mod lists only the framework and dependency bob repos (R7 allows nothing
  else), so the build needs no other module and runs offline with GOFLAGS= (readonly).
- D-M3d-4 Binary at `.bob/eval/bin/bob-eval` (SPEC: `.bob/eval/bob-eval`): the run binds the
  directory with exec rights and the compile step gets it writable.
- D-M3d-5 Compile and run are two sandbox runs; the compile run gets a persistent per-repo
  GOCACHE upper layer (`.bob/eval/gocache`) over the trusted seed. go build runs no user code
  (CGO_ENABLED=0, GOTOOLCHAIN=local, no generate/toolexec), and the cache belongs to one repo,
  so it cannot poison another repo; the run step never sees it. Needed for B-002 (≤ 1.5 s).
  Workers (M13.7) should not keep it across jobs.
- D-M3d-6 Rules are checked when the key misses (they run before compiling). R2/R4/R6
  violations caused only by edits to non-bob.go Go files show at the next key miss; the IR is
  not affected by those files.
- D-M3d-7 Code-less definition errors (bob.go compile errors, pkg/bob errors without a SPEC
  code) use BOB-E-1020 "<pos>: invalid build definition: <msg>" with Location set, until a
  dedicated code exists (see NEEDS).
- D-M3d-8 R2 for the root directory without Go code expects the last module path element
  (the checkout directory name is not stable); root rule messages say "./bob.go".
- D-M3d-9 Toolchain: `Request.GoRoot` (pinned Go from the store, passed by the caller) or the go
  on PATH; GOROOT and version from `$GOROOT/VERSION` (fast path), `go env` otherwise; a Go older
  than the framework's go directive → BOB-E-1021.
- D-M3d-10 The evaluator's GC waits for a 256 MiB soft limit (debug.SetGCPercent(-1) +
  SetMemoryLimit in the generated main; the S13 env contract forbids GOGC in the env); ~25 % of
  the 1,000-package run time.
- D-M3d-11 Evaluate checks only the IR's bobVersion (R-FW-044); full `ir.Parse` stays with the
  caller (build.Run), which links the ecosystems whose kinds it validates.
- D-M3d-12 Newer-framework check of dependency repos uses BOB-E-1021 ("<module>: toolchain
  mismatch: needs bob <v> or newer; this is bob <v'>") and is skipped for dev builds.

## NEEDS
- M4.6 (CLI/build.Run): call `eval.Evaluate` with `GoRoot` = pinned Go from the store; emit the
  `eval` event with `cached=Result.Cached`; render `ir.Errors` (each a *diag.Error, exit by
  diag.ExitCode); `bob check --determinism` = `eval.CheckDeterminism`; `ir.Parse(res.IR,
  eval.BobVersion())` after evaluation.
- diag / SPEC §02.3 (coordinator): (a) a definition code for code-less evaluation errors and
  bob.go compile errors, e.g. 1026 `{pos}: {message}` (I-M3a asked too); (b) BOB-E-1018's default
  hint must be the R-FW-008 text "do not range over maps when building lists; sort the keys"
  (tests/core TestRFW008NondeterministicBobGo expects it; codes.go has "bob.go must not depend
  on time, randomness, environment or map order"); (c) a "bob version" code for R-FW-043.4
  instead of 1021 if wanted.
- Coordinator: run `go generate ./pkg/engine/eval` (rewrites framework.zip) whenever L0–L3
  sources change, before committing; TestFrameworkZip fails otherwise.
- Budgets: register `BOB_EVAL_BUDGETS=1 go test ./pkg/engine/eval -run TestEvalBudgets -v
  -timeout 30m` (prints `BUDGET B-001/B-002/B-003 <s>`) in tools/internal/budgets/benches.go.
- I-M3a (pkg/bob, optional): the evaluator spends ~40 ms of 1,000 nodes in JSON canonicalisation
  (decode to interface + re-encode); a direct canonical encoder would leave more B-001 headroom.
- L2 units: acme/polyglot IR goldens wait for their APIs.

## TEST PROBLEM
- None new. Known: A66 (polyglot `[]bob.NamedVariant{`), A68 (toy kinds not linked in
  tests/core: TestRFW046ProjectionIgnoresUndeclaredAxes now reaches ir.Parse and fails there).
