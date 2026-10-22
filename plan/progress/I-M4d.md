# I-M4d progress (M4.6 engine half: pkg/engine/build Run)

Owner of: pkg/engine/build (not pkg/engine/build/testrun). Consumer: I-M4e (pkg/cli, cmd/bob).

## API (stable; I-M4e codes against this)

```go
package build // github.com/megakuul/bob/pkg/engine/build

// Command selects what Run does after realising.
type Command string
const (
	CmdBuild Command = "build" // realise the requested packages
	CmdTest  Command = "test"  // realise the test units (testrun kind) below the requested packages
)

// Options is one run of the pipeline (R-FW-049). Zero values are the defaults.
type Options struct {
	Command   Command  // "" = CmdBuild
	Root      string   // repository root (directory with bob.lock); the CLI resolves -C / FindRoot
	Patterns  []string // label patterns; empty = "//..." (the CLI turns "below cwd" into a pattern)
	Platform  string   // --platform ("" = host); appended to every pattern as "@<p>[+v…]"
	Variants  []string // --variant
	Store     *store.Store // opened by the CLI after store.Enter; nil: Run opens one with Home
	Home      string       // state home when Store is nil ("" = default)
	GoRoot    string       // pinned Go for the evaluator ("" = go on PATH)
	Lock      bool         // --lock: update stale lock sections (ecosystem Lock + nixpkgs pin)
	                       // instead of failing with BOB-E-1012
	Offline   bool         // --offline: no substituters, no downloads
	KeepGoing *bool        // nil = true (--keep-going=true default)
	MaxJobs   int          // -j (0 = number of CPUs)
	Limits    translate.Limits // --timeout / --max-silent-time; LocalBuilds forced true
	Rerun     bool         // bob test --rerun
	TestArgs  []string     // bob test -- args (become part of the test unit; changes its hash)
	PrintPaths bool        // fill Package.HostPaths and package.finish Paths
	Replay    string       // BOB_TEST_NIX_REPLAY dir: translate the capture, skip eval..nix
	Events    func(Event)  // live sink; called from one goroutine at a time, in order; may be nil
}

// Event is one typed event of a run. Event names: the §02.4 names (package.start,
// package.wait, upstream.start, log, error, warning, test.result, package.finish, run.start,
// run.finish) plus the R-FW-049 stage events and drv.imported.
// The CLI adds the envelope (v, time, command, args, bob/nix versions) and decides what to print.
type Event struct {
	Event      string      // name (see Stage* constants, "drv.imported", or a §02.4 name)
	Label      string      // package.*, log, test.result
	Action     string      // package.start: build | download | test
	Cache      string      // package.start: substituter name, "" for builds
	Name       string      // upstream.start
	Reason     string      // package.wait
	Line       string      // log (store paths rewritten)
	Status     string      // package.finish: built|cached|failed|skipped|cancelled; test.result: pass|fail|cached
	Error      *diag.Error // error, warning
	Hint       string      // error, warning: the hint to print (run-specific, A74)
	Requested  []string    // run.start: labels after pattern expansion
	Nix        string      // run.start: embedded Nix version
	Paths      []string    // package.finish with PrintPaths: host paths of the outputs
	Failed     []string    // test.result: failing test names
	DurationMs int64       // stage events, test.result, run.finish
	Count      int         // drv.imported
	Cached     bool        // eval stage: the evaluator key matched
	ExitCode   int         // run.finish
	Counts     translate.Counts // run.finish
	Drift      []string    // run.finish
}

// Stage event names, in the order Run emits them (tests/core buildStages). Run emits all but
// StageNamespace (the CLI emits it from store.Entered()).
const (
	StageNamespace = "namespace"; StageScan = "scan"; StageEval = "eval"; StageIR = "ir"
	StageLock = "lock"; StageIndex = "index"; StageSources = "sources"; StagePlan = "plan"
	StageFetch = "fetch"; StageDrv = "drv"; StageNix = "nix"; StageValidity = "validity"
	StageTranslate = "translate"
)

// Result is a finished run. It is never nil when Run returns.
type Result struct {
	Report    *translate.Report // never nil: errors before Nix are in Report.Errors too;
	                            // render with translate.Human(w, r.Report, style)
	Requested []string          // labels after pattern expansion (also in run.start)
	Packages  []Package         // requested packages, sorted by label
	Tests     []TestResult      // CmdTest only, sorted by label
	ExitCode  int               // = Report.ExitCode (diag.ExitCode priority), 1 if a test failed
}

type Package struct {
	Label     string
	Status    string            // built | cached | failed | skipped | cancelled
	Outputs   map[string]string // output name → store path (/nix/store/…)
	HostPaths []string          // PrintPaths: <home>/nix/store/… (readable outside the namespace)
}

type TestResult struct {
	Label      string
	Status     string   // pass | fail | cached
	DurationMs int64
	Failed     []string
}

// Run runs the pipeline: scan+eval → ir.Parse → lock check (or update with Lock) → nixpkgs
// pre-checks → source import → Plan per ecosystem (engine registry) → fetch → drv import →
// realise (Nix through Store.Command, allowSubstitutes=false on bob drvs, A71) → validity →
// translate. Events go to o.Events as they happen; Run itself emits run.start (after ir) and
// run.finish (last). Run returns an error only when ctx is cancelled before anything ran or
// Options are invalid (usage, exit 2); every build-related failure is in Result.Report.
func Run(ctx context.Context, o Options) (*Result, error)

// Evaluate runs scan+eval+ir only (bob graph, bob check): the validated graph and its
// requested labels. Errors are *diag.Error or ir.Errors (exit via diag.ExitCode).
func Evaluate(ctx context.Context, o Options) (*ir.Graph, error)

// Lock runs bob lock: evaluate, pin nixpkgs, ecosystem Lock for stale sections (or all named in
// Update), write bob.lock only on change. Changes is the result.data of `bob lock --json`.
type LockOptions struct {
	Update  []string // --update [names…]; Update != nil && len 0 = update all
	Nixpkgs string   // --nixpkgs <rev>
	Rebuild bool     // --rebuild
}
type LockChange struct{ Ecosystem, Name, From, To, Op string } // op add|update|remove
type LockResult struct {
	Changes     []LockChange
	Written     bool // bob.lock bytes changed
	Reformatted bool // only formatting changed ("note: bob.lock was reformatted")
}
func Lock(ctx context.Context, o Options, lo LockOptions) (*LockResult, error)

// SourcePaths (unchanged, R-FW-021): node id → imported source store path.
func SourcePaths(ctx context.Context, r Request) (map[string]string, error)
```

Notes for I-M4e:
- Exit status of `bob build/test` = `Result.ExitCode`; the CLI emits `run.finish` from Run's
  event (Run sends it). Human output: `translate.Human(stdout, res.Report, translate.StyleFor(...))`.
- Stage events and `drv.imported` are not in schema/events-v1.json's enum (see TEST PROBLEM).
- `run.start` comes after the scan/eval/ir stage events (requested labels need the IR); if the
  CLI must print run.start first it buffers earlier events until run.start arrives.

API addenda (compatible): `Options.JSONLog bool` (--json-log: forward `log` events);
`Options.Caches []store.Substituter` (bob's own caches for the A71 batch check);
`LockOptions{Update []string; All bool; Nixpkgs string; Rebuild bool}` (`All` = `--update`
without names); `LockChange` has JSON tags. `Run` returns `(res, err)`: `err` only for
non-diagnostic failures (e.g. `*evalbox.Error`, store damage); `res` is never nil and
`res.Report` never nil. `Request` (SourcePaths) gained `Home`.

## done (M4.6 engine half, 15:45)
- pkg/engine/build: build.go (Options/Event/Result/Run, stage events, run.start/run.finish,
  exit code), pipeline.go (R-FW-049 stages, replay, index pre-checks, fetch ≤ 8 parallel, drv
  import + drv.imported + drv cache entries, test-unit selection, per-package results),
  plan.go (dependency-wave planner over the engine registry; core kinds bob.run → bob-builder
  "run" job, bob.file, bob.fetch (lock-pinned FOD path + fetch.Request), nixpkgs.* (index
  entries), bob.devshell (nothing); resolver for nixpkgs drv modulo (index cache, else parse
  from the store, recorded back with RecordModulo); Nix hash placeholder), lock.go (lock stage,
  Lock for bob lock), realise.go (A71 batch narinfo check + bob substitution, nix build
  --log-format internal-json -L --keep-going via Store.Command with live translation, validity
  → Outcome.Valid, translator Context incl. helper drvs/Users/Nixpkgs/Sources, test results),
  sources.go (SourcePaths, Evaluate, source import), gotool.go (evaluator Go: pinned nixpkgs go
  when go is not on PATH).
- Tests (pkg/engine/build/build_test.go, test ecosystem "demo" registered from the test):
  TestRunEcosystemEndToEnd (stage order, both nodes built, outputs, second run cached and
  drv.imported 0), TestSourcePaths; with BOB_TEST_NIXPKGS=1: TestRunStepsWithNixpkgsTools
  (nixpkgs pinned on demand, bash/coreutils tools, /build/src, ${VAR}, BOB_IN_*, failing step →
  exit 1 with label), TestRunTests (pass → cached → --rerun pass; failing unit exit 1 / fail;
  bob build skips tests), TestRunChainOf100Steps, TestRunPinnedGo (skips on the Go mismatch
  below). All green; testrun green.
- Measured [8c/15G aarch64, shared host load ~14]: cold chain of 100 bob.Run steps through Run
  3.06 s (nix 2.36 s, drv import 0.47 s, plan 0.12 s), budget 12 s.
- gofmt clean, GOWORK=off go vet clean, `tools/ci -only lint,layering` ok (fmt/vet fail only in
  internal/* and out/uicheck, other units).

## still red (not in my package)
- Every CLI-driven tests/core and tests/nix test in this harness (PATH=/usr/bin:/bin, no go):
  the evaluator now gets nixpkgs' go (1.26.8 at the nixos-26.05 pin) and eval refuses with
  BOB-E-1021 "bob needs Go 1.26.9 or newer" (framework go directive = root go.mod `go 1.26.9`).
  See NEEDS 1. Verified: TestRFW051/TestRFW023* reach exactly that error through the CLI.
- tests/core acme/polyglot-based tests: L2 APIs missing (golang/cc/rust).
- TestRFW051ToyEcosystem: TEST PROBLEM 1.
- tests on t.TempDir homes under /tmp: the tmpfs is ~92-96 % full (other units); nixpkgs import
  fails there with "nix-store --import failed". Run with TMPDIR=/var/tmp/... .

## DECISIONS
- D-M4d-1 (R-FW-005 vs tests/core steps/chain repos): a *missing* nixpkgs pin or bob.Fetch
  entry is pinned on demand by bob build/test and written to bob.lock (adds only, never moves);
  a pin that no longer matches its declaration fails with 1012 unless --lock. The protected
  steps/chain/probe repos build with `{"lockVersion":1}` and no --lock.
- D-M4d-2: core kinds (bob.run, bob.file, bob.fetch, nixpkgs.*, bob.devshell) are planned by
  the engine itself; every other kind by engine.Lookup(<prefix>). Planning goes in dependency
  waves (one Plan call per ecosystem per wave), so PlanRequest.Inputs always holds a node's deps.
- D-M4d-3: bob.Run job = mkdir $out; copy the "<dir>-<name>-src" tree (repo-relative paths) to
  /build/src, else mkdir /build/src; exec argv in /build/src with PATH = tools' bin (output
  "bin" else "out"), env = step env + BOB_SRC/BOB_OUT/BOB_IN_<NAME>; ${VAR} in Cmd expanded at
  plan time from that env (+ HOME/TMPDIR/SOURCE_DATE_EPOCH/TZ/LC_ALL), unknown → "".
  Store name = drv.Name(dir, name, <projection tag after "@">). bob.File = "<step out>/<path>".
- D-M4d-4 (A71): every core job carries allowSubstitutes = false (a constant attribute, so
  cached outputs keep their paths). bob's own caches (Options.Caches) get one narinfo HEAD per
  missing bob output before Nix; hits are realised with nix-store --realise. With no bob cache
  (R-FW-009) nothing is asked. nixpkgs paths still substitute normally.
- D-M4d-5: the nixpkgs index is opened only when the graph has nixpkgs nodes, or has ecosystem
  nodes and bob.lock already has a pin (no nixpkgs download for graphs that do not use it).
- D-M4d-6: evaluator Go = Options.GoRoot, else go on PATH, else nixpkgs' go at bob.lock's pin
  (or the default channel's revision before a pin exists; the lock stage then records that pin
  without downloading again). The IR does not depend on the Go version; the eval key does.
- D-M4d-7: bob build does not run test units (requested test packages are left out of the
  installables, the translator's requested list and Result.Packages); bob test realises only
  the requested units, skips cached ones (status cached, read from the stored result).
- D-M4d-8: the "scan" stage event is emitted with durationMs 0: eval.Evaluate scans inside one
  call, so the scan time is part of "eval" (NEEDS 3).
- D-M4d-9: package.finish events are emitted by Run from Report.Packages (+ requested packages
  Nix had nothing to do for, as cached), with Paths when PrintPaths. run.start is emitted after
  the ir stage (requested labels need the IR); on an earlier failure just before the errors.
- D-M4d-10: errors that are not diagnostics are returned by Run (Result still filled, exit 70
  unless the caller maps them, e.g. *evalbox.Error.Exit); diagnostics go to Report.Errors and
  "error" events.

## NEEDS
1. **Blocking every harness CLI test** (coordinator / I-M3d eval): the framework's go directive
   is the root go.mod's `go 1.26.9`, newer than nixpkgs' go 1.26.8 at the nixos-26.05 pin, so the
   pinned Go is refused (1021). Fix: root go.mod `go 1.26.8` (or `go 1.26`), or framework.go
   writes the language version (major.minor) into the framework's minimal go.mod.
2. pkg/engine (I-M3a): `PlanRequest.Resolver drv.Resolver` — ecosystems need hashDerivationModulo
   of their input drvs (nixpkgs drvs, other ecosystems' drvs) to Finalize; the engine has one
   (build.resolver) and would fill it. Today an ecosystem can only finalise against its own drvs
   (the test ecosystem builds its own store resolver). Also `PlanRequest.TestArgs []string`
   (bob test -- args become part of the unit, R-FW-024/§02.6); Options.TestArgs is accepted but
   not passed on.
3. eval (I-M3d): a scan hook or `Result.ScanDuration` so the "scan" stage event has its real
   duration.
4. builder (I-M4a) + translator (I-M4b): R-FW-023 "<label>: step finished but did not create
   <path>" needs the builder to check bob.Run's declared Outputs (files under $out; e.g. an
   `outputs` list on exec or a check-files op, exit 4 with the path) and the translator to render
   the bob.Run text. The planner passes nothing for them today. Step Timeout (timeoutSeconds) is
   not enforced per derivation yet (Nix `timeout` attribute or builder-side).
5. CLI (I-M4e): link pkg/nixpkgs into cmd/bob (blank import), otherwise ir.Parse rejects
   nixpkgs.tool/lib/pkg; pass Options.Caches from the logged-in caches; Event names "scan",
   "eval", … and "drv.imported" are emitted by Run (see TEST PROBLEM 2).
6. store (I-M2b): `store.NixVersion` constant (build hardcodes "2.34.9" for run.start).
7. translator (I-M4b NEEDS 4 part 2): Context.Chains for failing nixpkgs drvs is not computed.

## TEST PROBLEM
1. tests/core/toyeco/toyeco.go (protected) cannot plan: it calls `d.Path()` right after
   `job.Derivation()` without `d.Finalize(...)`, and drv.Path refuses unfinalised derivations
   ("derivation … is not finalised (output out has no path)"); it also writes to
   "$out/result.txt" (bob-builder requires absolute paths; outputs are named by the Nix hash
   placeholder, D-M4a-3), does not list req.Builder in InputSrcs (D-I-M2a-2) and hardcodes
   System "aarch64-linux". TestRFW051ToyEcosystem cannot pass with any engine. Fix: Finalize with
   a resolver over its own drvs (and PlanRequest.Resolver once it exists), placeholder-based
   output path, InputSrcs [req.Builder] — as in pkg/engine/build/build_test.go's "demo".
2. Stage events vs schema: tests/core (buildStages, R-FW-049) and tests/nix (namespace, index,
   drv.imported) require --json events named "namespace", "scan", …, "drv.imported", while the
   protected schema/events-v1.json enumerates only the §02.4 events ("A line with an unknown
   event name is invalid") and tests/cli checkStream validates every line against it. Both
   cannot hold for `bob build --json`; the schema's event enum needs the stage names (owner).

## Coordinator rulings (2026-10-22, after commit) and follow-up work
Rulings: (1) eval/framework.go writes only the language version (`go 1.26`) in the framework
go.mod, root go.mod stays; (2) do NEEDS 2, 3, 4, 6, 7; (3) stage events are SPEC: add them to
schema/events-v1.json; (4) fix tests/core/toyeco/toyeco.go to the demo shape (owner-approved
evidence-based test correction). R1 (steward review): frameworkFiles skips the lock-time
resolver packages (`resolve` dirs); test that the zip has none.

Done:
- (1) framework.go: framework go.mod `go <major.minor>` (langVersion); framework.zip regenerated.
  TestRunPinnedGo now passes (nixpkgs go 1.26.8 compiles the evaluator); the lock stage records
  the transient nixpkgs pin used for the evaluator's Go.
- R1: frameworkFiles skips `resolve` directories; TestFrameworkZip asserts neither the tree view
  nor the zip holds a resolve package; zip regenerated; eval tests green.
- NEEDS 2: engine.PlanRequest gains `Resolver drv.Resolver` (the engine's resolver: earlier
  waves' bob drvs + nixpkgs drvs) and `TestArgs []string` (bob test -- args, CmdTest only).
- NEEDS 3: eval.Result.Scan (walk + hashes + key); the "scan" stage event carries it and "eval"
  the rest.
- NEEDS 4: bob-builder op `check-files {root, paths}` (missing → exit 4 + diag
  "<label>: step finished but did not create <path>") and exec `timeoutSeconds` (kill, diag
  "<label> timed out after Ns"); translator: the last builder error diagnostic becomes the cause
  of 2000 (multi-line: rest → excerpt), the timeout diag → 2007. bob.Run plans both. Embedded
  builder regenerated (`go generate ./pkg/engine/store`; BuilderVersion stays 1, D-M4c-7).
  Tests: pkg/builder TestCheckFiles/TestExecTimeout, translate TestBuilderDiagnostics, build
  TestRunStepsWithNixpkgsTools (missing output, 1 s timeout → 2007).
- NEEDS 6: store.NixVersion (+ TestNixVersion against `nix --version`).
- NEEDS 7: translate.Context.ChainOf (optional func, json:"-"), consulted by upstreamFailed and
  recorded in Chains; build fills it with a breadth-first walk of the store's .drv files from
  the direct nixpkgs inputs (bounded 20000 drvs). Tests: build TestChainOf, translate TestChainOf.
- drv.Placeholder(output) (I-M4a NEEDS), verified against `builtins.placeholder "out"`; build
  uses it instead of its own copy.
- (3) schema/events-v1.json: event enum + `ev.stage` (namespace … translate: durationMs,
  optional cached) and `ev.drv.imported` (count); additive diff only.
- (4) tests/core/toyeco/toyeco.go: Finalize against own drvs + req.Resolver, output path via
  drv.Placeholder, builder in InputSrcs, store.HostSystem(). The edit was not blocked.
- Found while running the CLI: evaluation inside bob's namespace (every CLI command after
  store.Enter) failed with "chown …/eval-seed/…/gocache: invalid argument": evalbox's root path
  (D-M3b-3) chowns to 65534, which is unmapped in bob's single-uid namespace. evalbox now takes
  the root path only for root of the initial user namespace (`hostRoot()`: uid 0 and uid_map
  "0 0 4294967295"); namespace-root takes the unprivileged path (D-M4d-11). Security note for
  the owner: a bob run by host root then evaluates as the namespace's uid 0 (= host root), not
  as nobody; for non-root users nothing changes.
- TestRFW051ToyEcosystem through the CLI now builds both nodes ("built", result.txt right); it
  fails only on TEST PROBLEM 3.

DECISIONS (new):
- D-M4d-11: evalbox privileged path only for initial-namespace root (above).
- D-M4d-12: the nixpkgs pin resolved for the evaluator's Go (no go on PATH, no pin yet) is
  recorded by the lock stage even when the graph has no nixpkgs nodes (reproducible toolchain).

TEST PROBLEM (new):
3. tests/core/ir_test.go TestRFW051ToyEcosystem runs noLeak over the whole output of
   `bob build … --print-paths --json`; package.finish `paths[]` must hold host store paths
   (<home>/nix/store/…, SPEC §02.6), which leakcheck reports as "store path". tests/cli
   checkStream deletes `paths` before its leak check for this reason. Fix: the same exclusion in
   TestRFW051 (or drop --print-paths from noLeak there).
4. tests/cli/events_test.go knownEvents() (protected) lists only the §02.4 events, so its
   validateEvent reports the R-FW-049 stage events and drv.imported as "unknown event" although
   schema/events-v1.json now has them. Fix: add the stage names + drv.imported (fields
   durationMs "n" / count "n").
- Observed under host load ~33: pkg/engine/build/testrun TestPostgresServiceInNix timing
  (4.8 s vs ≤ 3 s); unrelated to these changes (load), passes on a quiet host per I-M4c.
- tests/core via the CLI (TMPDIR=/var/tmp, host load ~33): TestRFW021SourceErrors now exits 3
  as required for all three cases, but the srcs message is printed on stdout, not stderr as the
  test expects (pkg/cli output stream: I-M4e). Each fresh-HOME CLI run rebuilds the evaluator
  seed (~1-2 min under this load), so whole tests/core runs need -timeout well above 10m.
- Last CLI attempt at R-FW-023 (16:20): the harness could not build cmd/bob-server
  (internal/store redeclarations, another unit's work in progress). R-FW-023 missing output and
  step timeout are verified at engine level (TestRunStepsWithNixpkgsTools).
- Checks: gofmt clean; go vet, staticcheck, boblint clean for pkg/engine/..., pkg/builder/...,
  tests/core/toyeco; TestLayering ok; pkg tests green (build, drv, builder, eval, evalbox,
  translate, engine, store TestNixVersion); tests/sandbox green. `tools/ci -only …` fails only
  on internal/store (other unit).
