# Q1 implementation plan for Q2 and Q3 (ADR 0020, owner D71/D72, 2026-10-24)

Binding inputs: REQUIREMENTS §24 D71/D72, R-FW-010 (amended), R-FW-017/018, R-CC-006;
SPEC §1.4.1 (`bob.Include`), §1.4.4 (`bob.Channel`, `PipelineOpts.Releases`), §1.4.5 (R-FW-070,
R-FW-071 + `bob.Slot`, R-FW-072), §1.7 (R-NIX-013, R-NIX-031, R-NIX-032), R-NIX-005, R-FW-002
R7/R8, §02.3 codes 1001 `pin` / 1024 `R8` / 1028 / 1029, §03.0.7, §03.1 (R-CC-006), §03.2.1,
§03.3.1, §03.4.1, §03.5.1, §03.6.1, §04 R-FW-032/R-FW-033; ADR 0020; HARNESS §18;
docs/review/typesafety-audit.md (V01–V31, C01–C10).

Tests that must turn green (no `red:` text) are listed per step. Fixtures, test repositories,
fault patches, goldens of the code table and budgets are already migrated (test corrections
98–104); do not edit tests — report problems under "TEST PROBLEM" in your progress file.

Ownership: **Q2** owns `pkg/bob` (except diag), `pkg/nixpkgs/**`, `cmd/nixpkgsgen/**`,
`pkg/engine/eval` (rules R7/R8, compile-error hints), `pkg/engine/nixidx`, `pkg/engine/lock`
(`DefaultRev`), `pkg/engine/srcs` (region rule), `pkg/engine/build` (slot/locality plumbing),
`website/content/docs/**`. **Q3** owns `pkg/cc`, `pkg/golang`, `pkg/rust`, `pkg/npm`, `pkg/ocaml`,
`pkg/proto`, `pkg/connect`, `pkg/graphql`, `pkg/openapi`, `pkg/services/postgres`, their `gen/`
and `bop/`, `pkg/output/**`, `pkg/release`. Q3 starts after Q2 step 2 (the `nixext` API exists),
and works against the final names below. `pkg/cli/cmd_init.go` (`bob init` templates, lines 244,
251) belongs to **Q4**: Q2 writes the new template text under NEEDS for Q4 (or edits after Q4
finishes, coordinator decides). Private copies and caches under `/var/tmp/<unit>/`.

## Q2 — typed nixpkgs surface, core API, migration of call sites, docs

### Step 1: `pkg/nixpkgs/nixext` (new package, layer L1)

Add `pkg/nixpkgs/nixext` to the L1 list of `pkg/layering_test.go` (it already covers
subpackages of `pkg/nixpkgs`; verify) and to the evaluator's framework package list
(`pkg/engine/eval/framework.go`, so bob.go compiles against it), but **reject it in R7**.

```go
package nixext // github.com/megakuul/bob/pkg/nixpkgs/nixext

// Attr is one nixpkgs attribute. Construct only with New (bob's own packages) or use the
// generated handles of pkg/nixpkgs. Comparable; zero value invalid (BOB-E-1020 when used).
type Attr struct{ name string }
func New(name string) Attr          // validates the attr grammar of R-FW-015 (panics never; an invalid name is reported at use)
func (a Attr) Name() string
func (a Attr) Tool(p *bob.P, opts ...Option) *ToolPackage
func (a Attr) Lib(p *bob.P, opts ...Option) *Package
func (a Attr) Pkg(p *bob.P, opts ...Option) *Package
func (a Attr) Exe(p *bob.P, opts ...Option) *Program   // main program: MainProgram option, else the snapshot's meta.mainProgram (generated table), else BOB-E-1020 "set nixpkgs.MainProgram"

type Option func(*options)
type Output string
const ( Out Output = "out"; OutBin = "bin"; OutDev = "dev"; OutLib = "lib"; OutMan = "man"; OutDoc = "doc"; OutDebug = "debug"; OutStatic = "static" )
func Outputs(names ...Output) Option
func Modules(names ...string) Option
func Cross() Option
func AllowInsecure(ids ...string) Option
func AllowUnfree() Option
func MainProgram(path string) Option

type Package struct{ bob.Node; … }     // methods as today: Outputs, LinkInfo, CrossSet(p) *Package
type ToolPackage struct{ bob.Node; … } // Outputs
type Program struct{ *Package; … }     // MainProgram() string

// Toolchain is the typed form of the attribute strings bob.Toolchain carried (V05).
type Toolchain struct{ CC, Bintools, CCLib, Emulator Attr }  // zero CCLib/Emulator natively
func Toolchain(p *bob.P) Toolchain        // same table as bob.ToolchainFor today

const SurfaceRev = "<40 hex>"           // generated: pkg/nixpkgs/nixext/surface_gen.go
```

Move the current implementation of pkg/nixpkgs/nixpkgs.go here (IR `nix` spec unchanged: the
IR keeps `attr` strings, the engine needs no change). Keep `mainProgram` defaults in a
generated, unexported map in `nixext/mainprog_gen.go` (attr → path) so `Exe` works for
`nixpkgs.Curl.Exe(p)`; the generator writes it.

### Step 2: `pkg/nixpkgs` becomes aliases + generated handles

- `pkg/nixpkgs/nixpkgs.go`: `type Attr = nixext.Attr`, `type Option = nixext.Option`,
  `type Output = nixext.Output`, the `Out…` constants re-declared (`const OutLib = nixext.OutLib`),
  `type Package = nixext.Package`, `type ToolPackage = nixext.ToolPackage`,
  `type Program = nixext.Program`, and **functions** (not vars, so the API lint sees them and gopls
  shows docs) `func Outputs(names ...Output) Option { return nixext.Outputs(names...) }`,
  `Modules`, `Cross`, `AllowInsecure`, `AllowUnfree`, `MainProgram`. Delete `Tool`, `Lib`, `Pkg`,
  `Exe` (the string functions).
- `pkg/nixpkgs/doc.go`: package doc (how to find a package: gopls completion, `go doc
  nixpkgs.Openssl`, the exclusion list, the include list) and
  `//go:generate go run ../../cmd/nixpkgsgen gen`.
- `pkg/nixpkgs/attrs_gen.go` (generated, see step 3).
- `pkg/nixpkgs/example_test.go`: `ExampleAttr_Tool`, `ExampleAttr_Lib`, `ExampleAttr_Pkg`,
  `ExampleAttr_Exe` with handles (TestAPISurface wants examples for constructors; methods take
  `*bob.P` so add them).

### Step 3: `cmd/nixpkgsgen` (new, root module, maintainer tool)

```
cmd/nixpkgsgen/main.go        subcommands: snapshot -rev <rev> [-out …]; gen [-snapshot …] [-out …]; -check
cmd/nixpkgsgen/naming.go      GoName(attr) per ADR 0020 §2 (+ reserved list: Attr, Option, Output, Out*, Outputs, Modules, Cross, AllowInsecure, AllowUnfree, MainProgram, Package, ToolPackage, Program, Tool, Lib, Pkg, Exe, Name)
cmd/nixpkgsgen/naming_test.go table: openssl→Openssl, go_1_26→Go1_26, nix-update→NixUpdate, python313→Python313, 3proxy→N3proxy, postgresql_17→Postgresql17, rust_1_97.packages.stable.rustc→Rust1_97PackagesStableRustc, collisions → A_… and fnv suffix
cmd/nixpkgsgen/snapshot.go    online: realise the pinned nixpkgs through pkg/engine/fetch+store (as R-NIX-005 does), evaluate a data-only expression in batches of ≤ 2000 attributes inside bob's namespace (reuse pkg/engine/nixidx's eval runner; extend resolve.nix or add snapshot.nix ≤ 60 lines, data only): per attribute isDerivation, version, description, license spdxIds, mainProgram, outputs, platforms ∩ {aarch64-linux, x86_64-linux}, broken, insecure, unfree, alias (allowAliases=false check), tryEval success
cmd/nixpkgsgen/gen.go         offline, deterministic: snapshot + exclude.json + include.json → pkg/nixpkgs/attrs_gen.go (sorted by Go name, gofmt'ed), pkg/nixpkgs/nixext/mainprog_gen.go, pkg/nixpkgs/nixext/surface_gen.go (SurfaceRev)
cmd/nixpkgsgen/exclude.json   [{"pattern": "python3Packages", "why": "language package set; use the python ecosystem (not in v1) or include.json"}, … the list of ADR 0020 §2]
cmd/nixpkgsgen/include.json   [{"attr": "rust_1_97.packages.stable.rustc", "why": "R-RS-004a needs a second rustc"}]
cmd/nixpkgsgen/snapshot/<rev>.json.zst   committed; <rev> = lock.DefaultRev
```

`-check`: regenerate in memory, byte-compare the three generated files, check snapshot rev ==
`lock.DefaultRev`, check every generated attribute is in the snapshot and in the pinned index's
names list (`nixidx` cache format; the snapshot carries the names list it was generated from, so
the check is offline). Exit 1 with a list of differences. Doc comment format exactly
`// <Name> is <attr> <version>: <description>. Licence: <spdx[, spdx]>.` (+ `// Unfree: needs
nixpkgs.AllowUnfree().` / `// Only on aarch64-linux.`); description cut at 160 bytes on a word
boundary, control characters dropped, a missing description → `<attr> <version>.`; a missing
licence → `Licence: unknown.`. File header: `// Code generated by nixpkgsgen from nixpkgs <rev>.
DO NOT EDIT.` followed by a comment listing the collision groups.

### Step 4: core API changes in `pkg/bob`

```go
type Channel int                                     // repo.go
const ( NixOS2605 Channel = iota + 1; NixOS2511; NixpkgsUnstable )
type RepoOpts struct{ Nixpkgs Channel; NixpkgsRev string; Variants []Named }
type PipelineOpts struct{ …; Releases []Pattern }    // validated: every pattern matches ≥ 1 release package (kind release), BOB-E-1020 at the pipeline position; IR pipelineJSON.Releases keeps resolved release names
type Include struct{ /* dir, pos */ }               // sources.go
func IncludeDir(dir string) Include                  // records the caller position; validated by R-FW-070 at use
type Slot[T any] struct{ … }                         // slot.go (new)
func NewSlot[T any]() *Slot[T]
func (s *Slot[T]) Bind(fn func(p *P) T) struct{}
func (s *Slot[T]) Get(p *P) T
type Toolchain struct{ Host, Target Platform; Triple, Prefix, EmulatorCmd string }  // CC/Bintools/CCLib/Emulator removed
```

- `Sources`: record the creation position of every `Glob/Files/Dir` (runtime.Caller of the
  first frame outside the framework, as definedAt does) so 1028 can print `{pos}`.
- Region rule (R-FW-070) where sources are resolved (`pkg/engine/srcs` + the evaluator's
  checks): reject `..` segments and absolute paths with 1028 `outside` (1025 stays for leaving
  the repository: check that first), reject literal prefixes inside a directory that has its own
  `bob.go` with 1028 `subpackage`, and exclude subpackage directories from `**`/`Dir` matches.
  The set of package directories is known from the R-FW-002 scan.
- Slots: `NewSlot`/`Bind` only in package-level var declarations (reuse Define's check);
  registry keyed by slot pointer; `Get` resolves the bound constructor (memoised), errors per
  SPEC §1.4.5 with BOB-E-1020.
- Unit counting for R-FW-071 lives in `ext`: `ext.Kind` gets `Unit string` (ecosystem name,
  "" = not a unit); `ext.New` records (bob.go dir, unit) → first label/pos; a second unit of the
  same ecosystem in the same directory → BOB-E-1029 (Q3 sets `Unit` on its kinds).
- `RepoOpts` zero channel = `lock.DefaultChannel`; IR `repo.nixpkgs` keeps the channel string.

### Step 5: evaluator rules and compile-error hints (`pkg/engine/eval`)

- R7: add `github.com/megakuul/bob/pkg/nixpkgs/nixext` to the refused imports (text of the
  R7 template unchanged). Tests: `TestRFW002BobGoRules/r7nixext`, fault 88.
- R8: AST check of every bob.go: assignment (`=`, op-assign, `++/--`) whose left side is a
  selector `<import>.<Name>` of a framework package, or `&<import>.<Name>` → 1024 variant R8
  `{dir}/bob.go:{line}: assigns to {pkg}.{Name}, …`. Tests: `TestRFW002BobGoRules/r8`, fault 89.
- Compile-error hints (1020): `undefined: nixpkgs.X` → `did you mean nixpkgs.<A>[, nixpkgs.<B>…]?`
  (edit distance ≤ 2 or same prefix over the generated names; read them from the framework's
  attrs_gen.go via go/parser once per bob version, cached); `undefined: nixpkgs.Lib|Tool|Pkg|Exe`
  with a string literal on that source line → `nixpkgs packages are typed: write
  nixpkgs.<GoName(literal)>.<Fn>(p)`. Tests: faults 86, 87.

### Step 6: pin tracking (`pkg/engine/lock`, `pkg/engine/nixidx`)

- `lock.DefaultRev = "<rev of the snapshot>"` next to `DefaultChannel`; a new lock with the
  default channel and no `NixpkgsRev` pins `DefaultRev` (R-NIX-005 amended). **Coordinate with
  Q4** (lock/init flows): Q2 adds the constant and the rule in the resolver; Q4 owns pkg/cli.
- 1001 variant `pin` when the pinned index lacks an attribute used by a handle and pin ≠
  `SurfaceRev`: args `pos` (definedAt of the role call), `rev`, `attr`, `name` (GoName), hint
  arg `surface`. Test hook `BOB_TEST_NIXPKGS_MISSING=<attr,…>` in nixidx (bobtest-only, like
  `BOB_TEST_CLONE_ERRNO`). Test: fault 90.
- `bob version --json` gains `nixpkgsSurface` (pkg/cli: NEEDS for Q4 or coordinator).

### Step 7: migrate every call site (mechanical; one change with step 2)

`grep -rn 'nixpkgs\.\(Tool\|Lib\|Pkg\|Exe\)(' pkg cmd internal` (47 sites outside pkg/nixpkgs):
computed attributes → `nixext.New(...)` (toolchains via `nixext.Toolchain(p)`), literal
attributes → generated handles where the package is not in nixext's own dependency chain
(`pkg/proto`: `nixpkgs.Buf.Tool(p)`; appimage `squashfsTools`, `glibc`; postgres `bash`,
`coreutils`; cc `patchelf`). Hint strings: pkg/rust/bop/bop.go:351, pkg/rust/gen/defaults.go:98,
pkg/ocaml/gen/lock.go:124, plan.go:297, pkg/npm/gen/eco.go:225 → handle wording of SPEC (Q3 for
the ecosystem files). Example tests in every package. `bob.Toolchain` users: pkg/cc/cc.go
`tools()`, pkg/golang/golang.go `ccTool`/`build` (Q3 edits those two files with Q2's nixext).

### Step 8: documentation (same change as step 2, AGENTS.md §5.9)

Pages of audit §3: replace every old form; Go examples declare binaries in their main
package's bob.go (`hello.Module(p).Binary(golang.BinaryOpts{})` in `cmd/hello/bob.go`), C/C++
quickstart split into per-directory packages; add to 04-guides/01-bob-go-reference.md sections
"Typed nixpkgs packages" (finding a package, roles, options, exclusions and include.json, pin
mismatch error) and "One package per directory" (region rule, units, `bob.Slot`); status page
entry for packages not in the surface; regenerate the API index (`BOB_DOCS_UPDATE=1 go test
./tests/docs`). `TestDocsPackageLocality` turns on with attrs_gen.go and must pass.

### Step 9: budgets and measurements

Register benchmarks for B-098…B-103 in `tools/internal/budgets/benches.go` (B-098 file size,
B-099 `go build ./pkg/nixpkgs` with an empty GOCACHE, B-100 polyglot evaluator cold compile
before/after, B-101/B-102 gopls through the LSP client of tests/core, B-103 evaluator size
delta). Record measured values with machine and load in `plan/progress/Q2.md`; if B-099/B-100
fail, split attrs_gen.go per initial letter (ADR 0020 §4), never back to strings.

### Q2 acceptance (all without `red:`)

`go test ./tests/core -run 'TestRNIX031GeneratedSurface|TestRFW002BobGoRules|TestRFW071Slots|TestRFW070GlobStopsAtSubpackage|TestRFW001'`;
`TestRFW072TypedSurface` with no V01–V07, V21(type) rows left (Q3 removes the rest);
`go test ./tests/docs` (incl. TestDocsPackageLocality, TestSnippetsTypeCheck, TestAPIReference);
`go test ./pkg/... ./cmd/nixpkgsgen/...`; `go run ./cmd/nixpkgsgen -check` offline;
faults 86–90, 92 live (`go test ./tests/errors -run 'TestCatalogue|TestLive' …` per HARNESS §5);
`go test ./pkg -run TestLayering`; `go run ./tools/ci -only fmt,vet,lint,layering,docs`.

## Q3 — per-package modules in cc and the other ecosystems, audit fixes

### cc (`pkg/cc`, `pkg/cc/gen`)

```go
type LibraryOpts struct {
	Srcs bob.Sources; Hdrs bob.Include; Private bob.Sources; Std Std; Defines map[string]string
	Flags, LinkOpts []string; Deps, PrivDeps []Linkable; Platforms []bob.Platform; Libs LibSource
}
type ExternalOpts struct{ URL string; Strip int; Build BuildSystem; Args []string; Deps []Linkable; Tools []bob.Tool; Libs []string; Include string }
```

- `Hdrs` is `bob.Include`; the include root is the Include's dir (`.` = package dir minus
  subpackages); `cc/gen` puts it into the library's `LinkInfo.IncludeDirs` only (no other
  path source).
- `flagProblem`/`pathFlag` (cc.go:305, :328): refuse `-I`, `-isystem`, `-idirafter`, `-iquote`,
  `-include`, `-imacros`, `--sysroot`, `-isysroot`, `-L`, `-l` (and `-l:`), `-B`, `-specs`, `@…` for
  any argument, relative or absolute (keep the texts `use Deps to add include paths` / `use Deps
  to add libraries`). Fault 85.
- Kinds `cc.library`, `cc.binary`, `cc.external` get `ext.Kind{Unit: "cc"}`; `cc.test` none.
  Fault 84, TestRFW070FixtureLocality semantics at run time (1029 text: `{dir}/bob.go already
  defines the cc library //libs/mathx:mathx at …`; ecosystem word "cc", kind words library /
  binary / external).
- Locality for `Srcs`, `Private`, `Hdrs` comes from Q2's sources; cc only passes positions.
- Acceptance: faults 82, 83, 84, 85, 91; `go test ./tests/eco/cc/...`; R-CC-006 test in
  tests/eco/cc (removing `mathx.Lib(p)` from app's Deps → BOB-E-2003); compile_commands golden
  regenerated as candidate (positions/labels unchanged except cpp-lib //tool:ringtool).

### Go (`pkg/golang`)

```go
func (m *Module) Binary(o BinaryOpts) *Binary
func (m *Module) CArchive(o BinaryOpts) *CLib
func (m *Module) CShared(o BinaryOpts) *CLib
func (m *Module) Cgo(o CgoOpts) *Package
func (m *Module) Tests(o TestOpts) *TestSet
type TestOpts struct{ Recursive bool; Name string; Tags []string; Services []bob.Service; Args []string }
type Generated struct{ Kind string; Dir string; Deps []bob.Package; Tools []nixpkgs.Attr; Spec any; Require []Requirement; At ext.Site; Also []Generated }
func GeneratorRuntime(path string, gen nixpkgs.Attr, exact bool) Requirement
```

- The directory is `ext.CallerDir()` relative to the module root (`m.dir`); outside the module →
  1020 `{dir} is not inside module {path}`; Binary/CArchive/CShared from a non-main Go package →
  1020 `{dir} is not a main package of module {module}`.
- `Tests`: package of the caller dir; `Recursive` adds every Go package below it whose directory
  has no bob.go (use the scan's package-dir set; never cross a nested module).
- Kinds: binary kinds `Unit: "go"` (one binary/C library per dir), cgo package `Unit: "go-cgo"`.
- `GenLabel` → `internal/gospec`. `ModuleOpts.Toolchain` default via `nixext.New("go_" + minor)`.
- Acceptance: `go test ./tests/eco/golang/...` (fixture go: api/cmd/api, api/internal/ffi,
  tools/cmd/stamp bob.go files), `tests/core` TestRFW070…; polyglot/acme/bigclosure via slots
  (TestRFW071Slots green from Q2); `go test ./tests/outputs/...` unchanged labels.

### Rust (`pkg/rust`)

```go
func Crate(name, req string, opts ...DepOpt) *CrateDep
func Indirect(name string) *CrateDep
type CrateDep struct{ /* unexported */ }   // implements Dep (Indirect handles do not add an edge: Dep use of an Indirect → 1020)
func Uses(crate *CrateDep, libs ...cc.Linkable) Option
type LibOpts struct{ …; Headers bob.Include; Toolchain bob.Tool; … }
```
`CfgTable` → `pkg/rust/internal/…`. Kinds: crate kinds `Unit: "rust"`. Lock: an `Indirect`
not in the resolved graph → 1015 with position; unknown feature of a crates.io crate → 1015
with suggestions. Hints (bop.go:351, defaults.go:98) print `rust.Uses(rust.Indirect("<crate>"),
nixpkgs.<Name>.Lib(p))`. Acceptance: `go test ./tests/eco/rust/...` (incl. R-RS-004a with
`nixpkgs.Rust1_97PackagesStableRustc`, which needs Q2's include.json entry).

### npm (`pkg/npm`)

```go
func Dep(name, req string, opts ...DepOpt) *Pkg
func Alias(as, name, req string) *Pkg
func Indirect(name string) *Pkg
type Pkg struct{ /* unexported */ }   // a Dependency (Dep/Alias); Indirect handles only carry options
func Peer(pkg *Pkg) Dependency
func AllowScripts(pkgs ...*Pkg) Option
func Extend(pkg *Pkg, deps ...Dependency) Option
type GenRuntime struct{ Name string; Generator nixpkgs.Attr }
type Quirk struct{ Package, Range, Why string; Skip bool; Tools []nixpkgs.Attr; Env map[string]string; Test string }
type ESTarget string; const ( ES2020 ESTarget = "ES2020"; ES2022 = "ES2022"; ES2023 = "ES2023"; ESNext = "ESNext" )
type ModuleKind string; const ( ModuleESNext ModuleKind = "ESNext"; ModuleNodeNext = "NodeNext"; ModulePreserve = "Preserve" )
type Resolution string; const ( Bundler Resolution = "Bundler"; NodeNextResolution = "NodeNext" )
type JSX string; const ( JSXNone JSX = ""; ReactJSX = "react-jsx"; Preserve = "preserve" )
type TSConfig struct{ Target ESTarget; Module ModuleKind; ModuleResolution Resolution; Strict *bool; JSX JSX; Lib, Types []string; Extends string; Include, Exclude []string; Extra map[string]any }
```
(`ESNext` for the target and `ModuleESNext` for the module kind avoid one Go name for two
types.) Importer kinds `Unit: "npm"`. Messages 2013/2014 and the lock note (gen/eco.go:225) use
the handle wording of SPEC §03.4.4 (code table already updated). Acceptance: `go test
./tests/eco/npm/...` (scripts_test expects the new wording), fault 54, faults 52/53 if live.

### OCaml (`pkg/ocaml`)

`func Indirect(name string) *OpamPkg`; `func Conf(pkg *OpamPkg, libs ...cc.Linkable) Option`;
findlib names validated at lock (1015). Kinds `Unit: "ocaml"` for Library/Executable. Hints
(gen/lock.go:124, plan.go:297) per SPEC §03.5.5. Acceptance: `go test ./tests/eco/ocaml/...`.

### Codegen and outputs

`proto.LibraryOpts.Root` removed (derived root); `openapi.NewSpec` file locality-checked;
`connect`/`graphql`/`openapi` recipes fill `Generated.Tools` with handles (`nixpkgs.ProtocGenGo`,
`nixpkgs.ProtocGenConnectGo`, `nixpkgs.ProtocGenEs`, `nixpkgs.Gqlgen`, `nixpkgs.OapiCodegen`, …
names from the generated surface; computed ones via `nixext.New`); `GeneratorRuntime` with
handles. Kinds `Unit: "proto"` (NewLibrary), `"graphql"` (NewSchema), `"openapi"` (NewSpec).
`pkg/services/postgres.Options{Server nixpkgs.Attr; Databases []string}` (default
`nixpkgs.Postgresql`). Outputs/release: internal nixpkgs calls via handles/nixext; no API change.
Acceptance: `go test ./tests/eco/gen/... ./tests/outputs/... ./tests/core -run TestRFW025`
(svc repo with `nixpkgs.Postgresql17`).

### Q3 acceptance

`TestRFW072TypedSurface` green (no violation rows left, no red); faults 82–85, 91 live; every
`tests/eco/*` suite at least as green as before; `go run ./tools/ci -only fmt,vet,lint,layering,docs`
(docs: update the ecosystem pages Q2 did not cover, write `docs:` notes).

## Order and hand-offs

1. Q2 steps 1–2 + 4 (types) land first, with docs (step 8) → Q3 starts.
2. Q2 steps 3, 5, 6, 9 in parallel with Q3.
3. Both: regenerate fixture-dependent golden candidates (`tests/goldens/ir/…`, `eco/cc/…`,
   `events/…`) once the fixtures evaluate; list them in out/golden-candidates/q1/README.md
   (owner approved all, chat 2026-10-24) and add test-correction entries after 104.
4. Coordinator: the parity floor may only rise; re-measure after both land.
