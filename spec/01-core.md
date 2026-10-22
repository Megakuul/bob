# §01 — Framework core (`pkg/`)

> Source of SPEC.md: `/root/bob/SPEC.md` is generated from spec/01…06 by concatenation (command in the SPEC.md preamble). Edit this file, then regenerate.

Status: SPEC draft (Phase 2.3), 2026-10-21. Covers R-ARCH-001…005, R-NIX-001…025, R-FW-001…031
(core part), R-NFR-001/002 (core budgets). ADRs: 0001, 0002 (locked), 0003, 0009 (model part).
Budgets are **[proposal]** and derive from the quiet-machine logs (`docs/spikes/measure/b-quiet.md`,
`docs/spikes/measure/quiet/*.log`) unless marked otherwise.

## Interfaces

Provided by §01 (names used by other sections):

- `bob.P`, `bob.Define`, `bob.Package`, `bob.Label`, `bob.Position`, `bob.Sources`, `bob.Glob`,
  `bob.Files`, `bob.Dir`, `bob.Output`, `bob.Tree`, `bob.Tool`, `bob.Executable` (`MainProgram()`),
  `bob.Linkable` + `bob.LinkInfo`, `bob.Run`, `bob.Service`, `bob.Pipeline` (registration only),
  `bob.Repo`, `bob.Platform`, `bob.Variant`, `bob.NamedVariant` (§1.4–§1.9). Host platform:
  `p.Host().Platform()`.
- `ext` package (`pkg/bob/ext`): `ext.New`, `ext.Kind`, `ext.Config`, `ext.Axis`
  — the only way ecosystem packages (§03, §04) create graph nodes (§1.5).
- Graph IR v1 JSON (§1.6); the `engine.Ecosystem` registration interface (§1.6.1; it lives in
  `pkg/engine` because it must not be linked into the evaluator).
- `bob.lock` container format, `lock.File` (`pkg/engine/lock`), §1.8; §03 owns
  the ecosystem sections and entry types.
- `engine/drv`: `Derivation` (structured attributes), `Name`, `MakeStorePath`, `FixedOutputPath`,
  `NAR` (§1.10).
- `engine/fetch.Fetcher` (§1.11), `engine/store.Store` (embedded rootless Nix, §1.14),
  `engine/nixidx.Index` (§1.15), `engine/build.Run` (whole pipeline, §1.16).
- `bob-builder` job contract `bspec.Job` v1 and core ops (§1.12); fixup contract (§1.13).
- `engine.Substituter` and `engine.RemoteExecutor` interfaces (R-FW-009, used by §02 `--remote`).

Expected from other sections:

- §02: error codes and templates (`BOB-E-NNNN`, S15) for every error text in this section,
  exit codes, `bob dev`/`bob env`/shims/LSP proxy, the `bob.DevShell`/`bob.NewShell` API
  (§02.7.0, part of `pkg/bob`), the CLI config file
  that lists extra substituters and trusted keys (`store.Config.Substituters`).
- §03: ecosystem API packages and generators, their `bob.lock` sections, bob-builder ecosystem
  ops, generated native files and drift detection, materialisation manifest (paths excluded from
  source hashing), `cc.Linkable = bob.Linkable` alias.
- §04: cross toolchain selection, per-kind projection tables, OCI/AppImage outputs, size budgets
  of bundles.
- §05: worker Nix profile (root worker, build users, per-job container, D56) built on
  `store.Config`; evaluator sandbox limits on workers; fetch egress policy on workers.
- §06: pipeline trigger semantics on the server, using `bob.Pipeline` registration.

---

## 1.1 Repository layout and layering

#### R-ARCH-001 Monorepo layout
- **Statement:** One Go module `github.com/megakuul/bob` with top-level `pkg/`, `cmd/`,
  `internal/`, `proto/`, `web/`. `pkg/` has exactly the packages in the table of R-ARCH-003
  (new packages are added to that table in the same change).
- **Acceptance:** Given the repo, when `go list ./...` runs, then every package path starts with
  one of `pkg/`, `cmd/`, `internal/`, `proto/` (generated), and `go.mod`'s module line is
  `module github.com/megakuul/bob`.
- **Source:** R-ARCH-001.

#### R-ARCH-002 Framework never imports server code
- **Statement:** No package under `pkg/` and no `cmd/bob` package depends (transitively) on
  `internal/…`, generated `proto/…` server packages, or `cmd/bob-server`.
- **Acceptance:** `TestLayering` (in `pkg/layering_test.go`) runs
  `go list -deps -f '{{.ImportPath}}' ./pkg/... ./cmd/bob ./cmd/bob-builder` and fails if any
  result matches `^github.com/megakuul/bob/(internal|proto|cmd/bob-server)(/|$)`. CI runs it.
- **Source:** R-ARCH-002.

#### R-ARCH-003 Layers
- **Statement:** `pkg/` is layered; a package may import only packages of its own or a lower
  layer, plus the standard library and the third-party modules listed per layer.

| Layer | Packages | May import (besides std) | Runs inside the evaluator? |
|---|---|---|---|
| L0 core API | `pkg/bob`, `pkg/bob/ext` | — | yes |
| L1 sources | `pkg/nixpkgs` | L0 | yes |
| L2 ecosystem APIs | `pkg/cc`, `pkg/golang`, `pkg/rust`, `pkg/npm`, `pkg/ocaml`, codegen `pkg/proto`, `pkg/connect`, `pkg/graphql`, `pkg/openapi` (§03), `pkg/services/...` | L0, L1, other L2 APIs (acyclic) | yes |
| L3 output APIs | `pkg/output/oci`, `pkg/output/appimage`, `pkg/output/tarball`, `pkg/release` (§04) | L0–L2 | yes |
| E0 engine primitives | `pkg/engine/drv`, `pkg/engine/nar`, `pkg/engine/ir` | L0 | **no** |
| E1 engine services | `pkg/engine/store`, `pkg/engine/fetch`, `pkg/engine/lock`, `pkg/engine/nixidx`, `pkg/engine/srcs` | E0, L0; `golang.org/x/sys`, `github.com/klauspost/compress`, `github.com/ulikunitz/xz` | no |
| E2 generators | `pkg/<eco>/gen` (one per L2/L3 package, implements `engine.Ecosystem`), `pkg/engine` (registry, types) | E0, E1, L0–L3 types | no |
| E3 orchestration | `pkg/engine/eval`, `pkg/engine/build`, `pkg/engine/translate` | E0–E2, L0 | no |
| builder | `pkg/builder`, `pkg/builder/bspec`, `pkg/<eco>/bop` | E0, `bspec` | no (runs in Nix builds) |
| glue | `cmd/bob`, `pkg/cli` (§02) | everything in `pkg/` | no |

- **Acceptance:** `TestLayering` encodes the table and fails on any import of a higher layer, any
  L0–L3 package importing `pkg/engine/...` or `pkg/builder/...` (this keeps the evaluator small
  and free of engine code), any non-listed third-party module, and any L2 import cycle.
- **Source:** R-ARCH-003, ADR 0001 Consequences, S13 §5.4 (IR → drv is trusted code).

#### R-ARCH-004 Two binaries
- **Statement:** `cmd/bob` builds `bob`; `cmd/bob-server` builds `bob-server`. A third binary,
  `cmd/bob-builder`, is not installed by anyone: it is embedded in `bob` and lives in the Nix
  store (§1.12).
- **Acceptance:** `go list -deps ./cmd/bob` contains no `internal/` package (R-ARCH-002 test);
  `bob --help` lists no server command.
- **Source:** R-ARCH-004, D36.

#### R-ARCH-005 Self-hosting and bootstrap
- **Statement:** bob's own `bob.go` files build `bob`, `bob-builder` and `bob-server`. Bootstrap:
  `go build ./cmd/bob-builder` then `BOB_BOOTSTRAP_BUILDER=<path> go build -tags bootstrap ./cmd/bob`
  produces a first `bob` that embeds that builder and a static Nix fetched by
  `scripts/fetch-nix.sh` (pinned URL + sha256 of nixpkgs `nixStatic` at the pin). That `bob` then
  runs `bob build //cmd/bob:bob`.
- **Acceptance:** Given a clean checkout and a machine with Go ≥ the go.mod version, when
  `scripts/bootstrap.sh` runs, then it ends with a `bob` built by bob whose embedded builder
  store path equals the one in `pkg/engine/store/builders.go` (bit-reproducible builder).
- **Source:** R-ARCH-005, D17, ADR 0001 Consequences.

---

## 1.2 `bob.go` convention (ADR 0003)

#### R-FW-001 Typed Go, gopls-visible
- **Statement:** Build definitions are Go files that compile with the standard toolchain under
  `-tags=bob` and are type-checked by gopls through the `.bob/bin/gopls` shim (which sets
  `GOFLAGS=-tags=bob`) and editor settings `build.buildFlags=["-tags=bob"]` (§02).
- **Acceptance:** The S1 LSP script (`bobtest/lsp/acme.steps`: diagnostics, completion `foo.` →
  `Lib`, definition `foo.Lib` → `libs/foo/bob.go:12`, type error on wrong handle type) passes
  against the `examples/acme` repo with gopls from the pin, both directly and through the shim
  as a non-root user in bind and pivot mode.
- **Source:** R-FW-001, ADR 0003, S1 §3.2–3.3, D59.

#### R-FW-002 File convention and rules
- **Statement:** Definitions live in files named exactly `bob.go`, at most one per directory, next
  to the code they describe. Rules R1–R7 below are checked before compiling; all violations are
  reported together, sorted by path; any violation stops evaluation (exit class *definition*).

| Rule | Check | Error text (first line) |
|---|---|---|
| R1 | line 1 is exactly `//go:build bob` | `<dir>/bob.go: the first line must be "//go:build bob" so that go build, go vet and go test ignore it` |
| R2 | package clause = package of the directory's non-test `.go` files; without Go files: dir name lowercased, chars outside `[a-z0-9_]` removed, prefixed `x` if empty or starting with a digit | `<dir>/bob.go: package <x>, but the Go code in this directory is package <y>; bob.go must use package <y>` |
| R3 | no `bob.go` imports a `bob.go` package whose directory is `package main` | `<dir>/bob.go: imports the build definitions of <maindir>/, which is a main package and cannot be imported; define the shared package in a bob.go of a non-main directory` |
| R4 | no top-level identifier of `bob.go` is declared in another file of the package | `<dir>/bob.go:<line>: <Name> is also declared at <file>:<line>; bob.go shares its package with that code, rename one of them` |
| R5 | dir has `.c .cc .cpp .cxx .m .f .F .f90 .swig .swigcxx` files next to `bob.go` | not an error: bob materialises `<dir>/zz_bob_cgo.go` (gitignored): `//go:build bob && cgo` / `package <x>` / `import "C"` |
| R6 | no import cycle that exists only under the tag (bob.go import graph ∪ real Go import graph) | `<dir>/bob.go imports <dir2>, whose Go code imports <dir> (cycle exists only for build definitions); move the definition` |
| R7 | `bob.go` imports no `github.com/megakuul/bob/pkg/engine/...`, `.../pkg/builder/...`, `.../pkg/cli/...`, and no package outside the std library, the framework L0–L3 packages, and `bob.go` packages (own repo or bob-repo Go modules) | `<dir>/bob.go: imports <path>, which build definitions cannot use; bob.go may import the bob framework, the standard library and other bob.go packages` |

- Repo root = the nearest ancestor directory that contains `bob.lock` (§02 `FindRoot`, error
  0002); the bob-generated root `go.mod` lives in the same directory.
- Scan: from the repo root, skipping directories whose name
  starts with `.` or `_`, `node_modules`, `testdata`, and directories containing their own
  `go.mod`. No other file in the repo or the framework is named `bob.go` (framework: `core.go`).
- **Acceptance:** Fixture `bobtest/fixtures/rules/` has one sub-repo per rule; for each,
  `bob build //...` exits 3 and stderr contains exactly the rule's text with paths filled in; R5
  produces the stub and gopls (CGO_ENABLED=1) reports no diagnostics.
- **Source:** R-FW-002, D22, ADR 0003 Decision 6, S1 §5.6, A14.

#### R-FW-043 Evaluator generation
- **Statement:** bob builds one evaluator binary per repo state as specified in S1 §5.3, with
  these exact rules:
  1. Eval key = sha256 over: sorted (path, content) of every `bob.go`, `go.mod`, `go.sum`; bob
     version; Go version; `GOARCH` of the host. Key equal to `.bob/eval/key` → reuse
     `.bob/eval/bob-eval`. A size+mtime index (`.bob/eval/index.json`) skips re-reading
     unchanged files; content is re-hashed when size or mtime differ.
  2. Overlay `.bob/eval/overlay.json` hides every other compilable file in a `bob.go` directory;
     `package main` `bob.go` files are replaced by a copy with the clause renamed to
     `bobmain<n>` on the same line; the virtual main `<root>/_bobeval/main.go` blank-imports all
     `bob.go` packages sorted by directory and calls `bob.Main()`.
  3. Dependency bob repos: their `go.mod` + `bob.go` files are copied to
     `.bob/eval/mods/<escaped module path>@<version>/` and swapped in through
     `.bob/eval/go.mod` (`replace` lines).
  4. The framework itself: `require github.com/megakuul/bob <bob's own version>` is written by
     bob into the generated root `go.mod`, and bob embeds the framework L0–L3 module zip of its
     own version, extracted into its module cache with the matching `go.sum` line. A dependency
     bob repo requiring a newer framework than the running bob → error
     `<module> needs bob <v> or newer; this is bob <v'>` + hint `update bob`.
  5. Compile: `GOWORK=off CGO_ENABLED=0 GOFLAGS= GOPROXY=off GOTOOLCHAIN=local go build -tags=bob
     -trimpath -modfile=<…> -overlay=<…> -o .bob/eval/bob-eval ./_bobeval` with the pinned Go,
     inside the evaluator sandbox (R-FW-045). Compiler messages naming
     `.bob/eval/rename/<n>.go` are rewritten to the original `bob.go` path.
  6. Run: `bob-eval` writes IR JSON (§1.6) to fd 3, diagnostics to stderr.
- **Acceptance:** (a) Two copies of `examples/acme` at different paths give byte-identical IR.
  (b) A real Go file in a `bob.go` directory that does not compile (`var X = generatedByBob`)
  does not affect evaluation. (c) A type error in `cmd/api/bob.go` (main package) is reported as
  `cmd/api/bob.go:<line>:<col>: …`. (d) Unchanged repo: second evaluation does not run
  `go build` (asserted via `--json` event `eval.cached=true`).
- **Source:** R-FW-001/002/008, ADR 0003 Decision 4, S1 §5.3.

#### R-FW-044 Evaluator ↔ bob version lock
- **Statement:** The IR carries `bobVersion` and `irVersion`; bob rejects IR whose `bobVersion`
  differs from its own (internal error; cannot happen unless rule R-FW-043.4 is broken).
- **Acceptance:** Unit test feeds IR with another `bobVersion` → error class internal, exit 70.
- **Source:** R-FW-008, BOOTSTRAP §5.4 (hostile input).

#### R-FW-045 Evaluator sandbox (local and worker)
- **Statement:** The evaluator (compile and run) always runs in the S13 evalbox (user, mount,
  net, pid, ipc, uts, cgroup namespaces; read-only binds; nested userns uid 1000; no_new_privs;
  Landlock; seccomp) with the S13 §5.2 contract. Locally, cgroup limits apply only when a
  delegated cgroup is available; wall-clock timeout and output caps always apply. Worker limits
  are §05's.
- **Local defaults [proposal]:** wall clock 120 s, IR output 64 MiB, stderr kept 1 MiB, pids 512,
  memory 2 GiB (enforced always, by RLIMIT_AS or a polling watchdog, and by the cgroup when delegated).
- **Acceptance:** the 15 S13 hostile cases (`bobtest/evalbox/`) each fail with their S13 error
  text locally without a delegated cgroup (except the three cgroup-only cases, which are skipped
  with a note), and all 15 on a machine with delegation.
- **Source:** R-SEC-000/004, S13 §5.1–5.3, S13-4.

#### R-FW-008 Determinism
- **Statement:** The evaluator has no access to time, environment, network, or files other than
  the read-only source tree; the framework API offers no such access; IR is canonicalised (§1.6).
  `bob check --determinism` evaluates twice in separate sandboxes and compares IR bytes.
- **Acceptance:** (a) `bob check --determinism` on every example repo passes. (b) A fixture whose
  `bob.go` builds a deps slice by ranging over a map with ≥ 8 keys makes `bob check
  --determinism` fail within 5 runs with BOB-E-1018 `✗ evaluating bob.go is not deterministic: <label>
  differs between two runs (field <json path>)` + hint `do not range over maps when building
  lists; sort the keys`.
- **Source:** R-FW-008.

---

## 1.3 Labels

#### R-FW-015 Label grammar
- **Statement:** Labels use this grammar (ABNF-like):

```
label      = local / foreign / nixlabel
local      = "//" [dir] ":" name
foreign    = modpath "//" [dir] ":" name
nixlabel   = "nixpkgs:" attr
dir        = seg *("/" seg)          ; seg = 1*( ALPHA / DIGIT / "_" / "-" / "." ), not "." or ".."
name       = (lower / DIGIT) *127(lower / DIGIT / "_" / "-" / ".")
attr       = 1*( ALPHA / DIGIT / "_" / "-" / "'" / "." )
configured = label "@" [platform] *("+" token)    ; §1.9
pattern    = "//..." / "//" dir "/..." / "//" [dir] ":all" / label / "//" dir   ; "//a/b" = "//a/b:b"
```

- **Bounds (hostile input):** the whole string is ≤ 1024 bytes and one `seg` ≤ 255 bytes; longer
  is invalid (a 1000-byte `dir` is valid, a 5000-byte one is not).
- `<dir>` is the directory of the `bob.go` that made the outermost constructor call (first stack
  frame outside `github.com/megakuul/bob/`); `<name>` is the constructor's name argument.
- CLI relative forms (`:name`, `./...`, `.`) resolve against the current directory (§02).
- Duplicate label → `<pos>: package <label> is already defined at <pos2>`.
- **Acceptance:** Table test `pkg/bob/label_test.go`: ≥ 30 valid and invalid strings incl.
  `//:x`, `//a/b`, `example.com/o//libs/bar:bar`, `nixpkgs:python3Packages.numpy`,
  `//api:api@linux-amd64+debug+asan`, `//api:API` (invalid), `//a/../b:x` (invalid),
  `//a:b+c` (invalid). `Parse` accepts all the forms of the table (`//a/b`, `//api:api@linux-amd64+debug+asan` are valid
  for `Parse`; `ParseLabel` accepts labels only). Round trip `Parse(s).String()==s` for canonical
  inputs. A 1000-byte dir is valid, a 5000-byte dir is invalid.
- **Source:** R-FW-015, ADR 0003 Decision 5, ADR 0009 Decision 5, S1 §5.4.

#### R-FW-016 Source positions
- **Statement:** Every graph node carries `definedAt` = module-relative file and line of the
  outermost constructor call in a `bob.go` (with `-trimpath`: `<module>[@<version>]/<dir>/bob.go`);
  bob displays own-repo positions repo-relative (`cmd/api/bob.go:15`).
- **Acceptance:** For each node in `examples/acme`'s IR, `definedAt` points at the line with the
  constructor call (golden file). `definedAt` is the line of the call to the ecosystem
  constructor (the first stack frame outside `github.com/megakuul/bob/`); in `libs/foo/bob.go`
  that is line 13 (line 12 is the `var Lib = bob.Define(` line used by R-FW-001). A failing C++
  build of `//libs/foo:foo` prints `(defined at libs/foo/bob.go:13)`.
- **Source:** R-FW-016, R-NIX-012.

---

## 1.4 Core API (`pkg/bob`)

Everything below is the complete public surface of L0. Doc comments are normative.

```go
// Package bob is the core of bob's build definitions. It is imported by bob.go files.
package bob

// P is the configuration a constructor builds for: one platform and one variant.
// bob creates one P per requested configuration; constructors must not keep it.
type P struct{ /* unexported */ }

// Platform returns the platform packages built with p run on.
func (p *P) Platform() Platform
// Variant returns the variant of p (profile, sanitisers, features, named variants).
func (p *P) Variant() Variant
// Host returns the configuration for build-time tools of p: the host platform, release
// profile, no sanitisers, no features. Host().Host() == Host().
func (p *P) Host() *P

// Define turns a constructor into a memoised, registered constructor. It must be called in a
// package-level variable declaration of a bob.go file. The returned function calls fn at most
// once per P and returns the same value on every later call with that P (R-FW-011).
// Every value reachable from fn's result that implements Package becomes part of the graph.
func Define[T any](fn func(p *P) T) func(p *P) T

// Main is called by the generated evaluator. bob.go files never call it.
func Main()

// Package is a node of the build graph. It is implemented only by types that embed Node,
// and a Node is created only by ext.New; the unexported method seals the interface.
type Package interface {
	Label() Label
	DefinedAt() Position
	// Outputs lists the package's store outputs ("out" first, then e.g. "dev", "lib").
	Outputs() []Output
	bobNode() *Node
}

// Node is embedded by every ecosystem package type. Its fields are unexported; ext.New
// creates it (through an internal hook in pkg/bob/internal/graph, which avoids an import cycle).
type Node struct{ /* unexported */ }
func (n *Node) Label() Label
func (n *Node) DefinedAt() Position
func (n *Node) Outputs() []Output

// Output names one store output of a package, e.g. the "dev" output of a library.
type Output struct {
	Package Package
	Name    string // "out", "dev", "lib", "debug"
}

// Tree is a package whose "out" output is a directory tree that other steps can read.
type Tree interface{ Package; tree() }

// Tool is a package that provides executables in bin/ for build steps and dev shells.
type Tool interface{ Package; tool() }

// Executable is a package with one main program, e.g. a Go or Rust binary.
// MainProgram is a path relative to the "out" output, e.g. "bin/api".
type Executable interface{ Tool; MainProgram() string }

// Linkable is a native (C ABI) library. cc.Linkable is an alias of it (§03).
type Linkable interface{ Package; LinkInfo() LinkInfo }

// LinkInfo is what a Linkable gives its consumers (cgo, Rust Native, OCaml Native, cc Deps).
// Paths are store paths; they are filled by bob's generators, not by the evaluator
// (the evaluator only records the Linkable's identity and Modules/Libs choices).
type LinkInfo struct {
	IncludeDirs []string   // -I
	LinkDirs    []string   // -L
	Libs        []string   // -l names, in link order
	Archives    []string   // static archives, absolute paths
	LinkArgs    []string   // extra linker arguments
	RuntimeDirs []string   // RUNPATH entries
	PkgConfig   []string   // pkg-config dirs (PKG_CONFIG_PATH)
	Public      []Linkable // transitive public set, needed at the final link
}

// Label identifies a package, e.g. //libs/foo:foo (see SPEC §1.3).
type Label struct{ Module, Dir, Name string; Nix string }
// ParseLabel accepts labels only (local, foreign, nixlabel).
func ParseLabel(s string) (Label, error)
func (l Label) String() string

// Parse accepts the union of label, pattern and configured forms (R-FW-015) and returns a typed
// value: a Label, a Pattern or a Configured label. Use ParseLabel when only a label is valid.
func Parse(s string) (Ref, error)

// Ref is the typed result of Parse: a Label, a Pattern or a Configured value; String() returns
// the canonical form.
type Ref interface{ String() string }

// Pattern is a label pattern ("//...", "//dir/...", "//dir:all", a label, "//dir"); a string type
// so that fixtures can write []bob.Pattern{"//..."}.
type Pattern string

// Position is a file and line in a bob.go file, module-relative.
type Position struct{ File string; Line int }
func (p Position) String() string // "libs/foo/bob.go:12"
```

#### R-FW-010 Values, not strings
- **Statement:** Packages are passed as typed values; the only strings naming packages are the
  constructor's own name argument and nixpkgs attribute names (R-NIX-013).
- **Acceptance:** `bobtest/typecheck/`: passing `golang.Binary` where `bob.Linkable` is expected
  fails `go vet -tags=bob` with `missing method LinkInfo`; the example programs (R-FW-013)
  contain no label string literals except constructor names.
- **Source:** R-FW-010.

#### R-FW-011 Memoised constructors
- **Statement:** `Define` memoises per (definition, `*P`). Calling a defined constructor outside
  evaluation (e.g. in `init`) panics with `bob: <pos>: <Var> was called before evaluation; call
  it only inside another bob.Define function`. Calling `Define` inside a constructor →
  definition error `<pos>: bob.Define must be used in a package-level var declaration`.
  Recursive calls during their own construction → definition error 1006 (cycle) naming the chain.
- **Example:**

```go
//go:build bob

package foo

import (
	"github.com/megakuul/bob/pkg/bob"
	"github.com/megakuul/bob/pkg/cc"
	"github.com/megakuul/bob/pkg/nixpkgs"
)

// Lib is libfoo.so (label //libs/foo:foo).
var Lib = bob.Define(func(p *bob.P) *cc.Library {
	return cc.SharedLibrary(p, "foo", cc.LibraryOpts{
		Srcs: bob.Glob("src/*.cpp"),
		Hdrs: bob.Dir("include"),
		Deps: []bob.Linkable{nixpkgs.Lib(p, "openssl")},
	})
})
```

- **Acceptance:** Unit tests: `Lib(p) == Lib(p)` (same pointer); two different `P` give two
  values; the three error cases print their texts.
- **Source:** R-FW-011, D23, S1 §5.5.

#### R-FW-012 / R-FW-014 API style and one way per task
- **Statement:** Constructors take `(p *bob.P, name string, opts <Kind>Opts)`; options are plain
  structs with zero-value defaults; builder chaining is used only for modifiers that refine an
  existing value (`rust.Crate("openssl-sys", "0.9").Uses(…)`). Each ecosystem offers exactly one
  constructor per kind. Every exported constructor has a doc comment and a runnable example
  (`Example<Func>` test in `_test.go` under `-tags=bob`).
- **Acceptance:** `TestAPISurface` lists exported functions of L0–L3 and fails if a constructor
  lacks an `Example` test or if two exported functions return the same kind type from the same
  package (manual review of the list at Gate S).
- **Source:** R-FW-012, R-FW-014, D24, R-FW-013.

#### R-FW-013 Example programs
- **Statement:** `examples/` holds six repos: `cpp-lib`, `go-cgo-service`, `rust-crate`,
  `svelte-frontend`, `polyglot` (= S0a fixture: C++ + openssl, Rust staticlib, protobuf, cgo Go),
  `cross-repo` (consumer + a served bob module). They are the API acceptance tests.
- **Acceptance:** CI: for each, `bob build //...` and `bob test //...` exit 0 on both profiles;
  `polyglot`'s `//cmd/api:api` prints `digest=81b637d8… numhex=3432 proto_bytes=116`.
  Readability review (manual) at Gate S.
- **Source:** R-FW-013.

### 1.4.1 Sources

```go
// Sources is a set of files from the repo, relative to the directory of the calling bob.go.
type Sources struct{ /* unexported */ }

// Glob selects files by patterns: "*" and "?" do not cross "/", "**" matches any depth,
// "[...]" classes as in path.Match. A pattern starting with "!" excludes.
//   bob.Glob("src/**/*.cpp", "!src/legacy/**")
func Glob(patterns ...string) Sources
// Files selects exact files (no patterns).  bob.Files("foo_test.cpp")
func Files(paths ...string) Sources
// Dir selects a whole directory subtree.  bob.Dir("include")
func Dir(path string) Sources
// Union combines sources.
func (s Sources) Union(o ...Sources) Sources

// FetchOpts declares one external file or archive [proposal, added at integration].
type FetchOpts struct {
	URL     string   // https (http only with a pinned hash)
	Mirrors []string // tried after URL, in order
	Archive bool     // unpack (formats of R-NIX-014); false = flat file
	Strip   int      // leading path components to drop (Archive)
	Name    string   // store name; default from the URL's base name via drv.Name
}

// Fetch declares an external input pinned in bob.lock "sources" (§1.8) and fetched by bob's
// Fetcher (§1.11), never by a Nix FOD. Its "out" is the file or the unpacked tree.
//
//	var Fonts = bob.Define(func(p *bob.P) bob.Tree {
//		return bob.Fetch(p, bob.FetchOpts{URL: "https://example.org/fonts-1.2.tar.gz", Archive: true, Strip: 1})
//	})
func Fetch(p *P, o FetchOpts) Tree
```

#### R-FW-021 Source hashing
- **Statement:** A `Sources` value resolves (outside the evaluator, by trusted bob code) to the sorted set
  of regular files and symlinks matching it, always excluding: `.git/`, `.bob/`, every `bob.go`
  and `zz_bob_cgo.go`, files ignored by any `.gitignore` on the path (gitignore semantics
  implemented in Go, no git binary), `.git/info/exclude` patterns, and paths in the
  materialisation manifest (§03). Each package's source set becomes one content-addressed store
  path named `<store-name>-src` whose tree keeps repo-relative paths (§1.11).
- Errors: pattern escaping the module root (`..`, absolute) → `<pos>: source pattern "<p>" leaves
  the repository`; a pattern that matches nothing → `<pos>: bob.Glob("<p>") matches no files`;
  a symlink pointing outside the repo → `<file>: symlink points outside the repository`.
- **Acceptance:** (a) Adding a gitignored file or editing `bob.go` changes no source store path.
  (b) Changing one `.cpp` file changes exactly the source path of the package(s) whose `Sources`
  contains it. (c) File modes other than the executable bit and mtimes do not change the path.
- **Source:** R-FW-021, S6 (materialisation), gen-b §4.

### 1.4.2 Hermetic custom step (`bob.Run`)

```go
// RunOpts declares a hermetic step. Everything the command may read is declared here.
type RunOpts struct {
	Tools   []Tool            // on PATH, in order (nixpkgs.Tool or bob-built tools)
	Srcs    Sources           // copied writable to $BOB_SRC (cwd)
	Inputs  map[string]Tree   // read-only at $BOB_IN_<NAME> (NAME = key upper-cased, [A-Z0-9_])
	Cmd     []string          // argv; no shell. ${VAR} expands from the step's environment only
	Env     map[string]string // extra environment
	Outputs []string          // paths relative to $BOB_OUT that must exist afterwards
	Timeout time.Duration     // 0 = 30 min
}

// Run creates a sandboxed, cached build step. Its output tree is the Step (a Tree).
//   gen := bob.Run(p, "version-file", bob.RunOpts{
//       Tools:   []bob.Tool{nixpkgs.Tool(p, "coreutils")},
//       Cmd:     []string{"sh", "-c", "…"}, // only if a shell is declared as a Tool
//       Outputs: []string{"version.txt"},
//   })
func Run(p *P, name string, o RunOpts) *Step

// Step is the result of Run; it implements Tree. File returns one declared output as a Tree.
type Step struct{ /* unexported */ }
func (s *Step) File(path string) Tree
```

#### R-FW-023 `bob.Run`
- **Statement:** `Run` becomes one derivation built by bob-builder op `exec`. Inside: `PATH` = the
  declared tools' `bin/` only; `HOME=/homeless-shelter`; `TMPDIR=/build/tmp`;
  `BOB_SRC=/build/src` (cwd), `BOB_OUT=$out`, `SOURCE_DATE_EPOCH=1`, `TZ=UTC`, `LC_ALL=C`; no
  network. A missing declared output → build error `<label>: step finished but did not create
  <path>`; undeclared files in `$out` are kept. There is no API that accepts Nix code.
- **Acceptance:** (a) A step running `curl` fails with the network error template (2009). (b) A
  step reading `/home` sees nothing. (c) The same step on two machines gives the same output path
  and NarHash. (d) Grep of `pkg/` public API for parameters named or typed `Nix…Expr` finds none.
- **Source:** R-FW-023, D27.

### 1.4.3 Tests and services

#### R-FW-024 Tests are packages
- **Statement:** Every ecosystem's `Test` constructor returns a `bob.Test` (`Package`). Running a
  test = building its derivation; the builder exits non-zero on failure (error 2005), so only
  passing results are cached. A passing test's output holds `test-result.json` (§03 `TestResult`
  v1) and `test.log`.
  `bob test --rerun` forces a rebuild (`nix build --rebuild`) of selected tests.
- **Acceptance:** Second `bob test //...` with no change runs no builder (cache hit, ≤ the
  no-op budget); a failing test reruns on every invocation.
- **Source:** R-FW-024.

#### R-FW-025 Loopback services
- **Statement:** API as S13 §5.5 with `*bob.P` (not `*bob.Project`):

```go
type Service interface{ serviceSpec() *ServiceSpec } // sealed
type ServiceSpec struct {
	Name    string            // "postgres"; used in errors and logs
	Start   []string          // argv inside the test sandbox
	Tools   []Tool
	Ready   Probe             // TCPReady(port) | CmdReady(argv...)
	Env     map[string]string // exported to the test
	Timeout time.Duration     // readiness timeout, default 30 s
}
func TCPReady(port int) Probe
func CmdReady(argv ...string) Probe
type ServiceOptions struct{ Args []string; Env map[string]string; Ready Probe; Timeout time.Duration }
// ServiceFrom runs a binary built in this repo as a service of a test.
func ServiceFrom(bin Tool, o ServiceOptions) Service
```

  Instance *i* (list order, from 0) binds `127.0.0.(i+2)` and exports `<NAME>_ADDR`; startup in
  list order, the test starts after every `Ready` passes; teardown SIGTERM, 5 s, then pid
  namespace end. `pkg/services/postgres` is the v1 reference service.
- **Acceptance:** S13 tests: Postgres test passes ≤ 3 s [8c/15G] (measured 1.3–2.0 s); two such
  tests in parallel both pass; 0 service processes remain; a never-ready service fails with
  `<label>: service <name> did not become ready within 30s` + last 20 log lines.
- **Source:** R-FW-025, S13 §3.4/§5.5.

### 1.4.4 Repository settings and pipelines (registration only)

```go
// Repo declares repository-wide settings. It is allowed only in the root bob.go, at most once.
//   var _ = bob.Repo(bob.RepoOpts{Nixpkgs: "nixos-26.05"})
func Repo(o RepoOpts) struct{}
type RepoOpts struct {
	// Nixpkgs is a channel name ("nixos-26.05") or "<channel>@<40-hex rev>". bob.lock pins the
	// revision; `bob lock --update nixpkgs` moves it to the channel's current revision.
	Nixpkgs string
	Variants []Named // see §1.9
}

// Pipeline registers a pipeline (R-FW-040). Trigger semantics on the server are SPEC §06.
func Pipeline(name string, o PipelineOpts) struct{}

// PipelineOpts is typed; the zero value is valid and means a manual-only pipeline. It maps
// 1:1 onto proto `Pipeline` (§6.5.6): triggers (human form), concurrency, releases.
type PipelineOpts struct {
	Triggers    []Trigger   // none = manual runs only
	Concurrency Concurrency // unset = the per-ref default of §05.6.2
	Targets     []Pattern   // packages to build
	Tests       []Pattern   // packages to test
	Releases    []string    // release names to publish; non-empty = release pipeline (proto releases=true)
}

// Trigger says when a pipeline starts.
type Trigger struct{ /* unexported */ }
func OnPush(branchGlob string) Trigger // human form "push <glob>"
func OnPullRequest() Trigger           // "pull_request"
func OnTag(glob string) Trigger        // "tag <glob>"

// Concurrency says what happens to a run of the same pipeline and ref while one is active.
type Concurrency int
const ( Queue Concurrency = iota + 1; Supersede; Parallel ) // zero value = per-ref default
```

- Repo errors: `Repo` outside the root `bob.go` or twice → definition error; missing →
  default `Nixpkgs` = the channel bob was released with (recorded in bob's build info).

---

## 1.5 Ecosystem extension API (`pkg/bob/ext`)

#### R-FW-046 Node creation contract
- **Statement:** Ecosystem API packages create graph nodes only through `ext.New`; the node's
  payload is a JSON-serialisable struct registered per kind. A kind declares the variant axes it
  reads; `ext.Config` exposes only those (R-FW-031 projection is structural).

```go
package ext

// Kind describes one node kind, e.g. "cc.library". Registered in an init() of the API package.
type Kind struct {
	Name   string  // "<ecosystem>.<kind>", [a-z0-9.]+
	Axes   []Axis  // variant axes this kind's derivation depends on
	Host   bool    // always built for p.Host() (codegen tools, proc-macros)
}
type Axis int
const ( AxisPlatform Axis = iota; AxisProfile; AxisSanitizers; AxisGoRace; AxisFeatures )
func Register(k Kind)

// Config is the projected configuration a node sees.
type Config struct{ /* unexported */ }
func (c Config) Platform() bob.Platform   // panics if AxisPlatform not declared
func (c Config) Profile() bob.Profile     // panics if AxisProfile not declared
func (c Config) Sanitizers() []bob.Sanitizer
func (c Config) Race() bool
func (c Config) Features(l bob.Label) []string

// New creates a node of kind for p. name is the label name; deps are the node's direct
// package dependencies (order is kept); spec is the kind's payload, serialised into the IR.
func New(p *bob.P, kind, name string, deps []bob.Package, srcs []bob.Sources, spec any) bob.Node
// ConfigOf returns the projected configuration of a node.
func ConfigOf(n *bob.Node) Config
```

- **Acceptance:** A unit test registers a kind with `Axes={AxisPlatform}`; its constructor
  calling `ext.ConfigOf(n).Profile()` panics with `ext: kind <k> reads the profile but did not declare
  AxisProfile`; two P differing only in profile produce IR nodes with equal `config` projections
  and equal derivation paths.
- The per-kind axis table is §04 R-FW-038; this entry makes it structural.
- **Source:** R-FW-031, R-FW-038 (§04), ADR 0009 Decision 4, R-LANG §7.7.

---

## 1.6 Graph IR v1

#### R-FW-047 IR format
- **Statement:** The evaluator writes one JSON document (UTF-8, canonical: keys sorted, no
  insignificant whitespace, nodes sorted by `id`) to fd 3. Schema:

| Field | Type | Meaning |
|---|---|---|
| `irVersion` | int | `1` |
| `bobVersion` | string | must equal the running bob's version |
| `module` | string | root Go module path |
| `repo` | object | `{"nixpkgs": "<RepoOpts.Nixpkgs>", "variants": [...]}` |
| `configs` | array | `{"id", "platform", "profile", "sanitizers":[], "race":bool, "features":{label:[...]}, "named":[...]}` |
| `nodes[]` | array | one per (package, projected config) |
| `nodes[].id` | string | canonical configured label (§1.9), unique |
| `nodes[].kind` | string | registered kind name |
| `nodes[].label`, `.definedAt` | string, `{file,line}` | |
| `nodes[].proj` | object | only the declared axes' values |
| `nodes[].deps` | array of ids | order kept |
| `nodes[].srcs` | array | `{"dir": "<bob.go dir>", "patterns": [...]}` (resolved by bob, not by the evaluator) |
| `nodes[].spec` | object | kind payload (§03/§04 schemas) |
| `nodes[].nix` | object | for `nixpkgs:` nodes: `{"attr","set":"host|target|cross","outputs":[...],"allow":{"insecure":[...],"unfree":bool}}` |
| `roots` | array of ids | packages requested by the CLI patterns |
| `pipelines` | array | §06 |

- bob validates before use: size ≤ 64 MiB, ≤ 1,000,000 nodes, every dep id exists, no cycles
  (error 1006 with the chain and each `definedAt`), unknown kinds rejected, every string ≤ 64 KiB. The node cap shares the IR-invalid error of the
  field validation: a rejection with any structured IR-invalid error satisfies the cap test.
- **Acceptance:** JSON-schema file `pkg/engine/ir/ir.schema.json`; fuzz test of the IR parser
  (no panic, bounded memory); golden IR for `examples/polyglot`.
- **Source:** R-FW-008, R-SEC-004, gen-b §1, S13 §5.2.

### 1.6.1 Ecosystem registration (engine side)

#### R-FW-051 `engine.Ecosystem`
- **Statement:** Each ecosystem registers one implementation from its generator package
  (`pkg/<eco>/gen`, init) with `engine.Register(e Ecosystem)`; the interface is §03.0.1's, with
  these request/result types (in `pkg/engine`, never linked into the evaluator):

```go
type Ecosystem interface {
	Name() string                                                    // bob.lock section key
	Lock(ctx context.Context, req LockRequest) (json.RawMessage, error)
	Plan(ctx context.Context, req PlanRequest) (PlanResult, error)
	Native(ctx context.Context, req PlanRequest) ([]GeneratedFile, error)
}
type LockRequest struct {
	Graph   *ir.Graph         // validated IR (all configurations)
	Prev    json.RawMessage   // previous section, nil if none
	Update  []string          // names to move (bob lock --update), empty = keep pins
	Fetch   fetch.Fetcher
	Nixpkgs *nixidx.Index
}
type PlanRequest struct {
	Graph   *ir.Graph
	Nodes   []*ir.Node          // this ecosystem's nodes (by kind prefix "<name>.")
	Lock    json.RawMessage     // this ecosystem's section
	Inputs  map[string]Resolved // node id → resolved deps: drv paths/outputs, store paths, LinkInfo
	Sources map[string]string   // node id → imported source store path (R-NIX-009)
	Nixpkgs *nixidx.Index
	Builder string              // bob-builder store path for the host system
}
type PlanResult struct {
	Drvs    []*drv.Derivation        // new derivations, any order (bob sorts topologically)
	Nodes   map[string]Resolved      // node id → its outputs and, for Linkables, filled LinkInfo
	Fetches []fetch.Request          // external inputs; their paths are computed from the lock
	                                 // hashes (drv.FixedOutputPath), the fetcher makes them valid before nix build
}
type Resolved struct {
	Drv      string            // drv path ("" for plain store paths)
	Outputs  map[string]string // output name → store path
	LinkInfo *bob.LinkInfo
	MainProgram string
}
```

  bob calls `Plan` per ecosystem in dependency order of the ecosystems' nodes (a node's deps of
  other ecosystems are resolved first); `Plan` must be deterministic and run no processes.
- **Acceptance:** a test ecosystem `bobtest/eco/toy` registered from its own package builds a
  two-node graph with no change to `pkg/engine` (self-contained ecosystems, §7.7).
- **Source:** §03.0.1, R-ARCH-003, REQUIREMENTS §7.7.

---

## 1.7 nixpkgs as package source (`pkg/nixpkgs`)

```go
package nixpkgs

// Tool is a nixpkgs package used at build time (always from the host package set).
//   nixpkgs.Tool(p, "protobuf")
func Tool(p *bob.P, attr string, opts ...Option) *Package
// Lib is a nixpkgs library linked into target code (target package set; see Cross).
//   nixpkgs.Lib(p, "openssl", nixpkgs.Modules("libcrypto"))
func Lib(p *bob.P, attr string, opts ...Option) *Package
// Pkg is any other nixpkgs package needed at run time (data, services, runtime tools).
func Pkg(p *bob.P, attr string, opts ...Option) *Package

type Option func(*options)
func Outputs(names ...string) Option        // default: the package's default outputs
func Modules(names ...string) Option        // pkg-config modules a Lib links; default: all modules of its dev output
func Cross() Option                         // take a target library from pkgsCross (A19); default: the target system's own set
func AllowInsecure(ids ...string) Option    // accept these knownVulnerabilities entries
func AllowUnfree() Option

// Package implements bob.Tool, bob.Linkable and bob.Tree (nixpkgs.Package of §03).
type Package struct{ bob.Node }
```

#### R-NIX-013 Only `Nixpkgs` leaks
- **Statement:** The only Nix concept in the public API is nixpkgs attribute names (and the
  channel name in `RepoOpts`). No public identifier contains `Derivation`, `Drv`, `StorePath`,
  `Flake` or `Expr`; users never see a `.nix` file.
- **Acceptance:** `TestAPISurface` greps exported identifiers of L0–L3 for those words → none;
  `find examples -name '*.nix'` → none, also after `bob dev`.
- **Source:** R-NIX-013, D6.

Typing and set selection (`Tool` = host set, `Lib` = target set, `Cross()`) are specified by
§04 R-FW-032/R-FW-036; §01 implements them in `resolve.nix` calls (`system` argument) and the IR
`nix.set` field. The index also answers "is this output cached?" for §04's note
(`Index.Cached(ctx, outPath) (bool, error)`: one `HEAD <substituter>/<hash>.narinfo` per configured
substituter, cached per run).

---

## 1.8 `bob.lock`

#### R-FW-004 Container format
- **Statement:** `bob.lock` at the repo root is canonical JSON: UTF-8, 2-space indent, object keys
  sorted bytewise, arrays in the order each schema defines, no floats, hashes in SRI form
  (`sha256-<base64>`, `sha512-<base64>`), `\n` line ends, one final newline. bob writes it
  atomically (temp file + rename) and only when the content changes. Top level:

| Key | Owner | Content |
|---|---|---|
| `lockVersion` | §01 | `1` |
| `nixpkgs` | §01 | `{"channel","rev","url","narHash"}` |
| `sources` | §01 | generic external sources (`bob.Fetch`): map id → entry |
| `go`, `rust`, `npm`, `opam`, `cc` | §03 | ecosystem sections (S2 §5.3, S3 §5.3, S4 §5.2, S5; `cc` holds `cc.External` entries, §03.7.6) |
| `nixpkgsFacts` | §01 | `{"<system>": {"<attr>": {"version"}}}` informational, for review diffs |

  `sources` entry: `{"url": "...", "kind": "file|archive", "hash": "<SRI of the downloaded
  file>", "narHash": "<SRI of the unpacked tree, archive only>", "name": "<store name>",
  "strip": <int components>}`.

- Example:

```json
{
  "lockVersion": 1,
  "nixpkgs": {
    "channel": "nixos-26.05",
    "narHash": "sha256-…",
    "rev": "cd4ea8d57de16f4810bc5f14207bac45fbf19f67",
    "url": "https://github.com/NixOS/nixpkgs/archive/cd4ea8d57de16f4810bc5f14207bac45fbf19f67.tar.gz"
  },
  "sources": {
    "//third_party/zlib:zlib": {
      "hash": "sha256-…",
      "kind": "archive",
      "name": "zlib-1.3.1-src",
      "narHash": "sha256-…",
      "strip": 1,
      "url": "https://zlib.net/zlib-1.3.1.tar.gz"
    }
  },
  "go": { "modules": [] }
}
```

- Every ecosystem section carries `"inputs": "<sha256 hex>"` = hash of the canonical JSON of that
  ecosystem's declared requirements (§03 R-FW-062); `nixpkgs` and `sources` carry their own
  `inputs`. `lock.File` (Go type in `pkg/engine/lock`):

```go
// File is bob.lock in memory. Sections are raw JSON owned by their ecosystem.
type File struct {
	LockVersion int
	Nixpkgs     NixpkgsPin
	Sources     map[string]Source
	Sections    map[string]json.RawMessage // "go", "rust", "npm", "opam", "cc"
}
func Read(path string) (*File, error)   // errors per R-FW-048
func (f *File) Write(path string) error // canonical, atomic, only if bytes change
```

- **Acceptance:** (a) `bob lock` twice → identical bytes. (b) Reformatting `bob.lock` by hand
  (other indent) → next `bob build` rewrites it canonically and prints `note: bob.lock was
  reformatted`. (c) JSON schema `pkg/engine/lock/lock.schema.json` validates every example lock.
- **Source:** R-FW-004, D2, D58 (crate data allowed), S2/S3/S4 lock proposals.

#### R-FW-005 Locking on demand
- **Statement:** Follows §03 R-FW-062: a section whose `inputs` differs from the current
  declarations is stale; `bob build`/`bob test` fail with the stale-lock error ("run bob lock")
  unless `--lock` is given; `bob lock`, `bob dev` and `--lock` re-resolve only the stale
  sections. Existing pins move only with `bob lock --update [<ecosystem>|nixpkgs|<name>]`; entries
  no declaration uses are removed by `bob lock`. Workers and `bob install` never re-resolve.
- **Acceptance:** adding `golang.Require("github.com/google/uuid","v1.6.0")` then `bob lock`
  changes only the `go` section (other sections byte-identical); `bob build` before `bob lock`
  exits with the stale-lock error and makes no network request.
- **Source:** R-FW-005, D26, R-FW-003, §03 R-FW-062.

#### R-FW-048 Unparseable lock
- **Statement:** A `bob.lock` that is not valid JSON (e.g. merge conflict markers) →
  BOB-E-1019 `✗ bob.lock cannot be read: <reason> (line <n>)` + hint `resolve the merge conflict, or run
  bob lock --rebuild to resolve everything again from bob.go`. `--rebuild` ignores the old file
  except `nixpkgs.rev`.
- **Acceptance:** fixture with `<<<<<<<` markers → exit 3 with that text; `bob lock --rebuild`
  succeeds and keeps the nixpkgs rev.
- **Source:** R-FW-004.

#### R-NIX-005 nixpkgs pin
- **Statement:** `nixpkgs.rev` comes from `https://channels.nixos.org/<channel>/git-revision`
  (verified for `nixos-26.05`, returns `cd4ea8d5…`), or from the `@rev` in `RepoOpts.Nixpkgs`.
  The source is fetched by bob's fetcher from `url` (GitHub archive tarball), unpacked with
  `strip=1`, and verified against `narHash`. A changed `rev` is the only way nixpkgs changes.
- **Acceptance:** fresh repo with `Nixpkgs: "nixos-26.05@cd4ea8d5…"`: `bob lock` writes the pinned
  entry; a tampered `narHash` → 3001 for `nixpkgs`.
- **Source:** R-NIX-002, A3, gen-b §4 (first fetch 16.5 s).

---

## 1.9 Platforms and variants (ADR 0009, model part)

```go
type Platform string
const (
	LinuxAMD64 Platform = "linux-amd64" // Nix system x86_64-linux
	LinuxARM64 Platform = "linux-arm64" // Nix system aarch64-linux
)
type Profile string
const ( Release Profile = "release"; Debug Profile = "debug" )
type Sanitizer string
const ( ASan Sanitizer = "asan"; UBSan Sanitizer = "ubsan" )

// Variant is the configuration of one build request. The zero value means release, no extras.
type Variant struct {
	Profile  Profile
	Sanitize []Sanitizer
	Race     bool                 // Go -race only
	Features map[Label][]string   // Rust features added on top of the crate's declared ones
	Named    []string             // named variants that produced this variant
}

// Named is a reusable variant (type Named; constructor NamedVariant, A66) referred to by name from the CLI and pipelines.
//   bob.NamedVariant("asan", bob.WithSanitize(bob.ASan, bob.UBSan), bob.WithProfile(bob.Debug))
func NamedVariant(name string, mods ...VariantMod) Named
type VariantMod func(*Variant)
func WithProfile(p Profile) VariantMod
func WithSanitize(s ...Sanitizer) VariantMod
func WithRace() VariantMod
func WithFeatures(crate Package, features ...string) VariantMod
```

Named variants may also be written as `func(*bob.Variant)`; such a function sets
`Variant.Features[bob.Label{Dir, Name}]` (a feature list per crate label) (R-FW-031, R-FW-038).

#### R-FW-030 Platform-generic packages, host builds
- **Statement:** Every package is defined once and built for any `Platform`. Derivations of
  host-side tools (bob-builder, generators) have the **host** Nix system; target libraries and
  outputs have the **target** `system` (consistent with R-FW-032); the target also changes
  toolchain and flags (§04 owns the toolchain table). Kinds may restrict platforms through their options
  (`Platforms []bob.Platform`); requesting another → `<label> does not support <platform>`.
- **Acceptance:** S7 matrix: `examples/polyglot` cross-builds aarch64→x86_64 and the binary runs
  under qemu-user with the expected output; every drv of a host-side tool has `system = "aarch64-linux"`, every drv of a target library or
  output has the target `system`.
- **Source:** R-FW-030, D3, ADR 0009 Decisions 1–2.

#### R-FW-050 Configured labels
- **Statement:** A configured label is `label "@" [platform] *("+" token)`, token ∈ {`release`,
  `debug`, `asan`, `ubsan`, `race`, named variant}. Canonical form: platform always printed;
  tokens ordered profile (omitted if `release`), `asan`, `ubsan`, `race`, named variants
  alphabetically. Unknown platform → `unknown platform "<x>" (known: linux-amd64,
  linux-arm64)`; unknown token → `unknown variant "<x>" (known: …)`. Variant semantics,
  projection and foreign-architecture tests are §04 (R-FW-031, R-FW-038, R-FW-039).
- **Acceptance:** `ParseConfigured` table test: `//api:api@+asan+debug` → canonical
  `//api:api@linux-arm64+debug+asan` on an arm64 host; `@linux-x86` → the unknown-platform text.
- **Source:** ADR 0009 Decision 5, S7 §5.2.

---

## 1.10 Derivation layer (backend B, ADR 0001)

#### R-NIX-001 Strategy B
- **Statement:** bob computes every derivation of its own packages in Go and imports the `.drv`
  files with `nix-store --import`; the only Nix expression bob evaluates is `resolve.nix`
  (§1.15). nixpkgs derivations are real `inputDrvs`.
- **Acceptance:** `grep -r --include='*.nix' -l . pkg/` lists only `pkg/engine/nixidx/resolve.nix`;
  `bob build -v` shows no `nix eval`/`nix-instantiate` except the index's.
- **Source:** R-NIX-001, D49, ADR 0001.

#### R-NIX-006 Exact derivation computation
- **Statement:** `pkg/engine/drv` implements, for Nix 2.34.9:
  - **ATerm:** `Derive([<outputs>],[<inputDrvs>],[<inputSrcs>],"<system>","<builder>",[<args>],[<env>])`;
    outputs `("<name>","<path>","<algo>","<hash>")` (input-addressed: algo and hash empty;
    fixed: `r:sha256`/`sha256` and lowercase hex); inputDrvs `("<drv>",["<out>",…])`. Strings
    escape `"`→`\"`, `\`→`\\`, LF→`\n`, CR→`\r`, TAB→`\t`. Sorted: outputs by name, inputDrvs by
    path, their output names, inputSrcs, env by key; args in order.
  - **makeStorePath(type, h, name)** = `/nix/store/` + nixbase32(xor-fold of
    sha256(`<type>:sha256:<hex(h)>:/nix/store:<name>`) to 20 bytes) + `-` + name.
  - **Output paths (input-addressed):** compute `hashModulo` with every output path and every
    env var named like an output set to `""`; output *o* → `makeStorePath("output:"+o, hashModulo,
    name + (o=="out" ? "" : "-"+o))`; then fill paths and env.
  - **hashDerivationModulo:** fixed-output: sha256(`fixed:out:<algo>:<hex>:<outPath>`);
    otherwise sha256 of the ATerm with each inputDrv path replaced by hex(hashModulo(input)).
    nixpkgs drvs are parsed (`drv.Parse`) recursively; their modulo hashes are cached per
    nixpkgs revision in the index (§1.15).
  - **drv path** = makeStorePath(`text` + concat(`:`+ref for sorted inputSrcs ∪ inputDrvs),
    sha256(ATerm), name + `.drv`).
  - **Fixed paths:** flat → makeStorePath(`output:out`, sha256(`fixed:out:<algo>:<hex>:`), name);
    recursive sha256 → makeStorePath(`source`, narHash, name).
  - Store names: `drv.Name(parts ...string) string` joins parts with `-`; inside a part `/` → `-`,
    `@` → `-`, any char outside `[A-Za-z0-9+._?=-]` → `-`; no leading `.`; > 200 bytes → cut to
    191 bytes + `-<hash8>` (first 8 nixbase32 chars of sha256 of the full name). bob's package
    names: `drv.Name(dir, name, ["host"], [platform if cross], [projection tag])` (§04 R-FW-038
    tag rules), e.g. `libs-foo-foo-linux-amd64-address`. This is the fallback for packages without
    an ecosystem name rule; the per-ecosystem names of §03 win for ecosystem packages (a local
    crate's version in names is `0.0.0`).
- **Acceptance:** (a) golden ATerm tests; (b) recomputing nixpkgs `hello` at the pin from its 187
  parsed input drvs gives the recorded output path; (c) cross-check suite: every drv of
  `examples/*` submitted with `nix derivation add` (Nix computes paths) yields the same drv and
  output paths; (d) `nix store add --mode flat --hash-algo sha512` and `--mode nar` give the
  same paths as `FixedOutputPath` (verified in Phase 2.3 against Nix 2.34.8 for both modes);
  (e) property test: changing any env value changes the output path.
- **Source:** ADR 0001, gen-b §1 (four-way cross-check).

#### R-NIX-007 Import and caching of drvs
- **Statement:** New drvs are imported in one `nix-store --import` stream (export format v1,
  topologically ordered, no signatures); already-valid drvs are skipped after one batched
  `nix-store --check-validity --print-invalid`. A drv cache `<home>/cache/drv/` maps
  sha256(canonical IR node + resolved input drv paths + generator version + bob-builder path) →
  drv path, so unchanged nodes are not recomputed.
- **Acceptance:** export-stream golden test against `nix-store --export` of the same drv; second
  `bob build` of `examples/polyglot` imports 0 drvs (`--json` event `drv.imported=0`).
- **Source:** gen-b §1/§4.

#### R-NIX-003 Standard store paths and caches
- **Statement:** All outputs are ordinary input-addressed store paths; sources and fetched files
  are ordinary content-addressed paths. They substitute from and upload to any standard binary
  cache.
- **Acceptance:** sign → `nix copy --to file://` → copy into a fresh store → `nix store verify
  --sigs` → run `//cmd/api:api` from there (gen-b round trip) in CI.
- **Source:** R-NIX-003, gen-b §1.

---

## 1.11 Sources and fetching (bob fetches; no user FODs)

#### R-NIX-008 No user-influenced fixed-output derivations
- **Statement:** bob never generates a fixed-output derivation for anything declared in `bob.go`
  or `bob.lock`. Repo sources and external downloads become content-addressed store paths that
  bob creates itself and references as `inputSrcs`; they equal the paths a FOD with the same
  name and hash would have (R-NIX-006 d), so "fixed-output input" in R-GO-002, R-NPM-002 and
  R-CC-005 is satisfied by path identity. Only nixpkgs' own FODs (trusted, from the pin) may run.
- **Acceptance:** for every drv generated from `examples/*`, no drv whose label is not
  `nixpkgs:` has `outputHash`; `bob build` of a package with an unreachable source URL succeeds
  when the path is already valid.
- **Source:** S13 §3.3/§5.4, S13-5, STATUS findings.

#### R-NIX-009 Repo source import
- **Statement:** For each package source set, bob serialises the filtered tree (R-FW-021) as a NAR
  in Go, computes `makeStorePath("source", sha256(NAR), name)`, checks validity in one batch, and
  imports only missing paths through one `nix-store --import` stream.
- **Acceptance:** for 50 random fixture trees, the path equals `nix store add --mode nar --name
  <name>` of the same filtered tree; importing 10,000 small source dirs takes ≤ 5 s [8c/15G]
  [proposal; measured 3–10 s with `nix-store --add` under load, gen-b §4].
- **Source:** gen-b §4, S13 §5.4.

#### R-NIX-014 Fetcher
- **Statement:** `engine/fetch.Fetcher` gets every external input (nixpkgs source, Go modules,
  crates, npm tarballs, opam archives, `sources` entries):

```go
type Request struct {
	URLs     []string // mirrors in order
	Kind     Kind     // File (flat) or Archive (unpacked tree)
	Hash     string   // SRI of the downloaded bytes (required for File; checked if set for Archive)
	NarHash  string   // SRI of the unpacked tree (required for Archive)
	Name     string   // store name
	Strip    int      // leading path components to drop (Archive)
	MaxBytes int64    // 0 = 2 GiB
}
type Fetcher interface {
	// Ensure makes the store path valid and returns it: already valid → substituters
	// (nix-store --realise of the path) → origin URLs. It never runs a Nix FOD.
	Ensure(ctx context.Context, r Request) (storePath string, err error)
	// Resolve downloads without a known NarHash (bob lock) and returns the hashes to record.
	Resolve(ctx context.Context, r Request) (hash, narHash string, err error)
}
```

  Rules: `https` (and `http` only with a pinned hash); 3 attempts with 1 s / 4 s backoff;
  connect timeout 10 s, idle timeout 60 s; size cap; `Hash` verified before unpacking; archives
  (`.tar`, `.tar.gz/.tgz`, `.tar.bz2`, `.tar.xz`, `.tar.zst`, `.zip`, `.crate`) are unpacked in Go
  with: paths cleaned and confined to the root (`..`, absolute, and paths through symlinks
  rejected), hard links turned into copies, device/fifo entries rejected, symlinks kept only if
  they resolve inside the tree, modes normalised to 0644/0755, ≤ 1,000,000 entries and ≤ 8 GiB
  unpacked; the tree's NAR hash must equal `NarHash`; import through `nix-store --import`
  (as R-NIX-009). Egress policy (private address ranges) is a `Policy` set by the caller:
  developers default to no restriction; workers use §05's policy.
- Errors: mismatch 3001 (prints URL, lock hash, got hash); download failure 3002; not found
  3003; unsafe archive → 3004 `<label>: archive <url> contains an unsafe entry <path> (<reason>)`.
- **Acceptance:** tests with a local HTTP server: tampered byte → 3001; 404 → 3003; tar with
  `../x`, absolute path, symlink escape, device node, 10⁶+1 entries → each refused; correct
  crate archive → path equals the S3 recursive FOD path for the same `narHash`.
- **Source:** S13 §5.4, R-CC-005, D44, R-SEC-004, S15 3001–3003.

---

## 1.12 `bob-builder` contract

#### R-NIX-015 Builder binary
- **Statement:** `bob-builder` is a static Go binary (`CGO_ENABLED=0 -trimpath -buildvcs=false
  -ldflags='-s -w -buildid='`), built from `cmd/bob-builder` (packages `pkg/builder`, `bspec`,
  `pkg/<eco>/bop`). It has its own version `bspec.BuilderVersion` (integer, starting at 1),
  bumped only when its behaviour changes. Its store path is the `source` path of a tree
  `bin/bob-builder`, named `bob-builder-<ver>`. `bob` embeds the host-arch binary and a table
  `builders.go` of `{system → store path}` for both systems; at first use bob imports the
  embedded binary and refuses to run if the resulting path differs from the table
  (BOB-E-9007 `embedded builder does not match its recorded hash`).
- **Acceptance:** building `cmd/bob-builder` twice (two machines of one arch, or two checkouts)
  gives identical bytes; a CI check fails when a builder source change does not bump
  `BuilderVersion`… and when the table is stale (`go generate ./pkg/engine/store` diff).
- **Source:** ADR 0001 Consequences, ADR 0005 Consequences (builder is an input of every drv).

#### R-NIX-016 Derivation shape and job format
- **Statement:** Every non-nixpkgs derivation uses **structured attributes**
  (`__structuredAttrs = true`; ATerm env = `__json` + one env var per output): `builder =
  <bob-builder>/bin/bob-builder`, `args = ["<kind>"]` (subcommand = job kind), and the JSON
  attributes `bobSpec` (the `bspec.Job`), `outputs`, `outputChecks.<out>.disallowedReferences`
  (fixup contract, §1.13) and, where a kind needs it (§04 OCI layers), `unsafeDiscardReferences.<out>
  = true`. The builder reads only `$NIX_ATTRS_JSON_FILE` and the variables Nix sets; there is no
  env-size limit on the spec. `__json` is canonical JSON (keys sorted, no whitespace).

```go
package bspec
const BuilderVersion = 1
// Job is one derivation's work. Kind selects the subcommand; Args is the kind's own schema.
type Job struct {
	V       int             `json:"v"`       // 1
	Kind    string          `json:"kind"`    // must equal argv[1]
	Label   string          `json:"label"`   // for log lines
	Outputs []string        `json:"outputs"` // "out", optional "dev", "lib", "debug"
	Args    json.RawMessage `json:"args"`
	Fixup   Fixup           `json:"fixup"`
}
type Fixup struct {
	Strip      bool     `json:"strip"`      // false for profile debug
	DebugOut   bool     `json:"debugOut"`   // move debug info to the "debug" output
	ShrinkRPath bool    `json:"shrinkRpath"`
	Forbidden  []string `json:"forbidden"`  // store paths that must not be referenced (also in outputChecks)
	Tools      Tools    `json:"tools"`      // store paths of strip, objcopy, patchelf
}
```

  Core kinds (in `pkg/builder`): `run` (bob.Run: `{steps:[{op,args}]}` with ops `mkdir`, `copy`,
  `symlink`, `write`, `exec {argv,env,cwd,path[]}`, `check-outputs`), `unpack {archive,to,strip}`,
  `copy-tree`, `test-run` (shared with §03). Ecosystem kinds are §03's table (§03.0.4) and §04's
  (`oci-layer`, `oci-image`, `appimage`, …); each registers through `pkg/<eco>/bop`. Unknown kind →
  exit 2. Every path a job writes is under an output or `/build`; others → exit 2.
- Environment for `exec`: exactly `PATH` (declared), `HOME=/homeless-shelter`,
  `TMPDIR=/build/tmp`, `NIX_BUILD_CORES`, `SOURCE_DATE_EPOCH=1`, `TZ=UTC`, `LC_ALL=C`, the outputs,
  plus the step's `env`.
- **Acceptance:** (a) ATerm golden for a structured-attrs drv equals Nix's: `nix derivation show`
  of a Nix-evaluated `derivation { __structuredAttrs = true; … }` and bob's drv with the same
  attributes give the same drv path (verified shape on Nix 2.34.8: env holds the output vars,
  attributes go to `structuredAttrs`); (b) unit tests per kind/op; (c) a fuzzed `Job` never
  escapes `/build`/outputs; (d) a 1 MiB job builds.
- **Source:** gen-b §2/§7, S3 §5.2, S15 §5.7, §04 R-OUT-011.

#### R-NIX-017 Builder logging and exit codes
- **Statement:** The builder writes plain log lines; structured diagnostics as
  `bob:: {"type":"diag","severity","file","line","col","msg"}` lines; phase changes as
  `@nix {"action":"setPhase","phase":"<op>"}`. It never relies on `start`/`result`/`stop`
  messages and never uses `/bin/sh` (the embedded sandbox shell has no applets). Exit codes:
  0 ok; 1 a step failed (user/build error; the last line names the step and argv[0]); 2 invalid
  job (bob bug → 9004); 3 fixup violation (→ 9003); 4 a declared output missing (→ 9002 or the
  `bob.Run` text).
- **Acceptance:** S15 fault rows 28 (builder missing → 9001) and the 2xxx rows pass with the
  real builder; a step that prints `@nix {"action":"msg","level":0,…}` does not change the
  result of a successful build (warning only).
- **Source:** S15 §4/§5.7, STATUS findings.

#### R-NIX-018 Success is structural
- **Statement:** A build succeeded iff `nix build` exits 0 **and** `nix-store --check-validity
  --print-invalid` reports every requested output valid. Log content never decides success.
- **Acceptance:** S15 full-disk fault (partial output file) is reported as failure 6001 even if the
  builder printed success; injected fake error messages in a successful build → exit 0 with a
  warning.
- **Source:** S15 §4, STATUS findings.

---

## 1.13 Fixup and closure budgets

#### R-NIX-019 Fixup contract
- **Statement:** After the last step the builder runs fixup on every output unless the kind
  disables it:
  1. **Prevent** (done by generators, §03): `-ffile-prefix-map=/build=.`, Rust
     `--remap-path-prefix=<src>=<label dir>`, Go `-trimpath`, so sources are not referenced.
  2. **Strip** ELF files under `bin/ libexec/ lib/` with nixpkgs binutils `strip` (an input of
     the drv): executables `--strip-all`, shared and static libs `--strip-debug`. Profile
     `debug`: no stripping; with output `debug` declared, debug info moves there
     (`objcopy --only-keep-debug` + `--add-gnu-debuglink`).
  3. **RUNPATH:** `patchelf --shrink-rpath` and drop entries outside `/nix/store` (e.g. `/build`).
  4. **Reference check:** scan outputs for store hashes (Nix's algorithm); a reference to a path
     in the kind's forbidden set → exit 3 → 9003 `<label> refers to <ref>, which it must not
     reference`. Forbidden for runtime outputs of all kinds: compilers and wrappers (gcc
     unwrapped, cc-wrapper, binutils, rustc, go, ocaml), any bob `-src` path, crate/module
     source paths, bob-builder. The same set goes into `outputChecks.<out>.disallowedReferences`,
     so Nix enforces it too (§02 maps Nix's message to 9003).
- **Acceptance:** `examples/polyglot` native `//cmd/api:api`: closure contains no `gcc-15.3.0`
  (only `gcc-15.3.0-lib`), no `-src` path, binary has no `.debug_info`; a fixture C program
  embedding `__FILE__` of a source path builds without a reference to it.
- **Source:** gen-b §7, S7 risks (366 MB closure via gcc), S8 §3.1, S5 (693 → 55 MB).

#### R-NIX-030 Closure budgets [proposal]
| Package (examples/polyglot) | Measured | Budget |
|---|---|---|
| native arm64 `//cmd/api:api` closure (NAR) | 75.9 MiB without fixup (S8 §3.1, includes itoa-src, unstripped 7.7 MiB binary) → 73.4 MiB after fixup (strip −2.5 MiB, itoa-src dropped; S8 §3.1 numbers); native with cross-checked gcc ref: 366 MB (S7) | **≤ 74 MiB**, 12 paths max [8c/15G]; §04 R-OUT-025 uses the same value |
| `api` binary, release, stripped | 5.2 MiB (S8) | ≤ 5.5 MiB |
| cross `//cmd/api:api@linux-amd64`, target libs native (two glibc) | 113.8 MB (S7) | ≤ 112 MiB |
| cross, target libs `Cross()` | 76.1 MB (S7) | ≤ 74 MiB |

- **Acceptance:** `bobtest/closure_test.go` measures `nix path-info -S` for each row on both
  architectures' native builds and fails above budget.
- **Source:** S7 §3.2, S8 §3.1. Bundle-level pruning (glibc locales, gconv) is §04.

---

## 1.14 Embedded rootless Nix (ADR 0002)

#### R-NIX-020 Embedded static Nix
- **Statement:** `bob` embeds nixpkgs `nixStatic` 2.34.9 (pin) for its own architecture, stripped,
  gzip-9 (13.8 MB), and extracts it on first use to `<home>/bin/nix-<first 12 hex of sha256>/bin/nix`
  (temp dir + atomic rename; observable as a temporary entry that is renamed to the final
  `nix-<12 hex>` name) with symlinks `nix-store`, `nix-build`, `nix-instantiate`,
  `nix-env`, `nix-hash`, `nix-daemon` → `nix`. Later runs only stat. The host's `nix` is never
  executed. `ATTRIBUTION.md` lists Nix (LGPL-2.1-or-later), its static dependencies and busybox
  (GPL-2.0) with source locations (D52).
- **Acceptance:** under `env -i PATH=/usr/bin:/bin` as a user without Nix, `bob build` of
  `examples/cpp-lib` works; first extraction ≤ 500 ms [8c/15G] (measured 294 ms); `strace -f -e
  execve` shows no exec of a `nix` outside `<home>/bin`.
- **Source:** R-NIX-020/023, D50, D52, ADR 0002 Decisions 2–3.

#### R-NIX-021 Namespace mechanism
- **Statement:** As ADR 0002 Decision 1: re-exec of `/proc/self/exe` with
  `CLONE_NEWUSER|CLONE_NEWNS`, uid/gid map `0 <uid> 1`, `setgroups deny`, `/` made `rprivate`;
  host has `/nix` → bind `<home>/nix` on `/nix`; otherwise tmpfs root mirroring top-level
  entries + `nix/`, `pivot_root`, old root detached. Every Nix process and every store binary
  bob runs (builds, `bob exec`, `bob dev`, shims) runs inside. One namespace entry per bob
  command; Nix subprocesses inherit it.
- **Acceptance:** S0b probes F1, F3 (network blocked, `/home` hidden, loopback present) pass in
  bind and pivot mode as non-root; 4 parallel `bob build` on one state finish without lock
  errors; the `namespace` stage event carries `durationMs`; namespace entry ≤ 10 ms [8c/15G] and [2c/4G] (measured 5–7 ms).
- **Source:** R-NIX-021, D11, ADR 0002 Decision 1, rootless-results.

#### R-NIX-022 Refusal with the exact fix
- **Statement:** Detection order: sysctl pre-check (hint only) → clone error class (ENOSPC, EPERM,
  EACCES) → first mount failing inside the namespace (child exit 125). Output (BOB-E-7005, exit 7):

```
✗ bob cannot create its private /nix: unprivileged user namespaces are blocked on this system
  cause: <one of the lines below>
  fix (run once as an administrator):
    <fix text>
```

| Cause (detected) | Fix text |
|---|---|
| AppArmor restricts user namespaces (Ubuntu ≥ 24.04; mount EPERM, or sysctl `kernel.apparmor_restrict_unprivileged_userns=1` + clone/mount failure) | the AppArmor profile from rootless-results.md with bob's real path (`readlink /proc/self/exe`), then `sudo apparmor_parser -r /etc/apparmor.d/bob`; alternative: the `60-bob-userns.conf` sysctl line |
| `user.max_user_namespaces = 0` (ENOSPC; RHEL STIG) | `echo 'user.max_user_namespaces = 15000' \| sudo tee /etc/sysctl.d/60-bob-userns.conf && sudo sysctl --system` |
| `kernel.unprivileged_userns_clone = 0` (Debian, linux-hardened) | `echo 'kernel.unprivileged_userns_clone = 1' \| sudo tee /etc/sysctl.d/60-bob-userns.conf && sudo sysctl --system` |
| NixOS with `security.allowUserNamespaces = false` (`/etc/NIXOS` exists) | `set security.allowUserNamespaces = true; in configuration.nix and run nixos-rebuild switch` |
| other EPERM/EACCES (container seccomp, SELinux) | `bob runs only where unprivileged user namespaces are allowed; inside containers, allow them (e.g. podman --security-opt seccomp=unconfined, or docker --security-opt seccomp=unconfined)` |

  There is no fallback mode.
- **Acceptance:** simulated cases (`max_user_namespaces=0` in a throwaway namespace; ENOSPC)
  print the matching fix (S0b-verified); the AppArmor case is verified on a real Ubuntu 24.04
  host (manual, Gate S open risk).
- **Source:** R-NIX-022, D11, ADR 0002 Decision 4.

#### R-NIX-024 bob owns its Nix configuration
- **Statement:** State home `<home>` = `$BOB_HOME`, else `${XDG_DATA_HOME:-$HOME/.local/share}/bob`:

```
<home>/nix/                     mounted at /nix (store + var/nix/db)
<home>/bin/nix-<h>/bin/         extracted Nix (R-NIX-020)
<home>/etc/nix/nix.conf         generated, mode 0644
<home>/etc/nix/netrc            generated, mode 0600 (cache tokens)
<home>/etc/nix/user.conf        empty (NIX_USER_CONF_FILES)
<home>/xdg/{config,cache,data,state}/   XDG_* for every Nix process
<home>/cache/{go-build,drv,nixpkgs,eval}/
<home>/lock                     flock for state creation and nix.conf rewrites
```

  Nix processes get exactly: `NIX_CONF_DIR=<home>/etc/nix`, `NIX_USER_CONF_FILES=<home>/etc/nix/user.conf`,
  the four `XDG_*` above, `HOME`, `PATH=<home>/bin/nix-<h>/bin`, `TMPDIR`, `TERM` (if set);
  `NIX_PATH` and every other `NIX_*` variable are removed. Generated `nix.conf` (dev profile):

```
# Generated by bob. DO NOT EDIT.
build-users-group =
sandbox = true
sandbox-fallback = false
require-drop-supplementary-groups = false
experimental-features = nix-command
substituters = https://cache.nixos.org <extra substituters from bob config, in order>
trusted-public-keys = cache.nixos.org-1:6NCHdD59X431o0gWypbMrAURkbJ16ZPMQFGspcDShjY= <extra keys>
netrc-file = <home>/etc/nix/netrc
narinfo-cache-negative-ttl = 60
fallback = true
connect-timeout = 5
max-jobs = auto
cores = 0
eval-cache = false
allow-import-from-derivation = false
auto-optimise-store = false
warn-dirty = false
```

  (Defaults checked on Nix 2.34.8: `sandbox-fallback=true`, `eval-cache=true`,
  `allow-import-from-derivation=true`, `narinfo-cache-negative-ttl=3600`,
  `require-drop-supplementary-groups=true` — so each line above changes behaviour.)
  `discard-references` is **not** listed: Nix 2.34.8 and 2.34.9 warn `unknown experimental
  feature 'discard-references'` (the feature is stable; `unsafeDiscardReferences` needs no flag —
  verified at integration on both versions with `experimental-features = nix-command`: the
  output's references are empty with it and present without it). §04's request is met by the
  derivation layer alone. Remote-build
  and release fetches pass `--option narinfo-cache-negative-ttl 0` (S12). bob validates cache
  credentials itself (§02 R-NIX-029, `AuthService/WhoAmI` → 4008/4010) before a build because Nix reports a 401 only as a level-1
  warning. §05 derives the worker profile from the same `store.Config`.
- **Acceptance:** `bob doctor --print-nix-env` shows exactly the variables above; a user
  `~/.config/nix/nix.conf` with `substituters = http://bogus` and `NIX_PATH` set have no effect;
  a system Nix's store, db and narinfo cache are unchanged after a bob build (R-NIX-025).
- **Source:** R-NIX-024/025, ADR 0002 Decision 1 + Consequences, S12, S15, STATUS findings.

#### R-NIX-025 Coexistence with a system Nix
- **Statement:** On a host with `/nix`, bob's namespace hides it (bind mount over `/nix`); bob
  never reads `/etc/nix`, the system daemon socket, or `~/.config/nix`.
- **Acceptance:** with a system Nix daemon running, `bob build` works and `nix-store -q
  --references` on the host store shows no bob paths.
- **Source:** R-NIX-025.

#### R-NIX-026 Single-user store, concurrency
- **Statement:** bob uses Nix in single-user (local store) mode, no daemon. Concurrent bob
  commands share the store through Nix's own locks; bob's own caches use `<home>/lock` for
  writes and atomic renames. SQLite busy errors are retried 3× with 200 ms backoff before 7004.
- **Acceptance:** 4 concurrent `bob build //...` on `examples/polyglot` all succeed; 8 concurrent
  `bob lock` produce one consistent `bob.lock`.
- **Source:** gen-b §9, rootless-results (4 parallel processes), S15 7004.

---

## 1.15 nixpkgs lookup and index

#### R-NIX-011 Pre-checks in Go
- **Statement:** Before any derivation is built, bob checks with index data: attribute exists
  (1001, with up to 3 suggestions by edit distance over the top-level names list, ≤ 2 edits or
  same prefix), broken (1002), insecure without `AllowInsecure` (1003), unfree without
  `AllowUnfree` (1004), unavailable on the system (1005), dependency cycle (1006, Go topo sort),
  unknown dependency (1007), toolchain version constraints (1008, §03 provides the rules), missing
  lock hash (1009), unbuildable system (1010).
- **Acceptance:** gen-b fault catalogue rows (unknown `opensll` → suggestion includes `openssl`;
  `aranym` broken; `bzip2_1_1` insecure; cycle; Go version) each print their template with
  `definedAt` and exit 3 without starting `nix build`.
- **Source:** R-NIX-011, gen-b §3, S15 §5.2.

#### R-NIX-027 `resolve.nix` and the index
- **Statement:** `pkg/engine/nixidx/resolve.nix` (embedded, ≤ 60 lines, no build logic) takes
  `{src, system, attrs, names, allowUnfree, permittedInsecure}` and returns, per attribute, data
  only: `missing`, `broken`, `insecure`, `knownVulnerabilities`, `unfree`, `available`, `version`,
  `drvPath`, `outputs`, `evalRefused` (gen-b version, extended with the two allow lists). It is
  run as `nix eval --json --file resolve.nix --arg … ` inside the namespace. Index cache
  `<home>/cache/nixpkgs/<rev>-<system>.json` holds results per attribute, the names list (first
  use), and modulo hashes of parsed nixpkgs drvs; it is keyed by (rev, system, index version) and
  re-queried only for new attributes or when a cached `drvPath` is no longer valid. A full
  `nix-env -qa` index is never built (56 s, 6.4 GB).
- If the batch evaluation itself fails (an `abort` that `tryEval` cannot catch), bob retries per
  attribute to isolate the failing one and reports `nixpkgs package "<attr>" cannot be
  evaluated on <system>` (BOB-E-1017, definition class) with the raw text under `-v`.
- **Acceptance:** cold index for the polyglot fixture ≤ 2 s, warm ≤ 50 ms [8c/15G] (measured 1.55 s
  / 0.02 s); a fixture attribute that aborts evaluation produces the per-attribute error, not
  9999.
- **Source:** gen-b §1/§4, ADR 0001 Consequences, R-NIX-010.

**R-NIX-010** (no raw Nix evaluation error) is specified once, in §02 (§02.3, error taxonomy):
`resolve.nix` is the only Nix evaluation bob performs, and any unclassified error from it is a
9999 bug with a regression test.

#### R-NIX-002 nixpkgs stays usable
- **Statement:** Any attribute that evaluates to a derivation at the pin can be used through
  `nixpkgs.Tool/Lib/Pkg`; uncached ones are built by Nix from the nixpkgs recipe (upstream
  failures → 5001).
- **Acceptance:** `examples/polyglot` built against nixos-25.11 (`b6018f87`) after lowering the
  Go version: no builder code changes; only real version constraints fail, with 1008/2006.
- **Source:** R-NIX-002, gen-b §5.

---

## 1.16 Build pipeline and generic interfaces

#### R-FW-049 `engine/build.Run` order
- **Statement:** One `bob build` runs: enter namespace → scan + rules (R-FW-002) → evaluator
  (cached) → IR validation → lock check/update → index pre-checks → source import → `Plan` per
  ecosystem → fetch (`Ensure` all `PlanResult.Fetches`, in parallel ≤ 8) → drv import → `nix build --no-link
  --print-out-paths --log-format internal-json -L --keep-going <drv>^<outputs>…` → validity
  check → translation (§02). Each stage emits a `--json` event (§02 schema) with its duration.
- **Acceptance:** `--json` event sequence for `examples/polyglot` cold build matches the stage list.
- **Source:** gen-b §1, S15 §5.1.

#### R-FW-009 Works without a server
```go
package engine
// Substituter is a binary cache bob passes to Nix (URL, public key, optional netrc credential).
type Substituter struct{ URL, PublicKey, NetrcMachine, NetrcPassword string }
// RemoteExecutor runs a build elsewhere and makes the outputs available locally (bob build --remote).
type RemoteExecutor interface {
	Build(ctx context.Context, req RemoteRequest) (<-chan Event, error)
}
```
- **Statement:** The framework has no other server concept; with no `Substituter` beyond
  cache.nixos.org and no `RemoteExecutor`, every command works.
- **Acceptance:** all example builds pass with network access to cache.nixos.org and the
  upstream registries only.
- **Source:** R-FW-009, D37.

#### R-FW-020 Fine-grained packages (core rule)
- **Statement:** The core imposes no per-derivation overhead beyond one bob-builder start
  (static binary, no shell setup); granularity per ecosystem is §03's.
- **Acceptance:** synthetic N=100 chain of trivial `bob.Run` steps builds cold in ≤ 12 s at both
  profiles [proposal; B measured 10.0 s / 9.4 s with bash builders].
- **Source:** R-FW-020, ADR 0001 (0.1 s/drv).

#### R-FW-022 Cross-repo use
- **Statement:** Another bob repo is a Go module required in `go.mod` through bob.go
  (`bob.Import("example.com/other", "v1.2.0")`, recorded in the generated `go.mod`); its
  `bob.go` packages are imported as Go packages; labels are `example.com/other//dir:name`. Its
  `bob.lock` is not used: the consumer's `bob lock` resolves all declarations of the graph.
- **Acceptance:** `examples/cross-repo` (module served from a `file://` GOPROXY) builds the
  dependency's package by typed handle; gopls completion on `bar.` offers `Lib`.
- **Source:** R-FW-022, S1 §3.5.

---

## 1.17 Budgets (R-NFR-001/002, core part) [proposal]

| Measurement | Measured (quiet) | Budget [8c/15G] | Budget [2c/4G] |
|---|---|---|---|
| Evaluation, 1,000 `bob.go`, no change (eval key hit) | 0.47 s with `go build` no-op (S1 quiet) | ≤ 0.1 s | ≤ 0.15 s |
| Evaluation, 1,000 `bob.go`, one body change | 0.93 s (S1 quiet) | ≤ 1.5 s | ≤ 3 s |
| Evaluation, 1,000 wide, cold (empty GOCACHE) | 10.8 s / 23.9 s (S1, quiet run) | ≤ 15 s | ≤ 30 s |
| graph → drvs, N=1,000 new / unchanged | 3.03 / 1.11 s; 2.96 / 1.17 s (b-quiet) | ≤ 3.5 s / ≤ 1.2 s | ≤ 3.5 s / ≤ 1.3 s |
| graph → drvs, N=10,000 new / unchanged | 24.9 / 7.7 s; 26.2 / 8.7 s | ≤ 30 s / ≤ 9 s | ≤ 30 s / ≤ 10 s |
| peak RSS, N=10,000 | 175 MB / 217 MB | ≤ 300 MB | ≤ 300 MB |
| `examples/polyglot` no-op `bob build` (all stages) | 0.33 s / 0.45 s (backend only) + eval no-op 0.09 s | ≤ 0.6 s | ≤ 0.8 s |
| `examples/polyglot` cold (toolchains present) | 11.8 s / 22.0 s (B, bash builders) | ≤ 13 s | ≤ 25 s |
| namespace entry | 5–7 ms | ≤ 10 ms | ≤ 10 ms |

The "unchanged" graph→drvs budgets are expected to drop sharply with the drv cache (R-NIX-007);
Stage 3 records the new numbers and may tighten them. Incremental budgets per ecosystem are §03.

---

## 1.18 ADR status changes (this section)

- ADR 0003 → **Accepted (locked at Gate S)**; owner amendments: D59 (cd + editor must work → gopls
  shim and settings carry the tag), A14 (next-to-code accepted), rules extended to R6/R7.
- ADR 0009 → **Accepted (locked at Gate S)** for the model part (§04 updates the cross part);
  amendments A18 (foreign-arch tests), A19 (target libraries default).

## Open points

1. **[open]** Corporate registries on private addresses for developer fetches: recommended no
   egress restriction on developer machines (they already reach their network); workers use §05.
2. **[open]** `git` sources for `cc.External`: no git fetcher in v1; recommended: use forge
   archive URLs of a commit (`/archive/<sha>.tar.gz`) as `Archive` sources.
3. **[open]** Import of sources and fetched trees via `nix-store --import` (fast, one process)
   versus `nix store add` per path (slower, Nix computes the CA): recommended `--import` with the
   path-equality oracle test (R-NIX-009) and `bob doctor` verification.
4. **[open]** Determinism check cost on workers: recommended a single evaluation on workers and
   `bob check --determinism` in pipelines' check stage only.
5. **[open]** `abi <abi/4.0>` line of the AppArmor profile is unverified (ADR 0002 risk 1).

## Escalations

None. Coordinator notes from drafting, resolved at integration (spec/00-integration-log.md):
R-GO-002/R-NPM-002/R-CC-005 wording now says path identity (R-NIX-008); §03's `fetch-crate`
uses the Fetcher; the `api` closure budget is ≤ 74 MiB in both §01 and §04; §04 now describes
the fixup as prevention + BOB-E-9003; §03's error names map to §02 codes (§02.3).
