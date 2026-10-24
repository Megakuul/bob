---
title: bob.go reference
section: Guides
order: 1
summary: Every constructor of the bob.go API by ecosystem - core, nixpkgs, Go, C/C++, Rust, npm, OCaml, code generators, services, outputs and releases.
---

This page walks through the packages a `bob.go` file imports, with the constructors you use
and an example for each ecosystem. The complete list of every exported function, type and
method, generated from the source, is the [API index](../07-reference/02-api-index.md). Field
documentation is in the doc comments: your editor shows it on hover, and
`go doc github.com/megakuul/bob/pkg/golang` prints it in a checkout of bob.

Every example on this page is a complete `bob.go` file that type-checks against the
framework.

## How constructors look

All constructors follow one shape:

- They take `p *bob.P` first, then a name (when the kind has one), then an options struct
  (`cc.LibraryOpts`, `golang.ModuleOpts`, …). Zero values are the defaults.
- They return a typed value that you pass to other constructors. Strings name only the
  package itself and nixpkgs packages.
- You call them inside `bob.Define`, assigned to a package-level variable.

```go verify
//go:build bob

package greet

import (
	"github.com/megakuul/bob/pkg/bob"
	"github.com/megakuul/bob/pkg/cc"
)

// Lib is libgreet.a (label //<dir>:greet).
var Lib = bob.Define(func(p *bob.P) *cc.Library {
	return cc.StaticLibrary(p, "greet", cc.LibraryOpts{Srcs: bob.Glob("src/*.c"), Hdrs: "include"})
})

// Tool links Lib. Calling Lib(p) again returns the same value: bob.Define memoises per P.
var Tool = bob.Define(func(p *bob.P) *cc.Binary {
	return cc.NewBinary(p, "tool", cc.BinaryOpts{Srcs: bob.Files("tool.c"), Deps: []cc.Linkable{Lib(p)}})
})
```

## Core (pkg/bob)

`import "github.com/megakuul/bob/pkg/bob"`

### Definitions and repository settings

| Function | Use |
|---|---|
| `bob.Define(fn)` | turns a constructor into a memoised, registered constructor; only in package-level `var` declarations |
| `bob.Repo(bob.RepoOpts{…})` | repository settings, root `bob.go` only, at most once: `Nixpkgs` (a channel such as `"nixos-26.05"`, or `"<channel>@<rev>"`) and named `Variants` |
| `bob.Import(module, version)` | requires another bob repository as a Go module; its `bob.go` packages become importable and its labels are `<module>//dir:name` |
| `bob.Main()` | the entry point of the generated evaluator; never call it |

`p.Platform()`, `p.Variant()` and `p.Host()` tell a constructor what it builds for. Build
tools use `p.Host()`: the host platform with the release profile. `p.Toolchain()` and
`bob.ToolchainFor(host, target)` describe the C/C++ compiler for a platform pair; ecosystems
use them, `bob.go` files rarely need them.

### Sources

| Function | Selects |
|---|---|
| `bob.Glob("src/**/*.cpp", "!src/legacy/**")` | files by pattern (`*`, `?`, `**`, `[…]`; `!` excludes) |
| `bob.Files("a.c", "b.c")` | exact files |
| `bob.Dir("include")` | a whole directory tree |
| `s.Union(o…)` | several selections together |

Paths are relative to the `bob.go` directory and may not leave the repository
(`BOB-E-1025`). A pattern that matches nothing is an error. Files ignored by git, `.bob/`
and `bob.go` files are never selected.

### Custom build steps and downloads

`bob.Run` is a hermetic step for anything the ecosystems do not cover. It sees only the
declared tools and inputs, has no network, and must create its declared outputs:

```go verify
//go:build bob

package version

import (
	"github.com/megakuul/bob/pkg/bob"
	"github.com/megakuul/bob/pkg/nixpkgs"
)

// File writes version.txt (label //<dir>:version-file).
var File = bob.Define(func(p *bob.P) *bob.Step {
	return bob.Run(p, "version-file", bob.RunOpts{
		Tools:   []bob.Tool{nixpkgs.Tool(p, "bash"), nixpkgs.Tool(p, "coreutils")},
		Cmd:     []string{"bash", "-c", "echo 1.2.3 > $BOB_OUT/version.txt"},
		Outputs: []string{"version.txt"},
	})
})

// License is a downloaded file, pinned in bob.lock (label //<dir>:license).
var License = bob.Define(func(p *bob.P) bob.Tree {
	return bob.Fetch(p, bob.FetchOpts{URL: "https://raw.githubusercontent.com/google/uuid/v1.6.0/LICENSE"})
})
```

Inside a step: the working directory is a writable copy of `Srcs` (`$BOB_SRC`), outputs go to
`$BOB_OUT`, each entry `name` of `Inputs` is at `$BOB_IN_<NAME>`, and `SOURCE_DATE_EPOCH=1`,
`TZ=UTC`, `LC_ALL=C`. `step.File("version.txt")` passes one output on as a `bob.Tree`.
`bob.Exe(step, "bin/tool")` makes a step's output usable as a program (for `bob run`, images
and AppImages).

`bob.Fetch` downloads before the build, with bob's own fetcher, and checks the hash that
`bob lock` recorded. `Archive: true` unpacks (`Strip` drops leading directories).

### Dev shells

`bob.NewShell(p, bob.DevShell{…})` declares a dev environment for `bob dev`, `bob env`,
`bob exec` and the shell hook: extra `Packages` (tools), `Env` variables, `Hooks` (shell
commands run before the shell starts) and `Servers`. The toolchains of the ecosystems you use
are added automatically for Go, Rust, npm and OCaml. See
[Dev environments](05-dev-environments.md).

```go verify
//go:build bob

package acme

import (
	"github.com/megakuul/bob/pkg/bob"
	"github.com/megakuul/bob/pkg/nixpkgs"
)

// Dev is the default dev shell.
var Dev = bob.Define(func(p *bob.P) *bob.Shell {
	return bob.NewShell(p, bob.DevShell{
		Packages: []bob.Tool{nixpkgs.Tool(p, "jq"), nixpkgs.Tool(p, "postgresql")},
		Env:      map[string]string{"ACME_ENV": "dev"},
		Hooks:    []string{"echo welcome"},
	})
})
```

### Test services

Tests can start loopback services first. Service *i* gets its own address `127.0.0.(i+2)`;
the test sees `<NAME>_ADDR` (the address) and the service's own variables.

| Function | Use |
|---|---|
| `bob.ServiceFrom(bin, bob.ServiceOptions{…})` | runs a program built in this repository as a service |
| `bob.TCPReady(port)`, `bob.CmdReady(argv…)` | when the service counts as ready |
| `bob.SpecOf(s)` | the resolved service description; used by ecosystem test constructors |

`pkg/services/postgres` is a ready-made PostgreSQL service, see
[Services](#services-pkgservicespostgres).

### Variants and platforms

Every package can be built for `bob.LinuxAMD64` and `bob.LinuxARM64` (`--platform`) and in
variants (`--variant`): the `release` and `debug` profiles, the sanitizers `asan` and
`ubsan`, Go's `race`, and named variants that you declare in `bob.Repo`:

| Function | Use |
|---|---|
| `bob.NamedVariant(name, mods…)` | declares a named variant |
| `bob.WithProfile(bob.Debug)` | sets the profile |
| `bob.WithSanitize(bob.ASan, bob.UBSan)` | adds sanitizers |
| `bob.WithRace()` | Go race detector |
| `bob.WithFeatures(crate, features…)` | extra Rust features of a crate |

`bob.ParseLabel`, `bob.Parse` and `bob.ParseConfigured` parse labels, patterns and configured
labels (`//api:api@linux-amd64+debug+asan`); bob uses them for the command line.

### Pipelines

`bob.Pipeline(name, bob.PipelineOpts{…})` registers a pipeline: what to build
(`Targets`), what to test (`Tests`), which releases to publish (`Releases`), when to run
(`Triggers`: `bob.OnPush(branchGlob)`, `bob.OnPullRequest()`, `bob.OnTag(glob)`) and what to do
with an older run of the same ref (`Concurrency`: `bob.Queue`, `bob.Supersede`,
`bob.Parallel`). See [Pipelines](06-pipelines.md).

```go verify
//go:build bob

package acme

import "github.com/megakuul/bob/pkg/bob"

var _ = bob.Repo(bob.RepoOpts{
	Nixpkgs:  "nixos-26.05",
	Variants: []bob.Named{bob.NamedVariant("asan", bob.WithSanitize(bob.ASan, bob.UBSan), bob.WithProfile(bob.Debug))},
})

var _ = bob.Pipeline("ci", bob.PipelineOpts{
	Triggers: []bob.Trigger{bob.OnPush("main"), bob.OnPullRequest()},
	Targets:  []bob.Pattern{"//..."},
	Tests:    []bob.Pattern{"//..."},
})

var _ = bob.Pipeline("release", bob.PipelineOpts{
	Triggers: []bob.Trigger{bob.OnTag("v*")},
	Releases: []string{"api"},
})
```

## nixpkgs packages (pkg/nixpkgs)

`import "github.com/megakuul/bob/pkg/nixpkgs"`

Packages from the pinned nixpkgs revision, named by their attribute (`"zlib"`, `"openssl"`,
`"jq"`). This is the only place a Nix concept shows up in `bob.go`.

| Function | Use |
|---|---|
| `nixpkgs.Tool(p, attr, opts…)` | a build-time tool, from the host package set; for `bob.Run`, dev shells, `ExternalOpts.Tools` |
| `nixpkgs.Lib(p, attr, opts…)` | a library linked into target code; a `cc.Linkable` |
| `nixpkgs.Pkg(p, attr, opts…)` | a run-time package (data, services) of the target set |
| `nixpkgs.Exe(p, attr, mainProgram, opts…)` | a run-time package used as a program, e.g. an extra AppImage entry |

Options: `nixpkgs.Modules("libcrypto")` (pkg-config modules to link; default all),
`nixpkgs.Outputs("dev", "lib")`, `nixpkgs.Cross()` (take a library from the cross package set
instead of the target system's own), `nixpkgs.AllowInsecure("CVE-…")` and
`nixpkgs.AllowUnfree()`. bob checks names, broken, insecure and unfree packages before it
builds anything (`BOB-E-1001` to `BOB-E-1005`), with suggestions for misspelled names.

## Go

`import "github.com/megakuul/bob/pkg/golang"` (the package is named `golang` because `go` is a
keyword).

| Constructor | Label | Builds |
|---|---|---|
| `golang.NewModule(p, golang.ModuleOpts{…})` | `//<dir>:module` | the module rooted at the `bob.go` directory; library packages need no declaration |
| `m.Binary(dir, golang.BinaryOpts{…})` | `//<dir>:<name>` | a main package |
| `m.Tests(pattern, golang.TestOpts{…})` | `//<pkg>:go_test` | one test unit per package with `_test.go` files |
| `m.Cgo(dir, golang.CgoOpts{Deps: …})` | `//<dir>:go` | gives a cgo package its C, C++ or Rust libraries |
| `m.CArchive(dir, …)`, `m.CShared(dir, …)` | `//<dir>:<name>` | a main package as a C library (`-buildmode=c-archive`/`c-shared`) |

- `golang.Require(path, version)` adds a third-party requirement at an exact version; bob
  generates `go.mod` and `go.sum` from these and `bob.lock`.
- `golang.GeneratorRuntime(path, attr, exact)` is the requirement code generators add for
  their runtime library, at the version of the generator in your nixpkgs pin. You do not
  call it yourself; `connect.Go` and friends do.
- `golang.GenLabel(dir)` returns the label name of a generated tree (`gen/proto/greet/v1` →
  `gen-proto-greet-v1`); it is used by the code generator packages.
- `BinaryOpts.X` sets `-ldflags -X` values, `CGo: golang.CGoOff` forces a pure Go build.
- `ModuleOpts.Go` is the language version (`"1.26"`); the toolchain is nixpkgs' `go_1_26`
  unless you set `Toolchain`.

```go verify
//go:build bob

package api

import (
	"github.com/megakuul/bob/pkg/bob"
	"github.com/megakuul/bob/pkg/cc"
	"github.com/megakuul/bob/pkg/golang"
	"github.com/megakuul/bob/pkg/nixpkgs"
)

// Module is example.com/api (label //<dir>:module).
var Module = bob.Define(func(p *bob.P) *golang.Module {
	return golang.NewModule(p, golang.ModuleOpts{
		Path:    "example.com/api",
		Go:      "1.26",
		Require: []golang.Requirement{golang.Require("github.com/google/uuid", "v1.6.0")},
	})
})

// FFI links the cgo package ./internal/ffi against zlib. Declare it before the binaries
// that use the package.
var FFI = bob.Define(func(p *bob.P) *golang.Package {
	return Module(p).Cgo("./internal/ffi", golang.CgoOpts{Deps: []cc.Linkable{nixpkgs.Lib(p, "zlib")}})
})

// API is the server binary with a version stamped in.
var API = bob.Define(func(p *bob.P) *golang.Binary {
	return Module(p).Binary("./cmd/api", golang.BinaryOpts{X: map[string]string{"main.version": "1.0.0"}})
})

// Tests runs every package's tests.
var Tests = bob.Define(func(p *bob.P) *golang.TestSet {
	return Module(p).Tests("./...", golang.TestOpts{})
})
```

> [!NOTE]
> A module whose directory is not the repository root gets its own generated `go.mod` in that
> directory. Import paths of `bob.go` packages follow the root `go.mod`.

## C and C++

`import "github.com/megakuul/bob/pkg/cc"`

| Constructor | Label | Builds |
|---|---|---|
| `cc.StaticLibrary(p, name, cc.LibraryOpts{…})` | `//<dir>:<name>` | `lib<name>.a` |
| `cc.SharedLibrary(p, name, cc.LibraryOpts{…})` | `//<dir>:<name>` | `lib<name>.so` |
| `cc.NewBinary(p, name, cc.BinaryOpts{…})` | `//<dir>:<name>` | `bin/<name>` |
| `cc.NewTest(p, name, cc.BinaryOpts{…})` | `//<dir>:<name>` | a test program, run as a test; TAP output is parsed |
| `cc.External(p, name, cc.ExternalOpts{…})` | `//<dir>:<name>` | a third-party project built with CMake, Autotools, Meson or Make from a pinned archive |

Libraries are `cc.Linkable`, as are `nixpkgs.Lib`, Rust `StaticLib`/`CDylib` and Go
`CArchive`/`CShared`. `Deps` propagate headers to consumers; `PrivDeps` link without
propagating. `Std` selects the language standard (`cc.C17`, `cc.CXX20`, …); `Defines`,
`Flags` and `LinkOpts` add flags (bob refuses flags that would break hermeticity, such as
`-I/nix/store/…`). See the [C/C++ quickstart](../03-quickstart/02-c-cpp.md) for a full project.

## Rust

`import "github.com/megakuul/bob/pkg/rust"`

| Constructor | Builds |
|---|---|
| `rust.Library(p, rust.LibOpts{…})` | an rlib, usable as a dependency |
| `rust.ProcMacro(p, rust.LibOpts{…})` | a proc-macro crate (compiled for the host) |
| `rust.StaticLib(p, rust.LibOpts{…})` | `lib<name>.a` plus headers, for C, C++ and cgo |
| `rust.CDylib(p, rust.LibOpts{…})` | `lib<name>.so` plus headers |
| `rust.Binary(p, rust.BinOpts{…})` | `bin/<name>` |
| `rust.Test(p, rust.TestOpts{Crate: …})` | the crate's unit tests and every `tests/*.rs` |

Dependencies (`[]rust.Dep`):

- `rust.Crate(name, req, opts…)`: a crates.io crate with a Cargo requirement (`"1"`,
  `"^0.13"`, `"=1.38.0"`). Options: `rust.Features("derive")`, `rust.NoDefaultFeatures()`,
  `rust.Rename("codename")`.
- a local crate value, or `lib.With("feature")` to enable features of it for this edge.

`rust.Uses(crate, libs…)` (in `Options`) gives a crates.io crate's build script native
libraries, for example `rust.Uses("openssl-sys", nixpkgs.Lib(p, "openssl"))`. `Native` links
C libraries into your own crate. `rust.CfgTable(triple)` returns bob's built-in `cfg` table of
a target, which bob uses to select platform-specific dependencies.

```go verify
//go:build bob

package demo

import (
	"github.com/megakuul/bob/pkg/bob"
	"github.com/megakuul/bob/pkg/nixpkgs"
	"github.com/megakuul/bob/pkg/rust"
)

// Core is a library crate with its own feature table.
var Core = bob.Define(func(p *bob.P) *rust.Lib {
	return rust.Library(p, rust.LibOpts{
		Name:     "core-lib",
		Deps:     []rust.Dep{rust.Crate("serde", "1", rust.Features("derive")), rust.Crate("hex", "0.4")},
		Features: map[string][]string{"fast": {}},
		Default:  []string{"fast"},
	})
})

// Macros is a proc-macro crate.
var Macros = bob.Define(func(p *bob.P) *rust.Lib {
	return rust.ProcMacro(p, rust.LibOpts{Deps: []rust.Dep{rust.Crate("syn", "2", rust.Features("full")), rust.Crate("quote", "1")}})
})

// Demo uses both, plus a crate whose build script needs OpenSSL.
var Demo = bob.Define(func(p *bob.P) *rust.Bin {
	return rust.Binary(p, rust.BinOpts{
		Deps:    []rust.Dep{Core(p), Macros(p), rust.Crate("openssl", "0.10")},
		Options: []rust.Option{rust.Uses("openssl-sys", nixpkgs.Lib(p, "openssl"))},
	})
})

// RCore is a staticlib for C consumers, with headers from include/.
var RCore = bob.Define(func(p *bob.P) *rust.CLib {
	return rust.StaticLib(p, rust.LibOpts{Name: "rcore", Headers: "include"})
})

// CoreTest runs Core's unit and integration tests.
var CoreTest = bob.Define(func(p *bob.P) *rust.TestSet { return rust.Test(p, rust.TestOpts{Crate: Core(p)}) })
```

## TypeScript and npm

`import "github.com/megakuul/bob/pkg/npm"`

| Constructor | Label | Builds |
|---|---|---|
| `npm.Library(p, npm.LibOpts{Name: "@scope/name", …})` | `//<dir>:<last part of Name>` | a TypeScript library (tsc into `dist/` plus `.d.ts`), linked into consumers' `node_modules` |
| `npm.NewApp(p, npm.AppOpts{Entry: …})` | `//<dir>:<name>` | a Node (or Bun, `Runtime: npm.Bun`) program, bundled with esbuild by default |
| `npm.Vite(p, npm.ViteOpts{…})` | `//<dir>:<name>` | a static site with `vite build`; `Kit: true` for SvelteKit with adapter-static |
| `npm.NewTest(p, npm.TestOpts{Of: …})` | `//<dir>:test` | vitest (default), `node --test`, `svelte-check` or `eslint` over an importer |

Dependencies (`[]npm.Dependency`): `npm.Dep(name, range, opts…)` with `npm.Dev()` or
`npm.Optional()`, `npm.Alias("lodash4", "lodash", "^4")`, and local libraries by value.

Options (`[]npm.Option`) fix packages that need help:

- `npm.AllowScripts("esbuild")`: run the package's install scripts (sandboxed, offline).
- `npm.Extend("@fastify/cors", npm.Peer("fastify"))`: declare a dependency a package uses
  without declaring it (a phantom dependency, `BOB-E-2014`). `npm.Peer` is valid only there.
- `npm.Hoisted()`: an npm-style hoisted `node_modules` for tools that need one.

bob ships a small table of curated fixes for well-known packages (`npm.Quirks()` returns it;
`npm.CheckQuirks` validates entries). You do not call either from `bob.go`.

```go verify
//go:build bob

package web

import (
	"github.com/megakuul/bob/pkg/bob"
	"github.com/megakuul/bob/pkg/npm"
)

// UI is a component library with a peer dependency on svelte.
var UI = bob.Define(func(p *bob.P) *npm.Lib {
	return npm.Library(p, npm.LibOpts{
		Name:  "@acme/ui",
		Peers: []npm.Dependency{npm.Dep("svelte", "^5")},
		Deps:  []npm.Dependency{npm.Dep("typescript", "~5.9.2", npm.Dev())},
	})
})

// Site is a Svelte site built by Vite.
var Site = bob.Define(func(p *bob.P) *npm.Site {
	return npm.Vite(p, npm.ViteOpts{Deps: []npm.Dependency{
		npm.Dep("svelte", "^5"),
		npm.Dep("vite", "^7.1.0", npm.Dev()),
		npm.Dep("@sveltejs/vite-plugin-svelte", "^6", npm.Dev()),
		UI(p),
	}})
})

// Check runs svelte-check as a test.
var Check = bob.Define(func(p *bob.P) *npm.Test {
	return npm.NewTest(p, npm.TestOpts{Of: Site(p), Runner: npm.SvelteCheck})
})

// Server is a Node program; its native dependency needs its install script.
var Server = bob.Define(func(p *bob.P) *npm.App {
	return npm.NewApp(p, npm.AppOpts{
		Entry:   "src/server.ts",
		Deps:    []npm.Dependency{npm.Dep("fastify", "^5"), npm.Dep("better-sqlite3", "^12.2.0")},
		Options: []npm.Option{npm.AllowScripts("better-sqlite3")},
	})
})
```

## OCaml

`import "github.com/megakuul/bob/pkg/ocaml"`

| Constructor | Builds |
|---|---|
| `ocaml.Library(p, ocaml.LibOpts{…})` | a native library from the directory's `.ml`/`.mli` files (wrapped like dune) |
| `ocaml.Executable(p, ocaml.ExeOpts{…})` | `bin/<name>` |
| `ocaml.NewTest(p, ocaml.ExeOpts{…})` | an executable that runs as a test |

`ocaml.Opam(name, formula)` is an opam package (`""` or `">= 5.7"`); `.Lib("lwt.unix")`
picks one of its findlib libraries. `ocaml.Conf(confPkg, libs…)` maps an opam `conf-*`
package to nixpkgs libraries. `Stubs` and `Native` add C stubs and C libraries; `Dune: true`
builds a library with a generated dune file (for ppx and menhir).

```go verify
//go:build bob

package geom

import (
	"github.com/megakuul/bob/pkg/bob"
	"github.com/megakuul/bob/pkg/nixpkgs"
	"github.com/megakuul/bob/pkg/ocaml"
)

// Lib is a library with a C stub and an opam dependency.
var Lib = bob.Define(func(p *bob.P) *ocaml.Lib {
	return ocaml.Library(p, ocaml.LibOpts{
		Deps:    []ocaml.Dep{ocaml.Opam("fmt", ""), ocaml.Opam("lwt", ">= 5.7").Lib("lwt.unix")},
		Stubs:   bob.Files("geom_stubs.c"),
		Options: []ocaml.Option{ocaml.Conf("conf-libcurl", nixpkgs.Lib(p, "curl"))},
	})
})

// Demo is bin/demo.
var Demo = bob.Define(func(p *bob.P) *ocaml.Exe {
	return ocaml.Executable(p, ocaml.ExeOpts{Name: "demo", Deps: []ocaml.Dep{ocaml.Opam("cmdliner", ""), Lib(p)}})
})
```

## Code generators

| Package | Function | Generates |
|---|---|---|
| `pkg/proto` | `proto.NewLibrary(p, proto.LibraryOpts{Deps: …})` | declares the `.proto` files of a directory (label `//<dir>:proto`) |
| | `proto.Lint(p, lib)`, `proto.Breaking(p, lib, rev)` | `buf lint` / `buf breaking` checks, run by `bob test` |
| `pkg/connect` | `connect.Go(lib, connect.GoOpts{})` | protoc-gen-go and connect-go code into a Go module |
| | `connect.TS(lib, connect.TSOpts{})` | protoc-gen-es code into an npm importer |
| `pkg/graphql` | `graphql.NewSchema(p, files)` | declares a GraphQL schema |
| | `graphql.Gqlgen(schema, graphql.GqlgenOpts{})` | gqlgen executable schema and models (never resolvers) |
| | `graphql.TS(schema, graphql.TSOpts{Documents: …})` | graphql-codegen types for your operations |
| `pkg/openapi` | `openapi.NewSpec(p, file)` | declares an OpenAPI document (YAML or JSON) |
| | `openapi.Go(spec, openapi.GoOpts{Package: …})` | oapi-codegen models, optional `net/http` server or client |
| | `openapi.TS(spec, openapi.TSOpts{})` | openapi-typescript types |

`connect.Go`, `graphql.Gqlgen` and `openapi.Go` return a `golang.Generated` for
`ModuleOpts.Generated`; the `TS` functions return an `npm.Generated` for the `Generated`
field of an npm importer. [Generated code](04-generated-code.md) shows each of them.

## Services (pkg/services/postgres)

`postgres.Service(p, postgres.Options{Version: "17", Databases: []string{"app"}})` runs a
fresh PostgreSQL for a test. The test sees `PGHOST`, `PGPORT`, `PGUSER` (`postgres`, trust
authentication), `POSTGRES_ADDR` and `DATABASE_URL`
(`postgres://postgres@127.0.0.2:5432/app?sslmode=disable` for the first service):

```go verify
//go:build bob

package acme

import (
	"github.com/megakuul/bob/pkg/bob"
	"github.com/megakuul/bob/pkg/golang"
	"github.com/megakuul/bob/pkg/services/postgres"
)

var Module = bob.Define(func(p *bob.P) *golang.Module {
	return golang.NewModule(p, golang.ModuleOpts{Path: "example.com/acme", Go: "1.26"})
})

// DBTests runs ./db's tests against PostgreSQL.
var DBTests = bob.Define(func(p *bob.P) *golang.TestSet {
	return Module(p).Tests("./db", golang.TestOpts{
		Services: []bob.Service{postgres.Service(p, postgres.Options{Databases: []string{"app"}})},
	})
})
```

## Outputs and releases

| Package | Constructor | Output |
|---|---|---|
| `pkg/output/oci` | `oci.NewImage(p, name, oci.ImageOpts{Program: …})` | an OCI image index for every platform (default linux-amd64 and linux-arm64) |
| `pkg/output/tarball` | `tarball.New(p, name, tarball.TarballOpts{Program: …, Kind: …})` | `<name>-<platform>.tar.gz`: the closure (`tarball.Closure`) or one static file (`tarball.Static`) |
| `pkg/output/appimage` | `appimage.NewBundle(p, name, appimage.BundleOpts{Program: …})` | a self-contained AppImage for one platform |
| `pkg/release` | `release.New(p, name, release.ReleaseOpts{…})` | a versioned release manifest over closures, images, tarballs and AppImages |

`Program` is any `bob.Executable`: a Go, C/C++, Rust, OCaml or npm program, `bob.Exe(…)` or
`nixpkgs.Exe(…)`. See [Images, tarballs and AppImages](../03-quickstart/07-outputs.md) and
[Releases](07-releases.md).

## Next steps

- [API index](../07-reference/02-api-index.md): every exported symbol.
- [Dependencies and locking](02-dependencies-and-locking.md).
- [Multi-language projects](03-multi-language.md).
