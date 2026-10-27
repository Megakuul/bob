# ADR 0020 — Typed surface everywhere, per-package build modules

- **Status:** Accepted — owner chat decision 2026-10-24 (D71, D72; REQUIREMENTS §24). The owner
  approved rewriting spec, harness, protected tests, fixtures and goldens for it.
- **Requirements:** R-FW-010 (amended), R-FW-070, R-FW-071, R-FW-072 (new), R-NIX-013 (amended),
  R-NIX-031, R-NIX-032 (new), R-NIX-005 (amended), R-FW-002 R7/R8 (amended), R-FW-032 (amended),
  R-CC-002 (amended), R-CC-006 (new), §03.0.7 (new), R-FW-040 pipeline releases (amended).
- **Amends locked ADRs:** 0003 (rules R7/R8, package locality), 0004 (Go: no directory
  arguments, binaries declared in the main package's own `bob.go`), 0005 (Rust: `Uses` takes a
  crate handle, `rust.Indirect`), 0006 (npm: `AllowScripts`/`Extend`/`Peer` take package
  handles, `npm.Indirect`), 0007 (OCaml: `Conf` takes an opam handle, `ocaml.Indirect`), 0008
  (codegen: generator tools are typed nixpkgs handles), 0009 (platforms: toolchain attributes are
  typed and live in `nixext`), 0017 (error translation: hints for typed-surface compile errors).
  ADRs 0001, 0002, 0010–0019 are unaffected.
- **Evidence:** `docs/review/typesafety-audit.md` (every violation with file:line); the cached
  nixpkgs index of the pin `da6dc0e7` holds 27 258 top-level names
  (`~/.local/share/bob/cache/nixpkgs/da6dc0e7…-aarch64-linux.json`, measured 2026-10-24); the
  generated-package budgets are measured by unit Q2 (rows B-098…B-103, §4 below).

## Context

The owner tried bob by hand on 2026-10-24 and rejected two designs (verbatim in REQUIREMENTS §24
D71/D72):

1. **Stringly-typed identifiers.** `nixpkgs.Tool(p, "gcc")` compiles for any string; a typo is
   found only at evaluation (BOB-E-1001), gopls cannot complete package names, and the doc of a
   package (version, licence) is invisible in the editor. The same pattern exists in
   `golang.(*Module).Binary("./cmd/api")`, `rust.Uses("openssl-sys", …)`,
   `npm.AllowScripts("esbuild")`, `ocaml.Conf("conf-libcurl", …)`, codegen `Tools []string`,
   `bob.Toolchain.CC`, `RepoOpts.Nixpkgs`, `PipelineOpts.Releases` (audit V01–V24).
2. **Central build files.** A `bob.go` could list the sources of other directories
   (`cpp-lib/bob.go` builds `src/`, `tool/` and `test/` as three targets; `polyglot/bob.go`
   declares the binary of `cmd/api`; `acme/deploy/bob.go` re-declares `cmd/api` by a directory
   string). That is a CMake file written in Go. The owner wants every package to carry its own
   `bob.go`, cross-package use only through typed handles.

AGENTS.md §2.3 already ranked "typed, discoverable, gopls-friendly, one obvious way" above
implementation convenience; R-FW-010 said "strings appear only as CLI labels" but left
nixpkgs attribute names as an exception (R-NIX-013). This ADR closes the exception and turns both
rules into checked, enforced requirements.

## Decision

### 1. No public API takes a string that names something in another namespace

A public API parameter or field (L0–L3, SPEC R-ARCH-003) of type `string`, `[]string`,
`...string` or `map[string]T` may carry only **data**: own names (the label name of the package
being defined), paths inside the calling package, versions, hashes, URLs, globs, commands,
environment, compiler flags, free text, label patterns (CLI form), and the **declaration** of an
external registry dependency (§3). A string never *refers* to a package, attribute, target,
tool or library that is defined elsewhere: that reference is a typed Go value. The rule is
enforced by `TestRFW072TypedSurface` (tests/core): every raw-string parameter and field of the
public API must be classified in its protected allowlist with a category and a reason; anything
unclassified fails CI. Enum-like values use named types with constants (`cc.Std`,
`npm.ESTarget`, `nixpkgs.Output`).

### 2. nixpkgs is a generated, typed Go surface

- **Shape.** One exported variable per nixpkgs attribute in package `pkg/nixpkgs`, of the sealed
  type `nixpkgs.Attr` (a struct with unexported fields: no string converts to it). The role is
  chosen by a method, which keeps R-FW-032's compile-time distinction:

  ```go
  nixpkgs.Protobuf.Tool(p)                              // *nixpkgs.ToolPackage: bob.Tool, not Linkable
  nixpkgs.Openssl.Lib(p, nixpkgs.Modules("libcrypto"))  // *nixpkgs.Package: bob.Linkable, not bob.Tool
  nixpkgs.Tzdata.Pkg(p)                                 // *nixpkgs.Package (run time data)
  nixpkgs.Curl.Exe(p)                                   // *nixpkgs.Program, main program from meta.mainProgram
  ```

  `nixpkgs.Tool/Lib/Pkg/Exe(p, attr string)` are removed (one obvious way, R-FW-014).
- **Generator.** `cmd/nixpkgsgen` (a maintainer `go run` tool in the root module, like
  `cmd/bob-builder/genbuilders`; not a shipped executable; invoked by `//go:generate go run
  ../../cmd/nixpkgsgen gen` in `pkg/nixpkgs/doc.go`) has two steps:
  1. `snapshot` (online, run at bob release time by a maintainer): evaluates the pinned nixpkgs
     inside bob's own namespace (R-NIX-021) in batches through a `resolve.nix`-style data-only
     expression and writes `cmd/nixpkgsgen/snapshot/<rev>.json.zst` (outside `pkg/`, so the
     framework zip embedded in bob, R-FW-043.4, does not carry it): per top-level
     attribute the version, description, licence SPDX ids, `mainProgram`, outputs, availability
     on `aarch64-linux`/`x86_64-linux`, broken/insecure/unfree flags and alias status. Memory is
     bounded by batching (a full `nix-env -qa` index is still never built, R-NIX-027).
  2. `gen` (offline, deterministic): turns the snapshot into `pkg/nixpkgs/attrs_gen.go`
     (`// Code generated by nixpkgsgen from nixpkgs <rev>. DO NOT EDIT.`). `go generate
     ./pkg/nixpkgs` runs only this step; `go run ./cmd/nixpkgsgen -check` regenerates in memory
     and fails on any byte difference, and checks that the snapshot revision equals the default
     pin (`lock.DefaultRev`) and that every generated attribute is in the snapshot and in the
     pinned index's names list (`pkg/engine/nixidx`, R-NIX-027). CI runs `-check` offline.
- **Names.** Go name = attribute split at `-`, `_`, `.`; each part's first letter upper-cased; parts
  joined, with `_` kept only between a part ending in a digit and one starting with a digit; a
  leading digit gets the prefix `N`; `'` becomes `Prime` (`openssl` → `Openssl`, `go_1_26` →
  `Go1_26`, `nix-update` → `NixUpdate`, `python313` → `Python313`, `3proxy` → `N3proxy`). When two
  attributes map to one name, or a name collides with a hand-written identifier of the package
  (`Attr`, `Tool`, `Option`, `Outputs`, …), every member of the group uses the fallback
  `A_<attr with - . ' replaced by _>`; remaining collisions get `_<8 hex of fnv32a(attr)>`. The
  generator writes the collision list into the file header comment; a golden test pins it.
- **Doc comment** (gopls hover and completion detail): `// Openssl is openssl 3.6.1: A
  cryptographic library that implements the SSL and TLS protocols. Licence: Apache-2.0.` plus
  `// Unfree: needs nixpkgs.AllowUnfree().` or `// Only on aarch64-linux.` when relevant.
  Descriptions are cut at 160 bytes on a word boundary, control characters removed.
- **Exclusions** (documented in `cmd/nixpkgsgen/exclude.json`, each with a reason, and on the
  docs status page): attributes that are not derivations (package sets and scopes:
  `python3Packages`, `pythonPackages`, `python3xxPackages`, `ocamlPackages`, `ocaml-ng`,
  `haskellPackages`, `haskell`, `perlPackages`, `nodePackages`, `rubyPackages`, `luaPackages`,
  `rPackages`, `emacsPackages`, `vimPlugins`, `linuxPackages*`, `rustPackages`, `pkgsCross`,
  `pkgsStatic`, `pkgsMusl`, `pkgsi686Linux`, `pkgsLLVM`, `darwin`, `lib`, functions,
  `recurseIntoAttrs` sets in general), aliases (an attribute that disappears with
  `config.allowAliases = false`), `meta.broken`, insecure (`meta.knownVulnerabilities`
  non-empty), attributes unavailable on both Linux systems, attributes whose evaluation fails
  (`tryEval`). Unfree packages are **included** and marked in the doc comment (R-NIX-011 1004
  still applies).
- **Getting an excluded package.** Ecosystem packages that need members of a language set take
  them internally through `pkg/nixpkgs/nixext` (`nixext.New("ocamlPackages.findlib")`), never
  through the user API: OCaml toolchains, Rust std, the cross toolchains of R-FW-033, generator
  programs. `cmd/nixpkgsgen/include.json` lists nested attributes that are exposed as typed
  handles anyway (`python3Packages.pip` → `nixpkgs.Python3PackagesPip`), each with a reason; it
  starts with the one entry the tests need — `rust_1_97.packages.stable.rustc` →
  `nixpkgs.Rust1_97PackagesStableRustc` (R-RS-004a needs a second rustc; nixpkgs has no top-level
  versioned rustc) — and grows only by an owner-visible change of that file. A user who needs a
  language set member today uses the ecosystem package for that language, or asks for an
  `include.json` entry (docs status page).
- **`nixext`** (`pkg/nixpkgs/nixext`, layer L1) defines `Attr`, the role methods and options;
  `pkg/nixpkgs` re-exports them as aliases and holds only the generated variables, so
  ecosystem packages import the small `nixext` and do not compile the generated file. `nixext`
  is the extension API of bob's own ecosystem packages: `bob.go` may not import it (R-FW-002
  R7), so `nixext.New(name)` is not reachable from build definitions. It also provides
  `nixext.Toolchain(p)` with the typed compiler attributes that `bob.Toolchain.CC/Bintools/
  CCLib/Emulator` carried as strings (those fields are removed).
- **Framework variables are read-only.** A `bob.go` that assigns to a package-level variable of a
  framework package (`nixpkgs.Openssl = nixpkgs.Zlib`) is rejected before compiling (R-FW-002
  R8, BOB-E-1024).

### 3. Pin tracking

- The generated surface is produced at bob release time for bob's default pin
  (`lock.DefaultChannel` + new `lock.DefaultRev`). A new repository's `bob lock` pins
  `DefaultRev` (not the channel head), so the typed surface and the pin agree by default;
  `bob lock --update nixpkgs` moves to the channel head.
- `bob.lock` records the pinned `nixpkgs.rev` (unchanged schema, R-FW-004); the surface revision
  is compiled into the framework (`nixext.SurfaceRev`, data) and printed by `bob version`, so no
  lock change is needed and existing locks stay valid.
- When the pinned revision differs from the surface revision, every used attribute is still
  checked against the pinned index before any build (R-NIX-011). An attribute the pin lacks
  fails with **BOB-E-1001 variant `pin`**: `{pos}: nixpkgs at the pinned revision {rev} has no
  package "{attr}" (nixpkgs.{name})`, hint `bob's typed nixpkgs packages come from {surface};
  move the pin with bob lock --update nixpkgs, or use another package`, with the source position
  of the call. A package that exists at the pin but not in the surface cannot be named until a
  bob release regenerates the surface (status page; `include.json` for urgent cases).
- `RepoOpts.Nixpkgs` becomes the typed `bob.Channel` (`bob.NixOS2605`, `bob.NixOS2511`,
  `bob.NixpkgsUnstable`; zero value = bob's default channel) plus `RepoOpts.NixpkgsRev` (a
  40-hex revision, data).

### 4. Budgets [proposal, measured by Q2 before the decision is final]

| Row | What | Budget |
|---|---|---|
| B-098 | `attrs_gen.go` size / attribute count | ≤ 8 MiB source; count recorded |
| B-099 | cold `go build ./pkg/nixpkgs` [8c/15G] | ≤ 15 s |
| B-100 | evaluator cold compile, polyglot, delta vs. string API | ≤ +5 s; warm delta 0 |
| B-101 | gopls first workspace load of acme, delta | ≤ +3 s |
| B-102 | gopls completion `nixpkgs.Op` → `Openssl` first, p95 | ≤ 500 ms |
| B-103 | evaluator binary size delta (linker removes unused handles) | ≤ 2 MiB |

If B-099/B-100 fail, the fallback (decided without a new ADR) is to split the generated file into
per-initial-letter files of the same package (compile parallelism) — never to return to strings.

### 5. Registries where a string name is unavoidable (Go modules, crates.io, npm, opam)

There is no generated surface for registries: crates.io (~180 k crates), npm (~3 M packages)
and opam cannot be snapshotted per bob release, and a surface generated from `bob.lock` would
make `bob.go` depend on code generated from the lock that `bob.go` itself determines (a
bootstrap cycle, and against D2 "bob.go + bob.lock are the only source of truth"). Instead:

- A registry name is written **once**, in a **declaration constructor**, which returns a typed
  handle: `golang.Require(path, version) Requirement`, `rust.Crate(name, req, …) *rust.CrateDep`,
  `rust.Indirect(name) *rust.CrateDep`, `npm.Dep(name, req, …) *npm.Pkg`,
  `npm.Alias(as, name, req) *npm.Pkg`, `npm.Indirect(name) *npm.Pkg`, `ocaml.Opam(name, req)
  *ocaml.OpamPkg`, `ocaml.Indirect(name) *ocaml.OpamPkg`, `bob.Import(module, version)`.
  `Indirect` declares a package that is not a direct dependency but needs an option (an
  allowed install script, a native library); bob lock fails with BOB-E-1015 and the source
  position if the resolved graph does not contain it.
- Every other API that refers to such a package takes the handle: `rust.Uses(c *CrateDep, …)`,
  `npm.AllowScripts(pkgs ...*Pkg)`, `npm.Extend(pkg *Pkg, …)`, `npm.Peer(pkg *Pkg)`,
  `ocaml.Conf(pkg *OpamPkg, …)`. A typo can no longer silently allow nothing.
- Names inside a resolved package's own namespace that are only known after resolution (Rust
  feature names of a crates.io crate, findlib sub-libraries, pkg-config modules of a nixpkgs
  library) stay strings, are validated at lock/plan time with the source position and
  suggestions (BOB-E-1015 / BOB-E-1020), and are classified in the allowlist as category
  `resolved-namespace`.

### 6. Build modules are per package, hierarchical, never central

- **Package** = a directory with its own `bob.go`. Its **region** is that directory plus every
  descendant directory that has no `bob.go` of its own.
- **Locality (R-FW-070).** A `bob.go` references only files of its own region: every `Glob`,
  `Files`, `Dir`, `IncludeDir` and ecosystem path (`Root`, `Entry`, `BuildScript`, `NewSpec`
  file, …) is relative to the `bob.go` directory, has no `..` segment and no absolute path, and
  does not name a file in a directory that has its own `bob.go`; `**` never descends into such a
  directory (it is excluded from the match, like a nested module). Violations: **BOB-E-1028**
  (variants `outside`, `subpackage`), with the position and the subpackage named. A pattern
  that leaves the repository keeps BOB-E-1025.
- **One unit per package (R-FW-071).** A package defines at most one build *unit* per
  ecosystem: C/C++ one library, binary or external (`cc.StaticLibrary`, `SharedLibrary`,
  `NewBinary`, `External`); Rust one crate; npm one importer; OCaml one library or executable;
  Go one binary or C library of the directory's main package; proto one library; GraphQL/OpenAPI
  one schema/spec. Tests, checks, codegen recipes, steps, shells and outputs (images, AppImages,
  tarballs, releases) are not units. A second unit: **BOB-E-1029**.
- **Go.** `golang.(*Module).Binary`, `CArchive`, `CShared`, `Cgo` and `Tests` lose their
  directory argument: they declare the Go package of the calling `bob.go`'s directory. The
  module stays declared in the module root (`golang.NewModule`); `Tests(golang.TestOpts{
  Recursive: true})` covers every Go package of the caller's region. A Go main package's
  `bob.go` cannot be imported (rule R3: Go itself refuses to import a main package, and gopls
  would show that error). Other packages reach the binary through a **typed slot**: an
  importable `bob.go` declares `var API = bob.NewSlot[*golang.Binary]()`, the main package's
  `bob.go` binds its constructor (`var _ = acme.API.Bind(Binary)`), and `deploy/bob.go` uses
  `acme.API.Get(p)`. Unbound or twice-bound slots are definition errors (BOB-E-1020). This keeps
  `deploy/` packages and every label as they were. Pipelines reference releases by label
  pattern (`PipelineOpts.Releases []bob.Pattern`, the CLI form already used by
  `Targets`/`Tests`), validated at evaluation (a pattern that matches no release: BOB-E-1020 at
  the pipeline's position).
- **C/C++ (R-CC-006).** One `cc` unit per directory package. Public headers are the typed
  include surface `Hdrs: bob.IncludeDir("include")` (a directory of the region, `.` = the
  package directory); consumers get include paths only from `Deps`/`PrivDeps` link info. No
  global include directories exist; `Flags` and `LinkOpts` refuse every `-I`, `-isystem`,
  `-iquote`, `-idirafter`, `-include`, `-imacros`, `--sysroot`, `-L`, `-l` and `@file`, relative
  or absolute (BOB-E-1020, hint `use Deps`). No whole-project source list exists: a project is a
  tree of packages; `compile_commands.json` (R-CC-003) is a generated view of those packages, not
  a build model.
- **Rust / npm / OCaml / proto** already follow one-unit-per-directory in every fixture; the rules
  make it binding. Codegen output directories (`gen/...`) are owned directories inside the
  consuming package (§03.6.6), so they comply.

### 7. Enforcement ("by force")

| Guard | Where | Fails on |
|---|---|---|
| `TestRFW072TypedSurface` | tests/core | any unclassified raw-string parameter/field of the public API; stale allowlist rows |
| `TestRNIX031GeneratedSurface` | tests/core | missing/hand-edited `attrs_gen.go`, `-check` difference, excluded sets present, doc comments without version/licence, string role functions present |
| `TestRFW070FixtureLocality` | tests/core | any fixture / testdata `bob.go` with a source outside its package, into a subpackage, a Go directory string, a nixpkgs string attribute, a nixext import, two units in one package |
| `TestDocsPackageLocality` | tests/docs | the same rules over every `bob.go` block of the documentation |
| faults 82–91 | tests/faults + tests/errors | each violation must give its BOB-E code, position and hint |
| `go vet`/R7/R8 rules | evaluator (R-FW-002) | `nixext` import, assignment to framework variables |

## Rejected

- **Keep strings, add gopls completion through a custom analyzer.** Not type safety; a typo
  still compiles; the owner explicitly rejected strings.
- **One function per attribute (`nixpkgs.Openssl(p)`)** — loses the Tool/Lib distinction unless
  doubled (`OpensslTool`, `OpensslLib`), doubling the surface.
- **Typed string constants (`type Attr string`)** — `nixpkgs.Lib(p, "gcc")` would still compile
  (untyped constants convert); not structural (AGENTS.md §2.5).
- **A separate import for the generated surface (`pkgs.Openssl`)** — two imports for every use;
  the `nixext` split gives the same compile isolation for bob's own packages.
- **Generating the surface per user pin at `bob lock` time** into the repository — generated Go in
  the user's tree for every pin change, large diffs, and the evaluator's framework zip (R-FW-043.4)
  would no longer be bob's own; kept as a possible later extension, not v1.
- **Allowing a root `bob.go` to declare targets of subdirectories without `bob.go`** — that is the
  central CMake-like file the owner rejected.
- **Moving every output of a Go binary into its main package's `bob.go`** — works, but forces
  label and release-name changes (a release named like its binary collides with the binary's
  label) and makes main packages carry deployment code; the slot keeps packages small.

## Consequences

- Every fixture, test repository, documentation page and example changes (unit Q1 did the test
  layer; Q2 and Q3 do the framework and docs, `plan/progress/Q1-impl-plan.md`). Labels do not
  change (slots keep `deploy/` packages); `definedAt` positions of moved declarations do (Go
  binaries now defined in their main package's `bob.go`; `cpp-lib`'s binary is `//tool:ringtool`,
  formerly `//:ringtool`).
- Fault 01 (unknown attribute) can no longer be produced from source: a misspelt handle is a Go
  compile error (BOB-E-1020 with a `did you mean` hint, fault 87); 01 remains as the engine-level
  replay of a pin that lacks an attribute.
- The generated file is large but compiled once per bob version and cached; budgets B-098…B-103
  decide the layout.

## Owner amendments

- 2026-10-24 (chat): "looking into bob.go i detect how you designed the interface and it is
  absolutely terrible.... I think in the harness I did absolutely specify that everything must be
  typesafe... nixpkgs.Tool(p, "gcc") for example is 100% not typesafe... stop this... there must
  be a generator that builds a go prop for every nixpkgs package to make it a typesafe option...
  this can exclude packages e.g. for python not required in other things but must be typesafe of
  course. Also the CPP one (the only I checked) kinda scared me... to me it looks like a cmake
  file just wrapped... no bro i want that cpp is also split up into modules same for other
  systems so you create sub 'bob' files for each package to group it not a central one where you
  hardcode all cpp files used... then we are just like cmake... Please rewrite the harness if it
  doesnt already clearly say this by force I approve everything and then start implementation
  fixing for this."
