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
framework. The examples whose first line names a path (`libs/greet/bob.go`) belong to one
example repository, `example.com/acme`, and use each other by import.

## How constructors look

All constructors follow one shape:

- They take `p *bob.P` first, then a name (when the kind has one), then an options struct
  (`cc.LibraryOpts`, `golang.ModuleOpts`, …). Zero values are the defaults.
- They return a typed value that you pass to other constructors. Strings are data only: the
  package's own name, paths inside its directory, versions, flags, commands. Everything defined
  elsewhere (a library, a tool, a nixpkgs package, another package's binary) is a typed Go
  value.
- You call them inside `bob.Define`, assigned to a package-level variable.

```go file=libs/greet/bob.go
//go:build bob

package greet

import (
	"github.com/megakuul/bob/pkg/bob"
	"github.com/megakuul/bob/pkg/cc"
)

// Lib is libgreet.a (label //libs/greet:greet).
var Lib = bob.Define(func(p *bob.P) *cc.Library {
	return cc.StaticLibrary(p, "greet", cc.LibraryOpts{Srcs: bob.Glob("src/*.c"), Hdrs: bob.IncludeDir("include")})
})

// Test links Lib. Calling Lib(p) again returns the same value: bob.Define memoises per P.
var Test = bob.Define(func(p *bob.P) *cc.Test {
	return cc.NewTest(p, "greet_test", cc.BinaryOpts{Srcs: bob.Files("test/greet_test.c"), Deps: []cc.Linkable{Lib(p)}})
})
```

## One package per directory

A **package** is a directory with its own `bob.go`; its **region** is that directory plus every
subdirectory without a `bob.go` of its own. Packages form a tree, and the root `bob.go` is one
package among others:

- **Locality.** Every path a `bob.go` passes (`bob.Glob`, `bob.Files`, `bob.Dir`,
  `bob.IncludeDir`, Rust `Root`, npm `Entry`, `openapi.NewSpec`, …) is relative to its
  directory and stays in its region: no `..`, no absolute path, no file of a subdirectory with
  its own `bob.go` (`BOB-E-1028`). `**` and `bob.Dir` skip such subdirectories.
- **One unit per package.** A package defines at most one library or program per ecosystem: one
  of `cc.StaticLibrary`, `cc.SharedLibrary`, `cc.NewBinary`, `cc.External`; one Rust crate; one
  npm importer; one OCaml library or executable; one Go binary or C library (of the directory's
  `main` package) plus one `Cgo` declaration; one protobuf library, GraphQL schema or OpenAPI
  spec (`BOB-E-1029`). Tests, checks, code generators, `bob.Run` steps, dev shells, services and
  outputs are not units and may appear any number of times.
- **Typed handles across packages.** Another package's library is used by value
  (`greet.Lib(p)`), its public headers come with it (`bob.IncludeDir` in its `bob.go`), never as
  a path string. Go `main` packages cannot be imported, by Go or by `bob.go`; their binaries
  reach other packages through a typed **slot**:

| Function | Use |
|---|---|
| `bob.NewSlot[T]()` | declares a slot in an importable `bob.go` (a package-level `var`) |
| `slot.Bind(constructor)` | binds a `bob.Define` result to it, in the `bob.go` of the package that defines the value; exactly once |
| `slot.Get(p)` | the bound value for `p`, memoised like the constructor |

An unbound slot, or one bound twice, is a definition error (`BOB-E-1020`). The [Go](#go) example
below binds the server binary to `acme.API`, and `deploy/bob.go` builds its image from
`acme.API.Get(p)`.

## Core (pkg/bob)

`import "github.com/megakuul/bob/pkg/bob"`

### Definitions and repository settings

| Function | Use |
|---|---|
| `bob.Define(fn)` | turns a constructor into a memoised, registered constructor; only in package-level `var` declarations |
| `bob.Repo(bob.RepoOpts{…})` | repository settings, root `bob.go` only, at most once: `Nixpkgs` (a channel: `bob.NixOS2605`, bob's default, `bob.NixOS2511` or `bob.NixpkgsUnstable`), `NixpkgsRev` (pin a 40-digit revision of it) and named `Variants` |
| `bob.Import(module, version)` | requires another bob repository as a Go module; its `bob.go` packages become importable and its labels are `<module>//dir:name` |
| `bob.Main()` | the entry point of the generated evaluator; never call it |

`p.Platform()`, `p.Variant()` and `p.Host()` tell a constructor what it builds for. Build
tools use `p.Host()`: the host platform with the release profile. `p.Toolchain()` and
`bob.ToolchainFor(host, target)` describe the C/C++ cross setup for a platform pair (target
triple, tool prefix, emulator command); ecosystems use them, `bob.go` files rarely need them.

### Sources

| Function | Selects |
|---|---|
| `bob.Glob("src/**/*.cpp", "!src/legacy/**")` | files by pattern (`*`, `?`, `**`, `[…]`; `!` excludes) |
| `bob.Files("a.c", "b.c")` | exact files |
| `bob.Dir("include")` | a whole directory tree |
| `s.Union(o…)` | several selections together |
| `bob.IncludeDir("include")` | a library's public include tree (`Hdrs`, Rust `Headers`); consumers get it through the library |

Paths are relative to the `bob.go` directory and stay in its package (`BOB-E-1028`; leaving
the repository is `BOB-E-1025`). A pattern that matches nothing is an error. Files ignored by
git, `.bob/`, `bob.go` files and subdirectories with their own `bob.go` are never selected.

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
		Tools:   []bob.Tool{nixpkgs.Bash.Tool(p), nixpkgs.Coreutils.Tool(p)},
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
		Packages: []bob.Tool{nixpkgs.Jq.Tool(p), nixpkgs.Postgresql.Tool(p)},
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
(`Targets`), what to test (`Tests`), which releases to publish (`Releases`, label patterns of
`release.New` packages; a pattern that matches none is a definition error), when to run
(`Triggers`: `bob.OnPush(branchGlob)`, `bob.OnPullRequest()`, `bob.OnTag(glob)`) and what to do
with an older run of the same ref (`Concurrency`: `bob.Queue`, `bob.Supersede`,
`bob.Parallel`). See [Pipelines](06-pipelines.md).

```go verify
//go:build bob

package acme

import "github.com/megakuul/bob/pkg/bob"

var _ = bob.Repo(bob.RepoOpts{
	Nixpkgs:  bob.NixOS2605,
	Variants: []bob.Named{bob.NamedVariant("asan", bob.WithSanitize(bob.ASan, bob.UBSan), bob.WithProfile(bob.Debug))},
})

var _ = bob.Pipeline("ci", bob.PipelineOpts{
	Triggers: []bob.Trigger{bob.OnPush("main"), bob.OnPullRequest()},
	Targets:  []bob.Pattern{"//..."},
	Tests:    []bob.Pattern{"//..."},
})

var _ = bob.Pipeline("release", bob.PipelineOpts{
	Triggers: []bob.Trigger{bob.OnTag("v*")},
	Releases: []bob.Pattern{"//deploy:api"},
})
```

## nixpkgs packages (pkg/nixpkgs)

`import "github.com/megakuul/bob/pkg/nixpkgs"`

### Typed nixpkgs packages

Every package of nixpkgs is a typed Go value of package `nixpkgs`, generated for the nixpkgs
revision this bob was released with: `nixpkgs.Openssl`, `nixpkgs.Zlib`, `nixpkgs.Jq`,
`nixpkgs.Go1_26`, `nixpkgs.Postgresql17`. This is the only place a Nix concept shows up in
`bob.go`. There is no way to name a package by a string, so a misspelt package does not compile,
and bob's error suggests the right name (`undefined: nixpkgs.Opensll` → `did you mean
nixpkgs.Openssl?`).

**Finding a package.** Type `nixpkgs.` and let your editor complete (`nixpkgs.Op` →
`Openssl`); hovering a name shows its version, description and licence, for example `Openssl is
openssl 3.6.5: Cryptographic library that implements the SSL and TLS protocols. Licence:
Apache-2.0.` In a checkout of bob, `go doc github.com/megakuul/bob/pkg/nixpkgs.Openssl` prints
the same. The Go name is the attribute with `-`, `_` and `.` removed and each part capitalised
(`nix-update` → `NixUpdate`, `python313` → `Python313`); a `_` stays between two numbers
(`go_1_26` → `Go1_26`), a leading digit gets an `N` (`3proxy` → `N3proxy`). The few attributes
whose names collide (`git-repo` and `gitRepo`) are written `nixpkgs.A_git_repo`,
`nixpkgs.A_gitRepo`.

**Roles.** The method says how the package is used, and the type it returns says where it may
go:

| Method | Use |
|---|---|
| `nixpkgs.Protobuf.Tool(p, opts…)` | a build-time tool, from the host package set; for `bob.Run`, dev shells, `ExternalOpts.Tools`; not a library |
| `nixpkgs.Openssl.Lib(p, opts…)` | a library linked into target code; a `cc.Linkable`, not a tool |
| `nixpkgs.Tzdata.Pkg(p, opts…)` | a run-time package (data, services) of the target set |
| `nixpkgs.Curl.Exe(p, opts…)` | a run-time package used as a program (its `meta.mainProgram`), e.g. an extra AppImage entry |

Options: `nixpkgs.Modules("libcrypto")` (pkg-config modules to link; default all, checked when
the build is planned), `nixpkgs.Outputs(nixpkgs.OutDev, nixpkgs.OutLib)` (outputs: `nixpkgs.Out`,
`OutBin`, `OutDev`, `OutLib`, `OutMan`, `OutDoc`, `OutDebug`, `OutStatic`), `nixpkgs.Cross()`
(take a library from the cross package set instead of the target system's own),
`nixpkgs.MainProgram("bin/curl")` (the program `Exe` runs, when the package has no
`meta.mainProgram` or you want another), `nixpkgs.AllowInsecure("CVE-…")` and
`nixpkgs.AllowUnfree()` (the doc of an unfree package says so). bob checks broken, insecure and
unfree packages before it builds anything (`BOB-E-1002` to `BOB-E-1005`).

**Not in the list.** Package sets and scopes (`python3Packages`, `ocamlPackages`,
`haskellPackages`, `pkgsCross`, …), aliases (use the name they point to), broken and insecure
packages, packages that exist on neither `aarch64-linux` nor `x86_64-linux`, and packages that
fail to evaluate. The ecosystems take what they need from the language sets themselves (OCaml's
`findlib`, Rust's standard library, the cross compilers). A few nested packages that bob needs
are added by name (`nixpkgs.Rust1_97PackagesStableRustc`, a second Rust compiler); ask for one
if you need another (see the [status page](../07-reference/03-status.md#nixpkgs-packages)).

**Another pin.** The list matches bob's default pin: a new `bob.lock` pins the revision the list
was generated from (`da6dc0e7b260` for this bob). With another pin (`bob lock --update nixpkgs`,
`RepoOpts.NixpkgsRev`), every package you use is checked against the pinned revision before
anything builds; one the pin lacks fails with `BOB-E-1001` naming the call, for example
`libs/foo/bob.go:17: nixpkgs at the pinned revision cd4ea8d57de1 has no package "openssl"
(nixpkgs.Openssl)`. A package that is newer than bob's list cannot be named until a newer bob.

## Go

`import "github.com/megakuul/bob/pkg/golang"` (the package is named `golang` because `go` is a
keyword).

| Constructor | Label | Builds |
|---|---|---|
| `golang.NewModule(p, golang.ModuleOpts{…})` | `//<dir>:module` | the module rooted at the `bob.go` directory; library packages need no declaration |
| `m.Binary(golang.BinaryOpts{…})` | `//<dir>:<name>` | the `main` package of the calling `bob.go`'s directory |
| `m.Tests(golang.TestOpts{…})` | `//<dir>:go_test` | the tests of the calling directory's package; with `Recursive: true` one unit per package of the directory's region (subdirectories without a `bob.go` of their own) |
| `m.Cgo(golang.CgoOpts{Deps: …})` | `//<dir>:go` | gives the calling directory's cgo package its C, C++ or Rust libraries |
| `m.CArchive(…)`, `m.CShared(…)` | `//<dir>:<name>` | the calling directory's `main` package as a C library (`-buildmode=c-archive`/`c-shared`) |

None of these take a directory: a Go package's binary, cgo libraries and tests are declared in
that package's own `bob.go`, which reaches the module through the module root's package
(`acme.Module(p)`). The module stays declared once, in the module root.

- `golang.Require(path, version)` adds a third-party requirement at an exact version; bob
  generates `go.mod` and `go.sum` from these and `bob.lock`.
- `golang.GeneratorRuntime(path, generator, exact)` is the requirement code generators add for
  their runtime library, at the version of the generator (a typed nixpkgs package) in your
  nixpkgs pin. You do not call it yourself; `connect.Go` and friends do.
- `BinaryOpts.X` sets `-ldflags -X` values, `CGo: golang.CGoOff` forces a pure Go build.
- `ModuleOpts.Go` is the language version (`"1.26"`); the toolchain is nixpkgs' `go_1_26`
  unless you set `Toolchain`.

The module root declares the module, the slot for the server binary and the tests of every
package that has no `bob.go` of its own:

```go file=bob.go
//go:build bob

package acme

import (
	"github.com/megakuul/bob/pkg/bob"
	"github.com/megakuul/bob/pkg/golang"
)

var _ = bob.Repo(bob.RepoOpts{Nixpkgs: bob.NixOS2605})

// Module is example.com/acme (label //:module).
var Module = bob.Define(func(p *bob.P) *golang.Module {
	return golang.NewModule(p, golang.ModuleOpts{
		Path:    "example.com/acme",
		Go:      "1.26",
		Require: []golang.Requirement{golang.Require("github.com/google/uuid", "v1.6.0")},
	})
})

// API is the server binary //cmd/api:api, bound by cmd/api/bob.go (a main package cannot be
// imported).
var API = bob.NewSlot[*golang.Binary]()

// Tests runs the tests of every Go package below the root without a bob.go of its own.
var Tests = bob.Define(func(p *bob.P) *golang.TestSet {
	return Module(p).Tests(golang.TestOpts{Recursive: true})
})
```

`internal/ffi` is a cgo package that links zlib:

```go file=internal/ffi/bob.go
//go:build bob

package ffi

import (
	"example.com/acme"
	"github.com/megakuul/bob/pkg/bob"
	"github.com/megakuul/bob/pkg/cc"
	"github.com/megakuul/bob/pkg/golang"
	"github.com/megakuul/bob/pkg/nixpkgs"
)

// FFI links this cgo package against zlib. Binaries that use the package call it first.
var FFI = bob.Define(func(p *bob.P) *golang.Package {
	return acme.Module(p).Cgo(golang.CgoOpts{Deps: []cc.Linkable{nixpkgs.Zlib.Lib(p)}})
})

// Tests runs this package's tests (unit //internal/ffi:go_test).
var Tests = bob.Define(func(p *bob.P) *golang.TestSet {
	FFI(p)
	return acme.Module(p).Tests(golang.TestOpts{})
})
```

`cmd/api` is the server. Its `bob.go` shares the `main` package of `main.go`:

```go file=cmd/api/bob.go
//go:build bob

package main

import (
	"example.com/acme"
	"example.com/acme/internal/ffi"
	"github.com/megakuul/bob/pkg/bob"
	"github.com/megakuul/bob/pkg/golang"
)

// Binary is the server with a version stamped in (label //cmd/api:api).
var Binary = bob.Define(func(p *bob.P) *golang.Binary {
	ffi.FFI(p)
	return acme.Module(p).Binary(golang.BinaryOpts{X: map[string]string{"main.version": "1.0.0"}})
})

var _ = acme.API.Bind(Binary)

// Tests runs this package's tests (unit //cmd/api:go_test).
var Tests = bob.Define(func(p *bob.P) *golang.TestSet {
	return acme.Module(p).Tests(golang.TestOpts{})
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

One library, program or `cc.External` per directory, each with its own `bob.go`; a test sits
next to the code it tests. Libraries are `cc.Linkable`, as are the `Lib` of a nixpkgs package,
Rust `StaticLib`/`CDylib` and Go `CArchive`/`CShared`. A library's public headers are
`Hdrs: bob.IncludeDir("include")` (a directory of its package, `"."` for the package directory
itself); consumers get that include path only from `Deps` (propagated to their consumers) or
`PrivDeps` (not propagated). There are no global include directories: `Flags` and `LinkOpts`
refuse `-I`, `-isystem`, `-iquote`, `-L`, `-l` and similar path flags, relative or absolute (use
`Deps`). `Std` selects the language standard (`cc.C17`, `cc.CXX20`, …); `Defines`, `Flags` and
`LinkOpts` add other flags. See the [C/C++ quickstart](../03-quickstart/02-c-cpp.md) for a full project.

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
libraries. The crate is a handle: the one from `rust.Crate`, or `rust.Indirect("openssl-sys")`
for a crate that is not a direct dependency (bob lock fails with `BOB-E-1015` when the resolved
graph does not contain it), for example
`rust.Uses(rust.Indirect("openssl-sys"), nixpkgs.Openssl.Lib(p))`. `Native` links C libraries
into your own crate. `Headers: bob.IncludeDir("include")` exports a staticlib's or cdylib's C
header directory to its consumers.

Each crate is a package of its own. A library with its own feature table and its tests:

```go file=crates/core/bob.go
//go:build bob

package core

import (
	"github.com/megakuul/bob/pkg/bob"
	"github.com/megakuul/bob/pkg/rust"
)

// Lib is a library crate with its own feature table (label //crates/core:core-lib).
var Lib = bob.Define(func(p *bob.P) *rust.Lib {
	return rust.Library(p, rust.LibOpts{
		Name:     "core-lib",
		Deps:     []rust.Dep{rust.Crate("serde", "1", rust.Features("derive")), rust.Crate("hex", "0.4")},
		Features: map[string][]string{"fast": {}},
		Default:  []string{"fast"},
	})
})

// Test runs the library's unit and integration tests.
var Test = bob.Define(func(p *bob.P) *rust.TestSet { return rust.Test(p, rust.TestOpts{Crate: Lib(p)}) })
```

A proc-macro crate:

```go file=crates/macros/bob.go
//go:build bob

package macros

import (
	"github.com/megakuul/bob/pkg/bob"
	"github.com/megakuul/bob/pkg/rust"
)

// Lib is a proc-macro crate (label //crates/macros:macros).
var Lib = bob.Define(func(p *bob.P) *rust.Lib {
	return rust.ProcMacro(p, rust.LibOpts{Deps: []rust.Dep{rust.Crate("syn", "2", rust.Features("full")), rust.Crate("quote", "1")}})
})
```

A program that uses both, plus a crate whose build script needs OpenSSL:

```go file=crates/demo/bob.go
//go:build bob

package demo

import (
	"example.com/acme/crates/core"
	"example.com/acme/crates/macros"
	"github.com/megakuul/bob/pkg/bob"
	"github.com/megakuul/bob/pkg/nixpkgs"
	"github.com/megakuul/bob/pkg/rust"
)

// Bin is bin/demo (label //crates/demo:demo).
var Bin = bob.Define(func(p *bob.P) *rust.Bin {
	return rust.Binary(p, rust.BinOpts{
		Deps:    []rust.Dep{core.Lib(p), macros.Lib(p), rust.Crate("openssl", "0.10")},
		Options: []rust.Option{rust.Uses(rust.Indirect("openssl-sys"), nixpkgs.Openssl.Lib(p))},
	})
})
```

A staticlib for C consumers, with its header directory:

```go file=crates/rcore/bob.go
//go:build bob

package rcore

import (
	"github.com/megakuul/bob/pkg/bob"
	"github.com/megakuul/bob/pkg/rust"
)

// Crate is librcore.a with the headers of include/ (label //crates/rcore:rcore).
var Crate = bob.Define(func(p *bob.P) *rust.CLib {
	return rust.StaticLib(p, rust.LibOpts{Name: "rcore", Headers: bob.IncludeDir("include")})
})
```

## TypeScript and npm

`import "github.com/megakuul/bob/pkg/npm"`

| Constructor | Label | Builds |
|---|---|---|
| `npm.Library(p, npm.LibOpts{Name: "@scope/name", …})` | `//<dir>:<last part of Name>` | a TypeScript library (tsc into `dist/` plus `.d.ts`), linked into consumers' `node_modules` |
| `npm.NewApp(p, npm.AppOpts{Entry: …})` | `//<dir>:<name>` | a Node (or Bun, `Runtime: npm.Bun`) program, bundled with esbuild by default |
| `npm.Vite(p, npm.ViteOpts{…})` | `//<dir>:<name>` | a static site with `vite build`; `Kit: true` for SvelteKit with adapter-static |
| `npm.NewTest(p, npm.TestOpts{Of: …})` | `//<dir>:test` | vitest (default), `node --test`, `svelte-check` or `eslint` over an importer |

One importer (`npm.Library`, `npm.NewApp` or `npm.Vite`) per directory, each with its own
`bob.go`. Dependencies (`[]npm.Dependency`): `npm.Dep(name, range, opts…)` with `npm.Dev()` or
`npm.Optional()`, `npm.Alias("lodash4", "lodash", "^4")`, and local libraries by value.
`npm.Dep` and `npm.Alias` return a handle (`*npm.Pkg`) that the options below take; a package
that is not a direct dependency is declared with `npm.Indirect("esbuild")` (bob lock fails with
`BOB-E-1015` when the resolved graph does not contain it).

Options (`[]npm.Option`) fix packages that need help:

- `npm.AllowScripts(sqlite, npm.Indirect("esbuild"))`: run these packages' install scripts
  (sandboxed, offline).
- `npm.Extend(npm.Indirect("@fastify/cors"), npm.Peer(fastify))`: declare a dependency a
  package uses without declaring it (a phantom dependency, `BOB-E-2014`). `npm.Peer` is valid
  only there.
- `npm.Hoisted()`: an npm-style hoisted `node_modules` for tools that need one.

`TSConfig` fields with a fixed set of values are typed: `Target: npm.ES2022`,
`Module: npm.ModuleNodeNext`, `ModuleResolution: npm.Bundler`, `JSX: npm.ReactJSX`.

bob ships a small table of curated fixes for well-known packages (`npm.Quirks()` returns it;
`npm.CheckQuirks` validates entries). You do not call either from `bob.go`.

A component library:

```go file=web/ui/bob.go
//go:build bob

package ui

import (
	"github.com/megakuul/bob/pkg/bob"
	"github.com/megakuul/bob/pkg/npm"
)

// Lib is a component library with a peer dependency on svelte (label //web/ui:ui).
var Lib = bob.Define(func(p *bob.P) *npm.Lib {
	return npm.Library(p, npm.LibOpts{
		Name:  "@acme/ui",
		Peers: []npm.Dependency{npm.Dep("svelte", "^5")},
		Deps:  []npm.Dependency{npm.Dep("typescript", "~5.9.2", npm.Dev())},
	})
})
```

A Svelte site that uses it, checked by `svelte-check`:

```go file=web/site/bob.go
//go:build bob

package site

import (
	"example.com/acme/web/ui"
	"github.com/megakuul/bob/pkg/bob"
	"github.com/megakuul/bob/pkg/npm"
)

// Site is a Svelte site built by Vite (label //web/site:site).
var Site = bob.Define(func(p *bob.P) *npm.Site {
	return npm.Vite(p, npm.ViteOpts{Deps: []npm.Dependency{
		npm.Dep("svelte", "^5"),
		npm.Dep("vite", "^7.1.0", npm.Dev()),
		npm.Dep("@sveltejs/vite-plugin-svelte", "^6", npm.Dev()),
		ui.Lib(p),
	}})
})

// Check runs svelte-check as a test.
var Check = bob.Define(func(p *bob.P) *npm.Test {
	return npm.NewTest(p, npm.TestOpts{Of: Site(p), Runner: npm.SvelteCheck})
})
```

A Node program whose native dependency needs its install script:

```go file=web/server/bob.go
//go:build bob

package server

import (
	"github.com/megakuul/bob/pkg/bob"
	"github.com/megakuul/bob/pkg/npm"
)

// App is the server (label //web/server:server).
var App = bob.Define(func(p *bob.P) *npm.App {
	sqlite := npm.Dep("better-sqlite3", "^12.2.0")
	return npm.NewApp(p, npm.AppOpts{
		Entry:   "src/server.ts",
		Deps:    []npm.Dependency{npm.Dep("fastify", "^5"), sqlite},
		Options: []npm.Option{npm.AllowScripts(sqlite)},
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

One library or executable per directory, each with its own `bob.go`; tests sit next to them.
`ocaml.Opam(name, formula)` is an opam package (`""` or `">= 5.7"`); `.Lib("lwt.unix")` picks
one of its findlib libraries. `ocaml.Conf(pkg, libs…)` maps an opam `conf-*` package (a handle:
`ocaml.Indirect("conf-libcurl")` when it is not a direct dependency) to nixpkgs libraries. `Stubs` and `Native` add C stubs and C libraries; `Dune: true`
builds a library with a generated dune file (for ppx and menhir).

```go file=ocaml/geom/bob.go
//go:build bob

package geom

import (
	"github.com/megakuul/bob/pkg/bob"
	"github.com/megakuul/bob/pkg/nixpkgs"
	"github.com/megakuul/bob/pkg/ocaml"
)

// Lib is a library with a C stub and opam dependencies (label //ocaml/geom:geom).
var Lib = bob.Define(func(p *bob.P) *ocaml.Lib {
	return ocaml.Library(p, ocaml.LibOpts{
		Deps:    []ocaml.Dep{ocaml.Opam("fmt", ""), ocaml.Opam("lwt", ">= 5.7").Lib("lwt.unix")},
		Stubs:   bob.Files("geom_stubs.c"),
		Options: []ocaml.Option{ocaml.Conf(ocaml.Indirect("conf-libcurl"), nixpkgs.Curl.Lib(p))},
	})
})
```

```go file=ocaml/demo/bob.go
//go:build bob

package demo

import (
	"example.com/acme/ocaml/geom"
	"github.com/megakuul/bob/pkg/bob"
	"github.com/megakuul/bob/pkg/ocaml"
)

// Exe is bin/demo (label //ocaml/demo:demo).
var Exe = bob.Define(func(p *bob.P) *ocaml.Exe {
	return ocaml.Executable(p, ocaml.ExeOpts{Name: "demo", Deps: []ocaml.Dep{ocaml.Opam("cmdliner", ""), geom.Lib(p)}})
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

`postgres.Service(p, postgres.Options{Server: nixpkgs.Postgresql17, Databases: []string{"app"}})`
runs a fresh PostgreSQL for a test (`Server` defaults to `nixpkgs.Postgresql`). The test sees `PGHOST`, `PGPORT`, `PGUSER` (`postgres`, trust
authentication), `POSTGRES_ADDR` and `DATABASE_URL`
(`postgres://postgres@127.0.0.2:5432/app?sslmode=disable` for the first service):

```go file=db/bob.go
//go:build bob

package db

import (
	"example.com/acme"
	"github.com/megakuul/bob/pkg/bob"
	"github.com/megakuul/bob/pkg/golang"
	"github.com/megakuul/bob/pkg/nixpkgs"
	"github.com/megakuul/bob/pkg/services/postgres"
)

// Tests runs this package's tests against PostgreSQL 17 (unit //db:go_test).
var Tests = bob.Define(func(p *bob.P) *golang.TestSet {
	return acme.Module(p).Tests(golang.TestOpts{
		Services: []bob.Service{postgres.Service(p, postgres.Options{Server: nixpkgs.Postgresql17, Databases: []string{"app"}})},
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

`Program` is any `bob.Executable`: a Go, C/C++, Rust, OCaml or npm program (a Go program
through its slot, `acme.API.Get(p)`), `bob.Exe(…)` or the `Exe` of a nixpkgs package
(`nixpkgs.Curl.Exe(p)`). Outputs are not build units: put them in a package of their own:

```go file=deploy/bob.go
//go:build bob

package deploy

import (
	"example.com/acme"
	"github.com/megakuul/bob/pkg/bob"
	"github.com/megakuul/bob/pkg/output/oci"
	"github.com/megakuul/bob/pkg/release"
)

// Image is the server's OCI image (label //deploy:api-image).
var Image = bob.Define(func(p *bob.P) *oci.Image {
	return oci.NewImage(p, "api-image", oci.ImageOpts{Program: acme.API.Get(p), Ports: []int{8080}})
})

// Release is the release api (label //deploy:api), published by the release pipeline.
var Release = bob.Define(func(p *bob.P) *release.Release {
	return release.New(p, "api", release.ReleaseOpts{
		Closures: []bob.Executable{acme.API.Get(p)},
		Images:   []*oci.Image{Image(p)},
	})
})
```

 See [Images, tarballs and AppImages](../03-quickstart/07-outputs.md) and
[Releases](07-releases.md).

## Next steps

- [API index](../07-reference/02-api-index.md): every exported symbol.
- [Dependencies and locking](02-dependencies-and-locking.md).
- [Multi-language projects](03-multi-language.md).
