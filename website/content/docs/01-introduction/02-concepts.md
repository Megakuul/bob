---
title: Concepts
section: Introduction
order: 2
summary: bob.go files, packages and labels, bob.lock, the store, early cutoff, hermeticity and how bob uses Nix.
---

This page explains the ideas you meet in every bob project. Read it once; the quickstarts and
guides assume these words.

## bob.go files

Build definitions live in files named exactly `bob.go`, at most one per directory, next to the
code they describe. They are ordinary Go, with three rules you will notice:

1. The first line is `//go:build bob`. That build tag hides the file from `go build`,
   `go vet` and `go test`, so it never mixes with your program.
2. The package clause is the package of the other Go files in the directory. In a directory
   without Go files it is the directory name (lower case). The repository root may use any name.
3. A `bob.go` may import only the standard library, the bob framework
   (`github.com/megakuul/bob/pkg/...`) and other `bob.go` packages of the same repository.

Why the build tag: a directory can hold a program and its `bob.go` in the same Go package,
and `go build ./...` must not compile your build definition into the program. The cost is that
tools that follow the Go rules (gopls, `go vet`) skip `bob.go` unless they run with
`-tags=bob` and can find the framework module. bob takes care of both, see
[Editors](../04-guides/05-dev-environments.md#editors): language servers started from
`.bob/bin` (the shims) have the tag, and `go.mod` points at the framework source in
`.bob/framework`, so a plain `go` or gopls resolves `github.com/megakuul/bob` with no network
and no proxy.

bob checks these rules before it compiles anything and lists every violation at once, with
the file and line (error `BOB-E-1024`).

A `bob.go` file declares packages with `bob.Define`:

```go
// Lib is libgreet.a (label //libs/greet:greet).
var Lib = bob.Define(func(p *bob.P) *cc.Library {
	return cc.StaticLibrary(p, "greet", cc.LibraryOpts{
		Srcs: bob.Glob("src/*.cpp"),
		Hdrs: bob.IncludeDir("include"),
	})
})
```

Each directory that builds something has its own `bob.go`, and that file describes only its
own directory: its paths are relative to it, never `..`, never into a subdirectory with its own
`bob.go`. A directory holds at most one library or program per ecosystem; a larger project is a
tree of such packages that use each other through typed values.

`bob.Define` turns a constructor into a memoised function. Other `bob.go` files call it to get
the same value (`greet.Lib(p)`), so dependencies are typed values, not strings. You cannot
pass a Go binary where a C library is expected: the compiler refuses it.

The `p *bob.P` argument carries the configuration being built: the target platform
(`linux-amd64` or `linux-arm64`) and the variant (release or debug, sanitizers, features).
bob calls every constructor once per configuration it needs.

## Packages and labels

Every value that comes out of an ecosystem constructor is a **package**: a node of the build
graph with one or more outputs. Packages have **labels**:

| Label | Meaning |
|---|---|
| `//libs/greet:greet` | package `greet` defined in `libs/greet/bob.go` |
| `//:module` | package `module` defined in the root `bob.go` |
| `nixpkgs:zlib` | the nixpkgs package `zlib` |
| `example.com/other//libs/x:x` | a package of another bob repository (cross-repo) |

The name after `:` is the constructor's name argument, or a default the ecosystem picks (the
directory name for Rust crates, `go_test` for Go test units, and so on). `bob graph` prints
every label.

Commands take **patterns**:

| Pattern | Matches |
|---|---|
| `//...` | every package of the repository |
| `//libs/...` | every package below `libs/` |
| `//libs/greet:all` | every package defined in `libs/greet/bob.go` |
| `//libs/greet` | short for `//libs/greet:greet` |
| `:greet`, `.`, `./...` | relative to the current directory: in `libs/greet`, `//libs/greet:greet`, `//libs/greet:all`, `//libs/greet/...` |

With no pattern, `bob build` and `bob test` use every package below the current directory.

## The repository root and bob.lock

The repository root is the nearest directory that contains `bob.lock`. Every command finds it
by walking up from the current directory, like git finds `.git`. `bob init` starts a new
project with its `bob.lock`. In a repository without `bob.lock`, `bob lock` uses the nearest
directory with `.git`, else the current directory when it holds `bob.go`, and creates
`bob.lock` there. An empty `bob.lock` marks the root and pins nothing yet.

`bob.lock` pins everything that comes from outside your repository:

- the nixpkgs revision, which decides the compilers and system libraries;
- Go modules, Rust crates, npm packages and opam packages, with their hashes;
- external downloads (`bob.Fetch`, `cc.External`).

It is canonical JSON, sorted, with one entry per pin, so code review shows real changes.
`bob lock` adds what is missing and removes what is unused. It never moves an existing pin
unless you ask with `bob lock --update`. `bob build` and `bob test` refuse to run with an
out-of-date lock (error `BOB-E-1012`) unless you pass `--lock`.

Commit `bob.lock`. See [Dependencies and locking](../04-guides/02-dependencies-and-locking.md).

## Generated files and .bob/

bob writes native files so that your editor and the usual tools understand the project:
`go.mod`/`go.sum`, `Cargo.toml`/`Cargo.lock`, `package.json`/`tsconfig.json`,
`compile_commands.json`, `.merlin`, and generated code directories such as `gen/proto`. They
start with a "generated by bob" header. Do not edit them: change `bob.go` and let bob rewrite
them. `bob check` reports a hand edit as `BOB-E-1011`.

The `bob.go` files are Go code, so every repository has a root `go.mod`, also one in C++ or
Rust only. It requires the bob framework and replaces it by `./.bob/framework`, a copy of the
framework's source that bob writes (and rewrites when bob is updated); `.bob/` is not in git. bob writes it: a Go module at the root (`golang.NewModule` in the root `bob.go`)
names its own path; otherwise `bob lock` writes one whose module path is the one the `bob.go`
files import each other by (`example.com/app/libs/foo`), or else the name of the repository
directory. A `go.mod` you wrote before using bob is taken over when it says the same.

bob keeps per-project state in `.bob/` at the repository root (the compiled evaluator, the dev
environment, shims). bob keeps everything it generates and does not commit (`.bob/`, `compile_commands.json`,
`Cargo.toml`, `package.json`, …; not `go.mod` and `go.sum`) out of git in a marked block of
your `.gitignore`, written by `bob init` and kept current by `bob lock`, `bob gen` and
`bob check --fix`. Your own lines are never touched.

## The store

bob keeps build outputs in its own store, in your home directory:

```tree
📁 ~/.local/share/bob/
  📁 nix/
    📁 store/
    📁 var/
  📁 bin/
  📁 etc/
    📁 nix/
      📄 nix.conf
  📁 cache/
```

`nix/store/` holds the build outputs, one directory per output, and `nix/var/` the store
database. `bin/` holds the unpacked embedded Nix (AppImage and source builds), `etc/nix/nix.conf`
the generated configuration (binary caches and their keys), and `cache/` evaluation, download
and index caches.

Inside bob, that directory is mounted at `/nix` in a private mount namespace, so store paths
look like ordinary Nix store paths (`/nix/store/…`). This needs no root: bob uses unprivileged
user namespaces. Your system's Nix store, if there is one, is hidden and never touched.

`bob build --print-paths` prints where outputs are on the host
(`~/.local/share/bob/nix/store/…`). You can read files there. Programs built by bob run
through `bob run` or `bob exec`, which enter the namespace for you.

## Caching and early cutoff

Each package becomes one or more build steps. A step's identity is the hash of everything that
goes into it: its sources, its dependencies' outputs, the toolchain and the flags. If the
identity is in the store, or in a binary cache, bob skips the step.

- **Sources are per package.** A package sees only the files its `bob.go` selects
  (`bob.Glob`, `bob.Files`, `bob.Dir`). Editing a file rebuilds only the packages that include
  it. Files ignored by git, `bob.go` itself and `.bob/` never count.
- **Small steps.** One Go package, one Rust crate, one C library: a change rebuilds the
  package and what depends on it, not the whole project.
- **Early cutoff for generated code.** bob builds code generators first and stores their
  output by content. If regenerating gives the same bytes (a comment change in a `.proto`
  file), nothing downstream rebuilds.
- **Early cutoff for Go (opt-in).** With `BOB_GO_CUTOFF=1`, Go packages compile against the
  export data of their imports. A change that keeps a package's API identical then rebuilds
  only that package and the final link. It is off by default while it is being measured.

Binary caches: bob substitutes from `cache.nixos.org` (for toolchains and system libraries)
and, after `bob login`, from your bob server.

## Hermeticity

Build steps run in Nix's sandbox:

- no network access (a step that tries gets `BOB-E-2009`);
- no access to your home directory or the rest of the file system;
- a fixed environment: `HOME=/homeless-shelter`, `TZ=UTC`, `LC_ALL=C`, `SOURCE_DATE_EPOCH=1`;
- only the tools and inputs you declared.

Downloads happen before the build, by bob itself, and are checked against the hashes in
`bob.lock`. Evaluating your `bob.go` files also runs in a sandbox: no network, no environment,
no clock. `bob check --determinism` evaluates twice and compares the results.

## How bob uses Nix

bob uses Nix only as an engine. Installed with Nix (a Nix profile or NixOS), bob runs the Nix
that installed it; the AppImage and source builds embed a static Nix (version 2.34.9). Either
way bob uses its own store and configuration:

1. bob compiles your `bob.go` files into a small evaluator program and runs it. The output
   is a build graph.
2. bob's generators turn each package into build steps (Nix derivations) directly, without
   writing any `.nix` file. Each step runs `bob-builder`, a small static program embedded in
   `bob`.
3. Nix realises the steps: it checks the store and the binary caches, runs the sandbox, and
   records outputs.
4. bob translates what happened into its own output and error codes.

Compilers and system libraries come from [nixpkgs](https://github.com/NixOS/nixpkgs) at the
revision pinned in `bob.lock` (`nixpkgs.Tool`, `nixpkgs.Lib`). That is the only Nix concept
you meet: a package name such as `zlib` or `openssl`.

## Next steps

- [Install bob](../02-install/02-install-bob.md).
- Build your first project: [Go](../03-quickstart/01-go.md),
  [C/C++](../03-quickstart/02-c-cpp.md), [Rust](../03-quickstart/03-rust.md),
  [TypeScript](../03-quickstart/04-typescript.md), [OCaml](../03-quickstart/05-ocaml.md).
- [bob.go reference](../04-guides/01-bob-go-reference.md).
