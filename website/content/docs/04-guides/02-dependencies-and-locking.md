---
title: Dependencies and locking
section: Guides
order: 2
summary: How bob.lock pins nixpkgs, Go modules, crates, npm and opam packages and downloads, and how to add, update and repair pins.
---

This guide shows how third-party code gets into a bob build: you declare it in `bob.go`,
`bob lock` pins it in `bob.lock`, and builds use only the pinned versions. You will add,
update and remove dependencies and repair a lock after a merge conflict.

## What bob.lock holds

| Section | Filled from | Holds |
|---|---|---|
| `nixpkgs` | `bob.Repo(bob.RepoOpts{Nixpkgs: …})` | channel, revision, source URL and hash |
| `go` | `golang.Require`, generator runtimes | the pruned module graph per Go module: versions, `go.sum` hashes, source tree hashes |
| `rust` | `rust.Crate` | every resolved crate with checksum, features and dependency edges |
| `npm` | `npm.Dep`, `npm.Alias` | every resolved package per importer, with integrity hashes |
| `opam` | `ocaml.Opam` | the opam-repository commit and every package's source archive and checksum |
| `cc` | `cc.External` | archive URLs and hashes |
| `sources` | `bob.Fetch` | file or archive URLs and hashes |

Each section records a hash of the declarations it was resolved from. When you change a
declaration, only that section becomes stale.

## The rules

- `bob lock` adds what is missing and removes what nothing uses. It never moves an existing
  pin on its own.
- `bob lock --update [names…]` re-resolves the named entries (or everything) to the newest
  versions your declarations allow.
- `bob build` and `bob test` never change the lock. With a stale section they stop with
  `BOB-E-1012` and make no network request; `--lock` re-resolves the stale sections first.
- Workers of a bob server and `bob install` never re-resolve: they build exactly what the
  lock says.

## Add a dependency

Add the declaration, then run `bob lock`. For example, a second Go module in the
[Go quickstart](../03-quickstart/01-go.md):

```go
Require: []golang.Requirement{
	golang.Require("github.com/google/uuid", "v1.6.0"),
	golang.Require("golang.org/x/sync", "v0.20.0"),
},
```

Before you lock, a build refuses to start:

```text
error[BOB-E-1012]: bob.lock is out of date: //:module needs go golang.org/x/sync v0.20.0 (not pinned)
  package defined at bob.go:18
  hint: run bob lock

bob: nothing was built (exit 3)
```

```text
$ bob lock
bob.lock: 1 change
  + go     golang.org/x/sync   v0.20.0
```

Run `bob lock` again and it says `bob.lock: no changes`. If you prefer one step,
`bob build --lock` locks the stale sections and builds.

How you write versions differs per ecosystem, as in its native tool:

| Ecosystem | Declaration | Version syntax |
|---|---|---|
| Go | `golang.Require("golang.org/x/sync", "v0.20.0")` | an exact version (MVS still picks the highest required version in the graph) |
| Rust | `rust.Crate("serde", "1")` | a Cargo requirement: `"1"`, `"^0.13"`, `"~1.2"`, `"=1.38.0"` |
| npm | `npm.Dep("zod", "^4.0.0")` | a node-semver range or a dist-tag |
| OCaml | `ocaml.Opam("lwt", ">= 5.7")` | an opam version formula, `""` for any |

## Update dependencies

Move pins forward within your declared ranges:

```bash
bob lock --update                 # everything
bob lock --update serde zod       # only these entries (and what they require)
bob lock --update nixpkgs         # the nixpkgs pin to the channel's current revision
```

`bob lock` prints every change:

```text
bob.lock: 1 change
  ~ nixpkgs  nixpkgs   cd4ea8d57de1 → da6dc0e7b260
```

To pin nixpkgs to a specific revision, use `bob lock --nixpkgs <40-hex revision>`, or write
it into `bob.go` as `Nixpkgs: "nixos-26.05@<revision>"`.

For Go, the version in `golang.Require` is the version: change it in `bob.go` and run
`bob lock`.

> [!WARNING]
> `bob lock --update <go module>` currently moves that module to its newest version even
> though `golang.Require` names an exact one, and a later `bob lock --update` moves it back.
> For Go modules, edit the version in `bob.go` instead.

## Remove a dependency

Delete the declaration and run `bob lock`. Unused entries, including transitive ones, are
removed:

```text
bob.lock: 2 changes
  - go     golang.org/x/sync    v0.20.0
  - go     golang.org/x/text    v0.30.0
```

## Merge conflicts in bob.lock

`bob.lock` is canonical, sorted JSON, so most merges are clean. If a merge leaves conflict
markers, every command stops:

```text
error[BOB-E-1019]: bob.lock cannot be read: merge conflict marker <<<<<<< (line 3)
  hint: resolve the merge conflict, or run bob lock --rebuild to resolve everything again from bob.go
```

Resolve the conflict by hand, or run `bob lock --rebuild`. It ignores the old file except the
nixpkgs revision and resolves every section again from `bob.go`.

## Offline work

After everything is downloaded, `--offline` (a global flag) turns off the binary caches, so
builds use only what is in the store.

> [!WARNING]
> `bob lock --offline` is meant to fail with `BOB-E-3002` when it would need the network. At
> the time of writing, the Go resolver still downloads new modules with `--offline`.

## Mirrors and private registries

Resolution reads the public registries by default. Environment variables point bob at a
mirror or a private registry:

| Variable | Default | Effect |
|---|---|---|
| `BOB_GOPROXY` | `https://proxy.golang.org` | Go module proxy for `bob lock`; an http(s) URL is recorded in `bob.lock` |
| `BOB_CRATES_INDEX` | the crates.io sparse index | crates index mirror; `bob.lock` still records crates.io |
| `BOB_NPM_REGISTRY` | the public npm registry | npm registry for resolution and downloads |
| `BOB_OPAM_REPOSITORY` | opam-repository on GitHub | a base URL serving `<commit>.tar.gz` snapshots of opam-repository; `bob.lock` still records GitHub and the commit |
| `BOB_OPAM_CACHE_URL` | none (opam's public cache is the last fallback) | an opam archive cache tried before each package's own URL |

The Go module proxy must speak the standard `GOPROXY` protocol; `direct` (fetching from VCS)
is not supported.

## Hashes and tampering

Every download is checked against the hash in `bob.lock` before it enters the store. A
mismatch stops the build with `BOB-E-3001` ("download does not match bob.lock"). If the
upstream file legitimately changed, re-lock it; otherwise, treat it as tampering.

## Next steps

- [Multi-language projects](03-multi-language.md).
- [Generated code](04-generated-code.md): generator runtimes are added to the lock for you.
- [CLI reference: bob lock](../05-cli/01-commands.md#bob-lock).
