# R-M2M4: reviewer report for M2, M3, M4.1-M4.5

Role: reviewer (AGENTS.md §7), fresh context, read-only on code and tests. Date: 2026-10-22.
Scope: `pkg/engine/{drv,nar,fetch,store,srcs}` (M2), `pkg/bob`, `pkg/bob/ext`,
`pkg/engine/{ir,eval,lock}` (M3), `pkg/builder`, `cmd/bob-builder`, `pkg/engine/{translate,nixidx}`,
`pkg/nixpkgs`, `pkg/engine/build/testrun` (M4.1-M4.5). Checked against SPEC.md (ids binding),
AGENTS.md §5 and plan/decisions.md (A1-A86).

Method: read every non-test file in scope; `go vet` and `go test` of every package in scope
(GOCACHE=/var/tmp/gocache-rm2m4, CGO_ENABLED=0, TMPDIR=/var/tmp, host load avg 20-28 on 8 vCPU
from other agents); three findings were confirmed with throw-away tests injected through
`go test -overlay` (no file in the repository was written except this report). Findings marked
"by inspection" were not executed.

## Findings, ranked

### F1 (high, security). Archive symlinks can escape the tree through other symlinks of the same archive

- **Where:** `pkg/engine/fetch/archive.go:326-331`, `pkg/builder/unpack.go:277-282`; same
  lexical check in `pkg/engine/srcs/srcs.go:310-319` (repo symlinks).
- **SPEC:** R-NIX-014 ("symlinks kept only if they resolve inside the tree"), R-NIX-016 (builder
  `unpack` uses the fetcher's rules), R-FW-021 ("a symlink pointing outside the repo → error").
- **Failure scenario (verified):** the target is checked with `path.Join(path.Dir(rel), target)`,
  i.e. lexically. A tar with `d/` (dir), `d/up -> ..` (accepted: resolves to the root) and
  `x -> d/up/../outside` (lexically `d/outside`, accepted) unpacks without error, and
  `os.ReadFile(root/x)` returns the file *next to* the unpack root. Repeating `d/up/` segments
  climbs any number of levels while the lexical form stays inside, e.g.
  `d/up/d/up/d/up/../../../home/u/.ssh/id_rsa`. Overlay test: fetcher `unpack` → `err=<nil>`,
  read `x` → `"secret"` (file outside the root); builder `unpackKind` → `err=<nil>`, read `x` →
  the `outside/sentinel` of the builder test env. The imported store path then holds a symlink to
  `/home/...`; anything that follows it on the host (materialised vendor/node_modules trees,
  editors, `bob dev`) reads outside the store. `bob lock` (`Resolve`) accepts any archive, so the
  narHash pins whatever the server sent.
- **Fix:** resolve targets against the archive's own entry table: walk the target component by
  component from `path.Dir(rel)`, substitute every component that is a symlink of this archive
  (bounded depth, e.g. 40), and refuse when the walk leaves the root at any step (not only at the
  end). Simpler alternative: refuse a target containing `..` after any component that is a
  symlink in `kinds`. Apply the same to `srcs.Resolve` (resolve with `filepath.EvalSymlinks`
  confined to the repo). Add regression tests for the chain case in `fetch_test.go`,
  `builder_test.go` and `srcs/resolve_test.go`.

### F2 (high, security, by inspection). Evaluator generation writes and reads host paths taken from hostile go.mod lines

- **Where:** `pkg/engine/eval/deps.go:65-80` (require/replace accepted verbatim), `:150-172`,
  `:267` (`filepath.Join(modcache, escapePath(mp)+"@"+version)`), `:95` (`copyDir`),
  `pkg/engine/eval/gen.go:94-100` and `:121-128` (`filepath.Join(out, name)` with
  `name = mods/<mp>@<version>/...`).
- **SPEC:** AGENTS.md §5.4 (lock/registry/repo input is hostile), R-FW-043.3, R-FW-045 (the
  generation runs on the host, outside the evalbox), R-SEC-004.
- **Failure scenario:** a repository's go.mod has `require example.com/x v1.0.0/../../../../../../tmp/t`
  and `replace example.com/x => ./evil`; a bob.go imports `example.com/x`. `resolveDeps` loads
  `./evil`, the rule check passes (the import resolves to a dependency bob.go), and `generate`
  writes `go.mod` and every `<rel>/bob.go` of `./evil` to `.bob/eval/mods/example.com!x@v1.0.0/../../../../../../tmp/t/...`,
  i.e. outside the repository, as the bob/worker user, before `go build` ever parses the bad
  go.mod. A relative or absolute `replace` target (`=> /`) makes `loadDep` walk and read any
  host directory. On a worker evaluating an untrusted repository this is an out-of-sandbox write
  primitive (files named `go.mod`/`bob.go` in any writable directory, e.g. other checkouts) and a
  host read.
- **Fix:** validate before use: module paths with `golang.org/x/mod/module.CheckPath` (or
  `lock.checkGoPath`), versions with `semver.IsValid` (or `goSemverRe`), reject any `..`; on
  workers refuse `replace` targets outside the repository root; assert
  `filepath.Rel(out, p)` stays inside before every write in `generate`. Add hostile-go.mod cases
  to the eval tests.

### F3 (high, correctness). The same nixpkgs attribute used twice with a different kind or options is a definition error

- **Where:** `pkg/bob/node.go:147-164` (node id = label + projection; a second node with the same
  id but different kind/spec is "already defined" or "differs between two calls"),
  `pkg/nixpkgs/nixpkgs.go:21-51` (all three kinds use the label `nixpkgs:<attr>` and the platform
  axis only).
- **SPEC:** §1.7 / R-FW-032 (Tool = host set, Lib = target set), R-FW-047 (ids unique), R-NIX-002
  ("any attribute ... can be used through nixpkgs.Tool/Lib/Pkg").
- **Failure scenario (verified):** in a native build (`host == target`) `nixpkgs.Tool(p, "protobuf")`
  (protoc) and `nixpkgs.Lib(p, "protobuf")` (libprotobuf) get the same id
  `nixpkgs:protobuf@linux-arm64` → `package nixpkgs:openssl is already defined at ...` (overlay
  test, Tool+Lib of `openssl`). Two `Lib(p, "openssl", Modules("libcrypto"))` /
  `Modules("libssl")` from different packages fail the same way; so do differing `Outputs` or
  `AllowInsecure`. The same graph evaluates fine when cross-compiling (ids differ), so a repo
  breaks only on its native platform.
- **Fix:** make the set and options part of the identity (e.g. id token for set `host|target|cross`
  plus a hash of the nix object, as `f-<hash8>` does for features), or merge: one node per
  (attr, set, platform) whose outputs/modules/allow lists are the union of all requests. Add a
  `pkg/bob` test for both cases.

### F4 (medium-high, security). Fetch redirects are not checked: https → http downgrade on unpinned downloads

- **Where:** `pkg/engine/fetch/client.go:41-60` (`http.Client` without `CheckRedirect`),
  scheme check only on the request URLs at `:94-108`.
- **SPEC:** R-NIX-014 ("`https` (and `http` only with a pinned hash)").
- **Failure scenario (by inspection):** `Resolve` (bob lock, nixpkgs pin, `sources` entries) has
  no pinned hash, so only https URLs pass the pre-check, but Go's client follows a 30x to
  `http://...`; the plain-text body is hashed and its hash written to bob.lock. A network
  attacker who can make any https mirror redirect (or a compromised mirror) gets unauthenticated
  content pinned. Redirects to private addresses are still caught by `denyPrivate` (dial-time).
- **Fix:** `CheckRedirect` that refuses a scheme other than https unless the request is pinned,
  caps the chain (10) and reports 3002 with the redirect URL. Test with an httptest TLS server
  redirecting to a plain one.

### F5 (medium, never-crash). Whole NARs and export streams are held in memory, twice

- **Where:** `pkg/engine/fetch/fetch.go:318-323` (`nar.Write` into a `bytes.Buffer`),
  `pkg/engine/store/store.go:341-347` (`WriteExport` into a second buffer),
  `pkg/engine/srcs/srcs.go:61-74,123` (256 MiB kept + the export copy).
- **SPEC:** AGENTS.md §2.1 / §5.3 (never crash), R-NIX-014 (archives up to 8 GiB), R-FW-049
  (fetches in parallel ≤ 8), R-NFR-008 (low-power 2c/4G budgets).
- **Failure scenario:** the nixpkgs tarball (R-NIX-005, a few hundred MB unpacked) costs roughly
  2× its NAR in RAM during the first `bob lock`; an 8 GiB archive (allowed) needs ~16 GiB; eight
  parallel crate/tarball fetches multiply it. On a 4 GiB machine bob is OOM-killed instead of
  failing with an error. I-M2a raised this as a NEEDS item (streaming import); it is neither
  done nor recorded in plan/decisions.md.
- **Fix:** stream: `ExportItem` carries an `io.WriterTo`/reader that `nar.Write`s straight into
  the `nix-store --import` stdin pipe; for the SQLite-busy retry regenerate the stream from the
  tree (sources and fetched trees are still on disk). Until then, cap `Import` input size with a
  clear error.

### F6 (medium, hostile input). Translator keeps up to 16 MiB of log per derivation for the whole run

- **Where:** `pkg/engine/translate/translate.go:17-19` (2,000 head + 2,000 tail lines × 4 KiB per
  drv), `:245-256` (a `buildLog` per drv, never released).
- **SPEC:** AGENTS.md §5.4 (bounded), R-NIX-012 (only the excerpt/tail of a failed build is needed).
- **Failure scenario:** a build of 1,000 crates/packages that each log a few thousand lines keeps
  every log until `Finish`: hundreds of MB to GBs in the bob process, although logs of
  successful builds are never shown.
- **Fix:** drop a drv's log when its build activity stops and the output becomes valid (or keep
  only the tail of the most recent N), and add a global byte budget.

### F7 (medium, correctness). Builder output can spoof Nix errors for other packages; error events are emitted before the structural decision

- **Where:** `pkg/engine/translate/translate.go:459-466` (`add` emits `error` events live),
  `:784-798` (demotion only at `Finish`), `:899-902` (`t.failed[d]` wins over `valid[d]`).
- **SPEC:** R-NIX-017 ("@nix msg level 0 does not change the result of a successful build"),
  R-NIX-018, R-CLI-002 (`--json` events).
- **Failure scenario:** a test or `bob.Run` step prints
  `@nix {"action":"msg","level":0,"msg":"No space left on device"}`: with `--json` the stream
  carries an `error` event (7xxx) for a run that then succeeds. In a run that genuinely fails
  elsewhere, a printed `Cannot build '<drv of another package>'. Reason: builder failed with exit code 1`
  marks that other package `failed` although its output is valid.
- **Fix:** emit `error` events only from `Finish` (or emit them as provisional and retract);
  never mark a drv failed whose output is valid; prefer attribution from build activities over
  text in `msg` lines.

### F8 (medium, hostile input, by inspection). Repository scan reads hostile files without caps

- **Where:** `pkg/engine/eval/scan.go:102-118,129` and `pkg/engine/eval/deps.go:211,239`
  (`os.ReadFile` of every bob.go, go.mod, go.sum, dependency bob.go; walk depth/width unbounded).
- **SPEC:** AGENTS.md §5.4; R-FW-045 worker mode (these reads happen on the host before the
  sandbox).
- **Failure scenario:** a worker evaluating an untrusted repo with a multi-GB `bob.go` (or a sparse
  file) allocates it in the worker process.
- **Fix:** cap per-file size (e.g. 1 MiB for bob.go/go.mod, 16 MiB go.sum) and the number of
  bob.go files, report a definition error.

### F9 (medium, SPEC gap → owner). Eval key does not cover locally replaced dependency bob repos; rules run only on a key miss

- **Where:** `pkg/engine/eval/scan.go:138-151` (key = this repo's bob.go/go.mod/go.sum),
  `pkg/engine/eval/deps.go:155-159` (local `replace` directories), `eval.go:125-137`.
- **SPEC:** R-FW-043.1 (the key definition itself), R-FW-002 ("any violation stops evaluation").
- **Failure scenario:** with `replace example.com/lib => ../lib`, editing `../lib/x/bob.go` does
  not change the key, so the stale evaluator keeps running. Separately, an R2/R4/R6 violation
  introduced by editing a non-bob.go Go file is not reported until a bob.go changes (recorded
  only as D-M3d-6 in plan/progress/I-M3d.md).
- **Fix:** add the content hashes of every dependency bob.go and go.mod read from a local replace
  to the key (SPEC R-FW-043.1 amendment); decide whether D-M3d-6 is accepted and record it in
  plan/decisions.md.

### F10 (medium, SPEC deviation). `Step.File(p)` accepts any path

- **Where:** `pkg/bob/sources.go:291-308`.
- **SPEC:** §1.4.2 ("File returns one declared output as a Tree"), R-FW-023.
- **Failure scenario:** `step.File("../../etc")` or a name not in `Outputs` evaluates fine; the
  error (or a read outside `$out`) surfaces only in the consumer's build.
- **Fix:** misuse error when `p` is not one of the step's declared `Outputs` (clean, relative).

### F11 (low-medium). test-run has no timeout

- **Where:** `pkg/builder/testrun.go:95-99` (`cmd.Run()` without deadline; `bspec.TestRunArgs`
  has no timeout field).
- **SPEC:** §03.0.5 is silent; BOB-E-2007 exists for steps; `bob.Run` defaults to 30 min.
- **Failure scenario:** a hanging test blocks `bob test` (and a worker slot) forever unless Nix's
  own `timeout` is set.
- **Fix:** a `timeoutS` in `TestRunArgs` (default as `bob.Run`), mapped to 2007. Owner/SPEC item.

### F12 (low). `nixidx` treats "file exists" as "path valid"

- **Where:** `pkg/engine/nixidx/nixidx.go:241-255`.
- **SPEC:** R-NIX-027 ("re-queried ... when a cached drvPath is no longer valid").
- **Failure scenario:** a drv file left on disk but invalid in the DB (interrupted GC/import)
  keeps a stale index entry; `nix build` then fails with a store error instead of re-querying.
  The trade-off (warm budget 50 ms) is reasonable but is not in plan/decisions.md.
- **Fix:** record the decision, or fall back to one batched `Store.Invalid` when any stat hits.

### F13 (low). `environment.locate` misattributes dependency modules nested under the main module path

- **Where:** `pkg/bob/env.go:71-85` (main-module prefix is tested before the dependencies).
- **SPEC:** R-FW-016 (positions `<module>@<version>/<dir>/bob.go` for foreign repos), R-FW-015.
- **Failure scenario:** main module `example.com/mono`, dependency bob repo
  `example.com/mono/tools`: its files `example.com/mono/tools@v1.2.0/x/bob.go` match
  `example.com/mono/` first and become local dir `tools@v1.2.0/x`, an invalid label directory.
- **Fix:** check dependency modules (longest first) before the main module.

### F14 (low). Node ids without a projected platform are not canonical configured labels

- **Where:** `pkg/bob/node.go:397-404` (`//a:b@+debug` when a kind projects profile but not
  platform).
- **SPEC:** R-FW-047 (`nodes[].id` = canonical configured label), R-FW-050 ("platform always
  printed").
- **Fix:** either always print the platform of the node's P, or record in SPEC that ids print only
  projected axes.

### F15 (low). `drv` silently rewrites invalid UTF-8

- **Where:** `pkg/engine/drv/json.go:14-26` (`json.Marshal` replaces invalid UTF-8 with U+FFFD),
  so the UTF-8 check at `:96-99` never fires.
- **Failure scenario:** a non-UTF-8 file name in a job spec is turned into a different path
  silently; the builder then writes/reads a path that does not exist.
- **Fix:** validate strings before `normalise` (walk the value) or marshal with a custom encoder
  that errors.

### F16 (low). DenyPrivate misses some non-public ranges

- **Where:** `pkg/engine/fetch/client.go:66-77`.
- **Failure scenario:** 100.64.0.0/10 (CGNAT, also cloud/Tailscale internal), 0.0.0.0/8,
  198.18.0.0/15 and NAT64 `64:ff9b::/96` mapped private v4 are reachable from workers.
- **Fix:** add them (check §05's worker policy for the authoritative list).

### F17 (low, process). Deviations recorded only in plan/progress, not plan/decisions.md

AGENTS.md §0 asks agents to record self-answered decisions in plan/decisions.md. These change or
extend SPEC behaviour and are only in progress files:
D-M2b-3 (`<home>/nsroot` not in R-NIX-024's layout), D-M2b-7 (`.gitignore` excluded from sources),
D-M3d-4 (evaluator at `.bob/eval/bin/bob-eval`, SPEC `.bob/eval/bob-eval`), D-M3d-6 (rules only on
key miss, see F9), D-M3d-9 (Go on PATH when no `GoRoot`; R-FW-043.5 says "with the pinned Go"),
D-M3d-12 (framework-too-old uses 1021), D-M4a-6 (builder `unpack` refuses xz/zstd), F12, and the
`syscall.Exit` uses in `pkg/engine/store/ns.go:69,86,110` (A70 covers only evalbox). Also note
the `noexit` linter (`tools/lint/noexit.go`) does not flag `syscall.Exit`.

### F18 (low, missing CI check). R-NIX-015 "BuilderVersion bump" check does not exist

- **Where:** `tools/ci/stage_gen.go:24-44` regenerates only budgets/parity. The stale-table half
  is covered by `tests/nix.TestRNIX015EmbeddedTableMatchesBuilder` (passes; I also rebuilt both
  builders and they match the embedded gz byte for byte). The "source changed without a
  `BuilderVersion` bump" check is missing (raised as NEEDS by I-M4a). Impact is cosmetic (the
  store path is content-addressed), so low; it is still a SPEC acceptance item.

### F19 (high, integration; fix in flight). Evaluation inside bob's own namespace failed with 9005

- **Where:** `pkg/engine/eval/evalbox/evalbox.go:188-192` and the `os.Chown` calls at `:221,241,256`
  as they were when this review started (`os.Getuid() == 0` taken as "host root").
- **SPEC:** R-FW-045, R-NIX-021 (every bob command runs in a user namespace that maps the user to
  uid 0), A70.
- **Failure scenario (observed):** `tests/nix TestRNIX019PolyglotClosure` and
  `TestRNIX019FileMacroDoesNotReferenceSources` ran `bob build`, which entered bob's namespace
  (uid 0 mapped to one host uid), then evalbox chowned the seed to 65534 → `chown ...: invalid
  argument` → BOB-E-9005 "building the evaluator's seed cache failed; this is a bob bug". The
  same check would also map the sandbox to the unmapped uid 65534 for non-root users inside the
  namespace. While this review ran, a `hostRoot()` helper (reads `/proc/self/uid_map`) appeared
  in the working tree (evalbox.go:520-534, mtime 16:03) and the chown sites now use it; I did not
  re-run the tests against it. **Action:** re-run both tests after that change lands and add a
  unit test that runs `evalbox.Run` from inside `store.Enter`.

## Missing tests (beyond the regression tests named above)

- Fuzz targets for hostile parsers that have none: `drv.Parse`, fetcher `unpack` / builder
  `unpack` (tar and zip), `nar.Write` filters, `lock.Parse` top level (only sections are fuzzed),
  `translate` line parser (`FuzzIRParse`/`FuzzLockSection` exist).
- Redirect handling and DenyPrivate edge ranges in `pkg/engine/fetch`.
- Hostile go.mod/import paths in `pkg/engine/eval` (F2) and file-size caps (F8).
- nixpkgs Tool/Lib/options collision (F3); `Step.File` misuse (F10).
- Translator memory bound and spoofed `@nix` lines in a mixed success/failure run (F6, F7).

## Test runs (this review)

- `go vet` on all packages in scope: clean.
- `go test` of all packages in scope: all pass except `pkg/engine/srcs` `TestImportManyTiming`
  (10,000 dirs cold import 20.2 s vs budget 5 s, warm 2.0 s) measured at load average ~28 on the
  8 vCPU host with other agents running: not a valid budget measurement; rerun on a quiet host
  (B-076 / R-NIX-009).
- Package-level acceptance tests (`tests/nix`): R-NIX-006 golden/escape/names/fixed-path/env,
  R-NIX-015 (both), R-NIX-016 (all five), R-NIX-017 logs, R-NIX-019 strip/rpath/reference pass;
  `TestRNIX019PolyglotClosure` and `TestRNIX019FileMacroDoesNotReferenceSources` fail with
  BOB-E-9005 (F19) through the in-progress M4.6 `bob build`; `TestRNIX006RecomputeHello` was still
  running when the 850 s test timeout hit (nixpkgs download + index on a loaded host).
  `tests/core`: `TestRFW002BobGoRules` fails (rule texts missing from stderr of the in-progress
  CLI), `TestRFW043EvaluatorIRIndependentOfPath`/`HidesOtherGoFiles` fail because the acme and
  polyglot fixtures use L2 APIs that do not exist yet (`golang.Module`, `cc.Linkable`, `rust.CLib`).
  Other agents were editing files in scope while this review ran (fetch, srcs, eval, evalbox);
  line numbers are those of 2026-10-22 ~16:05.

## Owner-relevant items

1. **F19**: the first CLI-driven builds failed in the evaluator sandbox inside bob's namespace;
   a fix is in the working tree but unverified.
2. **F2 and F1 are security issues on workers** (out-of-sandbox writes from a hostile go.mod;
   symlinks out of fetched trees). They should be fixed before M13 workers evaluate or fetch for
   untrusted repositories.
3. **F3** breaks a common pattern (`nixpkgs.Tool` + `nixpkgs.Lib` of one attribute, e.g.
   protobuf/openssl) on the native platform only; the fix touches node identity (IR ids), so it is
   cheaper before goldens for acme/polyglot are recorded.
4. **SPEC R-FW-043.1** (eval key) should include dependency bob repos taken from local `replace`
   directories (F9); decide whether rule checks may be skipped on a key hit (D-M3d-6).
5. **Test timeout** for test-run units is not specified (F11).
6. **Memory model of imports** (F5): accept a streaming import as a store API change, or a
   size cap with an error; the current code can be OOM-killed on the R-NFR-008 machine class.
7. Several SPEC deviations live only in plan/progress files (F17); they need a line each in
   plan/decisions.md so the owner can see and overturn them.
