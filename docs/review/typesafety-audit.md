# Typed-surface and central-listing audit (Q1, 2026-10-24)

Trigger: owner chat 2026-10-24 (REQUIREMENTS §24 D71/D72, ADR 0020). Scope: the public API of
every build-definition package (`pkg/bob`, `pkg/nixpkgs`, `pkg/cc`, `pkg/golang`, `pkg/rust`,
`pkg/npm`, `pkg/ocaml`, `pkg/proto`, `pkg/connect`, `pkg/graphql`, `pkg/openapi`,
`pkg/services/postgres`, `pkg/output/{oci,appimage,tarball}`, `pkg/release`), plus the designs
that let one `bob.go` describe the sources of other directories.

Method: `tests/harness/boblint.APIStrings` lists every exported function/method parameter and
exported struct field whose type is `string`, `[]string`, `...string` or `map[string]T`
(200 entries at the time of writing, test files and generated files excluded). Each entry was
classified: **data** (own names, local paths, versions, URLs, flags, commands, …; the
allowlist of `TestRFW072TypedSurface`, categories of SPEC §1.4.5) or **violation** (a string
that names a package, attribute, target, tool or library defined elsewhere, or a closed set
without a type). The violations are the protected list `typedSurfaceViolations` in
`tests/core/typesafe_test.go`; while one exists that test is red. Line numbers are of the tree
on 2026-10-24.

## 1. Stringly-typed identifiers (fixed by Q2 = nixpkgs/core, Q3 = ecosystems)

| Id | Where (file:line) | Signature today | Typed form (final name, SPEC) | Unit |
|---|---|---|---|---|
| V01 | pkg/nixpkgs/nixpkgs.go:23, :33, :41, :192 | `func Tool(p *bob.P, attr string, opts ...Option) *ToolPackage`, `Lib(…attr string…) *Package`, `Pkg(…)`, `Exe(p, attr, mainProgram string, …)` | generated handles `nixpkgs.<Name>` of sealed type `nixpkgs.Attr` with methods `.Tool(p, opts...)`, `.Lib(p, opts...)`, `.Pkg(p, opts...)`, `.Exe(p, opts...)` (SPEC §1.7, R-NIX-031) | Q2 |
| V02 | pkg/nixpkgs/nixpkgs.go:192 | `Exe(…, mainProgram string, …)` | main program from `meta.mainProgram` in the snapshot; override `nixpkgs.MainProgram(path string) Option` (output-path data) | Q2 |
| V03 | pkg/nixpkgs/nixpkgs.go:119 | `func Outputs(names ...string) Option` | `func Outputs(names ...nixpkgs.Output) Option` with constants `Out`, `OutBin`, `OutDev`, `OutLib`, `OutMan`, `OutDoc`, `OutDebug`, `OutStatic` | Q2 |
| V04 | pkg/nixpkgs/nixpkgs.go:124 | `func Modules(names ...string) Option` | **kept** (category `resolved-namespace`): pkg-config module names are known only after the dev output exists; validated at planning with BOB-E-1020 listing the available modules | Q2 (validation) |
| V05 | pkg/bob/toolchain.go:18, :20, :24, :27 | `bob.Toolchain{CC, Bintools, CCLib, Emulator string}` (nixpkgs attributes, used as `nixpkgs.Tool(p, tc.CC)` in pkg/cc/cc.go, pkg/golang/golang.go) | fields removed; `nixext.Toolchain(p) nixext.Toolchain{CC, Bintools, CCLib, Emulator nixpkgs.Attr}` (SPEC §04 R-FW-033 block) | Q2 |
| V06 | pkg/bob/repo.go:37 | `RepoOpts.Nixpkgs string` ("nixos-26.05", "chan@rev") | `RepoOpts.Nixpkgs bob.Channel` (`NixOS2605`, `NixOS2511`, `NixpkgsUnstable`; zero = default) + `RepoOpts.NixpkgsRev string` (hash data) (SPEC §1.4.4) | Q2 |
| V07 | pkg/bob/repo.go:92 | `PipelineOpts.Releases []string` (release names) | `[]bob.Pattern` (CLI label form, like `Targets`/`Tests`), validated against release packages, BOB-E-1020 at the pipeline position; IR keeps the resolved names | Q2 |
| V08 | pkg/bob/platform.go:129 | `WithFeatures(crate Package, features ...string)` | **kept** (`resolved-namespace`: crate features), validated at lock | — |
| V09 | pkg/golang/golang.go:351, :368, :374, :451, :494 | `(*Module).Binary(dir string, …)`, `CArchive(dir…)`, `CShared(dir…)`, `Cgo(dir…)`, `Tests(pattern string, …)` | no directory argument: `(*Module).Binary(o BinaryOpts)`, `CArchive(o)`, `CShared(o)`, `Cgo(o CgoOpts)`, `Tests(o TestOpts)` declare the package of the calling bob.go's directory; `TestOpts.Recursive bool` for the caller's region (SPEC §03.2.1). Other packages reach a main package's binary through `bob.Slot` (SPEC §1.4.5) | Q3 (Slot: Q2) |
| V10 | pkg/golang/golang.go:201 | `func GenLabel(dir string) string` | moved to `pkg/golang/internal/gospec` (not build-definition API) | Q3 |
| V11 | pkg/golang/golang.go:58, pkg/npm/api.go:137 | `Generated.Tools []string` (nixpkgs attributes) | `Generated.Tools []nixpkgs.Attr` | Q3 |
| V12 | pkg/golang/golang.go:81 | `GeneratorRuntime(path, attr string, exact bool)` | `GeneratorRuntime(path string, gen nixpkgs.Attr, exact bool)` (path stays: registry declaration) | Q3 |
| V13 | pkg/npm/api.go:150 | `GenRuntime{Name, Generator string}` | `GenRuntime{Name string; Generator nixpkgs.Attr}` | Q3 |
| V14 | pkg/npm/quirk.go:22 | `Quirk.Tools []string` (nixpkgs attributes) | `Quirk.Tools []nixpkgs.Attr` (`Quirk.Package` stays: the table's registry key) | Q3 |
| V15 | pkg/npm/api.go:81, :86, :58 | `AllowScripts(pkgs ...string)`, `Extend(pkg string, …)`, `Peer(name string)` | `npm.Dep`/`npm.Alias` return `*npm.Pkg`; new `npm.Indirect(name) *npm.Pkg`; `AllowScripts(pkgs ...*Pkg)`, `Extend(pkg *Pkg, deps ...Dependency)`, `Peer(pkg *Pkg) Dependency` (SPEC §03.4.1) | Q3 |
| V16 | pkg/rust/rust.go:102 | `Uses(crate string, libs ...bob.Linkable)` | `rust.Crate` returns `*rust.CrateDep`; new `rust.Indirect(name) *CrateDep`; `Uses(crate *CrateDep, libs ...cc.Linkable)` (SPEC §03.3.1) | Q3 |
| V17 | pkg/rust/rust.go:86 | `Rename(codeName string)` | **kept** (`own-name`: the identifier in the crate's own code) | — |
| V18 | pkg/ocaml/ocaml.go:72 | `Conf(opamName string, libs ...cc.Linkable)` | `ocaml.Indirect(name) *OpamPkg`; `Conf(pkg *OpamPkg, libs ...cc.Linkable)` (SPEC §03.5.1) | Q3 |
| V19 | pkg/services/postgres/postgres.go:20 | `Options.Version string` ("17" → `postgresql_17`) | `Options.Server nixpkgs.Attr` (default `nixpkgs.Postgresql`), e.g. `nixpkgs.Postgresql17` | Q3 |
| V20 | pkg/rust/cfg.go:12 | `func CfgTable(triple string) ([]string, error)` | moved to an internal package | Q3 |
| V21 | pkg/cc/cc.go:49, pkg/rust/rust.go:117 | `LibraryOpts.Hdrs string`, `LibOpts.Headers string` | `bob.Include` built by `bob.IncludeDir(dir)` (typed include surface; consumers get it through link info) (SPEC §1.4.1, R-CC-006) | Q2 (type) + Q3 (use) |
| V22 | pkg/bob/label.go:32 | `Label{Module, Dir, Name, Nix string}` | **kept** (`value`: the CLI label value itself) | — |
| V23 | pkg/graphql/graphql.go:42, :43, :108 | `GqlgenOpts.Models map[string]string`, `Autobind []string`, `TSOpts.Scalars` | **kept** (`target-language`: Go/TS type names of the generated code; bob.go cannot import those packages; validated by the generator, BOB-E-2000) | — |
| V24 | pkg/cc/external.go:50 | `ExternalOpts.Libs []string` | **kept** (`own-name`: the libraries the External itself installs) | — |
| V25 | pkg/proto/proto.go:25 | `LibraryOpts.Root string` (repo-relative directory outside the package) | removed; import root = nearest ancestor named `proto`, else the repository root (SPEC §03.6.1) | Q3 |
| V26 | pkg/openapi/openapi.go:38 | `NewSpec(p, file string)` | **kept** (`local-path`), locality-checked (R-FW-070) | Q3 (check) |
| V27 | pkg/bob/exe.go:17, pkg/bob/sources.go:312 | `bob.Exe(t, mainProgram string)`, `(*Step).File(p string)` | **kept** (`output-path`) | — |
| V28 | spec/03-ecosystems.md §03.1.2 (`Tools []*nixpkgs.Package`); pkg/cc/external.go:49 already `[]bob.Tool` | SPEC typed extra host tools as the Lib package type | SPEC aligned to the code: `Tools []bob.Tool` (`nixpkgs.Perl.Tool(p)`); no code change | Q1 (spec) |
| V29 | pkg/bob/repo.go:190 | `Import(module, version string)` | **kept** (`registry-declaration`) | — |
| V30 | pkg/golang/golang.go:49, rust.go:67, npm/api.go:44, :55, ocaml.go:45 | `Require(path, version)`, `Crate(name, req)`, `Dep(name, req)`, `Alias(as, name, req)`, `Opam(name, req)` | **kept** as the one declaration site; they return typed handles that every other option takes (ADR 0020 §5) | Q3 |
| V31 | pkg/npm/api.go:119–123 | `TSConfig{Target, Module, ModuleResolution, JSX string}` | named types with constants: `npm.ESTarget` (`ES2020`…`ESNext`), `npm.ModuleKind` (`ESNext`, `NodeNext`, …), `npm.Resolution` (`Bundler`, `NodeNextResolution`), `npm.JSX` (`JSXNone`, `ReactJSX`, `Preserve`) | Q3 |

Also stringly-typed, in internal call sites and messages (not public API, migrated with V01/V05):
`nixpkgs.Tool(p, tc.CC)` and friends in pkg/cc/cc.go:220–228, pkg/golang/golang.go:288–300,
pkg/proto/proto.go:152 (`"buf"`), pkg/output/appimage/appimage.go:192, :197, pkg/services/postgres/tools.go,
pkg/engine/build/dev.go (47 call sites outside pkg/nixpkgs, `grep -rn 'nixpkgs\.\(Tool\|Lib\|Pkg\|Exe\)(' pkg`);
hint texts that print the old form: pkg/rust/bop/bop.go:351, pkg/rust/gen/defaults.go:98,
pkg/ocaml/gen/lock.go:124, pkg/ocaml/gen/plan.go:297, pkg/npm/gen/eco.go:225; `bob init`
templates pkg/cli/cmd_init.go:244, :251 (unit Q4's file: coordinate).

## 2. Central-listing designs

| Id | Where | Design | Replacement (SPEC) |
|---|---|---|---|
| C01 | pkg/golang/golang.go:269 `relDir`, :278 `labelDir`, `ext.NewIn` at :354, :384, :464, :549 | a module root's `bob.go` declares binaries, cgo packages and tests of any directory by string; fixtures used it centrally: `polyglot/bob.go` (API + FFI of `cmd/api`), `acme/deploy/bob.go` (re-declared `cmd/api`), `go/api/bob.go` (`cmd/api`, `internal/ffi`), `go/tools/bob.go` (`cmd/stamp`), `codegen/svc/bob.go` (`cmd/server`, `cmd/client`), `bigclosure/bob.go` (`cmd/big`), cli/core/devenv test repos | V09; declarations move into each Go package's own `bob.go`; `bob.Slot` for importers of main packages (R-FW-070, R-FW-071) — fixtures migrated by Q1 |
| C02 | tests/fixtures/repos/cpp-lib/bob.go (before Q1) | one root `bob.go` built a library (`src/`), a binary (`tool/`) and a test (`test/`) — three units, sources of three directories | `tool/bob.go` holds the binary (R-CC-006); library and its test stay at the root (`src/`, `include/`, `test/` have no bob.go) — migrated by Q1 |
| C03 | pkg/cc/cc.go:305 `flagProblem`, :328 `pathFlag` | `-I`, `-isystem`, `-L` … are refused only with an **absolute** path; `-I../libs/mathx/include` passes, so a package can include another package's headers without depending on it | refuse every include/library path flag, relative or absolute (R-CC-006, fault 85) |
| C04 | pkg/cc/cc.go:49 `Hdrs string`, :341 `localDir` | public headers named by a path string; `localDir` refuses `..` but the include surface is not a typed value of the library | `bob.Include` (V21); include paths only from deps' link info |
| C05 | pkg/engine/srcs/srcs.go:220–248 | `**` in a root `bob.go` descends into every subdirectory, including packages with their own `bob.go` — a root glob is a whole-project source list | `**` and `bob.Dir` stop at subpackage directories (R-FW-070; `TestRFW070GlobStopsAtSubpackage`) |
| C06 | SPEC R-FW-021 / pkg/bob sources | `..` is refused only when it leaves the repository (BOB-E-1025); `../sibling/src/*.c` inside the repo is accepted | any `..` and any file of a subpackage → BOB-E-1028 (R-FW-070, faults 82, 83, 91) |
| C07 | pkg/proto/proto.go:25 | `Root` names a directory outside the package | V25 |
| C08 | pkg/golang/golang.go:494 | `Tests("./...")` from a module root covers packages that have their own `bob.go` | `Tests(TestOpts{Recursive: true})` covers the caller's region only |
| C09 | pkg/bob/repo.go:92 | pipelines reference releases by bare name strings | V07 |
| C10 | no check anywhere | nothing limits the number of libraries/binaries per `bob.go` | R-FW-071 / R-CC-006, BOB-E-1029 (fault 84) |

Rust (`rust/*`), npm (`npm/*`), OCaml (`ocaml/*`) and proto fixtures already had one unit per
directory; the rules make that binding.

## 3. Documentation

Every page below shows at least one old form (string nixpkgs attributes, `Hdrs: "include"`,
`Nixpkgs: "nixos-26.05"`, Go directory strings, string script allowlists) and is migrated by Q2 in
the same change as the API (AGENTS.md §5.9; `TestDocsPackageLocality` turns on with
`pkg/nixpkgs/attrs_gen.go`): website/content/docs/01-introduction/01-what-is-bob.md,
01-introduction/02-concepts.md, 03-quickstart/01-go.md … 07-outputs.md (all seven),
04-guides/01-bob-go-reference.md, 02-dependencies-and-locking.md, 03-multi-language.md,
05-dev-environments.md, 06-pipelines.md, concepts/01-build-graph.md.
