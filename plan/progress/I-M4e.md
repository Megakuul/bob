# I-M4e progress (M4.6 CLI half: pkg/cli, cmd/bob)

Owner of: pkg/cli (not pkg/cli/devenv), cmd/bob. Engine half: I-M4d (pkg/engine/build).

## State
- 14:40 started; I-M4d API read (plan/progress/I-M4d.md "API"); build.Run not in the tree yet.
- 15:30 written (compiles against a stub of the I-M4d API via `go build -overlay`, unit tests
  green): args.go (own parser, global flags anywhere, 0001/0004), output.go (--json envelope,
  run.start first, error objects, run.finish, 7005 refusal layout), cli.go (table, dispatch,
  SIGINT → 8001/130), env.go (root, patterns, TTY, server config), cmd_build.go (build/test via
  build.Run, event sink buffering until run.start, stage events, human build/test output,
  replay hook, bob run), cmd_graph.go, cmd_check.go, cmd_lock.go, cmd_info.go (version,
  doctor incl. --print-nix-env, log), complete.go (__complete, scripts), link.go (pkg/nixpkgs).
- Replay check: all 56 recorded fault goldens (tests/faults/*/2.34.9/golden.txt) match the
  output of a `-tags bobtest` bob byte for byte.
- 17:00 against the real build package (I-M4d): pkg/cli unit tests green; gofmt clean;
  go vet + GOWORK=off go vet clean for pkg/cli, cmd/bob; staticcheck, boblint and
  importboundary clean on pkg/cli, cmd/bob (the repo-wide `tools/ci -only fmt,vet,lint,layering`
  fails only on other units' WIP: pkg/npm/resolve does not compile, pkg/ocaml/resolve unused).
- Green: tests/cli TestRCLI001CommandSet, TestRCLI001UnknownCommand, TestRCLI006ExitFunction,
  TestRCLI006CatalogueExpectations; tests/errors: all 56 replay entries pass with
  GOFLAGS=-tags=bobtest (57 pass in total; live entries fail on the missing ecosystems).
  tests/nix TestRNIX024NixEnvironment: the `doctor --print-nix-env` half passes, the build half
  fails (evaluation).
- Still red/failing and why: every command that evaluates a fixture stops at evaluation —
  the fixtures use golang.Module, rust.CLib, cc.Library … (M5+ ecosystem APIs), exit 3; 1011/1012
  in check need build.Check/Gen (NEEDS); stage events vs schema (TEST PROBLEM 1/3); graph
  column (NEEDS ir); login, pipeline, dev, env, gen, install, registry, list are later
  milestones (red, BOB-E-9008); completion scripts: goldens missing (owner) and zsh/fish not on
  PATH (harness error); tests/install all red (registry/install not implemented).

## DECISIONS
- D-M4e-1: BOB_TEST_NIX_REPLAY is honoured only in binaries built with `-tags bobtest` (A27).
  The errors suite builds bob with plain `go build`, so its replay entries need
  `GOFLAGS=-tags=bobtest` in the environment of `go test ./tests/errors` (tools/ci stage
  `errors`, protected): see NEEDS.

- D-M4e-2: an error that is not a diagnostic (a Go error from a lower layer) is reported as
  BOB-E-9005 with its text as reason (exit 70); text that names /nix/store or spans lines goes
  to raw[] (-v) only. §02.3 has no generic "bob internal error" code; a dedicated one would be
  clearer (SPEC/diag owner).
- D-M4e-3: all human output (package lines, blocks, summaries) goes to stdout, so stdout+stderr
  reproduce the goldens; stderr carries only the live status line and -vv debug text.
- D-M4e-4: R-FW-049 stage events and drv.imported are written to --json as their own events
  ({"durationMs":…}), after run.start (earlier events are buffered until Run sends run.start).
  See TEST PROBLEM 1.
- D-M4e-5: `bob log` reads `nix-store --read-log <out path>` from `.bob/last-build.json`
  until build.Package carries its derivation (NEEDS); a failed build's output path is not
  valid, so the log of a failed package needs Package.Drv.
- D-M4e-6: `bob lock` without bob.lock uses the nearest `.git` ancestor as root (SPEC §02 open
  item 11 recommendation).

## NEEDS
- I-M4d (pkg/engine/build), used by pkg/cli and written against the names below:
  - `Package.Drv string` (the package's derivation): `bob log <label>` reads its log
    (`nix-store --read-log <drv>`); the CLI records label → drv in `.bob/last-build.json`.
  - `func Check(ctx, o Options) ([]*diag.Error, error)`: the findings of the drift (1011,
    R-FW-061) and lock-freshness (1012) stages without changing anything and without network
    (bob check).
  - `func Gen(ctx, o Options, force bool) ([]string, error)`: write the generated native files
    (bob check --fix = gen --force; M5.1 `bob gen`); returns the not-committed paths that
    belong in bob's `.git/info/exclude` block (1013).
  - `func CheckDeterminism(ctx, o Options) error`: eval.CheckDeterminism with the same pinned
    Go resolution as Evaluate (`bob check --determinism`); the CLI cannot find the pinned Go.
  - Evaluate/Run must resolve the pinned Go themselves when GoRoot is "" and a Store is given:
    the harness runs bob with PATH=/usr/bin:/bin, which has no go.
- tools/ci (protected, stage `errors`): run `go test ./tests/errors/...` with
  `GOFLAGS=-tags=bobtest` so the replay hook (A27) is compiled into the bob it builds.
- pkg/engine/ir (I-M3a): `Pos.Col` (bob graph positions are file:line:col, R-CLI-011).
- store (I-M2b): an exported embedded Nix version (e.g. `store.NixVersion = "2.34.9"`) for
  run.start/`bob version`; the CLI keeps its own constant until then.

## TEST PROBLEM
1. Stage events vs schema v1. tests/core TestRFW049StageEventSequence wants `--json` events
   named namespace, scan, eval, … with durationMs, tests/nix wants `drv.imported` and
   `namespace` events; schema/events-v1.json (protected) has a closed `event` enum without
   them, and tests/cli checkStream validates every `bob build --json` line against it. Both
   cannot pass. The CLI emits the stage events (R-FW-049, rulings A29/A56/G16), so the
   tests/cli schema check fails on them once builds work. Proposed fix (owner): add the 13
   stage names and `drv.imported` to the schema enum (v1 allows added events).
2. tests/cli TestRCLI011Graph wants `libs/foo/bob.go:13:9` (line:col) but the IR position
   (pkg/engine/ir Pos) has no column; graph prints file:line until the IR carries one
   (NEEDS for the ir owner, not a test defect).
3. Same conflict in tests/errors: checkJSON rejects the `namespace`/stage events of live runs
   (its event set equals schema v1), while tests/core and tests/nix require them.
