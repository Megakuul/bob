# §02 — CLI, errors and developer experience

> Source of SPEC.md: `/root/bob/SPEC.md` is generated from spec/01…06 by concatenation (command in the SPEC.md preamble). Edit this file, then regenerate.

Scope: the `bob` command line (commands, flags, output, exit codes), the error taxonomy and its
message templates, `bob dev`, `bob env`, the shell hook, `.bob/bin` shims, the LSP
path-rewriting proxy, editor settings, `bob install`, and `--remote`.
ADR: 0017 (Accepted, locked). Builds on ADR 0002 Decision 5 (locked), D51, D59.

## Interfaces

Provided by §02:
- `diag.Error`, `diag.Code`, `diag.Class`, and the code table (§02.3). Every section raises
  errors through them.
- The `--json` event schema v1 (§02.4). §06 reuses `package.*`/`error` for remote build events.
- `.bob/env.json` (§02.7.2), the shim templates (§02.7.3) and `bob exec [--lsp]` (§02.7.4).
- The editor setting templates (§02.7.6).
- The install profile layout and `bob install` (§02.8).

Expected from other sections:
- **§01:** `bob.FindRoot(dir) (root string, err error)` (the repo root = the nearest ancestor
  that contains `bob.lock`); the evaluator with its no-op key (`EvalKey`); `DevShell` (name,
  toolchain packages, extra packages, env vars, hooks, dev servers); labels and label patterns;
  `definedAt` positions; the translator `Context` inputs (drv → package map); the namespace entry
  (`ns.Enter`), the state dir layout (`$XDG_DATA_HOME/bob`, default `~/.local/share/bob`), the
  bob-owned `nix.conf`/netrc writer; the R-NIX-022 fix text; and the pre-checks that raise
  1xxx codes.
- **§03:** per-ecosystem working-tree preparation (`Prepare(ctx, shell) error`: native files,
  materialised codegen, `node_modules`); the list of extra in-tree tools (e.g.
  `node_modules/.bin/*`); the dev server contract (Vite wrapper config, A20); the toolchain
  diagnostic patterns for 2xxx codes.
- **§04:** `--platform` / `--variant` value sets; release artifact kinds; the entry binaries of a
  closure release.
- **§05:** `BuildRecord` / `JobRecord` state names for `bob build --remote` progress; the live
  log pull (D55).
- **§06:** OAuth endpoints `/oauth/authorize`, `/oauth/token` (code + PKCE, refresh),
  `/oauth/device/code`, `/oauth/revoke` (§6.3.2); RPCs `AuthService/{WhoAmI, GetServerInfo}`,
  `BuildService/{CreateBuild, WatchBuild, CancelBuild}`, `LogService/{GetLog, FollowLog}`,
  `ReleaseService/{ResolveRelease, GetRelease}`; the source upload `PUT /upload/source/sha256/<hex>`
  (§6.9.7); the `/nix/*` cache through the download proxy (D61), the per-path visibility
  documents (D58, §6.6.2). (Names aligned at integration.)

---

## 02.1 Command line grammar

```
bob [global flags] <command> [subcommand] [flags] [args] [-- passthrough args]
```

- Flags may appear anywhere before `--`. Everything after `--` goes unchanged to the program
  (`run`, `test`, `exec`, `dev`).
- Long flags are `--name`, `--name=value` or `--name value`. The only short flags are `-v`,
  `-j`, `-C`, `-f` and `-q`.
- Unknown command or flag → `BOB-E-0001`, exit 2. A close match is suggested
  (Levenshtein ≤ 2).
- Implementation: the standard library only, with bob's own small parser **[proposal]**.

### Global flags

| Flag | Meaning |
|---|---|
| `-C <dir>` | run as if started in `<dir>` |
| `-v` | add raw detail (`nix:` lines, raw build log excerpts) |
| `-vv` | also bob's debug log on stderr |
| `-q` | errors and the summary only |
| `--json` | NDJSON events on stdout (§02.4); human output is suppressed |
| `--json-log` | with `--json`: also emit `log` events with build log lines |
| `--color=auto\|always\|never` | default `auto` (TTY rules in §02.5) |
| `-j <n>` | parallel local builds (Nix `max-jobs`); default = number of CPUs |
| `--offline` | no network: substituters off, no registry revalidation |
| `--server <url>` | pick a logged-in server when there are several (default: the one marked default) |

### Commands

| Command | Synopsis | Requirement |
|---|---|---|
| `build` | `bob build [patterns…] [--remote] [--platform P] [--variant V] [--print-paths] [--keep-going=true] [--lock]` | R-CLI-001, R-DEV-005 |
| `test` | `bob test [patterns…] [--remote] [--rerun] [--lock] [--require-all-tests] [-- test args]` (`--require-all-tests`: a test that cannot run on this machine, §04 R-PLAT, fails with exit 1 instead of `skipped`) | R-CLI-001 |
| `run` | `bob run <label> [--platform native] [-- args]` | R-CLI-001 |
| `dev` | `bob dev [--shell <name>] [--no-servers] [--servers-only] [-- cmd args]` | R-DEV-001/002 |
| `env` | `bob env [--shell <name>] [--format sh\|fish\|json] [--hook=bash\|zsh\|fish]` | R-DEV-003 |
| `hook` | `bob hook bash\|zsh\|fish` | R-DEV-003 |
| `allow` / `deny` | `bob allow [dir]`, `bob deny [dir]` | R-DEV-009 |
| `exec` | `bob exec [--lsp] [--project <root>] [--shell <name>] -- <tool> [args]` | R-DEV-008 |
| `editor` | `bob editor vscode\|nvim\|helix\|zed\|jetbrains [--write]` | R-DEV-011 |
| `check` | `bob check [--fix]` | R-CLI-001, D2 |
| `gen` | `bob gen [patterns…] [--check] [--force]`: writes native tooling files and materialised codegen (§03.0.2, §03.6.6) | R-FW-006/007 |
| `status` | `bob status [--json]`: per generated root `fresh\|stale\|missing\|edited\|orphan` (§03.6.6) and lock freshness | R-FW-007 |
| `lock` | `bob lock [--update [names…]] [--nixpkgs <rev>] [--offline]` | R-CLI-001 |
| `graph` | `bob graph [patterns…] [--why <from> <to>] [--rdeps] [--depth N] [--format text\|dot\|json]` | R-CLI-001 |
| `log` | `bob log <label> [--build <id>] [-f] [--attempt N]` | R-CLI-001, R-LOG-003 |
| `cancel` | `bob cancel <build-id>` | R-DEV-005 |
| `pipeline run` | `bob pipeline run <name> [--remote]` | R-FW-041 |
| `login` / `logout` | `bob login [<url>] [--device] [--token-stdin]`, `bob logout [<url>]` | R-CLI-003 |
| `install` | `bob install <ref> [--as <name>] [--bin-dir <dir>]` | R-INST-001 |
| `uninstall` / `upgrade` / `rollback` / `list` | see §02.8 | R-INST-001 |
| `registry` | `bob registry add <name> <url> \| remove <name> \| list \| default <name>` | R-INST-004 |
| `completion` | `bob completion bash\|zsh\|fish` | R-CLI-004 |
| `gc` | `bob gc [--dry-run]`: collects garbage in bob's store (keeps install generations, the dev shells of projects used in the last 30 days, `.bob` roots) | hint target of 6001 |
| `doctor` | `bob doctor [--repair]`: namespace check, store verify, config check | hint target of 7003 |
| `version` | `bob version [--json]`: bob version, embedded Nix version, nixpkgs default pin | — |

Label patterns: §01 (`//...`, `//libs/...`, `//libs/foo:all`). With no pattern, `build`/`test`
use `//...` below the current directory.

#### R-CLI-001 Command set
- **Statement:** `bob` provides every command in the table above with the given synopsis.
  `bob help <command>` and `bob <command> --help` print the synopsis and the flags.
- **Acceptance:** Given the harness command list, when it runs `bob <cmd> --help` for each one,
  then each exits 0 and prints a `usage: bob <cmd>` line that matches the table. When it runs
  `bob frobnicate`, bob exits 2 with `BOB-E-0001` and `did you mean …?` when a command is within
  edit distance 2.
- **Source:** R-CLI-001, D9, D10, ADR 0017.

#### R-CLI-005 Commands work without a server
- **Statement:** Every command except `login`, `logout`, `--remote`, `cancel`, `install`
  (registry), `upgrade`, and `log --build` works with no server configured.
- **Acceptance:** Given a fresh HOME with no `credentials.json`, `bob build //...`,
  `bob test //...`, `bob dev -- true`, `bob check` and `bob graph` on the fixture exit 0. Given the
  same HOME, `bob build --remote` exits 4 with `BOB-E-4009` (`no bob server configured`).
- **Source:** R-FW-009, D37.

---

## 02.2 Exit codes

| Exit | Meaning | Classes |
|---|---|---|
| 0 | success (warnings possible) | — |
| 1 | the user's code failed to build or test | build (2xxx) |
| 2 | command-line usage error | usage (0xxx) |
| 3 | definition error in bob.go / bob.lock (before building) | definition (1xxx) |
| 4 | download, cache, server or registry failure | fetch (3xxx), cache/server (4xxx) |
| 5 | a nixpkgs package failed to build from source | upstream (5xxx) |
| 6 | machine resources or limits (disk, memory, killed) | resource (6xxx) |
| 7 | bob's local store or host setup is damaged, busy or inaccessible | store/host (7xxx) |
| 70 | bob bug (internal) | internal (9xxx) |
| 130 | cancelled (SIGINT/SIGTERM) | interrupt (8xxx) |

- With several errors, the highest priority wins: interrupt > internal > store > resource > cache
  > fetch > upstream > definition > build > usage.
- `bob run` and `bob exec`: once the program has started, bob exits with the program's own exit
  status (signal N → 128+N). bob's codes apply only to failures before the start.

#### R-CLI-006 Exit codes
- **Statement:** bob's exit status is a function of the error classes raised, as in the table and
  priority order above.
- **Acceptance:** For every fault in the fault catalogue (HARNESS, from S15 `faults/expect.json`
  plus the §02 additions), the harness checks the exit status against the expected value. Given
  `bob run //tools:exit3` (a program that exits 3), bob exits 3. Given a program killed by
  SIGKILL, bob exits 137.
- **Source:** S15 §5.3, ADR 0017.

---

## 02.3 Error taxonomy

Format `BOB-E-NNNN`. The first digit is the class. Codes are never reused or renumbered.
`{x}` is an argument. The template is the error's first line; the hint is the default hint.
1001–9999 come from S15 §5.2 (`spikes/s15-errors/translate/codes.go`). Codes marked **new** are
added by §02.

| Code | Class | Message template | Default hint |
|---|---|---|---|
| 0001 | usage | `unknown {kind} "{name}"` | did you mean {suggest}? / run bob help |
| 0002 | usage | `not inside a bob repository (no bob.lock in {dir} or any parent)` | run bob from your project, or use -C <dir> |
| 0003 | usage | `pattern {pattern} matches no package` | run bob graph to list packages |
| 0004 | usage | `{flag} needs {what}` | — |
| 0005 | usage | `{label} is not runnable` | bob run needs a binary package |
| 0006 **new** | usage | `{label} does not support {platform} (it allows: {platforms})` | pick a supported platform, or build without --platform |
| 0007 **new** | usage | `variants "{a}" and "{b}" conflict: {reason}` | drop one of them |
| 0008 **new** | usage | `unsupported host architecture "{arch}" (bob runs on linux-amd64 and linux-arm64)` | — |
| 0009 **new** | usage | `{ref} has nothing to install (it contains: {kinds}); download its artifacts from {url}` | — |
| 0010 **new** | usage | `unknown platform "{platform}" (known: {known})` | — |
| 0011 **new** | usage | `{label}: {ecosystem} packages cannot be cross-compiled yet (requested {platform} on a {host} host)` | build for the host platform, or use an ecosystem that supports cross-compilation |
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
| 1011 **new** | definition | `generated file {file} was edited by hand` | move the change into bob.go; bob check --fix regenerates the file |
| 1012 **new** | definition | `bob.lock is out of date: {what}` | run bob lock |
| 1013 **new** | definition | `{path} is not ignored by git` | bob check --fix adds it to bob's block in .git/info/exclude (§03.0.2; bob never edits a user's .gitignore) |
| 1014 **new** | definition | `no dev shell "{name}" (have: {names})` | — |
| 1015 **new** | definition | `cannot resolve {ecosystem} dependencies for {root}: {reason}` | relax the conflicting requirement named above |
| 1016 **new** | definition | `{path} conflicts with generated output of {label}: {reason}` | move or delete the file, or run bob gen --force (keeps a backup) |
| 1017 **new** | definition | `nixpkgs package "{attr}" cannot be evaluated on {system}` | pin another nixpkgs revision or use another package |
| 1018 **new** | definition | `evaluating bob.go is not deterministic: {label}` | bob.go must not depend on time, randomness, environment or map order |
| 1019 **new** | definition | `bob.lock cannot be read: {reason} (line {line})` | resolve the merge conflict, or run bob lock --rebuild to resolve everything again from bob.go |
| 1020 **new** | definition | `{label}: invalid {option}: {reason}` | — (option validation: image `MaxLayers`, forbidden compiler flags, release version, unsupported cross target) |
| 1021 **new** | definition | `{label}: toolchain mismatch: {reason}` | align the versions named above (Go/Rust/generator runtime/target glibc) |
| 1022 **new** | definition | `{label} is not a static binary (it needs {deps})` | use tarball.Closure |
| 1023 **new** | definition | `{package} cannot be pinned: {reason}` | — (no checksum; no nixpkgs mapping for an opam `conf-*` package) |
| 1024 **new** | definition | the rule's text from §01 R-FW-002 (R1–R7), e.g. `{dir}/bob.go: the first line must be "//go:build bob" …` | — (all violations are listed, sorted by path) |
| 1025 **new** | definition | `{pos}: source pattern "{pattern}" leaves the repository` / `{pos}: bob.Glob("{pattern}") matches no files` / `{file}: symlink points outside the repository` | — |
| 1026 **new** | definition | `nixpkgs mismatch: {what}` | the fix named by the check (set Libs: cc.CrossLibs on the packages that link the library, or move the nixpkgs pin; R-PLAT-007) |
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
| 2012 **new** | build | `dev server {name} exited with status {status}` | see {log} |
| 2013 **new** | build | `{label}: {package} needs its {script} script ({reason})` | add npm.AllowScripts("{package}") to {label} |
| 2014 **new** | build | `{label}: {package} uses {dep} without declaring it` | add npm.Extend("{package}", npm.Peer("{dep}")) |
| 2015 **new** | build | `{label}: prune pattern "{pattern}" removes {file}, which {user} needs` | narrow the pattern |
| 3001 | fetch | `{label}: download does not match bob.lock` | if the upstream file legitimately changed, run bob lock; otherwise the source was tampered with |
| 3002 | fetch | `{label}: download failed: {reason}` | check the URL and your network; bob retried {attempts} times |
| 3003 | fetch | `{label}: download not found (HTTP {status})` | the file was removed or the URL in bob.go is wrong |
| 3004 **new** | fetch | `{label}: archive {url} contains an unsafe entry {path} ({reason})` | the archive is malformed or hostile; bob refused it |
| 4001 | cache | `cache {cache} rejected bob's credentials` | run bob login |
| 4002 | cache | `cache {cache} lists {what} but cannot deliver it (HTTP {status})` | the cache is inconsistent; report it to the cache admin |
| 4003 | cache | `cache {cache} is unreachable: {reason}` | bob builds locally when a cache is down |
| 4004 | cache | `cache {cache} served {what} without a trusted signature` | the cache's signing key is not trusted; check bob registry list |
| 4005 | cache | `cache {cache} served corrupted data for {what}` | report this to the cache admin; bob refused the download |
| 4006 | cache | `{label} is not in any cache and local builds are disabled` | build it remotely (bob build --remote) or allow local builds |
| 4007 | cache | `uploading {label} to the cache failed` | the build itself succeeded; the worker retries the upload |
| 4008 **new** | server | `your login to {server} has expired or is invalid` | run bob login {server} |
| 4009 **new** | server | `no bob server configured` / `bob server {server} is unreachable: {reason}` | run bob login <url> / check the URL and your network |
| 4010 **new** | server | `{server} denied {action} on project {project}` | ask an admin for the permission {permission} |
| 4011 **new** | server | `release {ref} not found on {registry}` | bob list --available {project} shows the versions |
| 4012 **new** | server | `remote build {id} failed on the server: {reason}` | bob log --build {id} |
| 5001 | upstream | `nixpkgs package {attr} failed to build from source (needed by {users})` | this is not your code: pin another revision or ask your bob server admin |
| 6001 | resource | `no space left on device while building {what}` | free disk space (bob gc) or move bob's store to a larger disk |
| 6002 | resource | `{what} was killed: out of memory` | reduce parallel jobs (bob build -j) or raise the memory limit |
| 6003 | resource | `{what} was killed by signal {signal}` | run the build again; if it repeats, reduce parallel jobs |
| 7001 | store | `bob's store is locked by another process` | another bob command is running |
| 7002 | store | `bob's store is not accessible: {reason}` | bob's state directory belongs to another user or is read-only |
| 7003 | store | `bob's store is damaged: {what}` | run bob doctor --repair |
| 7004 | store | `bob's cache database is busy` | another bob process is using the same state directory |
| 7005 **new** | host | `bob cannot create its private /nix: unprivileged user namespaces are blocked on this system` (+ `cause:` line; block of §01 R-NIX-022) | `fix (run once as an administrator):` + the exact fix of §01 R-NIX-022 / ADR 0002 Decision 4 |
| 7006 **new** | host | `shim {shim} belongs to {root}, which has no bob environment` | run bob env in {root}, or delete {shim} |
| 7007 **new** | host | `storage backend {endpoint} is not supported: {reason}` | bob-server needs an S3 backend with conditional writes (D63) |
| 7008 **new** | host | `{bucket} bucket policy is too permissive: {reason}` | apply the shipped policy (§05.2.3) |
| 7009 **new** | host | `the worker must run as root; builds themselves never run as root` | start bob-server worker as root (D56) |
| 7010 **new** | host | `worker cannot isolate builds: {reason}` | see the worker host requirements (§05.8) |
| 8001 | interrupt | `build cancelled` | — |
| 9001 | internal | `bob's builder for {label} is broken: {reason}` | this is a bob bug; please report it with bob log {label} |
| 9002 | internal | `{label} did not produce its output` | this is a bob bug; please report it with bob log {label} |
| 9003 | internal | `{label} refers to {ref}, which it must not reference` | this is a bob bug (missing fixup) |
| 9004 | internal | `bob generated an invalid build step for {label}: {reason}` | this is a bob bug; please report it with bob build -v |
| 9005 | internal | `Nix stopped unexpectedly ({reason})` | run with -v and report it |
| 9006 **new** | internal | `quirks entry {entry} has no justification or passing test` | (CI check over the quirks tables, §03.0.6) |
| 9007 **new** | internal | `embedded builder does not match its recorded hash` | this is a bob bug (stale builders.go table) |
| 9998 | internal | `unrecognised Nix log format (Nix {nixVersion})` | bob only supports the Nix it embeds |
| 9999 | internal | `unrecognised Nix error` | this is a bob bug; the raw message is shown with -v |

Warnings use the same codes with `severity: "warning"`. For example, 4001–4004 are warnings
when Nix worked around the problem by building locally (S15 fault 43).
S12's `BOB-AUTH-001` is renamed to `BOB-E-4008`. 7007–7010 are raised by `bob-server` (exit 7), except start-up refusals (unsupported backend, policy self-check), which exit 78 (`EX_CONFIG`).

#### Cross-section code assignment (integration)

Every error text in §01, §03, §04 and §05 uses the code below; their prose names stay as
aliases only in this table.

| Name / text in a section | Code |
|---|---|
| §03 `E-DRIFT`; "go.mod or go.sum was changed outside bob" | 1011 |
| §03 `E-LOCK-STALE`; §01 "bob.lock is stale" | 1012 (entry missing for one label: 1009) |
| §03 `E-RESOLVE` | 1015 |
| §03 `E-FETCH-HASH`; §01 fetcher mismatch | 3001 |
| §03 `E-GEN-CONFLICT`; nested/shared generated dirs; user-owned `.cargo/config.toml` | 1016 |
| §03 `E-SCRIPT-DENIED` | 2013 |
| §03 `E-PHANTOM-DEP` | 2014 |
| §03 `E-QUIRK-TEST` | 9006 |
| §03 forbidden compiler flag; §04 `MaxLayers`, roots > MaxLayers, invalid release version | 1020 |
| §03 Go module needs newer Go | 2006 (at build) / 1008 (at evaluation); Rust mixed toolchains, generator/runtime mismatch | 1021
| §04 nixpkgs target-library / rustc mismatch (R-PLAT-007) | 1026 |
| §04 OCaml (native-only) requested for another platform (R-PLAT-005); unknown platform (R-PLAT-001) | 0011 / 0010 | |
| §03 opam package without checksum; `conf-*` without mapping | 1023 |
| §03 codegen generator failed | 2000 |
| §01 nixpkgs attribute cannot be evaluated | 1017 |
| §01 R1–R7 `bob.go` rule violations (R-FW-002) | 1024 |
| §01 source pattern leaves the repository / matches no files / symlink outside the repo (R-FW-021) | 1025 |
| §02 duplicate dev-shell names | 1020 |
| §01 non-deterministic evaluation | 1018 |
| §01 unreadable bob.lock | 1019 |
| §01 unsafe archive entry | 3004 |
| §01 embedded builder mismatch | 9007 |
| §01 user namespaces blocked | 7005 |
| §04 unknown platform / unknown variant token | 0001 (`kind` = platform / variant) |
| §04 package restricted to other platforms | 0006 |
| §04 conflicting profiles; Go `race` + `asan` | 0007 |
| §04 unsupported host architecture | 0008 |
| §04 non-static tarball | 1022 |
| §04 prune removes a needed library | 2015 |
| §05 unsupported storage backend / permissive policy / worker not root / no isolation | 7007 / 7008 / 7009 / 7010 |
| §02/§04 install of a release without closure artifacts | 0009 |

The server API uses its own `ErrorDetail.code` namespace `BOB-API-NNNN` (§06 §6.1). The CLI maps
API errors to BOB-E codes: Unauthenticated → 4008; PermissionDenied → 4010; NotFound of a
release → 4011, of anything else → 4010 (no existence leak); Unavailable/ResourceExhausted or no
connection → 4009; a remote build that ends FAILED → 4012 plus the worker's own BOB-E errors
(§05 `JobResult.Error`); Internal → 4012 with the server's `ref` id.

### Go types (`pkg/bob/diag`) [proposal]

```go
// Package diag defines bob's user-facing errors. Every error a user sees is a *Error with a
// stable Code; raw tool output is kept in Raw and shown only with -v.
package diag

// Code is a stable error number, e.g. 2003 for "header not found". Codes are never reused.
type Code uint16

// Class is the first digit of a Code. It decides the exit status (see ExitCode).
type Class uint8

const (
	ClassUsage      Class = 0
	ClassDefinition Class = 1
	ClassBuild      Class = 2
	ClassFetch      Class = 3
	ClassCache      Class = 4 // cache and server
	ClassUpstream   Class = 5
	ClassResource   Class = 6
	ClassStore      Class = 7 // store and host setup
	ClassInterrupt  Class = 8
	ClassInternal   Class = 9
)

// Severity is "error" or "warning".
type Severity string

// Pos is a source position in the repository, relative to the repo root ("libs/foo/bob.go:12:3").
type Pos struct {
	File      string
	Line, Col int
}

// Error is one diagnostic. Message and Hint come from the code's templates filled with Args.
type Error struct {
	Code      Code
	Severity  Severity
	Label     string            // package label, "" when not about a package
	DefinedAt Pos               // where the package was defined (bob.go position)
	Location  *Pos              // compiler diagnostic position, when one exists
	Cause     string            // one line
	Excerpt   []string          // ≤ 12 lines; store paths already rewritten (§02.5 rule 3)
	Args      map[string]string // template arguments
	Log       string            // "bob log //x:y" command, when a log exists
	Raw       []string          // raw Nix/tool text; output only with -v
}

// New returns an Error for code with the template arguments in args (key, value, key, value…).
// It panics if code is not in the table or an argument of the template is missing (tested).
//
//	err := diag.New(2003, "label", "//api:api", "header", "rcore.h")
//	err.Error() == "//api:api: header rcore.h not found"
func New(code Code, args ...string) *Error

// Warn is New with Severity "warning".
//
//	w := diag.Warn(4001, "cache", "bob.example.com")
func Warn(code Code, args ...string) *Error

// Error returns the filled message template (the first line of the human block).
func (e *Error) Error() string

// Class returns the code's class.
func (c Code) Class() Class

// ExitCode returns the exit status for a set of errors, using the priority order of §02.2.
// No errors (or warnings only) → 0.
//
//	diag.ExitCode([]*diag.Error{diag.New(2001, …), diag.New(4003, …)}) == 4
func ExitCode(errs []*Error) int
```

#### R-NIX-010 No raw Nix text by default
- **Statement:** No Nix message text reaches the user's default output (human or `--json`
  without `-v`). Every unmatched Nix error becomes `BOB-E-9999` and needs a regression fixture.
  The only Nix *evaluation* bob performs is `resolve.nix` (§01 §1.15); an error from it that is
  not classified per R-NIX-027 is a bob bug (9999) with a regression test.
- **Acceptance:** For each captured fault stream (≥ 48 faults × the embedded Nix), replayed
  through the translator, the default human output and the `--json` output contain none of these
  leak patterns: `^error:`, `Cannot build`, `Reason:`, `\.drv\b`, `/nix/store/[0-9a-z]{32}`,
  `builder for`, `derivation '`, `@nix `, `while evaluating`, and Nix source positions
  `/nix/store/…-source/….nix:`. With `-v`, the raw message appears in `nix:` lines (or
  in `raw[]`). An injected fault with a message missing from the pattern table yields
  `BOB-E-9999`, exit 70.
- **Source:** R-NIX-010, ADR 0017, S15 §3.2; merged with §01's evaluation part (integration).

#### R-NIX-012 Failures in bob terms
- **Statement:** A build failure is reported with the package label, the `bob.go` position where
  the package was defined, a log excerpt with repo paths, and a hint where one is known.
- **Acceptance:** For faults 6–15 and 22–33 of the catalogue, the golden output's error block
  always has `error[BOB-E-<code>]: <template>` and `package defined at <bob.go>:<line>:<col>`. The
  hint line is present iff the error table row has a hint; the excerpt lines (≥ 1) and
  `full log: bob log <label>` are present iff a builder log exists (a failure detected by bob
  before or without a builder run has neither). The block is byte-identical to
  `fixtures/<fault>/<nixver>/golden.txt`. A fault with `full log:` but no excerpt is a documented
  exception in the test (listed with its fault number), not a loosened rule.
- **Source:** R-NIX-012, S15 §5.4.

#### R-NIX-028 Error code stability
- **Statement:** The code table is append-only. Removing or changing the meaning of a code fails
  CI. Templates may be reworded only with a golden update.
- **Acceptance:** The harness compares `diag` codes with the protected `codes.lock` (code, class,
  template argument names). A removed code, a changed class or a changed argument set fails the
  check. `diag.New` panics on an unknown code or a missing argument; a test calls `New` for every
  code with its arguments.
- **Source:** ADR 0017 Decision 5.

#### R-NIX-029 Credential pre-check
- **Statement:** Before any Nix call that may substitute from a bob server, bob calls
  `AuthService/WhoAmI` (connect GET `/api/bob.auth.v1.AuthService/WhoAmI`) with the stored token, once per process. Unauthenticated (HTTP 401) → `BOB-E-4008`, PermissionDenied (403) →
  `BOB-E-4010`, unreachable → warning `4009` and the server's substituter is left out for this
  run.
- **Acceptance:** Given a server that returns 401 on `WhoAmI`, when `bob build //...` runs
  with a warm local store, then bob exits 4 with `BOB-E-4008` before Nix starts (no Nix process
  spawned, checked with a process counter). Given an unreachable server and a buildable target,
  bob prints warning 4009 and exits 0.
- **Source:** S12 §5, S15 fault 42/43, D61.

---

## 02.4 `--json` event stream, schema v1

NDJSON on stdout, one object per line. Every event has `"v":1`, `"event"` and `"time"` (RFC 3339
UTC with ms). Within v1, fields and events are only added, never renamed or removed. Nothing else
is written to stdout in `--json` mode; stderr gets only bob's own debug lines (`-vv`).

| event | fields |
|---|---|
| `run.start` | `command`, `args[]`, `requested[]` (labels after pattern expansion), `bob`, `nix` (versions) |
| `package.start` | `label`, `action` (`build`/`download`/`test`), `cache` (substituter name or `""`) |
| `package.wait` | `label`, `reason` |
| `upstream.start` | `name` (a nixpkgs package built from source) |
| `log` | `label`, `line` (only with `--json-log`) |
| `error` / `warning` | `error{code, severity, class, message, label, definedAt, location, cause, excerpt[], hint, log, args{}}`, `raw[]` only with `-v` |
| `note` **new** | `type`, `code` (string), `message`, optional `label`, `attr`, `platform`, `field`: a non-error remark that precedes the build it concerns (an uncached target library built from source, a dropped variant); the human format prints `note: <message>` on stderr |
| `test.result` | `label`, `status` (`pass`/`fail`/`cached`/`skipped`), `durationMs`, `failed[]` (test names, when the runner reports them), `runner` (**new**: `qemu-<arch>` when a cross test ran under emulation), `reason` (**new**: why a test is `skipped`, e.g. `not run: needs a native linux-amd64 machine`, R-FW-039) |
| `package.finish` | `label`, `status` (`built`, `cached`, `failed`, `skipped`, `cancelled`), `paths[]` (host paths, only with `--print-paths`) |
| `remote.submitted` **new** | `server`, `buildId`, `url` |
| `remote.state` **new** | `buildId`, `state` (§05 build states) |
| `result` **new** | `command`, `data` (command-specific object, below) |
| `run.finish` | `exitCode`, `counts{built,cached,failed,skipped,cancelled,upstream,downloaded}`, `drift[]`, `durationMs` |

`result.data` per query command:

| Command | `data` |
|---|---|
| `graph` | `{nodes:[{label, kind, definedAt}], edges:[{from, to}], why:[[label…]…]}` |
| `env` | `{root, shell, outside:{set:{K:V}, prepend:{PATH:[…]}}, insideOnly:[K…], tools:[name…]}` |
| `check` | `{findings:[error objects]}` (also emitted as `error` events) |
| `lock` | `{changes:[{ecosystem, name, from, to, op:"add"\|"update"\|"remove"}]}` |
| `list` | `{installed:[{name, ref, version, generation, bins:[…]}]}` |
| `version` | `{bob, nix, nixpkgs}` |
| `login` | `{server, user, expires}` |

Example (`bob build //api:api --json`, missing header):

```
{"v":1,"event":"run.start","time":"2026-10-21T10:00:00.000Z","command":"build","args":["//api:api"],"requested":["//api:api","//libs/foo:foo"],"bob":"0.1.0","nix":"2.34.9"}
{"v":1,"event":"package.start","time":"…","label":"//libs/foo:foo","action":"download","cache":"cache.nixos.org"}
{"v":1,"event":"package.finish","time":"…","label":"//libs/foo:foo","status":"cached"}
{"v":1,"event":"package.start","time":"…","label":"//api:api","action":"build","cache":""}
{"v":1,"event":"error","time":"…","error":{"code":"BOB-E-2003","severity":"error","class":"build","message":"//api:api: header rcore.h not found","label":"//api:api","definedAt":"api/bob.go:13:11","location":"api/cmd/api/main.go:7:10","cause":"","excerpt":["cmd/api/main.go:7:10: fatal error: rcore.h: No such file or directory"],"hint":"add the package that provides rcore.h to Deps","log":"bob log //api:api","args":{"header":"rcore.h","label":"//api:api"}}}
{"v":1,"event":"package.finish","time":"…","label":"//api:api","status":"failed"}
{"v":1,"event":"run.finish","time":"…","exitCode":1,"counts":{"built":0,"cached":1,"failed":1,"skipped":0,"cancelled":0,"upstream":0,"downloaded":1},"drift":[],"durationMs":2140}
```

#### R-CLI-002 Output modes
- **Statement:** Default output is concise (§02.5); `-v` adds raw detail; `--json` emits
  schema-v1 events and nothing else on stdout.
- **Acceptance:**
  - Every `--json` line of every golden run parses as JSON, has `v`=1, a known `event`, and
    validates against the protected JSON Schema `schema/events-v1.json`.
  - `run.finish` is the last line and its `exitCode` equals the process exit status.
  - Default output of a successful no-op build has ≤ 2 lines (a package line per requested label
    is suppressed when all are `cached` and stdout is not a TTY: only the summary line).
  - Every golden also exists with `-v` and differs from the default only by added lines.
- **Source:** R-CLI-002, S15 §5.5.

---

## 02.5 Human output

Error block and summary (S15 §5.4, unchanged):

```
FAILED  //api:api

error[BOB-E-2003]: //api:api: header rcore.h not found
  --> api/cmd/api/main.go:7:10
  package defined at api/bob.go:13:11
   | cmd/api/main.go:7:10: fatal error: rcore.h: No such file or directory
   |     7 | #include "rcore.h"
   |       |          ^~~~~~~~~
  hint: add the package that provides rcore.h to Deps
  full log: bob log //api:api

bob: 1 failed, 0 skipped, 0 built, 1 cached (exit 1)
```

Rules:
1. Package lines first: failed, then stopped, skipped, built, cached; sorted by label within each
   group. The words without a TTY are `FAILED`, `stopped`, `skipped`, `built`, `cached`; on a
   TTY the glyphs are `✗ - - ✓ ↓`. Without a TTY, package lines are printed as each package
   finishes.
2. Then one block per warning, then one block per error. Block lines, in order: `error[CODE]:` /
   `warning[CODE]:`, `-->`, `package defined at`, `cause:`, `|` excerpt (≤ 12 lines), `hint:`,
   `full log:`. Lines with no content are left out.
3. In excerpts, store paths are rewritten: a path inside an imported source becomes its repo
   path; any other `/nix/store/<hash>-<name>/<rest>` becomes `…/<name>/<rest>`.
4. Summary: `bob: N failed, N skipped, N built, N cached (exit E)`. Success without failures:
   `bob: N built, N cached`. For `test`: `bob: N passed, N failed, N cached (exit E)`.
5. `-v` adds `  nix: <raw line>` lines at the end of each block.
6. Colour only when stdout is a TTY and `TERM` is not `dumb`. Never with `NO_COLOR` set (any
   value). `BOB_COLOR=always` or `--color=always` forces it, but `NO_COLOR` still wins.
   Colours: error red bold, warning yellow bold, label bold, hint cyan, `-->` blue.
7. Live status line (TTY only, not `TERM=dumb`, not `-q`): one line on stderr, redrawn at most
   10×/s: `[done/total] building //x:y (12s) · downloading 3 toolchains 120 MB`. It is cleared
   before any other output.

Other output samples:

```
$ bob test //libs/...
PASS    //libs/foo:test (1.2s)
cached  //libs/bar:test
FAIL    //libs/greet:test (0.4s)

error[BOB-E-2005]: //libs/greet:test: tests failed
  package defined at libs/greet/bob.go:20:9
   | --- FAIL: TestHello (0.00s)
   |     greet_test.go:9: got "hi", want "hello"
  hint: run bob test //libs/greet:test -v to see the full test output
  full log: bob log //libs/greet:test

bob: 1 passed, 1 failed, 1 cached (exit 1)
```

```
$ bob graph --why //cmd/api:api nixpkgs:openssl
//cmd/api:api                   cmd/api/bob.go:9:5
└─ //libs/foo:foo               libs/foo/bob.go:12:9
   └─ nixpkgs:openssl           libs/foo/bob.go:15:20
```

`--why` prints the shortest path; `--why --all` prints up to 20 paths. `--format dot` prints a
Graphviz digraph with labels as node ids.

```
$ bob lock --update
bob.lock: 3 changes
  ~ go     golang.org/x/net   v0.30.0 → v0.31.0
  + npm    react-dom          19.3.0
  - crate  serde_derive       1.0.210
```

```
$ bob check
error[BOB-E-1011]: generated file web/package.json was edited by hand
  hint: move the change into bob.go; bob check --fix regenerates the file
error[BOB-E-1012]: bob.lock is out of date: //web:app needs npm zod (not pinned)
  hint: run bob lock

bob: 2 problems (exit 3)
```

#### R-CLI-007 Human output rules
- **Statement:** Human output follows rules 1–7.
- **Acceptance:** The S15 golden set (each fault, `TERM=dumb`, not a TTY) matches byte for byte.
  The same runs under a pseudo-TTY with `NO_COLOR=1` contain no `\x1b[` bytes; with
  `BOB_COLOR=always` and no `NO_COLOR` they contain colour sequences; with `TERM=dumb` and a
  pty, no `\r` redraws. No excerpt line in any golden matches `/nix/store/[0-9a-z]{32}`.
- **Source:** S15 §5.4, R-CLI-002.

---

## 02.6 Build, test, run, log, remote

### Local commands

- `bob build`: evaluates, runs the pre-checks, computes the derivations, realises them with the
  ADR 0017 invocation, checks output validity, and prints the results.
  - `--print-paths`: also prints, per package, the outputs' **host** paths
    (`<state>/nix/store/…`, §02.7.5). They can be read outside the namespace; binaries there run
    only through `bob run`/`bob exec`.
  - No `result` symlinks are created.
- `bob test`: builds the test derivations; cached test results count as passed (`cached`).
  `--rerun` re-runs the selected tests even when cached (Nix `--rebuild` on the test
  derivations) **[proposal]**. Test args after `--` become part of the test derivation (they
  change its hash, so the result is cached per argument set).
- `bob run <label> -- args`: builds the label for the native platform, then runs its main binary
  inside the namespace. The current directory, stdin, stdout and stderr are the caller's, and the
  environment is the caller's plus nothing (not the dev environment). A package with several
  binaries needs `--bin <name>`, else `BOB-E-0004`.
- `bob log <label>`: shows the full build log of the label's last local build (from Nix's log
  store), exactly as written, including raw text. `--build <id>` reads the log of a server build
  (§06 `LogService/GetLog`, from lake). `-f` follows a running server build by live pull (D55) and
  switches to lake on gaps. `--attempt N` picks the attempt.

### `--remote` (R-DEV-005)

```
$ bob build --remote //cmd/api:api
remote build b-01JB7Q8V3K queued on bob.example.com
  https://bob.example.com/p/acme/builds/b-01JB7Q8V3K
built   //libs/foo:foo
built   //cmd/api:api
downloading 2 outputs (14.2 MB)
bob: 2 built (remote), 0 cached (exit 0)
```

Steps:
1. Pre-check `whoami` (R-NIX-029).
2. Snapshot the source: the files that git tracks plus untracked files that are not ignored, as
   a content-addressed zstd tarball, uploaded with `PUT /upload/source/sha256/<hex>` (§06
   §6.9.7) unless the server already has its hash. The snapshot is **[open]**: the alternative is "commit must be pushed, send the
   SHA". Recommendation: snapshot, because a dirty tree is the normal developer case and the
   worker re-evaluates from source anyway; it is an input, not a cache push (D9 is about
   outputs).
3. `BuildService/CreateBuild{project_id, source.upload_sha256, targets, platforms, variants, test}` → build id.
4. `BuildService/WatchBuild` streams package events, shown with the same output rules. Errors arrive
   as `diag.Error` objects that the worker produced.
5. On success, the outputs are substituted from the server cache into the local store (negative
   narinfo TTL 0, S12), unless `--no-download`.
6. Ctrl-C detaches: the build continues on the server, and bob prints `bob cancel b-…` to stop
   it. A second Ctrl-C within 2 s cancels the server build (`BuildService/CancelBuild`).

#### R-DEV-005 Remote builds instead of pushes
- **Statement:** Developers never upload build outputs; `--remote` (on `build`, `test` and
  `pipeline run`) builds on the fleet and downloads the results.
- **Acceptance:**
  - Given a logged-in developer, the server's request log of `bob build --remote //...` contains
    no write to the cache endpoints, and the local store afterwards holds valid outputs with the
    server's signature.
  - The server's permission for a developer token rejects `PUT /nix/*` and the internal promote
    endpoint (403).
  - Ctrl-C during `--remote` leaves the server build running (`remote.state` = `running` 3 s
    later); `bob cancel <id>` moves it to `cancelled`.
- **Source:** R-DEV-005, D9, D61, S13 (dev stores not trustworthy).

#### R-CLI-008 Logs
- **Statement:** `bob log` shows the complete log of one package, local or from the server, and
  can follow a running server build.
- **Acceptance:** After fault 6 (C++ compile error), `bob log //libs/foo:foo` prints the full gcc
  output, and its last 10 lines match the excerpt. For a server build of a fixture that prints
  1,000 numbered lines, `bob log --build <id> //x:y` prints all 1,000 lines in order with no
  duplicates, and `bob log -f` started mid-build ends with line 1000, no gaps and no duplicates
  (dedupe by (build, package, attempt, seq), §05).
- **Source:** R-LOG-003, D55, S17.

---

## 02.7 Developer environment

The model (D51, D59, ADR 0002 Decision 5):
- **Inside the namespace** (`bob dev`, `bob exec`, `bob run`), the environment points at
  `/nix/store` toolchains like `nix develop`.
- **Outside the namespace** (the user's own shell, an editor started from the dock), only
  `.bob/bin` shims are put on `PATH`. A shim re-enters the namespace. Language servers are
  started behind a path-rewriting proxy, so locations into dependencies open from the host.
- **`cd` + start the editor works** through the shell hook (§02.7.1) for every editor that finds
  its language servers through `PATH` (Neovim, Helix, Emacs, Zed, Sublime) and for VS Code's
  Go, Rust and C/C++ extensions through the written settings. Editors that read toolchain files
  themselves (VS Code's built-in TypeScript, JetBrains IDEs) are started with `bob dev -- <editor>`.

### 02.7.0 `bob.DevShell` (declaration in `bob.go`) [proposal, added at integration]

Part of `pkg/bob` (L0, §01 §1.4); §01 lists it as provided by this section.

```go
// DevShell declares one development environment for bob dev, bob env, bob exec and the
// shell hook. Register it with bob.Define like any package; the shell named "default" (or the
// only one) is used when no --shell is given.
type DevShell struct {
	Name     string            // "default" when empty; [a-z0-9-]{1,32}
	Packages []Tool            // top-level tools/toolchains; their bin/ go on PATH in this order
	Env      map[string]string // extra variables (inside; exported outside per §02.7.2 rules)
	Hooks    []string          // shell commands run by bob dev before the shell starts
	Servers  []Package         // dev-server packages (e.g. npm.Vite, §03.4.8) started by bob dev
}

// NewShell validates s and returns it as a graph node (kind "bob.devshell"). The toolchains of
// every ecosystem used in the repo (Go, Rust, C/C++, Node, OCaml at the pins of §03) and their
// in-tree tools (e.g. node_modules/.bin) are added automatically; Packages adds extra tools.
//
//	var Dev = bob.Define(func(p *bob.P) *bob.Shell {
//		return bob.NewShell(p, bob.DevShell{
//			Packages: []bob.Tool{nixpkgs.Tool(p, "postgresql"), nixpkgs.Tool(p, "jq")},
//			Env:      map[string]string{"DATABASE_URL": "postgres://127.0.0.1/acme"},
//			Servers:  []bob.Package{web.Site(p)}, // web.Site: a bob.Define'd npm.Vite package
//		})
//	})
func NewShell(p *P, s DevShell) *Shell

// Shell is the graph node of a DevShell.
type Shell struct{ Node }
```

Errors: two shells with one name → `BOB-E-1020` (`option` = `Name`); `--shell` naming no shell
→ `BOB-E-1014`.

### 02.7.1 Shell hook (`bob hook`, `bob env --hook`)

Install once:

| Shell | Line in rc file |
|---|---|
| bash (`~/.bashrc`) | `eval "$(bob hook bash)"` |
| zsh (`~/.zshrc`) | `eval "$(bob hook zsh)"` |
| fish (`~/.config/fish/config.fish`) | `bob hook fish \| source` |

Exact hook output (`<bob>` = absolute path of the running bob, `os.Executable`):

```bash
# bob shell hook (bash)
_bob_hook() {
  local s=$?
  eval "$("<bob>" env --hook=bash)"
  return $s
}
if [[ ";${PROMPT_COMMAND[*]:-};" != *";_bob_hook;"* ]]; then
  if [[ "$(declare -p PROMPT_COMMAND 2>/dev/null)" == "declare -a"* ]]; then
    PROMPT_COMMAND=(_bob_hook "${PROMPT_COMMAND[@]}")
  else
    PROMPT_COMMAND="_bob_hook${PROMPT_COMMAND:+;$PROMPT_COMMAND}"
  fi
fi
```

```zsh
# bob shell hook (zsh)
_bob_hook() { eval "$("<bob>" env --hook=zsh)" }
typeset -ag precmd_functions chpwd_functions
(( ${precmd_functions[(I)_bob_hook]} )) || precmd_functions=(_bob_hook $precmd_functions)
(( ${chpwd_functions[(I)_bob_hook]} )) || chpwd_functions=(_bob_hook $chpwd_functions)
```

```fish
# bob shell hook (fish)
function __bob_hook --on-event fish_prompt --on-variable PWD
    "<bob>" env --hook=fish | source
end
```

`bob env --hook=<shell>` algorithm (runs at every prompt; never enters the namespace):
1. If `BOB_DEV=1` (already inside `bob dev`): print nothing.
2. `root = FindRoot($PWD)`. If there is no root and `BOB_PROJECT` is set → print the **leave**
   script. If there is no root and none is active → print nothing.
3. If `root` is not allowed (R-DEV-009) → leave any active project; print to stderr, once per
   root per shell (tracked in `BOB_DENIED_SEEN`):
   `bob: <root> is not allowed; run 'bob allow' to load its environment`.
4. If `root == BOB_PROJECT` and `.bob/env.json` hash == `BOB_ENV_HASH` and the §01 eval key is
   unchanged → print nothing (**fast path**).
5. If the eval key changed or `.bob/env.json` is missing → evaluate and write `.bob/env.json`
   plus the shims (§02.7.2/3), with one progress line on stderr
   (`bob: loading /home/u/acme (dev shell default)…`). This does not realise toolchains;
   shims realise on first use. Ctrl-C aborts and nothing is exported. An evaluation error prints
   the human error block on stderr and keeps the previous environment.
6. Print **leave** for the previous project (if another one was active), then **enter**:
   - save the previous values of every variable it sets in `BOB_ENV_RESTORE`
     (base64url JSON `{K: value|null}`);
   - `PATH=<root>/.bob/bin:$PATH`;
   - the dev shell's `outside.set` variables (§02.7.2);
   - `BOB_PROJECT=<root>`, `BOB_ENV_HASH=<sha256 of env.json, 16 hex>`.
   **leave** restores from `BOB_ENV_RESTORE`, removes `<root>/.bob/bin` from `PATH`, and unsets
   `BOB_PROJECT`, `BOB_ENV_HASH` and `BOB_ENV_RESTORE`.
7. Values are quoted for the target shell (POSIX single quotes with `'\''`; fish `string escape`
   rules).

Example output on entering (bash):

```
export BOB_ENV_RESTORE='eyJQQVRIIjoiL3Vzci9iaW46L2JpbiIsIkNHT19FTkFCTEVEIjpudWxsfQ';
export PATH='/home/u/acme/.bob/bin:/usr/bin:/bin';
export CGO_ENABLED='0';
export BOB_PROJECT='/home/u/acme';
export BOB_ENV_HASH='9f2c41d07a1be355';
```

`bob env` without `--hook` prints the same enter script for the current project (for direnv:
`.envrc` = `eval "$(bob env)"`, and for editor launchers); `--format json` prints the `result`
event data.

#### R-DEV-003 Automatic activation per directory
- **Statement:** With the hook installed, `cd` into an allowed bob project puts its shims first
  on `PATH` and sets the dev shell's outside variables; `cd` out restores the previous
  environment exactly.
- **Acceptance:** For bash 5, zsh 5.9 and fish 3.7, a harness pty session runs: `cd acme` →
  `command -v gopls` = `/…/acme/.bob/bin/gopls` and `echo $CGO_ENABLED` = `0`; `cd /` → `PATH`
  and `CGO_ENABLED` are byte-equal to their values before `cd acme` (including unset); `cd acme`
  then `cd acme/libs` → no output from the hook, and `BOB_ENV_HASH` is unchanged. Moving from
  project A to nested project B switches to B; moving back switches to A.
- **Source:** R-DEV-003, D43, D59.

#### R-DEV-007 Hook cost
- **Statement:** The hook's fast path does no evaluation and no namespace entry.
- **Acceptance:** `bob env --hook=bash` on the fast path: p95 ≤ 10 ms [8c/15G], ≤ 20 ms
  [2c/4G], over 100 runs **[proposal; not measured yet: derived from bob's static Go start, S1
  shim cost 6.8–7.8 ms including the namespace entry, which the fast path skips]**. On the fast
  path the process opens no files besides `bob.lock` lookups, `.bob/env.json` and the §01 eval-key
  index (checked with strace in the harness).
- **Source:** R-DEV-006, R-NFR-008, D59.

#### R-DEV-009 Trust before automatic activation
- **Statement:** The hook evaluates `bob.go` (it runs project code) only in roots the user has
  allowed with `bob allow`. Explicit commands (`bob build`, `bob dev`, …) need no allow.
- **Acceptance:** Given a fresh HOME and a cloned project, `cd project` with the hook prints the
  "not allowed" line once, runs no evaluator (no process is spawned besides the hook's bob), and
  sets no `BOB_PROJECT`. After `bob allow`, the next prompt activates it. `bob deny` deactivates
  it at the next prompt. Allowed roots are stored in `$XDG_STATE_HOME/bob/allow.json`
  (`{"v":1,"roots":["/abs/path",…]}`, mode 0600). A root is trusted by path, not by content
  **[proposal]**: re-asking on every bob.go change would make the hook useless in daily work.
- **Source:** D43 (direnv-style), BOOTSTRAP §2 security principle.

### 02.7.2 `.bob/env.json`

Written atomically (temporary file + rename) by the hook, `bob env`, `bob dev` and every
evaluating command when its content changes. Gitignored (`.bob/`).

| Field | Type | Meaning |
|---|---|---|
| `v` | int | 1 |
| `root` | string | absolute repo root |
| `shell` | string | dev shell name |
| `evalKey` | string | §01 eval key this was computed from |
| `inside` | map string→string | the full `nix develop`-style variables: `PATH` (toolchain `bin` dirs, prepended to the caller's `PATH` minus `<root>/.bob/bin`), `GOROOT`, `CC`, `CXX`, `PKG_CONFIG_PATH`, `RUSTC`, `NODE_PATH`, …, plus the dev shell's own variables |
| `outside.set` | map string→string | variables exported outside: dev shell variables whose value contains no `/nix/store` path, plus variables whose value is exactly `/nix/store/<p>/bin/<tool>` with a shim for `<tool>`, rewritten to `<root>/.bob/bin/<tool>` |
| `insideOnly` | []string | variables that reference the store in any other way; never exported outside |
| `tools` | map name→{`path`, `lsp`, `env`} | every shimmed tool: the store or in-tree path inside the namespace, whether it is an LSP server (§02.7.4), and extra variables for this tool only |
| `closure` | []string | top-level store paths of the dev shell (realised by `bob exec`/`bob dev`, kept alive as GC roots) |
| `hooks` | []string | dev shell hook commands, run by `bob dev` before the shell starts |
| `servers` | []{`name`, `cmd`[], `cwd`, `ready`} | dev servers (§03 contract) |

Example:

```json
{"v":1,"root":"/home/u/acme","shell":"default","evalKey":"sha256:5b1d…",
 "inside":{"PATH":"/nix/store/x1qs…-go-1.26.8/bin:/nix/store/7zwy…-gopls-0.22.0/bin","GOTOOLCHAIN":"local","GOPROXY":"off","CGO_ENABLED":"0"},
 "outside":{"set":{"CGO_ENABLED":"0"}},
 "insideOnly":["GOROOT"],
 "tools":{"go":{"path":"/nix/store/x1qs…-go-1.26.8/bin/go","lsp":false,"env":{}},
          "gopls":{"path":"/nix/store/7zwy…-gopls-0.22.0/bin/gopls","lsp":true,"env":{"GOFLAGS":"-tags=bob"}},
          "tsc":{"path":"/home/u/acme/web/node_modules/.bin/tsc","lsp":false,"env":{}}},
 "closure":["/nix/store/x1qs…-go-1.26.8","/nix/store/7zwy…-gopls-0.22.0"],
 "hooks":[],"servers":[{"name":"web-vite","cmd":["vite","--config",".bob/vite.bob.config.mjs"],"cwd":"web","ready":"http"}]}
```

Tool set: every executable in `bin/` of each top-level package of the dev shell (not
transitive), in dev shell order (the first one wins on a name clash, like `PATH`), plus §03's
in-tree tools (`node_modules/.bin/*` of materialised importers). The Go package always gets
`gopls` with `env.GOFLAGS=-tags=bob` (S1-7).

### 02.7.3 Shims (`.bob/bin/<tool>`)

```sh
#!/bin/sh
# Code generated by bob. DO NOT EDIT.
exec "${BOB:-/home/u/.local/bin/bob}" exec --project '/home/u/acme' -- gopls "$@"
```

LSP servers (`tools[name].lsp`) get `exec --lsp`. Mode 0755. `.bob/bin` contains only shims; a
shim for a tool that left the dev shell is deleted at the next write. The `BOB` override exists
for moved bob binaries; the recorded path is the absolute `os.Executable()` at generation.

#### R-DEV-008 `bob exec` and shims
- **Statement:** `bob exec -- <tool> args` runs the tool inside the namespace with
  `inside` + `tools[tool].env`, realising the dev shell closure first if any path is invalid.
  Its stdin/stdout/stderr, cwd, exit status and signals are the tool's.
- **Acceptance:**
  - As a non-root user on a root without `/nix` (`-hide-nix` harness mode),
    `.bob/bin/go version` prints the pinned Go version and exits 0.
  - `.bob/bin/go` subprocesses started by gopls run in bob's mount namespace (S1 method:
    uid_map `0 <uid> 1`, mntns ≠ host).
  - `sh -c '.bob/bin/go run ./x; echo $?'` with `os.Exit(5)` prints 5; SIGTERM to the shim
    reaches the tool.
  - Shim start overhead for `/bin/true` ≤ 15 ms p95 [8c/15G] and ≤ 20 ms [2c/4G] **[proposal;
    S1 measured 6.8–7.8 ms, 20 runs × 5 rounds, bobtest, indicative]**.
  - A shim whose root has no `.bob/env.json` exits 7 with `BOB-E-7006`.
- **Source:** ADR 0002 Decision 5, D51, S1 §3.3, S4 §3.6.

### 02.7.4 LSP path-rewriting proxy (`bob exec --lsp`)

Problem: a language server runs inside the namespace and answers with `file:///nix/store/…`
locations (Go std, crates, npm types, C headers). An editor outside the namespace on a host
without `/nix` cannot open them (S1, S4 O-S4-2, O-DX-1). On a host with a system `/nix` the
path may exist in the *system* store by coincidence, but nothing guarantees it.

Fix: `/nix/store/X` inside is the same file as `<state>/nix/store/X` on the host, because
bob's store directory is a plain directory in the user's home (ADR 0002 Decision 1). The proxy
rewrites store paths between the two views in every message:
- **server → client:** `/nix/store` → `<state>/nix/store`;
- **client → server:** `<state>/nix/store` → `/nix/store`, so the server recognises a store file
  the editor opened (e.g. `GOROOT` files) as its own.

Rules (implemented and tested in the proof, see below):
1. Placement: the bob child process inside the namespace starts the server as its own child and
   pumps both directions. No extra process outside. stderr passes through unchanged.
2. Framing: LSP base protocol (`Content-Length` header, `\r\n\r\n`, body). Other headers are
   dropped; the output frame has only `Content-Length`. Messages keep their order per direction.
3. Fast path: a body that contains none of `<from>/`, the JSON-escaped form `\/nix\/store\/`, or
   the percent-encoded form of `<from>/` is forwarded byte for byte.
4. Otherwise the body is decoded (`UseNumber`, so ids and numbers are kept exactly), every
   **string value and every object key** is rewritten (keys: `WorkspaceEdit.changes` is keyed by
   URI), and it is re-encoded without HTML escaping.
5. String rewriting is one left-to-right pass; replaced text is never rescanned. Two patterns:
   - `file://<esc(from)>/` → `file://<esc(to)>/`, where `esc` is URI path percent-encoding (a
     host home such as `/home/a b` becomes `/home/a%20b`);
   - a bare `<from>/`, only at a boundary: start of string, or a preceding byte that is not
     `[A-Za-z0-9/._+~-]`. Reason: the host path `…/share/bob/nix/store/` itself contains
     `/nix/store/`, and must never be rewritten again.
6. A body that is not valid JSON is forwarded unchanged (never dropped).
7. In `--lsp` mode bob writes **nothing** on stdout itself. Its errors (namespace setup,
   realisation) go to stderr as human blocks, and it exits with the class exit code before the
   server starts.
8. The working tree is never under `/nix`, and its paths are identical inside and outside, so the
   workspace URIs are not touched.
9. tsserver's own protocol (newline-delimited JSON requests, `Content-Length` responses, `file`
   fields with plain paths) is **[open]**. Recommendation: support it as a second framing in v1
   only if VS Code's TypeScript is made to work outside `bob dev` (see §02.7.6); the
   `typescript-language-server`/`vtsls` LSP servers (used by Neovim, Helix, Zed) are covered by
   rules 1–8.

LSP server table (`lsp: true`) **[proposal]**: `gopls`, `clangd`, `rust-analyzer`,
`typescript-language-server`, `vtsls`, `ocamllsp`, `vscode-eslint-language-server`,
`vscode-json-language-server`, `svelteserver`, `tailwindcss-language-server`. **[open]**: a
`DevShell` option to mark more tools as LSP (§02.7.0). Recommendation: add it as
`Tool("x").LSP()`, only if a user needs it.

**Proof (2026-10-21).** Code: `/root/scratch-spec02/lspproof` (`rewrite/` package with unit
tests, `cmd/lspproxy`, `cmd/lspcheck` = a scripted LSP client acting as an outside editor);
runner `/root/scratch-spec02/run.sh`; results `/root/scratch-spec02/results/`. Setup: the S1
fixture `acme` and store of user `bobtest`, gopls 0.22.0 and go 1.26.8 through the S1
`.bob/bin/gopls` shim. The client runs in a root **without `/nix`** (`bobexec -hide-nix`, so the
shim nests into pivot mode).

| Run | Client view | `def fmt.Println` result | File opens for the client | Navigation inside the opened std file | Raw `/nix/store` refs seen by client |
|---|---|---|---|---|---|
| A: no proxy | no `/nix` | `file:///nix/store/x1qs…-go-1.26.8/share/go/src/fmt/print.go:313` | ✗ | — | 1 |
| B: proxy | no `/nix` | `file:///home/bobtest/s01/state/nix/store/x1qs…/fmt/print.go:313` | ✓ | hover `Fprintln` ✓; def `Fprintln` → print.go:302 ✓; def `os.Stdout` → `…/os/file.go:72` ✓; references 2/2 openable | 0 (27 of 35 frames rewritten) |
| C: proxy | real host with system `/nix` | same as B | ✓ | same as B | 0 |
| baseline: no proxy | real host with system `/nix` | `/nix/store/…` (opens only because root's system store has the same go) | ✓ (coincidence) | — | 26 |

Timing: warm `textDocument/definition` round trip, mean of 20: 0.55 ms (B), 0.72 ms (C), 0.55 ms
(baseline, no proxy) [8c/15G, busy host]; 0.56 ms (B) [2c/4G]. Rewriting one location message:
21–24 µs; the pass-through check runs at 8.5 GB/s [8c/15G]. Proxy overhead is not measurable at
the request level. Not verified: a real Neovim/VS Code session (the client is scripted), clangd,
rust-analyzer, typescript-language-server.

#### R-DEV-010 LSP locations open outside the namespace
- **Statement:** Every LSP server started through a shim answers with host paths for store
  files and accepts host paths for them, so go-to-definition into dependencies works for an
  editor outside the namespace on a host without `/nix`.
- **Acceptance:**
  - (harness, bobtest, `-hide-nix` client) On the Go fixture, `lspcheck` through
    `.bob/bin/gopls`: `def fmt.Println` returns a `file://<state>/nix/store/…/fmt/print.go` URI
    that `os.Stat` finds. After `didOpen` of that URI, hover and definition inside it succeed and
    return host URIs. 0 raw `/nix/store/` references reach the client.
  - The same script shape runs for clangd (`#include <stdio.h>` → glibc header), rust-analyzer
    (`String` → std source) and typescript-language-server (`useState` → `@types/react`).
  - The proxy unit tests cover: URI, bare path, boundary (`<state>/nix/store` not rewritten
    twice, `/foo/nix/store` untouched), percent-encoding, object keys, numbers > 2^53,
    pass-through byte equality, invalid JSON pass-through.
  - Warm definition latency through shim + proxy ≤ 2 ms mean [8c/15G] and ≤ 3 ms [2c/4G]
    **[proposal; measured 0.55–0.72 ms / 0.56 ms]**.
- **Source:** D59, D51, owner Round 1 answer 8, S1 §3.2–3.3, S4 §3.6, proof above.

### 02.7.5 Host paths of store files

`<state>` in this section is §01's state home `<home>` (R-NIX-024: `$BOB_HOME`, else
`${XDG_DATA_HOME:-$HOME/.local/share}/bob`).

`bob build --print-paths`, the LSP proxy and editor settings use the host form
`$XDG_DATA_HOME/bob/nix/store/<hash>-<name>` (default `~/.local/share/bob/nix/store/…`). Files
there are readable from outside but read-only. Executables there do not run outside the
namespace (ELF interpreter is a `/nix/store` path, S0b).

### 02.7.6 Editor settings (`bob editor`)

`bob editor <name>` prints the settings; `--write` merges them into the project file (JSON merge
by key; other keys are kept; a comment-free JSON file is required, else `BOB-E-0004` with the
snippet to paste by hand).

| Editor | Works after `cd` + start? | What bob provides |
|---|---|---|
| Neovim (lspconfig / `vim.lsp`) | yes, with the hook | nothing to configure: `gopls`, `clangd`, `rust-analyzer`, `typescript-language-server` resolve to `.bob/bin`; `GOFLAGS=-tags=bob` is in the gopls shim env (S1: same result as `buildFlags`) |
| Helix, Emacs (eglot/lsp-mode), Zed, Sublime LSP | yes, with the hook (via `PATH`) | same as Neovim; `bob editor zed` prints `lsp.<server>.binary.path` entries for Zed builds that ignore `PATH` |
| VS Code: Go, rust-analyzer, clangd extensions | yes, after `bob editor vscode --write` | `.vscode/settings.json` below (absolute paths are not needed: `${workspaceFolder}`) |
| VS Code: built-in TypeScript/JavaScript | **no**: needs `bob dev -- code --new-window .` | tsserver runs on VS Code's own Node and reads `node_modules` itself (symlinks into `/nix/store`) |
| JetBrains IDEs | **no**: needs `bob dev -- <ide> .` | the IDE reads SDK/toolchain files itself |

```json
{
  "go.alternateTools": { "go": "${workspaceFolder}/.bob/bin/go", "gopls": "${workspaceFolder}/.bob/bin/gopls" },
  "go.toolsManagement.autoUpdate": false,
  "gopls": { "build.buildFlags": ["-tags=bob"], "build.directoryFilters": ["-.bob"] },
  "rust-analyzer.server.path": "${workspaceFolder}/.bob/bin/rust-analyzer",
  "clangd.path": "${workspaceFolder}/.bob/bin/clangd",
  "files.watcherExclude": { "**/.bob/**": true }
}
```

Only keys for ecosystems the project uses are written. **[open]** VS Code's built-in TypeScript
outside `bob dev`: `typescript.tsdk` = host path of the typescript package plus
`typescript.tsserver.nodePath` = `.bob/bin/node` would run tsserver inside the namespace, but
its `file` paths need the tsserver-framing proxy (rule 9). Recommendation: v1 documents
`bob dev -- code`; revisit after Stage 3 has a real VS Code test. **[open]**
`bob dev -- code .` forwards to an already running VS Code instance (outside the namespace)
unless `--new-window --user-data-dir` is used. Recommendation: `bob editor vscode` prints the
exact launch command with a per-project `--user-data-dir` under `.bob/vscode`; verify manually.

#### R-DEV-011 Editor settings
- **Statement:** `bob editor <name>` prints, and with `--write` merges, the settings above for
  the ecosystems in use.
- **Acceptance:** On a Go+Rust+C fixture, `bob editor vscode --write` on an existing
  `.vscode/settings.json` with an unrelated key keeps that key and adds exactly the keys above
  (golden). Run twice → byte-identical file. On a Go-only fixture, no `rust-analyzer.*` or
  `clangd.*` key is written. (manual) VS Code with the Go extension, started from the dock in the
  fixture: go-to-definition on `fmt.Println` opens print.go.
- **Source:** ADR 0002 Decision 5, S1 §5.2, D59.

### 02.7.7 `bob dev`

```
$ bob dev
bob: dev shell default (go 1.26.8, gcc 15.3, node 24.11) · 0.4s
bob: vite (web) ready at http://localhost:5173 · log .bob/dev/web-vite.log
[bob dev] ~/acme $ exit
bob: stopped vite (web)
```

Steps:
1. Find the root and the dev shell (`--shell`, else the shell named `default`, else the only
   shell; otherwise `BOB-E-1014`).
2. Evaluate, write `.bob/env.json` and the shims, and realise the closure (progress as for
   `build`).
3. Prepare the working tree (§03 `Prepare`: native files, materialised codegen, `node_modules`).
4. Enter the namespace with `inside` plus `BOB_DEV=1`, `BOB_PROJECT=<root>`,
   `BOB_DEV_SHELL=<name>`. Run `hooks` in order (a failing hook → its exit code and stderr, bob
   exits 1).
5. Start the dev servers in the background: their output goes to `.bob/dev/<name>.log`, and their
   readiness line is printed once (`ready: http` = a TCP connect to the port succeeds, or the
   §03 contract's ready pattern in the log). `--no-servers` skips them. `--servers-only` runs
   the servers in the foreground with prefixed output (`[web-vite] …`) and no shell.
6. Start `$SHELL` (else `/bin/sh`) interactively, or `cmd args` after `--`. The user's rc files
   run normally; bob does not touch `PS1`. Prompt integration is left to the user (`BOB_DEV_SHELL`).
7. While the shell runs, watch `bob.go`, `bob.lock` and the §01 eval-key inputs (inotify):
   - on a dependency change, re-lock if needed, re-prepare the tree, swap `node_modules`
     atomically, and restart the affected servers;
   - if `inside` changed, print once: `bob: dev environment changed; exit and run bob dev again
     to use it`.
   A server that exits on its own → `BOB-E-2012` warning on stderr; it is not restarted.
8. When the shell or command exits: stop the servers (SIGTERM, then SIGKILL after 5 s) and exit
   with the shell's/command's exit status.

#### R-DEV-001 Dev shells
- **Statement:** `bob dev` enters the dev shell defined in `bob.go`: its toolchains, extra
  packages, variables and hooks are present inside, and nothing else changes.
- **Acceptance:** As a non-root user on a host without `/nix`:
  - `bob dev -- sh -c 'command -v go; go env GOROOT; echo $CGO_ENABLED $BOB_DEV'` prints a
    `/nix/store/…-go-<pinned>/bin/go` path, a `/nix/store` GOROOT, and `0 1`, and exits 0.
  - `bob dev -- false` exits 1.
  - `bob dev --shell nope` exits 3 with `BOB-E-1014` listing the shell names.
  - A dev shell hook `echo hooked > .bob/hooked` has run before the command.
- **Source:** R-DEV-001, D51.

#### R-DEV-002 Tree preparation and dev servers
- **Statement:** `bob dev` prepares the working tree for tooling and runs the declared dev
  servers with live reload; dependency changes in `bob.go` are applied while the session runs.
- **Acceptance:** On the S4 vite-react fixture: after `bob dev --servers-only &`, an HTTP GET of
  `/` returns 200 within the entry budget. 20 saves of `src/App.tsx` from outside the namespace
  each produce an HMR `update` message on the websocket, p95 ≤ 50 ms server side
  **[proposal; measured median 3.3 ms, max 29 ms [2c/4G]]**. Adding an npm dependency in
  `bob.go` → within 10 s `node_modules/<dep>` exists and the server answers again **[proposal;
  measured relock 2.1–2.5 s + materialise 0.7–0.8 s + restart ~0.3 s]**. On exit, no process of
  the session is left (checked by the cgroup / process tree).
- **Source:** R-DEV-002, D34, S4 §3.5, §5.5, A20.

#### R-DEV-006 Developer-visible budgets
- **Statement:** The developer loop has measured budgets.
- **Acceptance** (the harness budgets file; profile [2c/4G] unless marked) **[proposal]**:

| Operation | Budget | From |
|---|---|---|
| `bob dev -- true`, nothing changed (eval no-op + materialise no-op + namespace) | ≤ 1.0 s [2c/4G], ≤ 0.5 s [8c/15G] | §01 eval no-op ≤ 0.1 s [8c/15G] / ≤ 0.15 s [2c/4G] (§1.17); S4 materialise no-op 0.42 s [2c/4G]; namespace 5–7 ms |
| Vite ready inside `bob dev` | ≤ 1 s | S4 257–309 ms [8c] |
| no-op `bob build` (Go fixture) | ≤ 0.5 s [8c/15G] | S2 quiet: 0.26 s hybrid |
| one-file incremental build (Go fixture `main.go`) | ≤ 4 s [8c/15G] (= §03.8) | S2 quiet: 2.87 s hybrid |
| shim start (`/bin/true`) | ≤ 15 ms [8c/15G] | S1 6.8–7.8 ms |
| hook fast path | ≤ 20 ms p95 | R-DEV-007 (not measured yet) |
| first `bob build` of the Go fixture on a fresh machine with a warm server cache | ≤ 2 s + closure size / 100 MiB/s + cold eval | S12 quiet: 100.7 MiB substitute 0.61–0.67 s [8c]; S0b extraction 294 ms |

- **Source:** R-DEV-006, R-NFR-002, R-NFR-008, quiet logs `docs/spikes/measure/quiet/s02-go.log`,
  S1, S4, S12.

#### R-DEV-004 Cache setup after login
- **Statement:** `bob login` configures bob's own Nix: substituter URL, trusted public keys and the
  netrc entry. Without a login, only cache.nixos.org and the binary caches configured in bob's
  config are used.
- **Acceptance:** After `bob login https://srv` (test server), `<home>/etc/nix/nix.conf` (§01 R-NIX-024) lists
  `https://srv/nix` after cache.nixos.org in `substituters`, its key from `AuthService/WhoAmI` (`nix.public_keys`) is in
  `trusted-public-keys`, and `<home>/etc/nix/netrc` (mode 0600) has
  `machine srv login bob password <token>`. No file outside bob's state and config dirs changed
  (checked by a file tree diff of HOME and `/etc`). After `bob logout`, those three entries are
  gone.
- **Source:** R-DEV-004, R-NIX-024, ADR 0014, S12 §5.

---

## 02.8 Login, registries and `bob install`

### Files

| File | Content | Mode |
|---|---|---|
| `$XDG_CONFIG_HOME/bob/config.json` | `{"v":1,"defaultServer":"…","registries":[{"name","url","publicKeys":[…]}],"defaultRegistry":"…","binDir":"~/.local/bin","caches":[{"url","publicKey"}]}` | 0644 |
| `$XDG_CONFIG_HOME/bob/credentials.json` | `{"v":1,"servers":{"<url>":{"kind":"user"\|"service","accessToken","refreshToken","expires","user"}}}` | 0600 |
| `$XDG_DATA_HOME/bob/install/generations/<n>.json` | `{"v":1,"n":7,"created","entries":[{"name","registry","ref","version","platform","storePaths":[…],"bins":{"<bin>":"<store path>/bin/<bin>"}}]}` (create-only: temp file + `rename`) | 0644 |
| `$XDG_DATA_HOME/bob/install/current` | symlink → `generations/<n>.json` | — |
| `<home>/nix/var/nix/gcroots/bob-install/<n>` | GC roots of the last 5 generations | — |

`config.json` is written by commands (`bob registry`, `bob login`). Hand edits are allowed and
validated at load (unknown key → `BOB-E-0004`).

### `bob login`

```
$ bob login https://bob.example.com
Open https://bob.example.com/device and enter code WDJB-MJHT (expires in 10 min)
Waiting… logged in to bob.example.com as linus (token valid 30 days, refreshed automatically)
```

- Flow choice: authorization code + PKCE with a loopback redirect (`http://127.0.0.1:<random>/callback`)
  when a browser can be opened (`$BROWSER`, or `xdg-open` with `DISPLAY`/`WAYLAND_DISPLAY` set,
  and not over SSH). Otherwise, or with `--device`, the device flow. The server does OIDC or
  local-password login behind it (§06).
- `--token-stdin`: stores a service-account token read from stdin (kind `service`, no refresh).
- Refresh: before any server call, if `expires` is less than 60 s away, call `POST /oauth/token` with `grant_type=refresh_token`
  once; failure → `BOB-E-4008`.
- After login: `AuthService/WhoAmI`, write `nix.conf`/netrc (R-DEV-004), and set `defaultServer` if it
  was unset. The first server is also added as registry `default`.
- `bob logout [<url>]`: `POST /oauth/revoke` (best effort), delete the credentials entry, the
  netrc line and the substituter.

#### R-CLI-003 Login
- **Statement:** `bob login` gets a server token by PKCE or device flow, stores it per user
  (0600) and refreshes it automatically.
- **Acceptance:** Against the test server: the device flow completes when the harness approves
  the code via the server API, and `credentials.json` has mode 0600. With the access token set to
  expire in 30 s, `bob build //...` (server substituter configured) calls Refresh exactly once,
  then succeeds. With Refresh answering 401 → exit 4, `BOB-E-4008`. The PKCE flow with a fake
  browser (`BROWSER=` script that GETs the URL and follows the redirect) completes, and the token
  request carries `code_verifier` with an S256 challenge.
- **Source:** R-CLI-003, R-AUTH-007.

### Install references

```
<ref>      = [<registry>:]<project>/<release>[@<version>|@latest]
<version>  = semver of the release (§06), or "latest" (default)
```

Examples: `bob install acme/cli`, `bob install corp:infra/deploytool@1.4.2`.

```
$ bob install acme/cli@1.4.2
installed acme/cli 1.4.2 (generation 7): acme, acme-migrate → ~/.local/bin
$ bob list
acme/cli     1.4.2   default   acme, acme-migrate
infra/dt     0.9.0   corp      dt
$ bob upgrade
acme/cli  1.4.2 → 1.5.0
bob: 1 upgraded (generation 8)
$ bob rollback
generation 8 → 7: acme/cli 1.5.0 → 1.4.2
```

Install steps:
1. `ReleaseService/ResolveRelease{project_id, name, constraint, platform}` on the registry → the release's closure
   artifact (store paths) and entry binaries (§04).
2. Public releases are fetched without a token; private ones need the server's credentials
   (R-INST-003). Missing or denied → `BOB-E-4008`/`4010`.
3. Substitute the closure through the server's `/nix` cache (download proxy, D61), with the
   registry's pinned public keys. A path without a valid signature → `BOB-E-4004`.
4. Write generation n+1 (previous entries + this one), add the GC root, flip `current`
   atomically.
5. Write shims for each entry binary into `binDir` (default `~/.local/bin`):
   ```sh
   #!/bin/sh
   # Code generated by bob install (acme/cli 1.4.2). DO NOT EDIT.
   exec "${BOB:-/home/u/.local/bin/bob}" exec --installed acme/cli -- acme "$@"
   ```
   `bob exec --installed <name>` reads `current`, enters the namespace and runs the binary with
   the caller's environment.
6. A binary name already provided by another entry or by an existing non-bob file in `binDir` →
   `BOB-E-0004` (`acme is already provided by infra/dt; use --as`), and nothing changes.
   `--as <name>` installs under another entry name and prefixes the shims (`<as>-<bin>`).
7. If `binDir` is not on `PATH`, print one hint line with the exact rc line.

`uninstall <name>` writes a new generation without the entry and removes its shims. `upgrade
[names…]` resolves `@latest` (or the pinned major with `--minor-only` **[proposal]**) for each
entry and installs all changes as one generation. `rollback [--to N]` flips `current` to the
previous (or N-th) generation and rewrites the shims. `list --available <project>` lists the
registry's releases. Generations beyond the last 5 lose their GC root (and `bob gc` may then
collect them).

#### R-INST-001 Install, upgrade, rollback, uninstall, list
- **Statement:** As above; every change is one atomic generation switch. `install` picks the
  release's `closure` artifacts for the host platform (§04 §4.8 manifest) and substitutes their
  closures from the registry's `/nix` cache (and cache.nixos.org); `list` shows the entries of
  the current generation. Profile semantics: §04 R-INST-005.
- **Acceptance:** Against the test server with releases `acme/cli` 1.0.0 and 1.1.0: `install
  acme/cli@1.0.0` → `~/.local/bin/acme` prints `1.0.0`. `upgrade` → prints `1.1.0`. `rollback` →
  `1.0.0`. `uninstall acme/cli` → shim gone, `list` empty. Killing bob (SIGKILL) at any step of
  `upgrade` (harness kill points) leaves `current` pointing to a complete generation, and every
  shim works. `bob list --json` matches the current generation file.
- **Source:** R-INST-001, D10, R-NFR-007; merged with §04 §4.9 (integration).

#### R-INST-002 No system Nix, no build knowledge
- **Statement:** `bob install` works for a consumer with only the `bob` binary, on a host
  without `/nix`, and needs no project checkout.
- **Acceptance:** In a container image with no `/nix`, no Go/Nix and a non-root user, the
  `bob` binary alone runs `bob install acme/cli` against a public registry, and `acme --version`
  works from a new login shell. A release without `closure` artifacts is not installable:
  `bob install acme/imgonly@1` → `✗ acme/imgonly@1 has nothing to install (it contains: oci
  image "image"); download its artifacts from <server>/p/acme/releases/imgonly/1`, `BOB-E-0009`, exit 2.
  The installed shim's start overhead is ≤ 15 ms p95 [8c/15G]
  **[proposal; S1 shim 6.8–7.8 ms]**.
- **Source:** R-INST-002, R-NIX-020/021, D51, ADR 0002; merged with §04 §4.9.

#### R-INST-003 Public without auth, private with login
- **Statement:** Public releases install anonymously; private releases need a user login or a
  service-account token (`bob login` or `BOB_TOKEN`), checked with `AuthService/WhoAmI` before Nix
  runs (R-NIX-029) and passed to Nix only through bob's netrc.
- **Acceptance:** With no credentials: a public release installs, and the server's log shows no
  `Authorization` header on its requests; a private release fails with exit 4, `BOB-E-4010` (or
  `4008` with an invalid token) and the hint names `bob login`. After `bob login --token-stdin`
  with a service token that has the read permission, the private release installs. No Nix
  401 warning is ever shown. Server side (§06 §6.8): anonymous `ResolveRelease` on a public
  release → OK and every closure path's narinfo is 200 anonymously; on a private release →
  NotFound; with a download token of a permitted user → OK.
- **Source:** R-INST-003, D10, D58, D61, R-REG-001; merged with §04 §4.9 and §06 §6.8.

#### R-INST-004 Several registries
- **Statement:** `bob registry add|remove|list|default` manages registries (stored in
  `config.json`, §02.8 Files); `<registry>:<project>/<release>@<v>` selects one, no prefix selects
  the default; each generation entry records its registry.
- **Acceptance:** With registries `default` and `corp`, `bob install corp:infra/dt` resolves on
  `corp` only (the `default` server sees no request). `bob registry add` pins the server's public
  keys from `AuthService/GetServerInfo` (`nix.public_keys`, anonymous); a later key change makes installs fail with `BOB-E-4004` until
  `bob registry add --refresh-keys corp` **[proposal: trust on first use; an admin can pre-seed
  keys with `--key`]**. With two harness servers, `bob list` shows the registry column and
  `bob upgrade` updates each entry from its own registry.
- **Source:** R-INST-004, D43; merged with §04 §4.9.

#### R-INST-006 Installs never use project dev state
- **Statement:** Installed programs run with the caller's environment and no project's
  `.bob/env.json`; installing never touches a working tree.
- **Acceptance:** Running an installed shim from inside an activated project (hook active, `bob
  dev` running) gives the same `env` output for the program as from `/`, apart from `PWD`,
  `BOB_PROJECT`, `BOB_ENV_*` and the project's exported outside variables, which bob does not
  remove.
- **Source:** R-INST-002, ADR 0002 Consequences.

---

## 02.9 Completion

`bob completion bash|zsh|fish` prints a script that calls the hidden `bob __complete <words…>`.
Candidates: commands, flags, flag values (shells, editors, formats, `--platform`/`--variant`
from §04), dev shell names, installed entry names, registry names, and **labels**. Labels come
from the last evaluation's label index (`.bob/labels`, written at every evaluation); completion
never evaluates.

#### R-CLI-004 Shell completion including labels
- **Statement:** As above.
- **Acceptance:** In the fixture after one `bob build`, `bob __complete build //li` prints
  `//libs/foo:foo`, `//libs/greet:greet`, … (one per line), each starting with `//li`, in
  ≤ 50 ms p95 [2c/4G] **[proposal]**. Without `.bob/labels`, it prints no labels and exits 0.
  bash, zsh and fish scripts are golden-tested and sourced without errors in each shell.
- **Source:** R-CLI-004.

---

## 02.10 `check`, `lock`, `graph`, `pipeline run`

#### R-CLI-009 `bob check`
- **Statement:** `bob check` reports drift without changing anything: generated native files
  that differ from what bob would write (1011), lock entries that are missing, extra or stale
  (1012), `.bob/`, generated or materialised paths that git does not ignore (1013). `--fix` regenerates the
  files (as `bob gen --force`, with the backup of §03.0.2), adds the ignore entries, and runs
  `bob lock` for 1012. `--lock` on `build`/`test` re-resolves a stale lock section instead of
  failing with 1012 (§03.0.3).
- **Acceptance:** On the fixture: no findings → exit 0, output `bob: no problems`. After a hand
  edit of a generated `go.mod` (`go get` of a new module): exit 3 with 1011 naming `go.mod`.
  After `--fix`, `bob check` exits 0 and the hand edit is gone. Removing bob's block from
  `.git/info/exclude` → 1013. `bob check` changes no file (tree hash before = after).
- **Source:** R-CLI-001, D2, D25, D33.

#### R-CLI-010 `bob lock`
- **Statement:** `bob lock` adds missing pins and removes unused ones, without updating existing
  pins. `--update [names…]` re-resolves the named (or all) entries. `--nixpkgs <rev>` moves the
  nixpkgs pin. The output lists every change (sample in §02.5).
- **Acceptance:** On a locked fixture, `bob lock` is a no-op (file byte-identical, output
  `bob.lock: no changes`). After adding one npm dependency in bob.go: exactly one `+` line and
  one new entry. `--update golang.org/x/net` changes only that module's entries (plus the
  transitive ones it requires, listed). `--offline` with an uncached need → exit 4, `BOB-E-3002`.
- **Source:** R-CLI-001, R-FW-004.

#### R-CLI-011 `bob graph`
- **Statement:** `bob graph` prints the dependency graph of the patterns (text tree, `dot`,
  JSON). `--why A B` prints the shortest dependency path from A to B (`--all`: up to 20 paths).
  `--rdeps` reverses the edges.
- **Acceptance:** On the fixture, `bob graph --why //cmd/api:api nixpkgs:openssl` matches the
  golden in §02.5. Without a path → `bob: //cmd/api:api does not depend on nixpkgs:zlib`, exit 0.
  `--format json` passes the schema and the edge set equals the evaluator's graph.
- **Source:** R-CLI-001.

#### R-CLI-012 `bob pipeline run`
- **Statement:** `bob pipeline run <name>` runs the named `bob.go` pipeline locally, step by step,
  with the same output rules as `build`/`test`; `--remote` submits it to the server (§02.6).
- **Acceptance:** The fixture pipeline `ci` (build `//...`, test `//...`) prints one step header
  per step (`== step build`), then that step's package lines. Its exit code is that of the first
  failing step. An unknown pipeline → exit 2, `BOB-E-0001`.
- **Source:** R-FW-041.

---

## [open] items

1. **Remote source transfer** (§02.6 step 2): snapshot upload (recommended) vs. "commit must be
   pushed". §06 §6.9.7 specifies the upload endpoint and §05 §05.2.4 the `src/` prefix; open only
   as the owner-visible choice of default.
2. **tsserver framing** in the proxy (§02.7.4 rule 9) and VS Code's built-in TypeScript outside
   `bob dev`. Recommendation: v1 documents `bob dev -- code`.
3. **`bob dev -- code`** joining an already running VS Code instance: per-project
   `--user-data-dir`; verify manually.
4. **JetBrains** under `bob dev` (GUI app in the namespace: X11/Wayland sockets are mirrored in
   pivot mode, but this is unverified). Recommendation: manual Stage 3 check.
5. **Extra LSP tools** (`Tool(...).LSP()` in the `DevShell` API, §02.7.0): add only if needed.
6. **Trust model** of the hook: per path (recommended) vs. per content hash (direnv).
7. **Registry key pinning**: trust on first use (recommended) vs. requiring `--key`.
8. **Working-tree symlinks** (`node_modules`, materialised codegen) point to `/nix/store` and
   dangle outside the namespace on hosts without `/nix`. Editors therefore browse them only
   through the LSP. Pointing top-level links to the host store path would fix direct browsing
   only one level deep. Recommendation: keep `/nix/store` in v1; §03 decides.
9. **`bob test --rerun`** mapping to Nix `--rebuild` (check mode compares outputs and may fail on
   non-reproducible tests). Recommendation: implement it as a fresh derivation with a nonce
   input instead, if `--rebuild` proves noisy.
10. **Per-ecosystem dev preparation** (added at integration): §02 expects one hook
    `Prepare(ctx, shell) error` per ecosystem; §03 provides the pieces (`engine.Ecosystem.Native`,
    codegen materialisation §03.6.6, `node_modules` swap §03.4.8, the Vite contract) but no single
    interface. Recommendation: an optional interface in `pkg/engine`,
    `type DevPreparer interface{ PrepareDev(ctx context.Context, req PlanRequest, shell string) (DevPrep, error) }`
    with `DevPrep{Files []GeneratedFile; Tools map[string]string; Servers []DevServerSpec}`,
    implemented by npm and codegen; specify in Stage 3 Phase 3.1.
11. **First `bob lock` in a new repository** (added at integration): the repo root is the
    nearest directory with `bob.lock` (§01 §1.2), so a repo without one has no root.
    Recommendation: `bob lock` (only it) treats the nearest ancestor with a `.git` directory as
    the root when no `bob.lock` exists and creates `bob.lock` there.

## Escalations

None. Everything above stays within D51/D59 and ADR 0002 Decision 5. The LSP proxy is the
mechanism the owner asked for in the Round 1 answer to item 8.
