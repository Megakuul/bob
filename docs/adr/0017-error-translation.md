# ADR 0017 — Error translation: Nix's structured log → bob errors

- **Status:** Accepted (Gate S pending). SPEC §02 (`spec/02-cli-dev.md`) holds the taxonomy,
  templates, exit codes and event schema.
- **Date:** 2026-10-20
- **Requirements:** R-NIX-010…013 (Nix never leaks), R-CLI-002 (concise output, `-v`, JSON mode),
  R-DEV-006 (developer-visible behaviour), R-LOG-003 (logs per package)
- **Evidence:** `docs/spikes/s15-errors.md`; code, fixtures and goldens in `spikes/s15-errors/`
  (56 captured runs × 2 Nix versions, golden harness `go test ./translate`)
- **Builds on:** ADR 0001 (strategy B), ADR 0002 (embedded static Nix 2.34.9)

## Context

bob runs Nix for every build and must never show Nix text by default. Nix offers one structured
channel: `--log-format internal-json`, which writes `@nix {json}` lines to stderr (activities,
results, messages). It carries **no error codes**: every error is a free-text `msg` at level 0.
What a build failed on is only visible from (a) which message names which `.drv`, (b) the build
log lines attributed to the build activity, and (c) what bob itself knows (its own derivations,
limits, caches, and which outputs exist afterwards).

## Decision

1. **One invocation shape.** bob realises with
   `nix build --no-link --print-out-paths --log-format internal-json -L --keep-going <drv>^out…`
   and parses stderr line by line. No other Nix output format is parsed anywhere.
2. **Translator inputs are bob's facts plus the stream.** A `Context` built when bob computes the
   derivations: drv → package (label, `definedAt`, kind, source dir), output → drv, imported
   source store path → repo path, direct nixpkgs inputs → attribute and users, configured limits
   (timeout, silence timeout, log size, local builds on/off) and substituters (name, auth).
   After Nix exits bob checks which of its outputs are valid in the store.
3. **Success is structural.** A run succeeds iff Nix exits 0 and every requested output is
   valid. Messages never turn a failure into success, and an unrecognised message never fails a
   run that Nix reports as successful (it becomes a warning). Reason: builders can inject
   level-0 messages into the stream (measured), so messages are untrusted.
4. **Classification order.** (1) Go pre-checks before Nix (R-NIX-011, codes 1xxx);
   (2) the level-0 message, by an anchored pattern table per supported Nix version;
   (3) for "builder failed with exit code N", the package's own build log, by toolchain
   patterns (gcc/clang, go, rustc, ld, go test); (4) warnings (level 1) explain cache and
   download failures that the final message hides; (5) anything else → `BOB-E-9999` with the raw
   text under `-v`, which is a bob bug and gets a regression fixture (R-NIX-010).
5. **Error codes** `BOB-E-NNNN`, grouped in classes that decide the exit code; codes are never
   reused. Taxonomy, templates and exit codes are in the spike write-up's SPEC input.
6. **Supported Nix = the embedded Nix only** (2.34.9 today). The pattern table and the golden
   fixture set are keyed by Nix version. A Nix bump is a change that must re-capture the fault
   catalogue with the new binary and pass the goldens unchanged (`faults/expect.json` holds the
   code per fault). Drift is detected at runtime (unknown action, activity type, result type,
   JSON key, unparsable `@nix` line, unmatched level-0 message) and reported in `run.finish`.
7. **Output.** Human output: one line per package result, then one block per error (code,
   message, `-->` location, `package defined at`, cause, excerpt, hint, `full log: bob log …`).
   Colours only on a TTY and never with `NO_COLOR` or `TERM=dumb`. `--json`: NDJSON events,
   schema version 1, no raw Nix text unless `-v`.
8. **bob-builder diagnostics** travel as plain build-log lines with a `bob::` prefix plus
   `@nix {"action":"setPhase"}` for phases (forwarded by Nix as result type 104). Builder-sent
   `start`/`result`/`stop` are dropped by Nix and must not be relied on.

## Consequences

- The translator is a pure function of (Context, stream, outcome) and is tested by replaying
  captured streams; the harness (HARNESS.md fault catalogue) re-uses `faults/make.sh` as the
  capture tool and `translate/golden_test.go` as the check.
- bob must check things Nix hides: cache credentials before a build (a 401 only appears as a
  level-1 warning and Nix silently builds locally), output validity after a build, and the
  configured limits (Nix prints the same "timed out after N seconds" for both timeouts).
- Toolchain bumps (gcc, rustc, go) can change compiler diagnostics; those patterns get their
  own goldens per toolchain.
- The host Nix (2.34.8 here) is not a supported engine; it was captured only to measure drift
  between adjacent versions.

## Rejected options

- **Parsing the human Nix output** (default log format): unstructured, coloured, log tails
  truncated to `log-lines`, no per-build attribution. Rejected.
- **`nix-store --realise` exit codes (100–104)**: `nix build` exits 1 for every failure we
  captured; the 100+ codes exist only on legacy commands and lose per-package detail. Not used.
- **Trusting builder-emitted `@nix` messages** for results: they are injectable by any build.
- **Supporting any installed Nix**: every version change can change message text; bob embeds
  its Nix (ADR 0002), so only that version is supported.

## Owner amendments (Phase 2.3, 2026-10-21)

- **D49 / D50:** strategy B and the embedded static Nix 2.34.9 are locked. Decision 6 ("only the
  embedded Nix is supported") is final; the host Nix is never a fallback.
- **D61 (downloads go through the orchestrator proxy, no presigned URLs):** cache and release
  download errors name the bob server, never an S3 host. New codes 4008–4012 cover the server's
  own answers (Unauthenticated/PermissionDenied from `AuthService/WhoAmI`, server unreachable, release not found). bob's
  pre-checks raise them, not the Nix stream (a 401 is only a level-1 warning, S15 §3.2).
- **D55 (live logs are pulled on demand):** `bob log -f` and `bob build --remote` show
  translated package results built from server events. Raw lines appear only with `-v` or in
  `bob log`, the same as for local builds.
- **D51 / D59 (shims, shell hook, LSP proxy):** `bob exec` uses the same taxonomy. New codes 7005
  (unprivileged user namespaces unavailable, R-NIX-022) and 7006 (a shim's project or env file is
  gone). In `bob exec --lsp` mode bob writes nothing to stdout except protocol frames; its own
  diagnostics go to stderr as human text.
- **D2 / D33 (drift):** new definition codes 1011–1013 for `bob check`.
- New class `0xxx` (usage, exit 2) for CLI errors that happen before any evaluation.
- The exit codes, their priority order and the `--json` v1 schema are unchanged. Two event kinds
  are added (`result` for query commands, `remote.*` for `--remote`); v1 allows adding events
  and fields.
