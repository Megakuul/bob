---
title: What is bob
section: Introduction
order: 1
summary: bob builds Go, C/C++, Rust, TypeScript and OCaml code from typed Go build files, with Nix as a hidden engine.
---

On this page you learn what bob is, what it is good at, and how its two programs fit together.

## bob in one paragraph

bob is a build system. You describe your software in small Go files named `bob.go`, next to
the code they describe. bob turns those descriptions into many small, cached, sandboxed build
steps and runs them with [Nix](https://nixos.org). You never write Nix,
and you never see a Nix error: bob reports problems in its own words, with the position in
your `bob.go`.

```go
// cmd/hello/bob.go
var Binary = bob.Define(func(p *bob.P) *golang.Binary {
	return hello.Module(p).Binary(golang.BinaryOpts{})
})
```

That is a complete build definition for a Go program: it sits in the program's own directory
and builds that directory. Your editor type-checks it, offers completion for every option, and
jumps to the definition of `hello.Module`. Nothing is named by a string: libraries, tools and
even nixpkgs packages are typed Go values (`nixpkgs.Openssl`), so a typo does not compile.

## What bob does for you

- **One typed build language for many ecosystems.** Go, C and C++, Rust, TypeScript/npm and
  OCaml, plus code generators for protobuf/connect, GraphQL and OpenAPI. They link into each
  other: a Go program can use a C library and a Rust static library through cgo.
- **Hermetic builds.** Every step runs in a sandbox without network access. Only what you
  declare is visible. The same inputs give the same outputs on every machine.
- **Fine-grained caching.** bob splits builds into small packages (one Go package, one Rust
  crate, one C library). A change rebuilds only what depends on it.
- **Pinned dependencies.** `bob lock` writes every third-party module, crate, npm package and
  opam package, plus the exact nixpkgs revision, to one file: `bob.lock`.
- **Native tooling keeps working.** bob writes `go.mod`, `Cargo.toml`, `package.json`,
  `tsconfig.json` and `compile_commands.json` for your editor and language servers.
- **Shippable outputs.** OCI images (without Docker), tarballs, AppImages and versioned
  releases are ordinary build targets.

## Two programs

| Program | Who runs it | What it does |
|---|---|---|
| `bob` | developers, CI | evaluates `bob.go`, builds, tests, runs, manages dev environments |
| `bob-server` | operators | an optional build and cache server: orchestrators, workers, a Nix binary cache, an OCI registry, releases and a web UI; it stores everything in S3 |

`bob` works on its own. You do not need a server to build, test or ship. The server adds
shared caching, remote builds on a worker fleet, CI pipelines and a place to publish releases.

> [!NOTE]
> bob is young. The framework and the CLI work for the flows in the quickstarts. Some
> commands and server features are not finished yet. The
> [status page](../07-reference/03-status.md) lists them, with workarounds.

## Where bob runs

bob runs on Linux, on `x86_64` (amd64) and `aarch64` (arm64). It needs unprivileged user
namespaces, which most distributions allow. It does not need root, Docker or a daemon, and
the AppImage does not even need Nix. See [Requirements](../02-install/01-requirements.md).

## Next steps

- [Concepts](02-concepts.md): `bob.go`, labels, `bob.lock`, the store, caching and hermeticity.
- [Install bob](../02-install/02-install-bob.md).
- Jump straight into the [Go quickstart](../03-quickstart/01-go.md).
