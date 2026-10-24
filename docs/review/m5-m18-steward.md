# Code steward review M5–M18 (unit I-S4)

Role: code steward (AGENTS.md §7, §8), fresh context, 2026-10-24. **Read-only review:** no code,
test or plan file was changed. The only files written are this one and `plan/progress/I-S4.md`.
No git commands were run.

Scope: the M5–M18 code. That is the ecosystem APIs and generators (`pkg/{golang,cc,rust,npm,ocaml}`,
`pkg/{proto,connect,graphql,openapi}`, `pkg/output/*`, `pkg/release`, and each package's `gen`/`bop`/`resolve`),
`pkg/engine/{build,materialise}`, `pkg/cli` (+`devenv`, `remote`), all of `internal/` except the
generated `internal/gen`, `cmd/bob-server`, and `web/src`. About 110,000 non-generated lines.

Method: I read the code. I also ran tools from outside the repo (`/var/tmp/I-S4/bin`, nothing
added to the repo): `staticcheck v0.8.2 -checks all` (minus ST1000/1003/1020-1022),
`deadcode -test ./...` (x/tools v0.51.0), and an AST scan for functions of 100+ lines and doc
comments that name a different function. On top of that I grepped for duplicated helper names,
TODO/debug output, error matching on strings, dynamitedb/lake/S3 imports, and decision ids
cited in code, which I checked against `plan/decisions.md`. Line numbers are from 2026-10-24.

**Overall verdict: no blocker.** The code is consistently clean:

- `staticcheck -checks all` reports 5 style nits for the whole tree.
- No TODO/FIXME is left in the code, and there are no debug prints.
- The only `nolint` suppressions are 3, in `internal/auth` and `internal/http`.
- Only `internal/store` imports dynamitedb, and only `internal/logstore` imports lake.
- Every lake workaround points to a program in `plan/progress/I-B16-repro/`.
- `web/src` follows §8: no class helpers, no `rounded-full` or gradients, no hex colours outside the theme, no `console.log`.

The value is in three areas:

- **Duplication between the ecosystem generators.** Seven copies of the job/input plumbing have
  started to drift apart.
- **Errors classified by string matching** in the server. One of these branches can never match.
- **Decision-log drift.** Ids collide, so code comments point to the wrong decision.

Every item below has a concrete, minimal refactor. None is applied.

---

## Ranked findings

### F1 (high): error conditions are matched on message strings, and one branch never matches

AGENTS.md §5.3 says to use sentinel errors for conditions callers act on. These places act on
`err.Error()` text instead:

| site | matches | produced by |
|---|---|---|
| `internal/api/interceptor.go:173-185` `workerError` | prefixes `"unauthenticated: "`, `"permission_denied: "` | `internal/orchestrator/registrar.go:256-258` (text only), `internal/orchestrator/hub.go:14` `errNoLease` |
| `internal/fleetsrv/service.go:315-324` `checkoutError` | prefix `"permission_denied: "` (a second copy of the same mapping) | `hub.go:14` |
| `internal/orchestrator/controllers_fsck.go:37` `Fsck` | prefixes `"corrupt:"`, `"invalid:"` | `corrupt()`/`invalid()` at `:45-47` in the same file |
| `internal/orchestrator/controllers_fsck.go:72` `decodeFailed` | falls back to `Contains("cbor:")` and `Contains("unexpected EOF")` | — |
| **`internal/orchestrator/registrar.go:176`** `consider` | `Contains(err.Error(), "decode record")` | **nothing.** No bob or dynamitedb v0.6.1 code produces this text: `dynamitedb.Get` returns the raw CBOR decoder error (`get.go:42-44` → `serialization.go:36`). So a garbage `wreg/` record is returned as an error instead of the intended "registration record unreadable" rejection. |
| `internal/logstore/partition.go:150` `noTable` | `"does not exist"` + `"table '"` | lake `engine.go:105` (`fmt.Errorf`, no sentinel in lake). Acceptable if a comment says so. |

As soon as any layer wraps one of these errors with a `"job x: %w"` prefix, the prefix checks
silently fall through to Internal.

Refactor:

- In `orchestrator`, add `var ErrNoLease, ErrUnauthenticated, ErrPermissionDenied = errors.New(...)`.
  `errNoLease` returns `fmt.Errorf("%w: worker holds no lease on job %s", ErrPermissionDenied, id)`,
  and the registrar uses `%w` the same way.
- `api.workerError` and `fleetsrv.checkoutError` switch on `errors.Is`.
  `api` must not import `orchestrator`, so the sentinels go in `internal/api`
  (`api.ErrWorkerUnauthenticated`, …), and the orchestrator wraps them.
- In fsck, use `errCorrupt` and `errInvalid` sentinels with `errors.Is`.
- In `registrar.go:176`, use `decodeFailed(err)`. It is in the same package. Add a unit test that
  writes `0xff 0x00` to `wreg/<id>`. `controllers_staging_test.go:92` already shows how.

### F2 (high): the bob-builder job plumbing is copied in every generator, and the copies drift

Each `pkg/<eco>/gen` has its own copy of the same five helpers. The copies are not identical:

| helper | copies | drift |
|---|---|---|
| `inputs` collector (`newInputs`/`add`/`list`) | `pkg/engine/build/plan.go:506`, `pkg/cc/gen/plan.go:203`, `pkg/golang/gen/plan.go:377`, `pkg/rust/gen/plan.go:346`, `pkg/ocaml/gen/plan.go:123`, `pkg/npm/gen/plan.go:357`, `pkg/output/appimage/gen/eco.go:92` | cc skips an empty output path (`:215`); rust/golang/engine-build add `""` to the sources. engine-build keeps sources as a slice and dedups at the end; the others use a set. appimage's `add` returns an error. |
| `job` (spec map → `drv.Job` → `Finalize` → `Path` → record in `pl.own`) | `npm/gen/plan.go:417`, `cc/gen/plan.go:265`, `golang/gen/plan.go:439`+`:444`, `rust/gen/plan.go:389`, `ocaml/gen/plan.go:193` | the bodies after the spec are line-for-line the same. |
| fixup contract written as `map[string]any{"strip":false,…}` | `engine/build/plan.go:428`, `npm/gen/plan.go:411`, `golang/gen/plan.go:429`, `cc/gen/plan.go:266`, `rust/gen/plan.go:383`, `engine/outputs.go:508`, `output/appimage/gen/eco.go:281` | `cc/gen/plan.go:250` and `ocaml/gen/plan.go:176` each redeclare `bspec.Fixup` as a private struct. |
| `pickOutput` | `engine/build/plan.go:488`, `cc/gen/plan.go:305`, `golang/gen/plan.go:556`, `rust/gen/plan.go:331`, `ocaml/gen/plan.go:273` | the same, byte for byte. |
| `errorList` + `joinErrs` | `engine/toolchain.go:214`, `npm/gen/plan.go:181`, `cc/gen/plan.go:96`, `golang/gen/plan.go:183`, `rust/gen/plan.go:126`, `ocaml/gen/plan.go:108` | the same. This is `errors.Join` plus a single-error shortcut. |
| `invalid` (BOB-E-1020 + `DefinedAt`) and `internal` | `cc/gen/plan.go:85,90`, `npm/gen/plan.go:170,175`, `rust/gen/plan.go:157`, `ocaml/gen/lock.go:216`, `golang/gen/plan.go:165,177` (method form) | the same. |
| `outPath(*drv.Derivation)` | `cc/gen/plan.go:296`, `golang/gen/plan.go:471`, `npm/gen/plan.go:440` | the same. |

Layering (R-ARCH-003): E2 generators cannot import `pkg/builder/bspec`, but they can import
`pkg/engine` (E2) and `pkg/engine/drv` (E0). Refactor:

- Add a small `pkg/engine/job.go` that exports:
  - `Inputs`: a set-based collector with an empty-path guard (cc's semantics);
  - `PickOutput`, `JoinErrors`, `Invalid(n, option, reason)`;
  - `(*drv.Derivation).Out()`, or `engine.OutPath`;
  - `Fixup`/`FixupTools` structs mirroring `bspec.Fixup`, plus `NoFixup()`.
- In the generators, delete their local copies and call these.
- A shared `Finalize(name, system, kind, label string, args any, fx *Fixup, in *Inputs, r drv.Resolver, record func(p string, d *drv.Derivation))`
  replaces the 5 `job` bodies, so each planner keeps only its own `pl.own` bookkeeping.

The generated derivations must stay byte-identical. The existing goldens and plan tests check
that, and `pkg/rust/gen/plan_test.go` already decodes the spec into `bspec.Job`. Do it one
ecosystem per commit. The `cc` empty-path guard decides the shared semantics: check that no
generator relies on recording `""`.

### F3 (high): decision-log drift. Ids collide, and code cites decisions that are not in the log

`plan/decisions.md` is the owner's review surface (AGENTS.md §0). Code comments cite decision ids,
and some of those ids mean two different things:

- **A310**: the log row is the coordinator's "harness problems T1/T6/T12" (`decisions.md:222`).
  The range row "A310-A318 | I-B39" (no-op performance) reuses the same id. Code citing A310 means
  the validity memo: `pkg/engine/build/build.go:286`, `pkg/engine/store/store.go:58`,
  `pkg/engine/store/memo_test.go:10`.
- **A280**: claimed by both "A270-A280 | I-B35" and "A280-A287 | I-B36". Code means the I-B36
  manifest/status digest: `pkg/engine/build/status.go:26`, `pkg/engine/materialise/manifest.go:40`.
- **A300/A301**: claimed by both "A290-A301 | I-B37" and "A300-A309 | I-B38". Code means the I-B38
  manifest fsync: `pkg/engine/materialise/manifest.go:86`.
- **A170–A173** have two rows each, in two formats (`decisions.md:200-207`).
- The A270–A318 range rows only say "see plan/progress/…". Code cites A272, A291–A299, A311–A315
  and A371, whose text exists only in progress files.
- 21 unit-local ids cited in code are not in `decisions.md` at all:
  D-IB1-5, D-IB2-3, D-IB3-3, D-IB4-3, D-IB4-6, D-IB5-7, D-IB8-1, D-IB9-2, D-IB11-{1,2,3,5,7},
  D-IB12-1, D-IB13-1, D-IB18-3, D-IB20-1, D-IC1-5, D-UI-7 (`grep -rhoE 'D-I[A-Z0-9]+-[0-9]+' pkg internal`).

Refactor (plan only):

- Renumber the coordinator's A310 (e.g. A319) and fix the overlapping ranges
  (A270-A279 / A280-A287, A290-A299 / A300-A309).
- Delete the duplicate A170–A173 rows.
- Expand each range row into one line per id, or at least list the ids that code cites.
- Copy the D-IB*/D-IC*/D-UI* decisions cited in code into `decisions.md`.
- A small CI-free check script (the grep above, joined against the log) keeps this from coming back.

### F4 (medium-high): an undo write after a failed update can lose an admin, silently

`internal/auth` removes a principal from `AdminIndex` first, then writes the user, service account
or role binding. When that write fails, it adds the principal back with
`_ = id.adminAdd(ctx, principal)`, discarding the error. This happens at
`users.go:313,361,367`, `roles.go:496` and `sa.go:204,234`. If the undo itself fails (same storage
outage), the index loses an admin with no log line. `adminRemove` relies on the index to refuse
removing the last admin (`roles.go:58-61`).

Refactor: add `func (id *Identity) restoreAdmin(ctx, principal string)` that retries once with a
fresh context and logs `slog.Error("admin index not restored", …)` on failure. Call it from the
6 sites. Raise the restore case with the reviewer for a regression test (principle 1).

### F5 (medium): dead, divergent duplicate of staging cleanup

`internal/orchestrator/controllers_staging.go:56-160` has `MaxComplete`, `completeDoc`,
`StagedKeys`, `Staged` and `Staged.Delete`: its own COMPLETE.json parser (its own store-path regex
`:77`) and its own delete-by-known-key loop. Nothing calls `Staged.Delete` (deadcode). The live
code is `internal/promote/promote.go:112` `ParseComplete` and `:969` `cleanup`, with stricter
validation (job/build/project match, version, kinds). `TestStagedKeys`
(`controllers_staging_test.go:19`) only tests the dead copy.

Refactor: delete `:56-160` of `controllers_staging.go` (keep `StagingSweep`) and `TestStagedKeys`.
These in-package tests are not protected (`tests/protected.sha256` covers `tests/**`).

Related: `internal/cache/narinfo.go:24` and the deleted `:77` are the 2nd and 3rd copies of the
store-path regex. Use `cache.HashOf` (already used by `promote.cleanup`).

### F6 (medium): the COMPLETE.json wire type lives in the promoter, so the worker links server code

`internal/worker/jobio.go:381` builds a `promote.Complete`, so the worker imports
`internal/promote` (promotion, signing and claims code) for three struct types. Workers and
orchestrators share wire formats through `internal/build/jobspec`.

Refactor: move `Complete`, `Image` and `Asset` (`promote/promote.go:71-100`) and their limits
(`MaxComplete`, `MaxPaths`) to `internal/build/jobspec`. `promote` keeps `ParseComplete`.

### F7 (medium): repeated API error texts and two constructors for one code

- The text `"the resource was changed by someone else (etag mismatch); reload and retry"` is
  written out 13 times, and there are 6 more variants of "changed concurrently, try again/retry":
  `api/conventions.go:79`, `auth/{users,sa,roles,oidc}.go`, `notify/service.go:187`,
  `forge/integration/service.go:265,300`, `project/projects.go:71`, `release/service.go:351,406`,
  `overview/settings.go:289`.
  Refactor: add `api.Changed(what string) error` (one wording, used by `api.CheckETag`) and use it
  at the 19 sites. Move `auth.storageError` (`auth/identity.go:159`), which maps
  `store.ErrConflict`/context errors, to `internal/api` as `api.StorageError`, so every service
  maps store errors the same way. Check first whether `internal/api` may import `internal/store`.
  Today it imports only `gen` and `safe`, so the `store.ErrConflict` check may need to stay with
  the caller.
- `api.Forbidden` (`api/rbac.go:27`) and `api.PermissionDenied` (`api/errors.go:65`) both return
  CodePermissionDenied. Keep both, but move `Forbidden` next to `PermissionDenied` in `errors.go`.
  Its "Hooks for RBAC (M15.4)" heading is historical.

### F8 (medium): the engine has Go-specific branches outside the `engine.Ecosystem` interface

`pkg/engine/build` (E3) imports `pkg/golang/gen` directly. The places are:

- `dev.go:143` `gogen.BobImports`, `:261` `gogen.ServeImports`, `:275` `withGoEcosystem`;
- the GOPROXY rewrite at `dev.go:196-205`;
- `native.go:277-299` `RootGoMod`/`RootGoSum`/`RootModule`.

The layering rules allow it (E3 → E2), but the other 12 generators go through
`engine.Ecosystem`/`DevPreparer`. This is the only place where the engine knows about one language.

Refactor: add two optional interfaces in `pkg/engine`:

- `RootNativeFiles(g *ir.Graph, root string, frameworkSum string) ([]GeneratedFile, error)`
- `DevShellHook(ctx, root string, env map[string]string) error`

`golang/gen.Ecosystem` implements them, and `build` loops over the registered ecosystems. This is
behaviour-neutral, and it lets `withGoEcosystem` go.

### F9 (medium): the "generated by bob" marker literal is shared across packages by copy

- `pkg/engine/eval/scan.go:45` (`generatedGoMod`, used to *detect* bob's go.mod),
  `pkg/golang/gen/root.go:35` (`goModHeader`) and `pkg/golang/gen/gomod.go:77` (the same text as an
  inline literal) must agree. If one changes, eval stops recognising bob's own go.mod.
  Refactor: export `golang/gen.GoModHeader`, or put it in `pkg/engine` (E3 may import either), and
  use it at all three sites.
- `gomod.go:77-115` and `root.go:60-70` render go.mod twice: header, "Edit … and run bob lock",
  module/go lines, require block. Add a
  `renderGoMod(module, goLine, bobGo string, direct, indirect []gospec.Requirement, replaces []local) []byte`.
- The headers differ by ecosystem: Go and npm write `Code generated by bob from bob.go. DO NOT EDIT.`,
  Rust and OCaml write `# generated by bob from bob.go — do not edit` (`rust/gen/native.go:16`,
  `ocaml/gen/native.go:15`). Tools such as GitHub linguist and gopls-style checkers recognise only
  `Code generated … DO NOT EDIT.` This is goldens-protected (`Cargo.toml` goldens), so it is an
  owner/golden decision. Only record it.

### F10 (medium): stale comments and stubs left from earlier milestones

| site | problem | fix |
|---|---|---|
| `internal/worker/runner.go:52-60` `EvalRequest` | says "eval.Request has no pass-through for [Strict] yet". `eval.Request.Strict` exists (`pkg/engine/eval/eval.go:37`), and `EngineRunner` sets `build.Options{Untrusted: true, Strict: true}` (`jobrun.go:106`). `EvalRequest` is called only by `runner_test.go:13`. | delete `EvalRequest` and its test line |
| `internal/worker/runner.go:62-69` `NotBuilt` | "until job execution … lands with M16/M17": it has landed (`EngineRunner`). Used only by 2 tests. | move to `runner_test.go` as a test double |
| `internal/fleetsrv/fleet.go:24-26,75-78,179-188` `Config.Forge` + `noForge` | "nil until they exist (M17.1)". The forge minter exists and is assigned *after* construction (`cmd/bob-server/parts_forge.go:46`, `wc.F.Checkouts.Forge = minter`), so `Config.Forge` is never set in production. | either pass the minter through `Config.Forge` or delete the field; fix the comments. |
| `internal/http/server.go:51` | "/metrics joins with the metrics milestone". No such milestone exists in the plan. `store.Panics()` (`record.go:72`), which feeds the SPEC metric `bob_store_panics_total` (R-STORE-005), is unreachable. | note for the reviewer (SPEC gap); name the task id in the comment |
| `pkg/cli/cli.go:221` `notYet` | "a later milestone implements" `bob gc` (the only remaining user, `cli.go:76`) | name the task |
| `internal/artifacts/doc.go` | an empty package: R-STORE-106 and `plan/architecture.md:63` name it, but the code uses `store.Objects`, as `docs/dynamitedb-lake-usage.md` says | delete the package and update SPEC R-STORE-106 (`SPEC.md:6389`) and `architecture.md:63` to name `store.Objects`. SPEC edit is an owner item. |
| doc comment names another function | `pkg/npm/gen/input.go:75` (`depIndex` → `depImporter`), `pkg/output/appimage/bop/bop.go:85` (`bundleStore` → `inBundle`), `pkg/rust/gen/plan.go:190` (`toolchainOf` → `rustcOf`), `pkg/output/appimage/gen/runtime/main.go:208` (`cacheBase` → `cacheBases`, and it describes one directory "else …" while the function returns a list) | rename in the comment |
| `pkg/builder/bspec/bspec.go:5-15` | the `BuilderVersion` history is one ~1,500-character line | one line per version (needs the builder regeneration, so batch it with the next builder change) |

### F11 (medium-low): dead code (deadcode -test, after removing user-API false positives)

User-facing constructors called only from users' `bob.go` (for example `oci.NewImage`,
`cc.External`, `bob.Exe`) are reachable through the evaluator, so I left them out. These are
really unused:

- `pkg/engine/codegen.go:46-70`: `ConsumerOf` and its private `contains`, which is the 8th copy of
  `slices.Contains`.
- `pkg/cli/devenv/env.go:114-115`: `IsLSP`. The map is used directly at `:184`.
- `pkg/golang/resolve/proxy.go:88,117`: `Proxy.URL`, `Proxy.ZipURL`.
- `internal/logstore/feed.go:346-350`: `Follow` (one-line wrapper around `FollowPackage`). Also
  `internal/logstore/tables.go:73` `NewJobRun`.
- `internal/store/records.go:911,917`: `ReleaseKey`, `CheckoutShardKey`. Check them against the
  SPEC §05.4 key table before deleting. They may be documented key builders whose callers build
  the key inline. In that case, use them at those callers instead.
- `internal/registry/semver.go:107`: `Version.String`.
- Still dead since the M2–M4 review (M5 did not use them): `pkg/rust/resolve/resolve.go:54`
  `Resolution.Edges`, `pkg/npm/resolve/semver.go:644` `Range.IsAny`,
  `pkg/ocaml/resolve/filter.go:40` `EvalString`. Delete them; since that review, resolve is no
  longer in `framework.zip` (`pkg/engine/eval/framework.go:163-165`).
- `pkg/golang/gen/plan.go:170` `defError(_ string, reason string)`: all 7 callers pass a `from`
  argument that is ignored. Drop the parameter.
- `tools/lint/dbusage.todo` has been empty since I-B27, so the allowlist mechanism
  (`tools/lint/dbusage.go`, `todo_test.go`) is now unused. It is a protected area (D68), so this is
  an owner item, low priority.

### F12 (medium-low): oversized functions (≥ 140 lines, new since M4)

| function | lines | suggested split |
|---|---|---|
| `pkg/npm/resolve/resolve.go:392` `(*builder).output` | 205 | still open from M2–M4 (four phases, quadratic importer loop) |
| `pkg/golang/gen/test.go:25` `planTest` | 202 | testmain generation / compile / link / test-run job as 4 functions |
| `pkg/engine/build/dev.go:72` `dev` | 198 | eval+lock prologue (shared with `runner.run`), shell selection, ecosystem dev preparation (`:149-260`), Go import serving (see F8) |
| `pkg/engine/build/pipeline.go:25` `runner.run` | 177 | the eval/lock/index/generate/plan prologue is repeated with `dev` (`dev.go:85-176`); extract `prepare(ctx) (*prepared, error)` |
| `internal/build/controller.go:419` `Builds.terminalJob` | 174 | split by outcome (ok / failed / infra) |
| `internal/worker/attach.go:240` `Process.attachOnce` | 144 | hello/handshake vs receive loop |
| `pkg/engine/build/lock.go:371` `Lock` | 141 | — |
| `pkg/cc/gen/target.go:415` `planTarget` | 139 | per-file compile loop vs link |
| `pkg/cli/cmd_graph.go:165` `runGraph` | 136 | output modes (text / json / dot) as functions |

Also between 100 and 131 lines: 34 more. The full list is in `/var/tmp/I-S4/flen-{pkg,int}.txt`
(regenerate with the AST scan). The largest files are `pkg/rust/gen/plan.go` (1,446 lines) and
`pkg/golang/gen/plan.go` (1,320). F2 removes about 150 lines from each.

### F13 (low): small helpers copied across packages (each one is small; collapse them together with F2)

| helper | copies | home |
|---|---|---|
| `labelDir` (different behaviour for foreign labels: `@r//d:x` gives `"d"`, `"@r//d"` or `""`) | `cc/gen/plan.go:398`, `ocaml/gen/graph.go:97`, `golang/gen/load.go:53`, `rust/gen/workspace.go:31`, `npm/gen/input.go:25`; `engine.LabelDir` (`engine/codegen.go:74`) exists | use `engine.LabelDir` everywhere; the cc/ocaml variants return `"@r//d"` for foreign labels, so check that callers never see one |
| `sortedKeys[V]` | `engine/build/native.go:659`, `npm/gen/plan.go:669`, `golang/gen/plan.go:365`, `rust/gen/native.go:362`, `rust/resolve/features.go:164`, `ocaml/gen/plan.go:343`, `engine/store/store.go:554` | `slices.Sorted(maps.Keys(m))` (the M2–M4 pass already did this in other engine packages) |
| `sortedSet`/`sortedUnique` | `golang/golang.go:552`, `rust/rust.go:632` (L2 cannot import E0, so these copies are justified), `golang/gen/load.go:672`, `cc/gen/plan.go:322`, `golang/resolve/scan.go:210`, `rust/resolve/features.go:173` | E2 copies: `slices.Compact(slices.Sorted(...))` |
| `nonNil` | `ocaml/gen/plan.go:809`, `rust/gen/plan.go:1110`, `output/oci/gen/plan.go:384`, `engine/nixidx/nixidx.go:620` (+ L0–L3 copies, which are justified) | `engine.NonNil[T]` with F2 |
| `readCapped` | `cli/devenv/env.go:282`, `golang/resolve/proxy.go:249`, `rust/resolve/sparse.go:217`, `engine/nixidx/nixidx.go:579`, `engine/eval/hostile.go:46`, `internal/worker/jobio.go:437`, `npm/resolve/registry.go:208` | still open from M2–M4; a one-line `io.ReadAll(io.LimitReader(f, max+1))` plus a length check. No common low layer exists for pkg and internal, so at least unify inside `pkg/` |
| `copyFile` in builder ops (5 variants: O_TRUNC vs O_EXCL, mode 0644 vs from source vs a parameter, symlink check or not) | `golang/bop/bop.go:306`, `rust/bop/std.go:110`, `ocaml/bop/ml.go:216`, `output/appimage/bop/bop.go:248`, `cc/bop/bop.go:304` | `builder.Ctx.Copy` exists (`pkg/builder/paths.go:164`). Changing it needs a BuilderVersion bump and builder regeneration, so batch it with the next builder change |

### F14 (low): tests: assertion strength and resistance to mutations

The ecosystem plan tests are strong: they decode the job spec into `bspec.Job` and compare whole
structures. In `internal/`, about 30 negative tests only check `err == nil` with a fixed message
and never check *which* error came back. A mutation that refuses for an unrelated reason passes.
Examples:

- `internal/rbac/rbac_test.go:195` (unknown role) and `:201` (escalation): check
  `connect.CodeOf(err) == connect.CodePermissionDenied` (or the `denied()` helper already used at `:186`).
- `internal/cache/jobtoken_test.go:131`: "RevokeJobDownload deleted a user's token" only checks the
  error. Also `store.Get` the user token afterwards and require that it still exists.
- `internal/forge/gitlab/gitlab_test.go:174,190,238,284`, `internal/forge/integration/integration_test.go:180,206`,
  `internal/build/jobspec/jobspec_test.go:38,91`, `internal/worker/logs_test.go:58`: check the
  connect code, or `errors.Is` against the sentinel (after F1).

These are unit tests inside the packages, not protected paths. Strengthening them is allowed.

### F15 (low): dynamitedb usage. Doc and code disagree on returning fetched records

The binding usage note (`docs/dynamitedb-lake-usage.md`) says "Never pass a fetched record to
Put/Update/Create … Build a fresh value." SPEC R-STORE-103 rule 4 says otherwise: "fields left as
read are written through the record type's Draft". The code follows SPEC.
`internal/store/record.go:196-237` (`writeValue`) uses reflection on each field's dynamic type to
tell read values from update operators. About 90 CAS funcs mutate `cur` in place
(`cur.X = store.Set(...); return cur, nil`, for example `internal/auth/identity.go:323`). Only one
caller uses `Draft()` explicitly.

This is not a shim: `writeValue` always hands dynamitedb a fresh value. But it is the most
reflection-heavy code in bob (§8 "no reflection-heavy magic"), and new agents read the doc and the
code as contradicting each other. Refactor (doc only): add one paragraph to
`docs/dynamitedb-lake-usage.md` that names `store.CAS`'s contract (returning the fetched record
with fields replaced is allowed; `writeValue` re-drafts the rest) and points to R-STORE-103 rule 4.
No code change.

The other dynamitedb/lake rules hold:

- `dynamitedb.Update` is not used, and `Query` is used only in `sweep`.
- `recover` is around every call (`store.Call`, `logstore`'s `safe`).
- Retries are bounded (`casAttempts`, `docAttempts`).
- `objectKey` and `keyOnly` read only struct tags, never data.

### F16 (nit): staticcheck

- `internal/notify/notify.go:55`, `internal/promote/keys.go:82`, `pkg/nixpkgs/nixpkgs.go:59`:
  receivers with different names on the same type (ST1016).
- `internal/registry/handler.go:505`, `pkg/rust/resolve/sparse.go:135` (open since M2–M4): a
  redundant type in a declaration (ST1023).

---

## Suggested order

1. **F1** (registrar fix + sentinels) and **F4** (log the undo). These are small and protect principle 1.
2. **F3** (plan-only renumbering), so code citations become unambiguous before more land.
3. **F5**, **F10**, **F11** (deletions and comment fixes, behaviour-neutral).
4. **F2** + **F13**, one ecosystem per commit, with derivations kept byte-identical (goldens and plan tests).
5. **F6**, **F7**, **F8**, **F9**.
6. **F12** whenever the function is touched next. **F14** together with F1.

Owner items in this list:

- the SPEC R-STORE-106 wording (`internal/artifacts`, F10);
- the Rust/OCaml generated-file header (protected goldens, F9);
- the empty `dbusage.todo` mechanism (protected tools, F11).

## Not reviewed

- Generated code (`internal/gen`, `web/src/lib/gen`, `pkg/engine/store/builders.go`).
- Protected `tests/`, except where it shows how a finding can be tested.
- Behaviour against SPEC; that is the reviewer's job. I only flagged the R-STORE-005 metric gap
  in passing (F10).
- `pkg/npm/resolve`, `pkg/ocaml/resolve` and `pkg/rust/resolve` beyond what the M2–M4 review said.
