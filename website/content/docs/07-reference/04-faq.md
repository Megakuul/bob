---
title: FAQ
section: Reference
order: 4
summary: Short answers to common questions about bob, Nix, caching, the server and editors.
---

Short answers, with links to the details.

## Do I need to know Nix?

No. You write Go in `bob.go`; the only Nix concept is a nixpkgs package name such as `zlib`.
bob never shows raw Nix errors; with `-v` it adds them as `nix:` lines for bug reports.

## Do I need Nix installed?

Not with the AppImage or a source build: they carry a static Nix. Installed with Nix (a
profile or NixOS), bob uses the Nix that installed it. Either way bob keeps its own store
in `~/.local/share/bob`. See [Install bob](../02-install/02-install-bob.md#which-nix-bob-uses).

## Why is my build definition Go?

So that your editor type-checks it, completes options and jumps to definitions, and so that a
dependency is a typed value instead of a string. `bob.go` files have the `bob` build tag, so
they never mix with your program. For gopls to see them anyway, start it from the project's
`.bob/bin` (the shell hook, or `bob editor nvim|helix|vscode --write`); see
[Editors](../04-guides/05-dev-environments.md#editors).

## Does bob replace go build, cargo, npm?

For builds, yes: bob calls the compilers directly, one small cached step per package or crate.
For tooling, no: bob writes `go.mod`, `Cargo.toml`, `package.json` and `node_modules`, so `go
vet`, `cargo check`, gopls and rust-analyzer keep working inside `bob dev`.

## Why does bob need user namespaces?

To mount its own store at `/nix` without root. Without them bob refuses to start and prints
the fix for your system. See [Requirements](../02-install/01-requirements.md).

## Where are my build outputs?

In bob's store. `bob build --print-paths` prints their paths; `bob run` runs programs.
There are no `result` links.

## Why did nothing rebuild after my change?

Because the change did not reach any package's inputs: the file is not in any package's
sources, is ignored by git, or the generated code came out byte-identical (early cutoff).
`bob graph` shows what depends on what.

## How do I free disk space?

`bob gc` is not implemented yet. Delete `~/.local/share/bob` (or `$BOB_HOME`) when no bob
command runs; bob downloads what it needs again.

## Can I use bob without a server?

Yes. Every command except `login`, `logout`, `--remote`, `cancel`, `install` and friends
works without one. The server adds shared caching, remote builds, pipelines and releases.

## Is the server's cache safe from a compromised worker?

Workers never publish directly: orchestrators verify and sign every output, and workers have
no access to users, tokens or signing keys. A compromised worker can still publish wrong
outputs for the jobs it claimed. See [Operations and security](../06-server/12-operations.md).

## Next steps

- [Limitations and status](03-status.md).
- [Concepts](../01-introduction/02-concepts.md).
