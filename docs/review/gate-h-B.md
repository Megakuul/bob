# Gate H test review, unit GH-B

Scope: R-CLI-*, R-INST-*, R-DEV-*, R-OUT-*, R-PLAT-* (tests/cli, tests/install, tests/devenv, tests/outputs, tests/platforms, tests/serverenv/clients).
Reviewer: fresh context. Checkpoint: plan/progress/GH-B.md. `go vet` (also `-tags slow` for tests/outputs) and gofmt are clean for all touched packages.
FIXED = I edited the test (minimal, test-side bug). OPEN = needs an owner decision, not edited.

## HIGH

1. **R-OUT-\* and R-PLAT-\* (every test that builds with `--print-paths`)** — FIXED.
   tests/outputs/helpers_test.go `bobEnv` (~l.112) and tests/platforms/helpers_test.go `bobEnv` ran `leakcheck` over the combined output of every build. `bob build --print-paths --json` prints host paths `<home>/nix/store/<32-hash>-…` on purpose (SPEC §02.6, §02.7.5), and `leakcheck` flags `/nix/store/<32 chars>`. A correct implementation would fail every `w.build`/`buildOne`/`printPaths` call. Now the `paths[]` array is blanked before the check.
2. **R-CLI-002** — FIXED. tests/cli/events_test.go `checkStream` failed the whole run when stdout contained `/nix/store/` and ran `noLeak` over all of stdout. That false-fails the `--print-paths` run (host paths) and the `--json-log` run (raw build log lines). Now `paths` and log `line` are excluded from the leak checks.
3. **R-INST-002** — OPEN. tests/install/nonix_test.go `noNix` script asserts `! command -v nix`, but this host has a real `/usr/bin/nix` and the script's PATH is `…:/usr/bin:/bin`. Once `bob install` exists the test still dies in the precondition (exit 1, "install without /nix"), so it cannot go green here. A /dev/null bind-mount does not help (bash `command -v` still reports it). Decide: drop nix from the precondition (keep `/nix/store` empty and `go` absent), or run with a PATH that has no `/usr/bin`.
4. **R-OUT-006** — OPEN. tests/outputs/build_test.go ~l.72: the first build of the three bundles runs under `unshare --net` with a fresh BOB_HOME, and only `//cmd/api:api` was built before. nss-cacert, tzdata, glibc and friends for the image/appimage/tarball cannot be substituted offline, so a correct implementation fails. The comment says "everything else is warm", but nothing warms it. Suggested fix: build the targets online first, then assert that bundle derivations are non-fixed-output and that an offline rebuild is a no-op.
5. **R-CLI-010** — OPEN. tests/cli/commands_test.go `--update` block: it uses fixture `go`, whose `bob.lock` is empty. After `bob lock` pins the latest, `--update golang.org/x/sync` has nothing to change and prints `no changes`; the loop over `lines()[1:]` is empty. A no-op `--update`, or one that updates everything, passes. SPEC names `golang.org/x/net` and "changes only that module's entries (plus the transitive ones, listed)". Needs an intentionally outdated lock fixture, then assert that the changed names are exactly the module and its requirements.
6. **R-OUT-009** — OPEN (probable false red). tests/outputs/oci_test.go ~l.132 asserts that layer 0 holds `-glibc-` alone as the "most popular path". By the SPEC definition (popularity = number of closure paths whose closure contains p), glibc's own references (libidn2, libunistring in the nixpkgs closure) are at least as popular and more popular than glibc, so a correct implementation puts them in layer 0. This is not in the SPEC acceptance (14 layers, one blob differs, ≤ 16 %, `MaxLayers: 4`). Compute the expected order from the closure (`nix path-info -r`) or drop the assertion.

## MEDIUM

7. **R-CLI-002** — OPEN. tests/cli/events_test.go `checkSchemaFile` returns silently when `schema/events-v1.json` is absent (the `schema/` directory does not exist), and otherwise only checks that the file mentions the event names. Validation is the hand-written `validateEvent`, never the protected schema, so acceptance bullet 1 is weak. Once the schema file exists, validate with it (and fail if it is missing).
8. **R-CLI-002** — FIXED. output_test.go `TestRCLI002VerboseAddsLines` compared stdout only, so it passed vacuously if the human output was on stderr. Now stdout+stderr, and the default run must be non-empty. Still only 3 runs, not "every golden" (LOW).
9. **R-CLI-007** — OPEN. output_test.go `TestRCLI007Colour` requires `ESC[1;31m` or `ESC[31;1m` for "error red bold". SPEC says only "red bold"; `ESC[1m ESC[31m` or `ESC[1;91m` would fail. Stricter than SPEC.
10. **R-CLI-008** — OPEN. logs_test.go local log: excerpt lines only need to appear, in any order, somewhere in the last 12 log lines, and lines containing `…/` are skipped. SPEC: "its last 10 lines match the excerpt". An implementation that prints the excerpt scattered is accepted.
11. **R-CLI-011** — OPEN. commands_test.go: "`--format json` passes the schema and the edge set equals the evaluator's graph" is checked only against bob's own `--json` result plus one hard-coded edge. No schema check, and the exit status of the `--format json` run is not checked.
12. **R-INST-001** — FIXED. lifecycle_test.go: after `uninstall`, the `list --json` loop was vacuous when there was no result event or `list` failed, and `list` human output was only compared to empty. Now exit 0 and a result event with an empty `installed` are required.
13. **R-INST-001** — OPEN. `TestRINST001KillPoints` uses wall-clock offsets 0–1500 ms against a local fake server. `upgrade` probably finishes in less than 50 ms, so most offsets kill nothing, and nothing shows that a kill landed mid-upgrade. A non-atomic `current` flip passes unless a kill hits by luck. The SPEC's "harness kill points" are not defined anywhere (SPEC problem). A deterministic hook (e.g. an env var that makes bob pause at named steps) is needed.
14. **R-INST-005** — OPEN. profile_test.go `TestRINST005GCRoots` only checks that the root directory lists `[3 4 5 6 7]`. "Every store path of each of the last 5 generations has a GC root" is not asserted (the comment claims it). Create-only temp+rename of `generations/<n>.json` is untested.
15. **R-INST-006** — FIXED. isolation_test.go: the env comparison was vacuous if the hook never activated the project or `bob dev` never set `BOB_DEV`. Now it fails first if `BOB_PROJECT` is unset after the hook, or `BOB_DEV` != 1 under `bob dev`.
16. **R-DEV-005** — FIXED. devenv/login_test.go: the request-log check used `TrimPrefix(concat(all logs), concat(earlier logs))`. This fails when several process logs grow, so the harness's own earlier `PUT /nix/…` (403) line would be reported as a write by `bob build --remote`. Now per-file offsets (`logSizes`/`logsSince`).
17. **R-OUT-010** — FIXED. oci_test.go skipped the `nix/` and `nix/store/` (0555, uid 0) directory check for layers 1..n-2, but SPEC says each layer includes them. Now every layer is checked.
18. **R-OUT-015** — OPEN. appimage_test.go ~l.222: every Go dependency of the runtime must start with `golang.org/x/`. SPEC: "no LGPL/GPL runtime code (licence check on the module graph)". Any permissive third-party module fails.
19. **R-OUT-023** — FIXED. release_test.go required `release.json` to equal compact canonical JSON byte for byte. SPEC asks for sorted keys (its own example is indented). Now a token-order check (`unsortedKeys`); the golden still pins the exact bytes.
20. **R-OUT-007 / R-OUT-019 / R-DEV-001 / R-DEV-011** — FIXED (stream). Several checks read only `r.Stderr` where SPEC does not name the stream: the R-OUT-019 warning, the BOB-E-1014 text and its shell list (R-DEV-001), and BOB-E-0004 plus snippet (R-DEV-011). They now read `Combined()`. R-OUT-007's prune error stays on stderr because SPEC §4.11 says "stderr".

## LOW

- R-CLI-004: SPEC lists `//libs/greet:greet`, but no fixture has `libs/greet`; the test requires only `//libs/foo:foo` and no complete label set (SPEC/fixture mismatch).
- R-CLI-008: the `-f` case does not assert that the build was still running when `-f` started (only a 5 s sleep), and requires `-f` output to start at line 1 (SPEC: ends with 1000, no gaps/duplicates).
- R-CLI-009: `TestRCLI009CheckLock` never runs `check --fix` on a 1012 finding; "check changes no file" is asserted only for the clean and 1011 cases.
- R-CLI-010 (FIXED): "exactly one `+` line" counted only zod lines; now all `  +` lines.
- R-INST-003: netrc mode 0600 and "token not on a command line" are not asserted; a failed private install does not assert that nothing was installed. The server-side half belongs to the server suites.
- R-DEV-002: "server answers again" after the dependency change is satisfied by a server that never restarted. The orphan check covers only descendants captured before SIGTERM (reparented daemons missed; SPEC says cgroup/process tree).
- R-DEV-005: the "internal promote endpoint" is approximated by `WorkerControlService/GetCheckoutCredential` (SPEC names no promote RPC). The cache-write regex depends on an unknown request-log format.
- R-DEV-008: the namespace check runs `go run ./ns` through the shim, not subprocesses started by gopls. The shim overhead is measured as `bob exec -- /bin/true`, without the `sh` hop.
- R-DEV-010: hover and definition inside the opened store file are asserted for gopls only.
- R-OUT-001: uses `package.finish` paths instead of the SPEC's `{"type":"result"}` (logged SPEC problem).
- R-OUT-009: the v2 config blob differing from v1 is not asserted.
- R-OUT-010: the `history[i].created` loop is vacuous if the config has no history. Gzip DefaultCompression, JSON field order and blob reuse by diff_id are not asserted.
- R-OUT-011: the image layout rule "layer blobs are symlinks, config/manifests/index regular files" is not asserted. The layer-references check tolerates a self-reference.
- R-OUT-013: uses `bob build --remote --version`, not the release pipeline. The staging `COMPLETE.json images[]` half is not checked.
- R-OUT-015: first-run (≤ 0.5 s) and cached (≤ 0.1 s) start times are in the SPEC acceptance but left to budget B-086. squashfs flags `-all-root/-no-xattrs/-all-time` are only partly checked.
- R-OUT-016: `BOB_APPIMAGE_MODE=ldso` is never run where user namespaces are blocked. The debug-choice check is a substring test. Child exit-code propagation and the `read launch.json:` message are untested.
- R-OUT-023: per-kind required fields exist only in the schema's description; the validator subset ignores them. The invalid-version message is compared with `%q`, SPEC shows the raw value.
- R-PLAT-004: "must not run natively" fails on hosts with qemu binfmt registered. "extra-platforms unset" is not asserted.
- R-PLAT-005: cross outputs run under qemu only for polyglot and cc (SPEC: every fixture prints the golden line).
- R-PLAT-007: relies on the invented `BOB_TEST_NIXPKGS_INDEX` (decision A34). The rustc case asserts only "nixpkgs mismatch" plus both versions.

## Ids reviewed with no findings

R-CLI-001, R-CLI-003, R-CLI-005, R-CLI-006 (live fault-catalogue check is in tests/errors), R-CLI-012, R-DEV-001, R-DEV-003, R-DEV-006, R-DEV-007, R-DEV-009, R-DEV-011, R-INST-004 (apart from the LOW items above, none), R-OUT-003, R-OUT-004, R-OUT-017, R-OUT-018, R-OUT-020 (slow-tag matrix), R-PLAT-001, R-PLAT-006, tests/serverenv/clients (smoke tests only). Not tested by design: R-PLAT-002 (manual), R-PLAT-008, R-OUT-024, R-OUT-025 (budgets stage).
Reviewed with findings: R-CLI-002, 004, 007, 008, 009, 010, 011; R-INST-001, 002, 003, 005, 006; R-DEV-002, 004, 005, 008, 010; R-OUT-001, 002, 006, 007, 008, 009, 010, 011, 012, 013, 015, 016, 019, 022, 023; R-PLAT-004, 005, 007.

## Fixes made (test side)

- tests/cli/events_test.go: `checkStream` leak check excludes `paths[]` and log `line` (HIGH 2).
- tests/cli/output_test.go: `VerboseAddsLines` compares stdout+stderr, requires non-empty default output.
- tests/cli/commands_test.go: R-CLI-010 counts all `+` lines.
- tests/install/lifecycle_test.go: post-uninstall `list` checks are no longer vacuous.
- tests/install/isolation_test.go: preconditions for hook and `bob dev` activation.
- tests/devenv/login_test.go: per-file log offsets (`logSizes`/`logsSince`).
- tests/devenv/dev_test.go, tests/devenv/editor_test.go, tests/outputs/appimage_test.go (R-OUT-019 only): error and warning text read from `Combined()`.
- tests/outputs/helpers_test.go, tests/platforms/helpers_test.go: `--print-paths` host paths excluded from the leak check (HIGH 1).
- tests/outputs/oci_test.go: `nix/` and `nix/store/` dir check on every layer.
- tests/outputs/release_test.go: sorted-keys check by token order instead of compact byte equality.
