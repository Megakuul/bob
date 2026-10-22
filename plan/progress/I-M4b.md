# I-M4b progress (M4.2 `pkg/engine/translate`: internal-json → diag, R-NIX-010, R-NIX-012)

## done
- pkg/engine/translate (own package only):
  - nixlog.go: parser of `@nix {json}` lines (all actions / activity / result types of Nix 2.34,
    unknown ones and unknown JSON keys → drift); `scanLines` cuts lines at MaxLineBytes (1 MiB)
    and reports the cut as drift.
  - context.go: Context (drv → package, outputs, sources, nixpkgs, users, chains, limits,
    substituters), Outcome (exit, signal, interrupt, Valid = bob's structural check).
  - translate.go: Translator (New / Feed(line) / Finish(Outcome) → *Report, live `Sink`
    events). Anchored level-0 pattern table, level-1 cache warnings, drv-less fatal messages
    (timeouts vs silence timeout from Limits, interrupts), signals, 9999 for unknown messages,
    9998 for drift + only unknown errors. Bounded state: per-drv log head 2000 + tail 2000
    lines (4 KiB/line), ≤ 10000 warnings, ≤ 64 unknown errors, raw text ≤ 64 KiB.
  - classify.go: build-log classifier (gcc/clang, go, rustc, ld, go test, curl in fetchers,
    network in sandbox, broken builder) → 2xxx/3xxx/9001.
  - report.go: Report (Errors/Warnings as *diag.Error, Packages, Counts, Drift, ExitCode via
    diag.ExitCode, Hint(e)), Event (typed, CLI adds the JSON envelope), ErrorObject (schema
    v1 `error` object, every field present), PosString/ParsePos.
  - render.go: Human (package lines, blocks, summary; S15 golden layout), Block, StyleFor
    (SPEC §02.5 rules 6/7, SPEC colours), FinalEvents.
  - replay.go: Capture/LoadCapture/Replay(dir)/FeedAll — the BOB_TEST_NIX_REPLAY path
    (meta.json + stream.log[.gz], meta ≤ 64 MiB, stream streamed); pre-check captures.
- Tests (pkg/engine/translate/translate_test.go), all green:
  - TestReplayCatalogue: every capture of tests/faults (56 × Nix 2.34.9): codes + exit vs
    tests/faults/expect.json (alt outcomes), golden.txt byte for byte, golden.tty.txt with
    colours stripped, tests/errors/leakcheck on default output and on --json error objects
    (+ the fault's `forbid`), -v superset with `nix:` lines and no `@nix`, build errors carry
    label + definedAt + `bob log`, no template argument left unfilled.
  - TestReplayBothNixVersions: the same over spikes/s15-errors/fixtures (56 × 2.34.8 and
    56 × 2.34.9) against the spike's expect.json (byVersion for fault 34). Both versions green.
  - Unknown message → 9999 exit 70, text only under -v; messages (unknown or forged known ones)
    in a structurally successful run → warnings, exit 0; exit 0 with an invalid output → 9002;
    drift + unknown → 9998; long lines; bounded log; Pos round trip; StyleFor; FuzzFeed
    (90 s total, ~3M execs, no panic, a failed run never reports success).
- gofmt clean, `GOWORK=off go vet` clean, staticcheck and boblint clean for the package.

## still red (not in my package)
- tests/nix TestRNIX010NoRawNixText, TestRNIX012FailuresInBobTerms (checkEntries part) and
  tests/errors TestFaults: `bob build` is the 9008 stub (pkg/cli, M4.6). The G4 golden
  structure part of TestRNIX012 passes (only the 4 documented exceptions are logged).
- `go run ./tools/ci -only fmt,vet,lint,layering`: fmt/vet/lint fail in pkg/engine/eval
  (another unit's work in progress: evalbox.go unformatted, framework.zip missing);
  layering fails on translate → diag (NEEDS 1).

## files
- pkg/engine/translate/{doc,nixlog,context,translate,classify,errors,report,render,replay}.go,
  translate_test.go

## DECISIONS
- D-M4b-1: the translator is ported from the S15 spike translator (bob's own Stage 2 code,
  ADR 0017 Consequences names it as the check), adapted to diag.Error, with these fixes:
  deterministic attribution (post-build hook = last hook activity, substituter of a path from
  a path→cache map, longest source path replaced first) and bounded memory.
- D-M4b-2: success is structural (R-NIX-018): if Nix exits 0, no signal/interrupt and every
  bob output Nix built or downloaded (and every requested package) is valid, every message-
  derived error becomes a warning — also a forged *known* message (the spike only demoted
  unknown ones). Exit 0 with an invalid output and no message → BOB-E-9002 per package.
- D-M4b-3: a 9999 `cause:` line from an unknown message is shown only when it contains no Nix
  vocabulary (store paths, .drv, "derivation", "Cannot build", "Reason:", substituter, …);
  the raw text is always in Raw (-v). Keeps the ok-structured golden ("builder says error")
  and R-NIX-010. 9005 (Nix printed a non-JSON fatal line) no longer copies that line into the
  message: reason "Nix could not start", raw line under -v.
- D-M4b-4: run-specific hints. The recorded goldens (binding, byte for byte) extend the table
  hint for 4002/4003/4004/5001/6003 (engine killed)/9003 and use "bob built locally instead…"
  for cache warnings. diag.Error has no hint field, so Report keeps them (Report.Hint(e),
  Event.Hint); see NEEDS 2.
- D-M4b-5: human output follows the S15 goldens (summary omits zero counts, e.g.
  "bob: 1 failed (exit 1)"), which the R-CLI-007/R-NIX-012 acceptance makes byte-binding; the
  SPEC §02.5 rule-4 sample with all four counts differs (finding for the owner). Colours follow
  SPEC rule 6 (error red bold, warning yellow bold, hint cyan, --> blue); golden.tty.txt
  (spike colours) is compared with colours stripped.
- D-M4b-6: excerpts are capped at 12 lines when rendered (SPEC §02.3); no golden exceeds it.
- D-M4b-7: --json: translate gives typed Events plus ErrorObject (schema v1 error object with
  all fields); the envelope (time, command, args, versions, durationMs) is the CLI's. The S15
  golden.jsonl files omit fields the protected schema requires, so they are not reproduced.

## NEEDS
1. pkg/layering_test.go owner / SPEC R-ARCH-003: E3 must be allowed to import L0
   (`pkg/bob/diag`). The table says E3 may import "E0–E2", but every lower engine layer
   (E0, E1, E2) imports L0 and M4.2's task is "internal-json → diag": the translator returns
   *diag.Error. Fix: `mayImport[e3] = {e0, e1, e2, l0}` and SPEC E3 row "E0–E2, L0".
   Until then TestLayering reports translate → diag.
2. pkg/bob/diag owner: an optional per-error hint override (e.g. field `HintText string`,
   used by `Hint()` when non-empty), so run-specific hints travel with the error into
   pre-check/CLI rendering; then Report.hints and Event.Hint go away.
3. pkg/cli + pkg/engine/build (M4.6): wire `bob build` to the translator:
   - live: build the Context from the drv plan, run `nix build --no-link --print-out-paths
     --log-format internal-json -L --keep-going …`, `translate.FeedAll` stderr, then
     `nix-store --check-validity` → Outcome.Valid, `Finish`;
   - BOB_TEST_NIX_REPLAY=<dir>: `translate.Replay(dir)` instead of evaluating/running Nix;
   - output: `translate.Human(stdout+stderr stream, r, translate.StyleFor(isTTY, os.Getenv, -v))`
     is exactly the golden (nothing else on the default output); `--json`: Events →
     schema-v1 lines (+time, run.start command/args/bob/nix, run.finish durationMs, `raw[]`
     only with -v; `NewErrorObject(e, ev.Hint)`); exit status = Report.ExitCode.
   With that, TestRNIX010/012 and tests/errors TestFaults turn green for all replay entries
   (the translator half is verified in TestReplayCatalogue).
4. pkg/engine/build: Limits must record which of --timeout / --max-silent-time it set (same
   Nix text); Context.Chains for failing nixpkgs drvs (S15 §5.7).

## TEST PROBLEM
- none in tests/. Finding (not a test change): SPEC §02.5 rule 4 summary text vs goldens
  (D-M4b-5).
