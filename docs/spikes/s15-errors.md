# Spike S15 — Error translation: Nix's structured log → bob errors

Status: **done**, 2026-10-20, host `vibe-engine` (see `docs/environment.md`). Code:
`spikes/s15-errors/` (throwaway, module `bobspike/s15`). ADR: `docs/adr/0017-error-translation.md`
(Proposed).

## 1. Question and requirements

How does bob turn what Nix reports during a B-style build into bob errors that name the package
label and its `definedAt` Go position, the cause, the relevant log lines and a hint, with a defined
exit code, a human format and a `--json` stream? Which Nix messages exist, how stable are they
across Nix versions, and how does bob notice drift?

Requirements: R-NIX-010…013 (§5.2), R-DEV-* (§9), R-CLI-001/002 (§10), R-LOG-003 (§19).

## 2. What was built

| Path | What |
|---|---|
| `b/`, `ir/`, `fixture/` | Copy of backend B, IR and fixture. Changes: switchable Nix binary (`run.NixDir`), test stores (`NIX_REMOTE`); IR gains a spike-only `script` kind (fault injection) and `GoAttrs.Test`. `fixture/*/bob.go` are illustrative `bob.go` files, so `definedAt` points at real `file:line:col` positions. |
| `nixlog/` | Parser of `--log-format internal-json`: every action, activity type, result type and level. It reports unknown types, unknown JSON keys and unparsable lines. |
| `translate/` | The translator. It is fed live, one event at a time, and ends with the process outcome. Also the taxonomy (`codes.go`), build-log classifier (`classify.go`), human and JSON rendering (`render.go`) and fixture replay. |
| `translate/golden_test.go` | Golden harness. It replays every fixture and checks the error codes and exit code against `faults/expect.json`. It compares three goldens (plain, TTY, JSONL) and runs a leak check (no store hashes, `.drv`, "derivation", "Cannot build", "substituter", Nix option names in default output). It also runs drift tests and the NO_COLOR/TTY style rules. |
| `cmd/s15` | `capture` runs a B build with internal-json and stores `stream.log` + `meta.json`. The meta holds bob's context, the outcome and the valid outputs. Also `replay` and `types` (writes `fixtures/catalogue.json`). |
| `cmd/s15cache` | HTTP binary cache with injectable faults: basic auth, stall, NAR 404, corrupt NAR, 500. |
| `faults/make.sh` | Fault catalogue: captures every fault with **static Nix 2.34.9** (the embedded one) and **host Nix 2.34.8**. Ports 21503–21516 and 21598/21599 (refused) are used. |
| `fixtures/<fault>/<nix>/` | 56 faults or scenarios × 2 versions = 112 captures (7.4 MB; streams over 1 MB are gzipped). Each holds `golden.txt`, `golden.tty.txt` and `golden.jsonl`. |
| `measure.sh` | Replay throughput [8c/15G] and [2c/4G], golden run, optional full capture (`FULL=1`). |

Go: 2,992 lines in `nixlog/`, `translate/` and `cmd/s15` (the B copy not counted).

## 3. Results

### 3.1 Catalogue of internal-json types (both versions, all 112 captures; `fixtures/catalogue.json` has a sample of each)

| Line | Meaning | Fields | Levels seen | Notes |
|---|---|---|---|---|
| `start` type 0 | unknown activity ("querying info about missing paths") | — | 6 | ignore |
| `start` 100 copy-path | substituted path being copied | `[path, from-URI, to-URI]` | 3 | `from` names the cache |
| `start` 101 file-transfer | download | `[url]` | 4 | parent = build (FOD fetch) or query-path-info |
| `start` 102 realise / 103 copy-paths / 104 builds | top-level counters | — | 0 | progress via result 105, expected via 106 |
| `start` 105 build | one derivation build | `[drvPath, machine, round, nrRounds]` | 3 | **the key for per-package attribution** |
| `start` 108 substitute | download of an output | `[storePath, substituterURI]` | 0 | maps an output to "cached" |
| `start` 109 query-path-info | narinfo lookup | `[storePath, substituterURI]` | 4 | |
| `start` 110 post-build-hook | hook run | `[drvPath]` | 4 | |
| `start` 111 build-waiting | "waiting for lock on '<out>'" | — | 1 | another process builds the same path |
| `stop` | end of activity | `id` | — | no success or failure flag |
| `result` 101 build-log-line | builder output line | `[line]` | — | ANSI from the compilers is kept |
| `result` 104 set-phase | phase | `[name]` | — | stdenv phases; **also accepted from bob's builder** |
| `result` 105 progress | `[done, expected, running, failed]` | ints | — | about 90 % of all lines in a big build |
| `result` 106 set-expected | `[activityType, n]` | ints | — | |
| `result` 107 post-build-log-line | hook output | `[line]` | — | |
| `msg` level 0 with `raw_msg`, `file`, `line`, `column` (`null` for build errors) | **errors** | — | 0 | `raw_msg` = `msg` without the "error:" prefix, but **still contains ANSI** |
| `msg` level 0 plain | builder-injected or "error (ignored)" | — | 0 | |
| `msg` level 1 (with or without `raw_msg`) | warnings: download retries, 401 on `nix-cache-info`, untrusted signature, missing NAR | — | 1 | the only place cache and network causes show up |
| `msg` level 3 | "these N derivations will be built:" plus one line per drv | — | 3 | plan |
| non-`@nix` line | only when Nix cannot start (e.g. `runuser … Permission denied`) | — | — | |

Result types 100 (file-linked), 102 (untrusted-path), 103 (corrupted-path), 108 (fetch-status)
and activities 106/107/112 never appeared in B-style builds. They are known to the parser but have
no translation.

**Builder-emitted `@nix` lines (measured, `ok-structured`).** Nix forwards `setPhase` as result 104
and `msg` as a **top-level level-0 message with no id**. It drops `start`, `result` and `stop`
sent by the builder. So any build step can inject a message that looks exactly like a Nix error.
Consequence: success is decided structurally (§4), and an unknown message in a successful run
becomes a warning.

### 3.2 Fault catalogue (`faults/make.sh`, `faults/expect.json`; every row passes on both Nix versions)

Exit: what bob returns. "Raw Nix" is the first line of the level-0 message (ANSI stripped). The full
bob output is in `fixtures/<fault>/2.34.9/golden.txt`.

| # | Fault | Raw Nix (level 0) | bob code | Exit |
|---|---|---|---|---|
| 1 | unknown nixpkgs attr | (Nix not run) | 1001 + "did you mean openslp, openssl, openal?" | 3 |
| 2 | broken nixpkgs pkg | (Nix not run) | 1002 | 3 |
| 3 | insecure nixpkgs pkg | (Nix not run) | 1003 with CVE | 3 |
| 4 | dependency cycle | (Nix not run) | 1006 with each `definedAt` | 3 |
| 5 | Go version too new | (Nix not run) | 1008 | 3 |
| 6 | C++ compile error | `Cannot build '…libfoo-foo.drv'. Reason: builder failed with exit code 1.` | 2001, `--> fixture/libfoo/src/foo.cpp:13:13` + gcc excerpt | 1 |
| 7 | C compile error | same | 2001 `fixture/cbad/src/cbad.c:4:12` | 1 |
| 8 | Go compile error | same | 2001 `fixture/api/cmd/api/main.go:59:28` | 1 |
| 9 | Rust compile error | same | 2001 `fixture/rcore/src/lib.rs:30:5` + rustc excerpt | 1 |
| 10 | Rust missing crate | same | 2010 crate `serde` | 1 |
| 11 | link error (undefined symbol) | same | 2002 `foo_sha256_hex` | 1 |
| 12 | missing header (dep dropped) | same | 2003 `rcore.h`, `--> fixture/api/cmd/api/main.go:7:10` | 1 |
| 13 | missing library `-lzstd_s15` | same | 2004 | 1 |
| 14 | Go test failure | same | 2005 `test TestDigest failed` + test output | 1 |
| 15 | two independent failures | 2× `Cannot build`, then `Reason: 2 dependencies failed.` | 2× 2001; api "skipped" | 1 |
| 16 | FOD hash mismatch | `hash mismatch in fixed-output derivation '…': specified … got …` | 3001 url / bob.lock / downloaded | 4 |
| 17 | fetch 404 (crates.io answers **403**) | `Cannot build '…crate.drv'. Reason: builder failed with exit code 1.`; cause only in the build log | 3003 HTTP 403 | 4 |
| 18 | fetch DNS failure | same + 4 level-1 retry warnings | 3002 "host not found", retried 5× | 4 |
| 19 | fetch connection refused | same | 3002 "connection refused" | 4 |
| 20 | fetch HTTP 500 | same | 3002 "server error (HTTP 500)" | 4 |
| 21 | fetch stalled (`stalled-download-timeout 3`) | same | 3002 "timed out" | 4 |
| 22 | build timeout (`--timeout 3`) | `timed out after 3 seconds` (**no drv named**, printed twice with keep-going) | 2007 attributed to the build without output | 1 |
| 23 | timeout with keep-going, a sibling and a dependent | same + `build of '…^out', '…^out' failed` | 2007 on //tools:sleep; sibling built, dependent skipped | 1 |
| 24 | silence timeout (`--max-silent-time 3`) | `timed out after 3 seconds` (**same text as 22**) | 2008 (bob knows which limit it set) | 1 |
| 25 | network access in sandbox | `Cannot build … exit code 1`; log: `Network is unreachable` | 2009 | 1 |
| 26 | `__noChroot` with sandbox | `derivation '…' has '__noChroot' set, but that's not allowed when 'sandbox' is 'true'` | 9004 | 70 |
| 27 | builder script syntax error | `… exit code 2`; log: `bash: -c: line 2: syntax error …` | 9001 | 70 |
| 28 | builder binary missing | `… exit code 1`; log: `error: executing '…/bob-builder': No such file or directory` | 9001 | 70 |
| 29 | build produced no output | `builder for '…' failed to produce output path for output 'out' at …` | 9002 | 70 |
| 30 | forbidden reference (fixup bug) | `output '…' is not allowed to refer to the following paths:` | 9003 naming `//tools:base` | 70 |
| 31 | wrong platform | `Cannot build … Reason: platform mismatch / Required system: 'x86_64-linux'` | 1010 | 3 |
| 32 | log flood (`max-build-log-size`) | `building derivation '…' killed after writing more than 20000 bytes of log output` | 2011 | 1 |
| 33 | OOM (cgroup 300M, `OOMPolicy=continue`) | `Reason: builder failed due to signal 9 (Killed).` | 6002 | 6 |
| 34 | OOM, systemd default `OOMPolicy=stop` | 2.34.9: `interrupted by the user`; 2.34.8: Nix itself SIGKILLed, no message (race) | 8001 / 6003 | 130 / 6 |
| 35 | Ctrl-C (SIGINT to Nix) | `interrupted by the user` | 8001, packages "stopped" | 130 |
| 36 | Nix killed (SIGKILL) | stream just ends | 6003 "bob's build engine was killed by signal SIGKILL" + running packages | 6 |
| 37 | post-build hook fails (worker upload) | `program "…/hook.sh" failed with exit code 1` + result 107 lines | 4007 with hook output | 4 |
| 38 | store not writable (other user, no daemon) | `opening lock file "/nix/var/nix/db/big-lock": Permission denied` | 7002 | 7 |
| 39 | nixpkgs package built from source fails | `Cannot build '…hello-s15-2.12.3.drv'. Reason: builder failed …` | 5001 "nixpkgs package hello-s15 … (needed by //tools:upstream)" + phase log | 5 |
| 40 | disk full (24 MB tmpfs store) | `error (ignored): write … No space left on device`, then `committing transaction: database or disk is full` | 6001 | 6 |
| 41 | store path registered but missing on disk | `getting attributes of path "…": No such file or directory` | 7003 | 7 |
| 42 | cache 401, local builds off | level 1 `unable to download '…/nix-cache-info': HTTP error 401`; level 0 `Reason: local builds are disabled (max-jobs = 0)` | 4001 | 4 |
| 43 | cache 401, local builds on | the same warning; **Nix builds locally, exit 0** | warning 4001 "bob built locally instead"; exit 0 | 0 |
| 44 | cache NAR 404 | level 1 `file 'nar/…' does not exist in binary cache` | 4002 | 4 |
| 45 | cache refused | level 1 ×5 `Could not connect to server` | 4003 "connection refused" | 4 |
| 46 | cache stalls | level 1 ×5 `Timeout was reached` | 4003 "timed out" | 4 |
| 47 | cache untrusted signature | level 1 `ignoring substitute … not signed by any of the keys in 'trusted-public-keys'` | 4004 | 4 |
| 48 | cache serves corrupt NAR | `hash mismatch importing path '…'; specified … got …` + 2 follow-ups | 4005 | 4 |
| — | ok: no-op, cold store (all toolchains substituted, 377k lines), local substitute, FOD fetch, lock wait (activity 111), builder setPhase/msg | — | none (ok-structured: warning 9999 for the injected message) | 0 |

Counts over 48 faults: **0 leaks of raw Nix text** in default output (checked by the harness).
12 faults are explained only by bob's own context (limits, substituters, valid outputs, `Chains`);
the level-0 message alone does not identify them. These are 17–24, 33, 36, 39 and 42–43.

S0b corrected: a 401 is **not silent** in the stream. It shows up as a level-1 message on
`nix-cache-info`. The final error never mentions it (42), and with local builds on Nix just builds
(43). bob has to correlate the warnings itself and should check credentials before the build.

### 3.3 Nix 2.34.8 (host) vs 2.34.9 (embedded static)

- JSON shape: identical. Same key sets per line type, same activity, result and field layouts in all
  112 captures.
- Level-0 and level-1 message texts: identical after normalising hashes, ports and times, except
  for **libcurl wording**: `Failed to connect to 127.0.0.1 port 21599` (Fedora curl) vs
  `127.0.0.1:21599` (static curl). The translator keys on curl's stable part ("Could not connect
  to server (7)").
- Sandbox `/bin/sh`: host busybox vs the static Nix's embedded shell, which has **no applets**
  (`sh: mkdir: not found`). bob's builder must not rely on `/bin/sh`.
- One timing race differs (fault 34). It is a race, not a format difference.
- `nix build` exits **1** for every failure. `nix-store --realise` gives 100 (build failure) and
  102 (hash mismatch), but loses per-package detail (verified once each).

### 3.4 Measurements (indicative, busy host; rerun with `measure.sh`)

| Measure | [8c/15G] | [2c/4G] |
|---|---|---|
| Replay of the cold-store stream (377,309 lines, 35 MB) | 1.46 s (3.9 µs/line), 349 MB alloc | 1.84 s (4.9 µs/line) |
| Same as share of that build's wall time (119 s) | ≈ 1.2 % CPU | ≈ 1.5 % |
| Golden harness (112 fixtures × 3 goldens + drift + style) | 8–30 s (dominated by ok-cold) | not measured |
| Full fault capture, both versions | ≈ 15 min | not measured |

The parser cost is mostly `encoding/json` on progress results (90 % of lines). Two fixes are known:
a byte-prefix fast path for `"type":105`, or throttling `--log-format` progress. Neither is needed
for v1.

## 4. Answer

- The translator works on **Nix's internal-json stream plus bob's own facts**, and the stream
  alone is not enough. It needs the drv→package map (with `definedAt` and the source dir), the
  output→drv map, the source-path→repo-path map, the configured limits and caches, the chain from a
  failing nixpkgs drv to the bob package that uses it, and **which outputs are valid after the
  run**. Validity comes from `nix-store --check-validity`, not file existence: a full disk leaves
  a partial output file.
- **Success is structural**: exit 0 and every requested output valid. Messages can be injected by
  builders, so they never decide success. An unknown message in a successful run is a warning.
- Classification order: Go pre-checks → anchored level-0 pattern table (one per supported Nix
  version) → the failing package's build log (toolchain patterns) → level-1 warnings for
  cache/network causes → `BOB-E-9999` (bob bug, raw text under `-v`).
- Only the **embedded Nix** is supported. Across 2.34.8→2.34.9 the format did not drift, but the
  messages carry no codes, so every bump must re-run the capture suite.

## 5. SPEC input

### 5.1 Invocation **[proposal]**

```
nix build --no-link --print-out-paths --log-format internal-json -L --keep-going \
  [--timeout N] [--max-silent-time N] [--option max-build-log-size N] [--option max-jobs 0] <drv>^out…
```
After exit: `nix-store --check-validity --print-invalid <all bob outputs>`.

### 5.2 Error taxonomy **[proposal]**

Format `BOB-E-NNNN`. The first digit is the class. Codes are never reused or renumbered.
`{x}` = argument. Templates are the error's first line, copied from `translate/codes.go`.

| Code | Class | Message template | Default hint |
|---|---|---|---|
| 1001 | definition | `nixpkgs has no package "{attr}"` | did you mean {suggest}? |
| 1002 | definition | `nixpkgs package "{attr}" ({version}) is marked broken` | pin a nixpkgs revision where it builds, or use another package |
| 1003 | definition | `nixpkgs package "{attr}" ({version}) is marked insecure: {cves}` | use a fixed version; bob refuses known-vulnerable packages unless allowed per package |
| 1004 | definition | `nixpkgs package "{attr}" has an unfree licence` | allow it explicitly for this package if your licence permits |
| 1005 | definition | `nixpkgs package "{attr}" is not available for {system}` | — |
| 1006 | definition | `dependency cycle: {cycle}` | a package cannot depend on itself; move the shared code into its own package |
| 1007 | definition | `{label} depends on unknown package {dep}` | — |
| 1008 | definition | `{label} needs Go {want} but nixpkgs {rev} provides Go {have}` | lower GoVersion or pin a newer nixpkgs |
| 1009 | definition | `{label} has no pinned hash in bob.lock` | run bob lock |
| 1010 | definition | `{label} targets {system}, which this machine cannot build` | build it remotely (bob build --remote) or for this machine's platform |
| 2000 | build | `{label} failed to build` | — (excerpt = last 10 log lines) |
| 2001 | build | `{label}: compile error in {file}` | — |
| 2002 | build | `{label}: undefined symbol {symbol}` | the symbol is not provided by any linked library; a dependency is probably missing from Deps |
| 2003 | build | `{label}: header {header} not found` | add the package that provides {header} to Deps |
| 2004 | build | `{label}: library {lib} not found` | add the package that provides lib{lib} to Deps, or remove the link flag |
| 2005 | build | `{label}: tests failed` | run bob test {label} -v to see the full test output |
| 2006 | build | `{label}: dependency {module} needs Go {want} (toolchain is Go {have})` | use an older version of {module} or pin a newer nixpkgs |
| 2007 | build | `{label} timed out after {seconds}s` | raise the package's timeout if the step is legitimately slow |
| 2008 | build | `{label} produced no output for {seconds}s and was stopped` | a step is probably waiting for input or hanging |
| 2009 | build | `{label} tried to use the network during the build` | builds are offline; declare the download as a fetched dependency so bob.lock pins it |
| 2010 | build | `{label}: Rust crate {crate} not found` | add the crate to the package's dependencies |
| 2011 | build | `{label} wrote more than {bytes} bytes of log output and was stopped` | a step is printing in a loop; reduce its output or raise the log limit |
| 3001 | fetch | `{label}: download does not match bob.lock` | if the upstream file legitimately changed, run bob lock; otherwise the source was tampered with |
| 3002 | fetch | `{label}: download failed: {reason}` | check the URL and your network; bob retried {attempts} times |
| 3003 | fetch | `{label}: download not found (HTTP {status})` | the file was removed or the URL in bob.go is wrong |
| 4001 | cache | `cache {cache} rejected bob's credentials` | run bob login |
| 4002 | cache | `cache {cache} lists {what} but cannot deliver it (HTTP {status})` | the cache is inconsistent; … |
| 4003 | cache | `cache {cache} is unreachable: {reason}` | bob builds locally when a cache is down; … |
| 4004 | cache | `cache {cache} served {what} without a trusted signature` | the cache's signing key is not trusted; … |
| 4005 | cache | `cache {cache} served corrupted data for {what}` | report this to the cache admin; bob refused the download |
| 4006 | cache | `{label} is not in any cache and local builds are disabled` | build it remotely (bob build --remote) or allow local builds |
| 4007 | cache | `uploading {label} to the cache failed` | the build itself succeeded; the worker retries the upload |
| 5001 | upstream | `nixpkgs package {attr} failed to build from source (needed by {users})` | this is not your code: … pin another revision or ask your bob server admin |
| 6001 | resource | `no space left on device while building {what}` | free disk space (bob gc) or move bob's store to a larger disk |
| 6002 | resource | `{what} was killed: out of memory` | reduce parallel jobs (bob build -j) or raise the memory limit |
| 6003 | resource | `{what} was killed by signal {signal}` | run the build again; if it repeats, reduce parallel jobs |
| 7001 | store | `bob's store is locked by another process` | another bob command is running; … (not triggered: Nix waits instead, see activity 111) |
| 7002 | store | `bob's store is not accessible: {reason}` | bob's state directory belongs to another user or is read-only |
| 7003 | store | `bob's store is damaged: {what}` | run bob doctor --repair |
| 7004 | store | `bob's cache database is busy` | another bob process is using the same state directory |
| 8001 | interrupt | `build cancelled` | — |
| 9001 | internal | `bob's builder for {label} is broken: {reason}` | this is a bob bug; please report it with bob log {label} |
| 9002 | internal | `{label} did not produce its output` | same |
| 9003 | internal | `{label} refers to {ref}, which it must not reference` | this is a bob bug (missing fixup) |
| 9004 | internal | `bob generated an invalid build step for {label}: {reason}` | this is a bob bug; please report it with bob build -v |
| 9005 | internal | `Nix stopped unexpectedly ({reason})` | run with -v … |
| 9998 | internal | `unrecognised Nix log format (Nix {nixVersion})` | bob only supports the Nix it embeds |
| 9999 | internal | `unrecognised Nix error` | this is a bob bug; the raw message is shown with -v |

Warnings use the same codes with `severity: "warning"`. For example, 4001–4004 appear as warnings
when Nix worked around the problem by building locally.

### 5.3 Exit codes **[proposal]**

| Exit | Meaning | Classes |
|---|---|---|
| 0 | success (warnings possible) | — |
| 1 | the user's code failed to build or test | build |
| 2 | command-line usage error | — |
| 3 | definition error in bob.go / bob.lock (before building) | definition |
| 4 | download or cache failure | fetch, cache |
| 5 | a nixpkgs package failed to build from source | upstream |
| 6 | machine resources or limits (disk, memory, killed) | resource |
| 7 | bob's local store is damaged, busy or inaccessible | store |
| 70 | bob bug (internal) | internal |
| 130 | cancelled | interrupt |

With several errors, the highest priority wins: interrupt > internal > store > resource > cache >
fetch > upstream > definition > build.

### 5.4 Human output **[proposal]**

```
FAILED  //api:api

error[BOB-E-2003]: //api:api: header rcore.h not found
  --> fixture/api/cmd/api/main.go:7:10
  package defined at fixture/api/bob.go:13:11
   | cmd/api/main.go:7:10: fatal error: rcore.h: No such file or directory
   |     7 | #include "rcore.h"
   |       |          ^~~~~~~~~
  hint: add the package that provides rcore.h to Deps
  full log: bob log //api:api

bob: 1 failed (exit 1)
```

Rules:
1. Package lines come first. Failed first, then stopped, skipped, built, cached; by label.
   Non-TTY words are `FAILED`, `stopped`, `skipped`, `built`, `cached`. On a TTY the glyphs are
   `✗ - - ✓ ↓`.
2. Then one block per warning, then one per error.
3. Block layout:
   - `error[CODE]: message`
   - `--> location`, when a compiler diagnostic exists
   - `package defined at definedAt`
   - `cause:` (one line)
   - `| excerpt` lines (at most 12; store paths become repo paths, or `…/name`)
   - `hint:`
   - `full log: bob log <label>`
4. Then a summary: `bob: N failed, N skipped, N built, N cached (exit E)`.
5. `-v` adds `nix:` lines with the raw messages under each block. Only `-v` and `bob log` show raw
   Nix text.
6. Colour only on a TTY. Never with `NO_COLOR` (any value) or `TERM=dumb`. `BOB_COLOR=always`
   forces it, unless `NO_COLOR` is set.
7. Glyphs and the live status line appear only on a TTY that is not `TERM=dumb`. The live line is
   one redrawn line: `[done/total] building //x:y (12s) · downloading 3 toolchains 120 MB`.
   It comes from activities 104 (`builds` progress), 105, 108 and 101.
8. Non-TTY output prints the package lines as each package finishes.

### 5.5 `--json` event stream, schema v1 **[proposal]**

NDJSON on stdout, one object per line, always `"v":1`. Fields are only added, never renamed or
removed, within v1. Events:

| event | fields |
|---|---|
| `run.start` | `requested[]` |
| `package.start` | `label`, `action` (`build`/`download`), `cache` |
| `package.wait` | `label`, `reason` (another process builds it) |
| `upstream.start` | `name` (a nixpkgs package built from source) |
| `log` | `label`, `line` (only with `--json-log`) |
| `error` / `warning` | `error{code, severity, class, message, label, definedAt, location, cause, excerpt[], hint, log, args{}}` (`raw[]` only with `-v`) |
| `package.finish` | `label`, `status` (`built`, `cached`, `failed`, `skipped`, `cancelled`) |
| `run.finish` | `exitCode`, `counts{built,cached,failed,skipped,cancelled,upstream,downloaded}`, `drift[]` |

The product adds `time` (RFC 3339 with ms) to every event; the goldens leave it out. An example is
in `fixtures/missing-header/2.34.9/golden.jsonl`.

### 5.6 Supported Nix and drift detection **[proposal]**

- Supported: exactly the embedded Nix (2.34.9). The pattern table is versioned with the binary.
  The host Nix is never used (ADR 0002).
- Drift checks while running:
  - unknown action, activity type, result type or JSON key, or an unparsable `@nix` line →
    `drift[]` in `run.finish` and in `-v`;
  - an unmatched level-0 message → 9999;
  - a failure where every error is 9999 and drift was seen → 9998.
- Drift never turns a failure into a success (tested in `TestDrift`).
- Bump gate in CI: run `faults/make.sh` with the new Nix binary, then `go test ./translate`. The
  codes per fault in `faults/expect.json` must hold. New goldens may be blessed only if they still
  match it, and HARNESS.md protects `expect.json`.
- Toolchain bumps (gcc, rustc, go) re-capture the 2xxx faults. Compiler wording is a separate
  drift source from Nix.

### 5.7 Obligations on other parts of bob

- bob-builder:
  - write diagnostics as plain log lines `bob:: {json}` plus `@nix {"action":"setPhase","phase":"compile"}`;
  - never rely on builder-sent `start`, `result` or `stop`, because Nix drops them;
  - never use `/bin/sh` applets.
- The translator gets a lazy "drv → which bob package uses it" walk over `.drv` files for failing
  nixpkgs drvs. The spike precomputes this as `Context.Chains`.
- bob checks cache credentials itself before building (4001), because Nix only warns.
- bob gives `--timeout` and `--max-silent-time` different values, or records which one it set,
  because the message is identical.

## 6. Decisions, open risks, still to verify

### Agent decisions

| Decision | Reason |
|---|---|
| Fault injection through a spike-only `script` IR kind instead of real bob-builder steps | The spike measures Nix's reporting, not the builder; B has no bob-builder yet. |
| `definedAt` comes from illustrative `fixture/*/bob.go` positions | B's helper records `fixture.go:NN`; real positions show the format. |
| Cache faults use separate chroot stores (`local?root=/root/scratch-s15/stores/…`) | No root-store GC or shared-path changes (BRIEF). |
| A 401 with local builds on is a warning, not an error | DX: the build works. The user still learns that the cache is broken (principle 3). |
| A signal 9 on a builder is classified as out of memory | It was true in every capture. The cause text says "almost always". A cgroup `memory.events` check would make it exact (open item). |
| crates.io's 403 for a missing file is treated as "not found" | Measured: static.crates.io (S3) returns 403. |
| An unknown message in a successful run is a warning | Builders can inject level-0 messages (measured). |

### Owner decision needed

None. The exit-code numbers and the code ranges are SPEC proposals. They are not architecture.

### Open risks and to verify

1. **Message text is the contract.** Nix has no error codes. A Nix bump can silently change
   wording; this is mitigated only by the capture suite. Upstream structured errors would remove
   the risk.
2. **Attribution without a drv name.** Timeouts and interrupts name no derivation. Attribution
   relies on "stopped, no output, no other error". If two builds time out at once, both are
   reported. With remote builders (not used) this would be unverified.
3. **Spoofing.** A build can print `@nix {"action":"msg","level":0,…}` and it reaches bob as a Nix
   error. On the server, per-job Nix processes keep spoofing inside one job (S13, S10 should note
   it).
4. **Not captured:** a corrupted file inside a valid store path (Nix does not check content on use;
   needs `bob doctor` / `nix store verify`), SQLite busy (7004, only from a shared `XDG_CACHE_HOME`,
   which B avoids), daemon mode (bob does not use a daemon), real Ubuntu/AppArmor setups, x86_64,
   TLS errors.
5. OOM vs other SIGKILL needs a cgroup check to be exact. The `OOMPolicy=stop` race (fault 34) gives
   different codes on different runs.
6. The rootless namespace path (ADR 0002) was not used for captures (root single-user store). The
   message formats are the same binary, but the namespace setup errors are covered only by S0b.
7. Toolchain diagnostics were captured only for gcc 15.3, rustc 1.95, go 1.26.8.
