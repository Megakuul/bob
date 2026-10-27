---
title: CLI reference
section: CLI
order: 1
summary: Every bob command and flag, the global flags, exit codes, JSON output and environment variables.
---

This page lists every command of `bob` with its flags. `bob help <command>` and
`bob <command> --help` print the usage line of a command; this page is checked against the
command table in the source on every CI run.

## Command line grammar

```text
bob [global flags] <command> [subcommand] [flags] [args] [-- passthrough args]
```

- Flags may come anywhere before `--`; everything after `--` goes unchanged to the program
  (`run`, `test`, `exec`, `dev`).
- Long flags are `--name`, `--name=value` or `--name value`; boolean flags take `--name=false`.
- The short flags are `-C`, `-v`, `-vv`, `-q`, `-j` and `-f`.
- An unknown command or flag is `BOB-E-0001` (exit 2), with a suggestion when one is close.

Patterns select packages: `//...` (everything), `//dir/...` (below a directory),
`//dir:all` (one `bob.go`), `//dir:name` (one package), `//dir` (`//dir:dir`). Relative
forms start from the current directory: `:name` (a package of this directory), `.` (all of
them; for `bob run`, the package named like the directory), `./...` (everything below) and
`./sub/dir:name`. Without a pattern, `build` and `test` use every package below the current
directory.

## Global flags

| Flag | Meaning |
|---|---|
| `-C <dir>` | run as if bob was started in `<dir>` |
| `-v` | add raw detail: `nix:` lines and raw build log excerpts |
| `-vv` | also bob's debug log on stderr |
| `-q` | print only errors and the summary |
| `-j <n>` | parallel local builds; default: the number of CPUs |
| `--json` | NDJSON events on stdout instead of human output (see [JSON output](#json-output)) |
| `--json-log` | with `--json`: also emit build log lines as `log` events |
| `--color=auto\|always\|never` | colour; `auto` colours a terminal unless `TERM=dumb`; `NO_COLOR` always wins |
| `--offline` | no network: binary caches off |
| `--server <url>` | pick a logged-in server when there are several |
| `--help` | print the command's usage line |

## Commands

### bob build

```text
bob build [patterns…] [--remote] [--platform P] [--variant V] [--print-paths] [--keep-going=true] [--lock]
```

Builds packages. Tests are not run (use `bob test`). No `result` links are created.

| Flag | Meaning |
|---|---|
| `--platform <p>` | build for `linux-amd64` or `linux-arm64` (cross-compiling Go, C/C++ and Rust) |
| `--variant <v>` | `debug`, `release`, `asan`, `ubsan`, `race` or a named variant; combine with `+` (`debug+asan`) |
| `--print-paths` | also print each package's output paths on the host |
| `--keep-going` | keep building independent packages after a failure (default `true`) |
| `--lock` | re-resolve stale `bob.lock` sections first instead of failing with `BOB-E-1012` |
| `--dry-run` | evaluate and plan only; print the planned build steps, build nothing |
| `--version <v>` | the version for release packages (default `0.0.0-dev`) |
| `--remote` | build on the logged-in bob server and download the results |
| `--no-download` | with `--remote`: do not download the outputs |

Output: one line per package (`built`, `cached`, `FAILED`, …), then error blocks, then a
summary such as `bob: 2 built, 1 cached`. A fully cached run prints `bob: nothing to do`.

### bob test

```text
bob test [patterns…] [--remote] [--rerun] [--lock] [--require-all-tests] [-- test args]
```

Builds and runs test packages. Passing results are cached (`cached`); failing tests run
again every time. Arguments after `--` are passed to the test programs and are part of the
cache key.

| Flag | Meaning |
|---|---|
| `--rerun` | run the selected tests even when a cached result exists |
| `--lock` | re-resolve stale `bob.lock` sections first |
| `--require-all-tests` | a test that cannot run on this machine fails instead of being `skipped` |
| `--platform <p>` | test for another platform (under qemu-user emulation) |
| `--variant <v>` | test a variant (`asan`, `race`, …) |
| `--keep-going` | keep testing after a failure (default `true`) |
| `--remote` | run the tests on the bob server |

Summary: `bob: 2 passed, 1 failed, 0 cached (exit 1)`.

### bob run

```text
bob run <label> [--platform native] [-- args]
```

Builds a program for this machine and runs it with your current directory, environment and
terminal. Once the program runs, bob exits with the program's status. The label must be a full
label (`//cmd/hello:hello`).

| Flag | Meaning |
|---|---|
| `--bin <name>` | which program to run when the package has several |
| `--platform <p>` | only `native` (the host) is supported for running |

### bob dev

```text
bob dev [--shell <name>] [--no-servers] [--servers-only] [-- cmd args]
```

Starts `$SHELL` (or the command after `--`) in the dev environment: toolchains on `PATH`,
`BOB_DEV=1`, `BOB_PROJECT` and `BOB_DEV_SHELL` set. Exits with the shell's status. See
[Dev environments](../04-guides/05-dev-environments.md).

| Flag | Meaning |
|---|---|
| `--shell <name>` | the dev shell to enter (default: `default`, or the only one) |
| `--no-servers` | do not start dev servers |
| `--servers-only` | run only the dev servers, in the foreground |

> [!WARNING]
> Dev servers are not started yet; start them inside the dev shell (`bob dev -- vite`).

### bob env

```text
bob env [--shell <name>] [--format sh|fish|json] [--hook=bash|zsh|fish]
```

Prints the script that puts the project's `.bob/bin` on `PATH` and exports its variables (for
direnv: `eval "$(bob env)"`).

| Flag | Meaning |
|---|---|
| `--shell <name>` | the dev shell |
| `--format <f>` | `sh` (default), `fish` or `json` |
| `--hook <shell>` | the incremental form the shell hook calls at every prompt |

### bob hook

```text
bob hook bash|zsh|fish
```

Prints the shell hook to put in your rc file (`eval "$(bob hook bash)"`).

### bob allow

```text
bob allow [dir]
```

Allows the shell hook to activate the project in `dir` (default: the current project).

### bob deny

```text
bob deny [dir]
```

Removes that permission.

### bob exec

```text
bob exec [--lsp] [--project <root>] [--shell <name>] -- <tool> [args]
```

Runs one tool of the dev environment inside bob's namespace, with the tool's exit status. The
`.bob/bin` shims call it.

| Flag | Meaning |
|---|---|
| `--lsp` | run a language server behind the path-rewriting proxy |
| `--project <root>` | the project whose environment to use (default: the current one) |
| `--shell <name>` | the dev shell |
| `--installed <name>` | run a program of a release installed with `bob install` |

### bob editor

```text
bob editor vscode|nvim|helix|zed|jetbrains [--write]
```

Prints what the editor needs to use the project's toolchains.

| Flag | Meaning |
|---|---|
| `--write` | merge the settings into the project's settings file |

### bob check

```text
bob check [--fix]
```

Reports drift without changing anything: native files edited by hand (`BOB-E-1011`), a stale
lock (`BOB-E-1012`), bob paths that git does not ignore (`BOB-E-1013`). Exit 0 with
`bob: no problems`, else exit 3.

| Flag | Meaning |
|---|---|
| `--fix` | rewrite the files, add the ignore entries and re-lock |
| `--determinism` | evaluate `bob.go` twice in separate sandboxes and compare the results |

### bob gen

```text
bob gen [patterns…] [--check] [--force]
```

Writes native tooling files and generated code into the tree without building anything else.

| Flag | Meaning |
|---|---|
| `--force` | overwrite generated files that were edited, keeping a backup in `.bob/gen/backup/` |
| `--check` | report what would change (not implemented yet: `BOB-E-9008`) |

### bob status

```text
bob status [--json]
```

Shows each generated root (`fresh`, `stale`, `missing`, `edited`, `orphan`) and whether the
lock is fresh.

### bob init

```text
bob init [dir] [--lang go|cc|rust|npm|ocaml]
```

Starts a new project in `dir` (default: the current directory, created when missing): writes
`bob.go` and the program and test of the language's [quickstart](../03-quickstart/01-go.md),
then locks it (`bob.lock`) and writes the files bob generates (`go.mod`, `Cargo.toml`,
`package.json`, …). `bob build`, `bob test` and `bob run` work right after it. The Go module
path is `example.com/<directory name>`; change it in `bob.go` and run `bob lock`.

`bob init` never overwrites anything: a directory that already holds `bob.go`, `bob.lock` or
a file of the template is refused (`BOB-E-0004`) before a file is written.

| Flag | Meaning |
|---|---|
| `--lang` | the language of the project: `go` (default), `cc` (C++), `rust`, `npm` (TypeScript), `ocaml` |

### bob lock

```text
bob lock [--update [names…]] [--nixpkgs <rev>] [--offline]
```

Adds missing pins and removes unused ones. Prints every change (`+`, `~`, `-`) or
`bob.lock: no changes`. See [Dependencies and locking](../04-guides/02-dependencies-and-locking.md).

In a new project without `bob.lock`, run `bob lock` in the directory that holds the root
`bob.go`: it becomes the repository root (or the directory of the nearest `.git` above it)
and `bob lock` creates `bob.lock` there. An empty `bob.lock` counts as "nothing locked yet".
Every other command needs `bob.lock` to find the root and says which command creates it.

| Flag | Meaning |
|---|---|
| `--update` | re-resolve the named entries (or all) to the newest allowed versions; `nixpkgs` names the nixpkgs pin |
| `--nixpkgs <rev>` | move the nixpkgs pin to a revision |
| `--rebuild` | resolve everything again from `bob.go` (after a broken or conflicted lock) |

### bob graph

```text
bob graph [patterns…] [--why <from> <to>] [--rdeps] [--depth N] [--format text|dot|json]
```

Prints the dependency graph with each package's definition position.

| Flag | Meaning |
|---|---|
| `--why <from> <to>` | the shortest dependency path between two packages |
| `--all` | with `--why`: up to 20 paths |
| `--rdeps` | reverse the edges (who depends on a package) |
| `--depth <n>` | limit the depth |
| `--format <f>` | `text` (default), `dot` (Graphviz) or `json` |

### bob log

```text
bob log <label> [--build <id>] [-f] [--attempt N]
```

Prints the full build log of a package's last local build, or of a server build.

| Flag | Meaning |
|---|---|
| `--build <id>` | read the log of a server build |
| `-f` | follow a running server build |
| `--attempt <n>` | the attempt of a retried job |

### bob cancel

```text
bob cancel <build-id>
```

Cancels a remote build.

### bob pipeline

```text
bob pipeline run <name> [--remote]
```

Runs a pipeline of `bob.go` step by step on this machine (`== step build`, `== step test`).
See [Pipelines](../04-guides/06-pipelines.md).

| Flag | Meaning |
|---|---|
| `--remote` | submit the pipeline to the server (not implemented yet: `BOB-E-9008`) |

### bob login

```text
bob login [<url>] [--device] [--token-stdin]
```

Logs in to a bob server: in a browser (authorization code with PKCE) or with a device code,
then configures the server as a binary cache. The first server becomes the default server
and the registry `default`.

| Flag | Meaning |
|---|---|
| `--device` | use the device flow: open the printed URL anywhere and enter the code |
| `--token-stdin` | store a service-account token read from stdin |

### bob logout

```text
bob logout [<url>]
```

Revokes the login and removes the server's credentials and cache entry.

### bob whoami

```text
bob whoami [<url>]
```

Shows the logged-in principal, the token scope and the projects it can access.

### bob install

```text
bob install <ref> [--as <name>] [--bin-dir <dir>]
```

Installs a release's programs for this machine from a registry. `<ref>` is
`[<registry>:]<project>/<release>[@<version>|@latest]`. See [Releases](../04-guides/07-releases.md#install-with-bob-install).

| Flag | Meaning |
|---|---|
| `--as <name>` | install under another entry name (prefixes the shims) |
| `--bin-dir <dir>` | where to write the shims (default `~/.local/bin`) |

### bob uninstall

```text
bob uninstall <ref>
```

Removes an installed release (a new generation).

### bob upgrade

```text
bob upgrade
```

Upgrades every installed release to its newest version, as one generation.

| Flag | Meaning |
|---|---|
| `--minor-only` | stay within the installed major version |

### bob rollback

```text
bob rollback
```

Goes back to the previous install generation.

| Flag | Meaning |
|---|---|
| `--to <n>` | go to generation `n` |

### bob list

```text
bob list
```

Lists installed releases.

| Flag | Meaning |
|---|---|
| `--available` | list the releases of a project on the registry (`bob list --available <project>`) |

### bob registry

```text
bob registry add <name> <url> | remove <name> | list | default <name>
```

Manages release registries. `add` pins the server's signing keys (trust on first use).

| Flag | Meaning |
|---|---|
| `--key <key>` | with `add`: pin this key instead of asking the server |
| `--refresh-keys` | with `add`: replace the pinned keys with the server's current ones |

### bob completion

```text
bob completion bash|zsh|fish
```

Prints the completion script (`source <(bob completion bash)`).

### bob gc

```text
bob gc [--dry-run]
```

Collects garbage in bob's store. Not implemented yet: it exits with `BOB-E-9008`.

| Flag | Meaning |
|---|---|
| `--dry-run` | show what would be deleted |

### bob doctor

```text
bob doctor [--repair]
```

Checks the namespace, the embedded builder, the store and the configuration files.

| Flag | Meaning |
|---|---|
| `--repair` | verify every file in the store and repair what can be repaired |
| `--print-nix-env` | print the environment bob gives Nix |

### bob version

```text
bob version [--json]
```

Prints bob's version, the Nix version, the default nixpkgs channel and the nixpkgs revision the
typed nixpkgs packages were generated from (`nixpkgsSurface` in `--json`).

## Progress line

Some steps take a while: the first run unpacks Nix, a new project fetches its toolchain, and
`bob lock` asks the registries. When such a step runs longer than about half a second and
stderr is a terminal, bob shows one line that updates in place:

```text
⠹ fetching or building go-1.26.8 · 7s
```

The line names what bob is doing (a spinner, the step, how long it has taken), is erased before
bob prints anything else, and is gone when the command ends. In a build it shows
`[3/8] building //cmd/hello:hello` for the packages that are running.

bob draws it only when it is useful, so scripts and logs never see it:

- stderr is not a terminal (a pipe, a file, a CI log);
- `TERM=dumb` or no `TERM`, or the `CI` variable is set;
- `--json`, `-q` or `-vv` is given;
- the command's output is read by a program (`bob env`, `bob hook`, `bob exec`, `bob completion`);
- `BOB_PROGRESS=0` is set.

`NO_COLOR` switches the spinner to plain ASCII (`| / - \`).

## Exit codes

The exit status tells you who has to act. With several errors, the highest-priority class
wins: interrupt, internal, store, resource, cache, fetch, upstream, definition, build, usage.

| Exit | Meaning | Error codes |
|---|---|---|
| 0 | success (warnings possible) | — |
| 1 | your code failed to build or test | `BOB-E-2xxx` |
| 2 | command-line usage error | `BOB-E-0xxx` |
| 3 | definition error in `bob.go` or `bob.lock`, found before building | `BOB-E-1xxx` |
| 4 | download, cache, server or registry failure | `BOB-E-3xxx`, `BOB-E-4xxx` |
| 5 | a nixpkgs package failed to build from source | `BOB-E-5xxx` |
| 6 | machine resources (disk, memory, killed) | `BOB-E-6xxx` |
| 7 | bob's store or host setup is damaged, busy or blocked | `BOB-E-7xxx` |
| 70 | a bug in bob | `BOB-E-9xxx` |
| 130 | cancelled (Ctrl-C, SIGTERM) | `BOB-E-8xxx` |

`bob run` and `bob exec` exit with the program's own status once it started (a signal N gives
128+N). Every code is listed in [Error codes](02-errors.md).

## JSON output

With `--json`, bob writes one JSON object per line to stdout and nothing else. Every event
has `"v":1`, `"event"` and `"time"`:

| Event | Fields |
|---|---|
| `run.start` | `command`, `args`, `requested` (labels after pattern expansion), `bob`, `nix` |
| `package.start`, `package.finish` | `label`, `action` / `status` (`built`, `cached`, `failed`, `skipped`, `cancelled`), `paths` |
| `test.result` | `label`, `status` (`pass`, `fail`, `cached`, `skipped`), `durationMs`, `failed`, `runner` |
| `error`, `warning` | `error{code, severity, class, message, label, definedAt, location, cause, excerpt, hint, log, args}` |
| `note` | `type`, `code`, `message` |
| `remote.submitted`, `remote.state` | `server`, `buildId`, `url`, `state` |
| `result` | `command`, `data` (the result of `graph`, `env`, `check`, `lock`, `list`, `version`, `login`) |
| `run.finish` | `exitCode`, `counts`, `drift`, `durationMs`; always the last line |

```text
$ bob version --json
{"v":1,"event":"run.start","time":"…","command":"version","args":["--json"],"requested":[],"bob":"v0.0.0-dev","nix":"2.34.9"}
{"v":1,"event":"result","time":"…","command":"version","data":{"bob":"v0.0.0-dev","nix":"2.34.9","nixpkgs":"nixos-26.05","nixpkgsSurface":"da6dc0e7b26060f2f71ce3ef7088fb56ee44fa36"}}
{"v":1,"event":"run.finish","time":"…","exitCode":0,"counts":{"built":0,"cached":0,"failed":0,"skipped":0,"cancelled":0,"upstream":0,"downloaded":0},"drift":[],"durationMs":0}
```

Fields are only added within version 1, never renamed or removed. The schema is
`schema/events-v1.json` in the bob repository.

## Environment variables

Variables you set:

| Variable | Effect |
|---|---|
| `BOB_HOME` | bob's state directory (default `~/.local/share/bob`) |
| `BOB_COLOR` | `always` forces colour (like `--color=always`); `NO_COLOR` still wins |
| `NO_COLOR` | any value turns colour off |
| `BOB_PROGRESS` | `0` turns off the [progress line](#progress-line) |
| `BOB_TOKEN` | a server token to use instead of the stored login (CI) |
| `BOB_SERVER` | the server to use when none is given |
| `BOB_SERVER_PROJECT` | the server project for `--remote` (default: the repository directory's name) |
| `BOB_GOPROXY`, `BOB_CRATES_INDEX`, `BOB_NPM_REGISTRY`, `BOB_OPAM_REPOSITORY`, `BOB_OPAM_CACHE_URL` | registry mirrors for `bob lock` (see [Dependencies](../04-guides/02-dependencies-and-locking.md#mirrors-and-private-registries)) |
| `BOB_GO_CUTOFF` | `1` turns on the Go export-data early cutoff |
| `BOB` | the bob binary that `.bob/bin` shims and `bob install` shims call |
| `XDG_CONFIG_HOME`, `XDG_STATE_HOME`, `XDG_DATA_HOME` | where `config.json`/`credentials.json`, `allow.json` and the default store live |

Variables bob sets:

| Variable | Where |
|---|---|
| `BOB_DEV`, `BOB_PROJECT`, `BOB_DEV_SHELL` | inside `bob dev` |
| `BOB_PROJECT`, `BOB_ENV_HASH`, `BOB_ENV_RESTORE`, `BOB_DENIED_SEEN` | in your shell, by the shell hook |
| `BOB_SRC`, `BOB_OUT`, `BOB_IN_<NAME>` | inside a `bob.Run` step: sources, output directory, inputs |
| `BOB_ENTRY`, `BOB_APPIMAGE_MODE`, `BOB_APPIMAGE_DEBUG` | read by bob-built AppImages: pick an entry, force a launcher mode, print launcher details |

## Files

| File | Content |
|---|---|
| `~/.config/bob/config.json` | default server, registries, extra binary caches |
| `~/.config/bob/credentials.json` | server logins (mode 0600) |
| `~/.local/state/bob/allow.json` | projects the shell hook may activate |
| `~/.local/share/bob/` | the store and caches (`$BOB_HOME`) |
| `<repo>/bob.lock` | the pins of a repository |
| `<repo>/.bob/` | per-project state: evaluator, dev environment, shims, labels |

## Next steps

- [Error codes](02-errors.md).
- [Dev environments](../04-guides/05-dev-environments.md).
