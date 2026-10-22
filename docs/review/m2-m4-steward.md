# Code steward review M2–M4.5 and resolvers (unit S-M2M4)

Role: code steward (AGENTS.md §7, §8), fresh context, 2026-10-22. Scope: `pkg/engine/*` (without
`pkg/engine/build`), `pkg/engine/engine.go`, `pkg/bob/...`, `pkg/builder/...`, `cmd/bob-builder`,
`pkg/nixpkgs`, `pkg/npm/resolve`, `pkg/ocaml/resolve`, `pkg/rust/resolve`, `internal/store/...`,
`internal/safe` (about 29,000 non-test lines).

Method: reading the code, plus tools run outside the repo (none added to it): `deadcode -test`
(x/tools v0.52.0), `staticcheck -checks all` (style checks too), the `modernize` analyzers, a
function-length scan (≥ 80 lines) and a check that each doc comment names its function. Behaviour
was not changed and no tests were touched.

**Overall verdict: no blocker.** The code is consistently clean. Errors are wrapped, hostile
input is capped and validated, comments mostly explain *why*, and nothing is reflection-heavy.
`staticcheck -checks all` reports one style nit for the whole scope. What is left is duplicated
small helpers across packages, a few oversized functions, and one structural issue: the
resolvers are embedded in the framework zip (R1 below).

## 1. What I changed (mechanical, behaviour-preserving)

I edited only packages whose units are finished and that are not embedded anywhere. I left these
alone on purpose:

- `pkg/builder`, `pkg/builder/bspec`, `cmd/bob-builder`: any byte change changes the bob-builder
  binary and its store path, so `builderbin/*` and `builders.go` would have to be regenerated
  (R-NIX-015).
- `pkg/bob`, `pkg/nixpkgs`, `pkg/{npm,ocaml,rust}/resolve`: they are part of
  `pkg/engine/eval/framework.zip`, so any edit makes `TestFrameworkZip` stale and needs a
  regeneration that would race with other units editing L0–L3.

| file | change |
|---|---|
| `pkg/engine/lock/{lock,sections,validate}.go` | removed the hand-written `contains` helper; `slices.Contains` at the 5 call sites |
| `pkg/engine/translate/translate.go` | removed the `slicesContains` and `sortedKeys` helpers (now `slices.Contains`, `slices.Sorted(maps.Keys(…))`); the "touched" condition, which appeared twice, is now `(*Translator).touched`; `failedHelper` no longer sorts map keys only to test whether any key matches |
| `pkg/engine/translate/errors.go`, `replay.go`, `render.go` | `slices.Sorted(maps.Keys)`, `maps.Copy`, `strings.SplitSeq` |
| `pkg/engine/eval/rules.go` | removed `showDir` (an identity function whose comment named a different function, `rootDirName`); `slices.Sorted(maps.Keys)` |
| `pkg/engine/eval/scan.go` | the doc comment of `hashes` named the old function `readContents`; sorted keys via `slices.Sorted(maps.Keys)` |
| `pkg/engine/eval/framework.go` | 3 × hand-written sort of map keys → `slices.Sorted(maps.Keys)` |
| `pkg/engine/eval/compile.go`, `deps.go` | `slices.Contains`, `strings.SplitSeq` |
| `pkg/engine/eval/evalbox/evalbox.go` | `wait` returns `classify(...)` directly instead of `if err != nil { return out, err }; return out, nil` |
| `pkg/engine/eval/evalbox/stage.go` | stage2 had its own copy of the exit-code/signal mapping; it now calls `exitCode` |
| `pkg/engine/eval/evalbox/cgroup.go` | `hasControllers` inner loop → `slices.Contains`; the doc comment of `fd` named `cgroupFD` |
| `pkg/engine/eval/evalbox/{landlock,watch}.go` | `errors.New` for constant messages; `strings.FieldsSeq`/`SplitSeq` |
| `pkg/engine/ir/ir.go` | `validate`: removed the `resolve` closure and the no-op `n.Deps[i] = dn.ID` / `g.Roots[i] = rn.ID` rewrites (ids were looked up by id and written back unchanged, left over from label references before A67) |
| `pkg/engine/drv/{aterm,drv,json}.go` | `sortedSet` → `slices.Compact(slices.Sorted(slices.Values(s)))`; `maps.Copy`; sorted keys; `errors.New` for constant messages |
| `pkg/engine/store/{export,builder}.go`, `pkg/engine/srcs/srcs.go` | removed two copies of a private `narBuf` type (a hand-written `bytes.Buffer`); `bytes.Buffer` instead (`nar.Write` already special-cases `*bytes.Buffer` and skips its bufio layer) |
| `pkg/engine/store/{drvcache,store}.go`, `pkg/engine/nar/nar.go`, `pkg/engine/nixidx/nixidx.go`, `pkg/engine/fetch/{archive,client}.go`, `pkg/engine/srcs/srcs.go` | `errors.New`, `maps.Copy`, `range n`, `strings.SplitSeq`, `slices.Clip` (modernize fixes) |
| `internal/store/{store,record}.go`, `internal/store/s3client/s3client.go` | `maps.Copy`; `errors.New` for constant messages |

Not applied on purpose:

- `errors.AsType` (17 sites): the rest of the code base, including units in progress, uses
  `errors.As`, so changing only these sites would make the code less consistent.
- `WaitGroup.Go` (4 sites): the explicit `go` statement keeps goroutine starts visible next to
  their `recover` and to the `safego` lint.

Verification (GOCACHE and TMPDIR under /var/tmp):

- `gofmt -l` and `go vet` are clean on every package I touched.
- `go test -count=1` passes on all touched packages except `pkg/engine/srcs`
  `TestImportManyTiming`, a 5 s budget test. It also failed before my changes (16.3 s cold). The
  host was at load average 25 on 8 vCPU, so this is the environment, not the edit. The rest of
  `srcs` passes with `-skip TestImportManyTiming`.
- `pkg/engine/store` `TestConcurrentOpenImport` failed in the baseline run (nix-store exit 1)
  and passed after my changes, so it looks flaky.
- `go run ./tools/ci -only fmt,vet,lint,layering`: lint and layering pass. fmt fails only on
  other units' files (`internal/fleet/fleet.go`, `internal/orchestrator/{planner,queue,reconcile}.go`,
  `internal/worker/loop.go`). vet fails only on R-ARCH-001 for `out/uicheck` (a package outside
  the allowed roots). Neither failure touches this unit.

Note: `pkg/engine/drv/path.go` gained `Placeholder` from another unit while I was working
(D-M4a-3), so `drv` is still being edited even though M2 is finished. My edits there are disjoint.

## 2. What remains (not changed), ranked

### R1 (structural, should be decided): the resolvers are part of the evaluator's framework module

- `pkg/engine/eval/framework.go:38` (`frameworkDirs`) and `frameworkFiles` skip only `gen/`,
  `bop/` and `testdata/`. `pkg/npm/resolve`, `pkg/ocaml/resolve` and `pkg/rust/resolve` (about
  10,000 lines of engine-side, lock-time code; nothing imports them yet) therefore go into
  `framework.zip` (checked: 76 files, including the three `resolve` directories). As a result:
  1. The trusted seed build (`seed.go:64`, `go build std <fw.packages()>`) compiles them on every
     toolchain or framework change.
  2. R7 (`rules.go`, `fwPkgs`) lets a user's bob.go import them.
  3. Any resolver edit makes `framework.zip` stale. This blocked cleanups here and will affect
     every resolver fix.
- Fix: move them below `gen/`, or add `resolve` to the skip list in `frameworkFiles` and
  regenerate the zip. Owner of the call: whoever owns `pkg/engine/eval` together with the
  resolver units.

### Duplication across packages (each copy is small; a shared helper would need a home that layering allows)

| what | where |
|---|---|
| atomic "write if changed" (4 variants with different durability: fixed `.tmp` name and no fsync, CreateTemp + chmod, CreateTemp + fsync) | `pkg/engine/eval/scan.go:176`, `pkg/engine/store/layout.go:184`, `pkg/engine/nixidx/nixidx.go:587`, `pkg/engine/lock/lock.go:289` (`WriteBytes`, the most careful one) |
| "read at most N bytes or fail" | `pkg/engine/nixidx/nixidx.go:571`, `pkg/rust/resolve/sparse.go:217`, `pkg/npm/resolve/registry.go:208`, plus inline copies in `lock.Read`, `builder.load`, `eval.unzipFramework` |
| SRI parsing and validation | `pkg/engine/fetch/fetch.go:109` (`parseSRI`), `pkg/engine/lock/validate.go:44` (`checkSRI`), and the npm integrity check in `pkg/npm/resolve/validate.go:60` |
| Nix base-32 alphabet and store-path shape | `pkg/engine/drv/path.go:18` and `pkg/engine/store/store.go:334` (`isStorePath` at `:312`); `store` already imports `drv` |
| three `Substituter` types | `pkg/engine/engine.go:145` (SPEC R-FW-009 literal), `pkg/engine/store/store.go:20` (identical fields), `pkg/engine/translate/context.go:35` (different, a display struct). `engine.Substituter` could be `= store.Substituter`, but SPEC shows the struct, so this is an API decision |
| `eval.Limits` mirrors `evalbox.Limits` minus `CPUs`, with a field-by-field copy | `pkg/engine/eval/eval.go:37`, `:46` (`box()`), `pkg/engine/eval/evalbox/evalbox.go:34` |
| sandbox env lists that differ in 3 entries | `pkg/engine/eval/toolchain.go:73` (`env`) and `:81` (`buildEnv`) |
| canonical JSON (decode with UseNumber, encode without HTML escaping) | `pkg/engine/ir/ir.go:330` (`Canonical`), `pkg/bob/irout.go:128` (L0 cannot import E0, so this copy is justified), `pkg/engine/lock/canon.go` |
| last-N-lines helpers | `pkg/engine/eval/eval.go:324` (`tail`), `pkg/engine/translate/classify.go:250` (`lastN`), `pkg/engine/eval/evalbox/evalbox.go:486` (`lastLines`), `pkg/builder/testrun.go:370` (`lastLines`, on a file) |
| exit status → int (signal → 128+n) | `pkg/engine/eval/evalbox/evalbox.go:446` (`exitCode`), `pkg/engine/store/ns.go:276,353`, `pkg/builder/run.go:222`, `pkg/builder/testrun.go:126` |
| bounded worker pools with recover | `pkg/engine/srcs/srcs.go:140` (`parallel`), `pkg/npm/resolve/resolve.go:626`, `pkg/rust/resolve/sparse.go:96`. One `internal`-free helper in a low layer would remove three copies |
| "misuse outside evaluation" branch | `pkg/bob/node.go:71-77` (`graph.Misuse`) and `:85-90` inside `newNode`; `newNode` could call the same function |
| sorted, deduplicated copy | `pkg/nixpkgs/nixpkgs.go:95` (`sorted`) is the same as `drv.sortedSet`; fine to keep (L1 cannot import E0) |

### Oversized functions (≥ 100 lines, or hard to follow)

| function | lines | note |
|---|---|---|
| `pkg/npm/resolve/resolve.go:386` `(*builder).output` | 204 | four phases in one body: importer roots, optional reachability, dropping instances with a missing requirement, emitting the section. Split by phase. `:505` and `:577` loop `for dir, rec := range sec.Importers { for _, r := range ims { if r.dir != dir … } }`, which is quadratic and only needs `ims` with `sec.Importers[r.dir]` |
| `pkg/engine/srcs/srcs.go:196` `Resolve` | 149 | pattern compilation, the ignore-rule prefix walk and the recursive walker are closures in one function; extracting `compilePatterns` and a `walker` type would make it readable |
| `pkg/engine/eval/evalbox/evalbox.go:187` `start` | 126 | bind/landlock rule assembly could be its own function |
| `pkg/ocaml/resolve/solve.go:275` `build` | 112 | |
| `pkg/npm/resolve/place.go:268` `newPlaceDep`, `:85` `checkCurrent` | 106, 98 | these follow arborist's structure (cleanroom port); keep them mappable to the original |
| `pkg/engine/drv/aterm.go:267` `(*parser).derivation` | 105 | repeated `expect(",")` + field; acceptable for a hand parser |
| `pkg/engine/translate/translate.go:760` `Finish` | 101 | |
| `pkg/builder/run.go:51` `parseStep` | 99 | every case repeats `decodeStrict` + `Invalidf("%v")`; a generic `decodeArgs[T]` would halve it (needs the bob-builder regeneration, see §1) |
| `pkg/engine/translate/translate.go` (whole file) | 1,026 lines | the largest file in scope; the cache warning/hint code (`cacheWarning`, `cacheHint`, `cacheBase`, `isSubstituter`, `netReason`, about 120 lines) could move to its own file |

### Dead or unreachable code (from `deadcode -test`; none removed)

- Possibly dead, in packages I could not edit (§1):
  - `pkg/rust/resolve/resolve.go:49` `(*Resolution).Edges`
  - `pkg/ocaml/resolve/filter.go:40` `EvalString`
  - `pkg/npm/resolve/semver.go:629` `(*Range).IsAny`
  - `pkg/builder/builder.go:75` `(*Ctx).Logf`

  All are exported and may be meant for the coming `gen`/`bop` packages. Delete them if M5 does
  not use them.
- `internal/store/records.go:343,349,365,419,426,505,511`, `record.go:32` (`Panics`) and
  `store.go:485` (`Lease.Release`) are unreachable today, but they are the server units' data
  model (orchestrator and worker are in progress). Keep them.
- The `pkg/bob` and `pkg/nixpkgs` entries in the deadcode output (`Main`, `WithRace`, `Import`,
  `nixpkgs.Lib`, …) are false positives: they are user-facing API or are called by the generated
  evaluator main.

### Smaller items

- `pkg/engine/fetch/fetch.go:193-196`: `p.path()` already prefixes `fetch <label>:`, and `Ensure`
  wraps it again (double prefix; unreachable after the checks above it). This is not
  behaviour-neutral, so I left it.
- `pkg/engine/translate/translate.go:745` `mapPaths` sorts all source paths on every log line it
  rewrites. Sort once in `New`.
- `pkg/builder/run.go:223`: `fmt.Errorf("%s", ee.ProcessState.String())` should be `errors.New`
  (builder regeneration needed).
- Error prefixes are inconsistent across engine packages: `store:`, `lock:`, `nixidx:`, `srcs:`,
  `nar:` have a package prefix; `drv`, `eval` and `ir` do not, and `fetch` uses
  `fetch <label>:`. Not worth churn now, but new code should follow one rule.
- Rust resolver: about 40 constant `fmt.Errorf("…")` messages in `pkg/rust/resolve/{toml,semver,cfg}.go`
  (also `pkg/npm/resolve/validate.go:60` and `semver.go:267`) should be `errors.New`. They are
  in the framework zip, so fix them together with R1.
- staticcheck ST1023: `pkg/rust/resolve/sparse.go:135` (`var cerr error = os.ErrNotExist`).

## 3. Not reviewed

`pkg/engine/build`, `pkg/cli`, `cmd/bob`, `internal/{orchestrator,worker,api}`, `cmd/bob-server`
and `web/` were excluded as in progress. No tests were reviewed or touched.
