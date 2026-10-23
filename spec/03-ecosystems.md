# §03 — Ecosystems: C/C++, Go, Rust, npm/TS, OCaml, code generators

> Source of SPEC.md: `/root/bob/SPEC.md` is generated from spec/01…06 by concatenation (command in the SPEC.md preamble). Edit this file, then regenerate.

Scope: the public API of `pkg/cc`, `pkg/golang`, `pkg/rust`, `pkg/npm`, `pkg/ocaml`, `pkg/proto`,
`pkg/connect`, `pkg/graphql`, `pkg/openapi`; derivation layout per ecosystem; resolver rules;
`bob.lock` entry schemas; generated tooling files and drift rules; quirks tables; materialisation
of generated code; ecosystem budgets.
ADRs: 0004 (Go), 0005 (Rust), 0006 (npm), 0007 (OCaml), 0008 (codegen + materialisation), all
`Accepted (locked at Gate S)` with owner amendments.

## Interfaces

Expected from other sections (names are what this section uses; the coordinator reconciles):

- **§01 provides**
  - `bob.P` (evaluation context for one platform × variant), `bob.Define[T]`, labels
    (`bob.Label`), source positions, `bob.Sources` + `bob.Glob(patterns ...string) bob.Sources` +
    `bob.Files(paths ...string) bob.Sources`.
  - `bob.Linkable` + `bob.LinkInfo` (defined in core so that `nixpkgs` and every ecosystem can
    implement it without import cycles; `cc.Linkable` is an alias, §03.1.1).
  - `nixpkgs.Lib(p, attr, ...nixpkgs.Option)` (target-set library, implements `bob.Linkable`),
    `nixpkgs.Tool(p, attr)` (host-set tool), `*nixpkgs.Package` (struct, §01 §1.7), the nixpkgs
    index and pre-checks (attr exists, broken/insecure/unfree/platform).
  - The `bob.lock` container: a versioned JSON document with one top-level section per
    ecosystem (`"go"`, `"rust"`, `"npm"`, `"opam"`, `"cc"`), read/written through
    `lock.File`; the entry types `lock.GoModule`, `lock.Crate`, `lock.CrateDep`,
    `lock.NpmImporter`, `lock.NpmPackage`, `lock.OpamRepository`, `lock.OpamPackage`,
    `lock.CExternal` are **specified here** (§03.7) and live in `pkg/engine/lock` (§01 §1.8).
  - The derivation layer: `drv.Derivation`, `drv.Name(parts ...string) string` (store-name
    sanitiser: allowed chars `[A-Za-z0-9+._?=-]`, `/`→`_`, `@`→`-`, other → `-`, ≤ 211 chars),
    fixed-output store paths (`drv.FixedOutputPath`), the source import (`bob` fetches with
    `engine/fetch.Fetcher` and imports with `nix store add`/`nix-store --import`; Nix never runs
    an ecosystem fetcher, S13, R-NIX-008), fixup (strip, reference scrubbing, §04 budgets).
  - The `bob-builder` contract: one static Go binary, one JSON job in env `bobSpec`, job kinds
    registered by ecosystems (§03.0.4 lists the kinds this section needs). Success = exit 0
    **and** every output path exists (S15: the builtin `/bin/sh` has no tools).
  - `engine.Ecosystem` (registration interface in `pkg/engine`, §03.0.1 = §01 R-FW-051) and the
    engine that calls it.
- **§02 provides** `bob lock`, `bob gen [--check|--force]`, `bob build`, `bob test`, `bob dev`,
  `bob status`; the error taxonomy and message templates (names used here → §02 codes:
  `E-DRIFT` = BOB-E-1011, `E-LOCK-STALE` = 1012, `E-RESOLVE` = 1015, `E-FETCH-HASH` = 3001,
  `E-GEN-CONFLICT` = 1016, `E-SCRIPT-DENIED` = 2013, `E-PHANTOM-DEP` = 2014,
  `E-QUIRK-TEST` = 9006; §02.3 "Cross-section code assignment"); `.bob/bin` shims, `bob env`, editor settings and the LSP path-rewriting proxy
  (all generated tooling files below reference real `/nix/store/…` paths; tools run through
  shims inside bob's namespace, D51/D59).
- **§04 provides** variant projection rules per package kind (which of platform / profile /
  sanitiser / features enter each derivation of this section), cross toolchain selection,
  `bob.Platform`, `bob.Variant`, and the `Features` variant axis for Rust.
- **§03 provides** to §01/§02/§04: the `Ecosystem` implementations, `lock.*` entry types,
  `GeneratedFile` lists per ecosystem, the drift checker inputs, `TestResult` (§03.0.5),
  quirks tables, the codegen manifest (`.bob/gen/manifest.json`), dev-server contracts.

## §03.0 Rules common to all ecosystems

### §03.0.1 Ecosystem registration (request to §01)

Specified as `engine.Ecosystem` in `pkg/engine` (§01 R-FW-051, with the request/result types);
it is never linked into the evaluator. Registered from `pkg/<eco>/gen` with `engine.Register`.

```go
// Ecosystem is implemented once per pkg/<eco> and registered from that package's init.
// Adding an ecosystem is a self-contained package under pkg/ (REQUIREMENTS §7.7).
type Ecosystem interface {
	// Name is the bob.lock section key ("go", "rust", "npm", "opam", "cc").
	Name() string
	// Lock resolves the declared external dependencies of all packages of this ecosystem
	// in the graph and returns the new lock section. Network allowed. Called by `bob lock`
	// and by `bob build`/`bob dev` when the section is stale (§03.0.3).
	Lock(ctx context.Context, req LockRequest) (json.RawMessage, error)
	// Plan turns the ecosystem's packages into derivations. No network, no process
	// execution except reading the store; deterministic (R-FW-008).
	Plan(ctx context.Context, req PlanRequest) (PlanResult, error)
	// Native returns the tooling files (go.mod, Cargo.toml, …) for the working tree.
	Native(ctx context.Context, req PlanRequest) ([]GeneratedFile, error)
}

// GeneratedFile is one tooling file bob writes into the working tree (R-FW-006).
type GeneratedFile struct {
	Path    string      // repo-relative, clean
	Content []byte
	Mode    fs.FileMode // always 0o444
	Owner   bob.Label   // package whose definition produced it (for drift messages)
	Explain func(onDisk []byte) []DriftLine // maps a byte diff to bob.go terms (§03.0.2)
}
```

### §03.0.2 Generated tooling files and drift

#### R-FW-060 Generated native files are written read-only and tracked
- **Statement:** Every file listed in §03.0.2 table is written by bob with mode 0444, its
  sha256 is recorded in `.bob/state/native.json`, and it starts with the header given in the
  table where the format allows comments.
- **Acceptance:** Given a fixture repo per ecosystem, when `bob gen` runs, then every file of
  the table exists with mode 0444, its content equals the golden, and `.bob/state/native.json`
  holds `{"version":1,"files":{"<path>":{"sha256":"<hex>","owner":"<label>"}}}` for each.
- **Source:** R-FW-006, D2, ADR 0004–0008.

| Ecosystem | File (repo-relative) | Header | Committed? |
|---|---|---|---|
| Go | `<module dir>/go.mod`, `<module dir>/go.sum` | `// Code generated by bob from bob.go. DO NOT EDIT.` + `// Edit <module dir>/bob.go and run bob lock.` (go.mod only; go.sum has no comments) | **yes** (Go module consumers and `go install` need them, R-FW-022) |
| Go | `<dir>/zz_bob_cgo.go` (ADR 0003 R5) | `//go:build bob && cgo` | no |
| C/C++ | `compile_commands.json` (repo root) | none (JSON) | no |
| Rust | `rust-project.json`, `Cargo.toml` (root workspace), `<crate dir>/Cargo.toml`, `Cargo.lock`, `.cargo/config.toml`, `.bob/vendor/` | `# generated by bob from bob.go — do not edit` (TOML files) | no |
| npm | `<importer dir>/package.json`, `<importer dir>/tsconfig.json`, `<importer dir>/node_modules/` (directory of symlinks), `.bob/vite.bob.config.mjs` | package.json: first key `"//": "generated by bob from bob.go — do not edit"`; tsconfig.json: `// generated by bob from bob.go — do not edit` (JSONC) | no |
| OCaml | `<dir>/.merlin`; opt-in: `dune-project`, `<dir>/dune`, `<lib>.opam` | `# generated by bob from bob.go — do not edit` (`dune-project`: header on line 2, `(lang dune 3.21)` must be line 1) | no |
| codegen | owned directories (§03.6) | per generator | no (`.gitignore` with `*`) |

Not-committed files are kept out of git by a bob-managed block in `.git/info/exclude`
(`# >>> bob generated files (do not edit) >>>` … `# <<< bob <<<`); bob never edits a
user-owned `.gitignore`. **[open]** Alternative: require users to commit a `.gitignore` entry;
recommendation: `.git/info/exclude` (no user file touched, local to the clone).

#### R-FW-061 Drift is detected before every build and rejected
- **Statement:** Before `bob build`, `bob test`, `bob dev` and `bob gen`, bob regenerates every
  file of the table in memory and compares bytes with the working tree; any difference fails
  with `E-DRIFT` (BOB-E-1011) listing each difference in bob.go terms; `bob gen --force` overwrites (backup
  in `.bob/gen/backup/<UTC time>/`).
- **Acceptance:** Given the Go fixture, when `go get golang.org/x/text@v0.20.0` is simulated by
  editing go.mod/go.sum (chmod + write), then `bob build //...` exits 3 (BOB-E-1011) before any Nix call,
  prints exactly the template below (golden), and leaves the files unchanged; `bob gen --force`
  restores them byte-identical and moves the edited copies to the backup dir. Same pattern per
  ecosystem with one golden each (Cargo.toml dep added, package.json dep added, tsconfig option
  changed, `.merlin` edited).
- **Source:** R-FW-006, R-GO-003, D2, S2 §5.4.

```
✗ api/go.mod or go.sum was changed outside bob (they are generated from bob.go)
    ~ go 1.26.0 → 1.25.0 (set Go in api/bob.go)
    ~ golang.org/x/sync v0.24.0 → v0.25.0  (api/bob.go declares v0.24.0)
    + golang.org/x/text v0.20.0  (not declared in bob.go)
  hint: declare Go modules in bob.go (golang.Require("golang.org/x/text", "v0.20.0")) and run `bob lock`;
        `go get` and `go mod tidy` are not used with bob. To discard the edit: bob gen --force
```

A missing generated file is not drift: bob rewrites it silently.

### §03.0.3 Lock freshness

#### R-FW-062 Each ecosystem section records the declaration it was resolved from
- **Statement:** Every lock section stores `inputs`: the sha256 of the canonical JSON of the
  ecosystem's declared external requirements (names, version requirements, options that affect
  resolution). If it differs from the current declarations, `bob build`/`bob test` fail with
  `E-LOCK-STALE` (BOB-E-1012, "run bob lock") unless `--lock` is given; `bob dev` and `bob lock` re-resolve.
- **Acceptance:** Given a locked fixture, when a requirement is added in bob.go and `bob build`
  runs, then it exits 3 with `E-LOCK-STALE` (BOB-E-1012) naming the ecosystem and the bob.go position of the
  changed declaration, and makes no network request (harness: network namespace without
  routes); `bob lock` then succeeds and a second `bob lock` without changes makes no registry
  request when caches are warm and `--refresh` is not given, and rewrites nothing.
- **Source:** R-FW-004, R-FW-005, R-NIX-011, R-DEV-006.

### §03.0.4 Fetching and bob-builder job kinds

- Every external source is a **content-addressed store path imported by bob** whose hash comes
  from bob.lock. bob's fetcher (§01 R-NIX-014 `engine/fetch.Fetcher`) downloads, verifies the
  registry checksum **and** the lock hash, and imports the result (`nix store add` /
  `nix-store --import`, §01 R-NIX-009) at exactly the path a fixed-output derivation with the
  same name and hash would have (path identity, §01 R-NIX-008). bob generates no fixed-output
  derivation for these sources, so Nix never executes an ecosystem fetcher (S13: FODs get host
  network; workers never build user FODs). "FOD" in R-GO-002, R-NPM-002 and R-CC-005 is met by
  this path identity.

#### R-FW-063 Ecosystem fetches never run as Nix builds
- **Statement:** For all ecosystem sources (Go modules, crates, npm tarballs, opam sources and
  repository snapshot, `cc.External` sources), the output is realised by bob before `nix build`.
- **Acceptance:** Given each fixture with an empty store and network, when `bob build //...`
  runs, then no generated derivation of this section has `outputHash`, the Nix internal-json log
  contains no `build` activity for a source path, and every source path equals
  `drv.FixedOutputPath` for (name, hash mode, lock hash) and the path of `nix store add` of the
  same tree (flat: `--mode flat`).
- **Source:** R-FW-021, S13, ADR 0001.

bob-builder job kinds needed by this section (names are the `kind` field of `bobSpec`):

| Kind | Ecosystem | Purpose |
|---|---|---|
| `cc-compile-link` | C/C++ | compile all sources of one library/binary, archive or link |
| `cc-external` | C/C++ | configure/build/install a CMake/autotools/Meson/Make project |
| `go-std`, `go-tools` | Go | std library (`go list -export -deps std`) / `cmd/pack` |
| `go-compile`, `go-cgo`, `go-link`, `go-testmain`, `test-run` | Go | §03.2.3 |
| `rustc`, `rust-build-run` | Rust | §03.3.3 |
| `npm-unpack`, `npm-script`, `npm-node-modules`, `npm-tool` | npm | §03.4.3 |
| `opam-build`, `ml-compile`, `ml-link` | OCaml | §03.5.3 |
| `codegen` | codegen | run one generator with a bob-written config |
| `test-run` | all | run a test binary, write `TestResult` |

### §03.0.5 Tests are packages (all ecosystems)

Each test constructor yields one **test derivation per test unit** (Go: package; Rust: crate
test target; npm: test file set of one importer; OCaml: test executable; C/C++: test binary).
The run derivation (`test-run` job) executes the test binary with `HOME=$TMPDIR`, no network
(loopback allowed for `bob.Services`, R-FW-025), and writes `$out/test-result.json`:

```go
// TestResult is the output of every test-run derivation (schema version 1).
type TestResult struct {
	Version  int          `json:"version"`  // 1
	Label    string       `json:"label"`    // //pkg/foo:foo_test
	Passed   bool         `json:"passed"`
	Duration float64      `json:"durationSeconds"`
	Cases    []TestCase   `json:"cases"`    // empty if the framework gives no per-case data
}
type TestCase struct {
	Name     string  `json:"name"`
	Result   string  `json:"result"` // "pass" | "fail" | "skip"
	Duration float64 `json:"durationSeconds"`
	Output   string  `json:"output,omitempty"` // failing cases only, ≤ 64 KiB each
}
```

A failing test makes the derivation fail after writing the result to the build log (so
failures are never cached); passing results are cached and substitutable (R-FW-024).

Every test unit has a timeout: 10 minutes by default; a test constructor may set another
(`timeoutSeconds` of the `test-run` job, 0 = the default; ecosystems expose it as a
`Timeout time.Duration` field of their test options). A test still running at its timeout is
stopped and fails with BOB-E-2007 `<label> timed out after <n>s` (its result goes to the build
log, nothing is cached). [A92]

#### R-FW-064 Test results are cached per test unit
- **Acceptance:** Given the polyglot fixture, when `bob test //...` runs twice, then the second
  run builds 0 derivations, exits 0 and prints the cached result per unit; after editing one Go
  test file only that package's test derivation (compile + run) rebuilds.
- **Source:** R-FW-024.

### §03.0.6 Quirks and mapping tables (common admission rule)

bob ships three small Go tables: npm quirks (§03.4.5), Rust native-crate defaults (§03.3.6),
OCaml `conf-*` mapping (§03.5.5). Each entry has `Why` (justification, shown with `-v`) and a
`Test` run in bob's CI. A CI check fails if an entry lacks either, and a release note lists the
table size.

---

## §03.1 C / C++ (`pkg/cc`)

No spike measured C/C++ alone; the design follows backend B (gen-b §2, §7–8) and S7 (cross,
variants). Compilers are the nixpkgs cc-wrapper (`stdenv.cc`, gcc 15.3 at the pin), called
directly by bob-builder; no stdenv, no setup hooks. The wrapper adds RUNPATH for each `-L` store
directory (gen-b §2), so outputs run under `env -i`.

### §03.1.1 API

```go
package cc // github.com/megakuul/bob/pkg/cc

// Linkable is anything a C/C++/cgo/Rust/OCaml consumer can link: bob C/C++ libraries,
// Rust StaticLib/CDylib, cc.External, nixpkgs.Lib. Alias of the core type (§01).
type Linkable = bob.Linkable

// Std is the language standard (-std=…).
type Std string

const (
	C11 Std = "c11"; C17 Std = "c17"; C23 Std = "c23"
	CXX17 Std = "c++17"; CXX20 Std = "c++20"; CXX23 Std = "c++23"
)

// LibraryOpts configures StaticLibrary and SharedLibrary.
type LibraryOpts struct {
	Srcs     bob.Sources         // .c .cc .cpp .cxx .S; required
	Hdrs     string              // public include dir, relative to the bob.go dir ("include"); "" = none
	Private  bob.Sources         // private headers (not exported to consumers)
	Std      Std                 // default C17 for .c, CXX20 for C++ sources
	Defines  map[string]string   // -DKEY=VALUE ("" value = -DKEY)
	Flags    []string            // extra compile flags (validated: no -o, -I/nix/store, -fplugin)
	LinkOpts []string            // extra link flags (shared libs and binaries only)
	Deps     []Linkable          // public deps: their headers propagate to consumers
	PrivDeps []Linkable          // private deps: linked, headers not propagated
	Platforms []bob.Platform     // restrict; default all (§04)
}

// StaticLibrary builds lib<name>.a. Label //<dir>:<name>.
//
//	var Foo = bob.Define(func(p *bob.P) *cc.Library {
//		return cc.StaticLibrary(p, "foo", cc.LibraryOpts{Srcs: bob.Glob("src/*.cpp"), Hdrs: "include"})
//	})
func StaticLibrary(p *bob.P, name string, o LibraryOpts) *Library

// SharedLibrary builds lib<name>.so (soname lib<name>.so).
//
//	var Foo = bob.Define(func(p *bob.P) *cc.Library {
//		return cc.SharedLibrary(p, "foo", cc.LibraryOpts{
//			Srcs: bob.Glob("src/*.cpp"), Hdrs: "include", Std: cc.CXX20,
//			Deps: []cc.Linkable{nixpkgs.Lib(p, "openssl")},
//		})
//	})
func SharedLibrary(p *bob.P, name string, o LibraryOpts) *Library

// BinaryOpts configures NewBinary and NewTest.
type BinaryOpts struct {
	Srcs     bob.Sources
	Std      Std
	Defines  map[string]string
	Flags    []string
	LinkOpts []string
	Deps     []Linkable
	Platforms []bob.Platform
}

// NewBinary builds bin/<name> (type Binary).
//
//	var Tool = bob.Define(func(p *bob.P) *cc.Binary {
//		return cc.NewBinary(p, "tool", cc.BinaryOpts{Srcs: bob.Glob("*.c"), Deps: []cc.Linkable{foo.Foo(p)}})
//	})
func NewBinary(p *bob.P, name string, o BinaryOpts) *Binary

// NewTest builds a binary (type Test) and runs it in a test-run derivation; exit 0 = pass.
// Output is parsed as TAP if it starts with "TAP version", else one case named <name>.
//
//	var FooTest = bob.Define(func(p *bob.P) *cc.Test {
//		return cc.NewTest(p, "foo_test", cc.BinaryOpts{Srcs: bob.Files("foo_test.cpp"), Deps: []cc.Linkable{Foo(p)}})
//	})
func NewTest(p *bob.P, name string, o BinaryOpts) *Test

// Library implements Linkable. Shared libraries also expose the .so for bundling.
type Library struct{ /* unexported */ }
func (l *Library) LinkInfo() bob.LinkInfo
```

`bob.LinkInfo` (requested from §01) carries: include dirs (store paths), link dirs, link
names (`-l`), static archives (absolute paths), extra link args, runtime dirs (RUNPATH),
`pkg-config` dirs, and the transitive public set; consumers use it uniformly (cgo `CDeps`,
Rust `Native`, OCaml `Native`). `nixpkgs.Lib(p, "openssl")` links all pkg-config modules
of the package's dev output by default; `nixpkgs.Lib(p, "openssl", nixpkgs.Modules("libcrypto"))`
narrows (gen-b §8 IR gap).

### §03.1.2 `cc.External` (R-CC-005)

```go
// BuildSystem selects how an External is built inside its derivation.
type BuildSystem int

const (
	CMake BuildSystem = iota + 1 // cmake -G Ninja -DCMAKE_INSTALL_PREFIX=$out -DCMAKE_BUILD_TYPE=<profile>
	Autotools                    // ./configure --prefix=$out (autoreconf -fi if no configure)
	Meson                        // meson setup --prefix=$out --buildtype=<profile>; ninja
	Make                         // make PREFIX=$out install
)

// ExternalOpts describes a third-party C/C++ project with its own build system.
type ExternalOpts struct {
	URL     string        // https archive (.tar.gz .tar.xz .tar.bz2 .tar.zst .zip); required
	Strip   int           // leading path components to strip; default 1
	Build   BuildSystem   // required
	Args    []string      // extra configure args (-DFOO=ON, --disable-x, -Dfoo=false)
	Deps    []Linkable    // available to the build (CMAKE_PREFIX_PATH, PKG_CONFIG_PATH, CFLAGS/LDFLAGS)
	Tools   []*nixpkgs.Package // extra host tools (nixpkgs.Tool(p, "perl"))
	Libs    []string      // link names exported to consumers; default: from installed .pc files, else all lib*.so/lib*.a
	Include string        // include dir under $out; default "include"
}

// External fetches URL (pinned by narHash in bob.lock) and builds it with its own build
// system inside one derivation. To consumers it is an ordinary Linkable.
//
//	var Zstd = bob.Define(func(p *bob.P) *cc.External {
//		return cc.External(p, "zstd", cc.ExternalOpts{
//			URL:   "https://github.com/facebook/zstd/releases/download/v1.5.7/zstd-1.5.7.tar.gz",
//			Build: cc.CMake, Args: []string{"-S", "build/cmake", "-DZSTD_BUILD_PROGRAMS=OFF"},
//		})
//	})
func External(p *bob.P, name string, o ExternalOpts) *ExternalLib
```

Lock entry `lock.CExternal` (§03.7.6), keyed by label. Host tools come from nixpkgs (`cmake`,
`ninja`, `meson`, `autoconf`, `automake`, `libtool`, `gnumake`, `pkg-config`) and are inputs
only for the selected build system.

#### R-CC-005a cc.External sources are pinned and verified before building
- **Statement:** `bob lock` downloads the URL, unpacks it with `Strip`, normalises modes, and
  records `narHash`; `bob build` refuses an External whose URL is not in bob.lock or whose
  lock entry URL differs, and the fetcher refuses content whose NAR hash differs.
- **Acceptance:** Given the cc fixture with a zstd External, when (a) the entry is removed from
  bob.lock, then `bob build` exits 3 with BOB-E-1009 (no pinned entry) before Nix runs; when (b) the served
  archive is replaced by different bytes (harness HTTP server), then the build exits 4 with
  `E-FETCH-HASH` (BOB-E-3001) naming the label, URL, expected and actual hash; when (c) unchanged, a C
  consumer linking `Zstd(p)` builds and runs under `env -i`.
- **Source:** R-CC-005, D44, R-NIX-011.

### §03.1.3 Derivation layout

| Derivation | Name | Job | Inputs | Output |
|---|---|---|---|---|
| sources | `cc-src-<label>` | source import | the package's Srcs + Private + Hdrs files | store path with the files at bob.go-relative paths |
| library / binary | `cc-<name>[-<projection>]` | `cc-compile-link` | sources, cc-wrapper, binutils, deps' `LinkInfo` paths | `lib/lib<name>.{a,so}` + `include/` (copy of Hdrs) / `bin/<name>` |
| external source | `ccx-src-<name>-<narhash8>` | bob-imported source path (FOD-identical, recursive sha256) | — | unpacked tree |
| external build | `ccx-<name>[-<projection>]` | `cc-external` | source, build-system tools, deps | install tree (`lib`, `include`, `lib/pkgconfig`, `bin`) |
| test run | `cc-<name>-run` | `test-run` | test binary | `test-result.json` |

- Compile flags (fixed order): `-std=<Std>`, profile flags from §04 (release `-O2`, debug
  `-O0 -g`), sanitiser flags from §04, `-fPIC` for shared libs and for static libs consumed by
  shared outputs (default on), `-ffile-prefix-map=<src store path>=<label dir>`, Defines
  sorted by key, `-I` own Private/Hdrs, then `-I` of deps' include dirs in Deps order then
  transitive order, then Flags. Objects are compiled in parallel inside the derivation
  (`NIX_BUILD_CORES`).
- Shared libraries are linked with `-Wl,-soname,lib<name>.so`; RUNPATH comes from the wrapper.
- Granularity: one derivation per library/binary in v1. **R-CC-004** (per-object derivations):
  **[open]**, recommendation: keep per-library in v1 and measure per-object on a 200-file C++
  library in Stage 3 (adopt if one-file change is ≥ 2× faster on [2c/4G] and cold cost ≤ 1.3×);
  the `cc-compile-link` job already records per-object commands so a split stays local.

#### R-CC-001a C/C++ libraries, binaries and tests build hermetically
- **Acceptance:** Given `tests/fixtures/repos/cc` (static lib, shared lib using nixpkgs
  openssl, binary linking both, a test, an External), when `bob build //... && bob test //...`
  runs for linux-arm64 and linux-amd64 (§04 cross), then all succeed, the binary runs under
  `env -i` (native or qemu-user), and a rebuild on a second store is bit-identical.
- **Source:** R-CC-001, R-NFR-006.

#### R-CC-002a Typed options map to exact flags
- **Acceptance:** Golden test of the `bobSpec` job of each fixture package (flag order above);
  passing `Flags: []string{"-I/nix/store/x"}` fails at evaluation with
  `✗ //cc/foo:foo: flag "-I/nix/store/x" is not allowed; use Deps to add include paths`.
- **Source:** R-CC-002, R-NIX-013.

### §03.1.4 `compile_commands.json` (R-CC-003)

One file at the repo root, an array sorted by `file`, one entry per source file of every
C/C++ package and of every cgo package (its C files)
for the **host** platform and default variant:

```json
[{"directory": "/home/me/repo",
  "file": "libs/foo/src/foo.cpp",
  "arguments": ["/nix/store/…-gcc-wrapper-15.3.0/bin/g++", "-std=c++20", "-O2", "-fPIC",
                "-Ilibs/foo/include", "-I/nix/store/…-openssl-3.6.1-dev/include",
                "-c", "libs/foo/src/foo.cpp", "-o", "/dev/null"],
  "output": "/dev/null"}]
```

Own sources and own headers point to the working tree (repo-relative to `directory`);
dependency include dirs point to the store. clangd is started through `.bob/bin/clangd`
(§02), so the store paths resolve.

#### R-CC-003a compile_commands.json lets clangd resolve every include
- **Acceptance:** Given the cc fixture after `bob gen`, when `.bob/bin/clangd --check=libs/foo/src/foo.cpp`
  runs as a non-root user on a host without `/nix`, then it reports 0 errors and resolves
  `<openssl/ssl.h>`; the file equals the golden after replacing `directory`.
- **Source:** R-CC-003, D59.

---

## §03.2 Go (`pkg/golang`) — ADR 0004

### §03.2.1 API

```go
package golang // github.com/megakuul/bob/pkg/golang ("go" is a keyword)

// ModuleOpts describes one Go module; its go.mod/go.sum are generated from it (R-GO-003).
type ModuleOpts struct {
	Path      string          // module path, required ("example.com/api")
	Go        string          // "1.26": go directive (written "1.26.0") and toolchain minor, required (R-GO-004)
	Toolchain *nixpkgs.Package // default nixpkgs.Tool(p, "go_1_26"); must report version 1.26.x
	Require   []Requirement   // direct third-party requirements, exact versions (R-GO-002)
	Generated []Generated     // codegen outputs materialised into this module (§03.6)
	Tags      []string        // build tags for every package of the module
}

// Requirement is one direct module requirement.
type Requirement struct{ Path, Version string }

// Require returns a requirement; version must be a canonical semver ("v1.6.0") or a
// pseudo-version; ranges and "latest" are rejected at evaluation.
func Require(path, version string) Requirement

// NewModule declares the module whose root is the directory of the calling bob.go.
// Library packages need no declaration: bob follows the imports of binaries and tests.
// Label //<dir>:module.
//
//	var Module = bob.Define(func(p *bob.P) *golang.Module {
//		return golang.NewModule(p, golang.ModuleOpts{
//			Path: "example.com/api", Go: "1.26",
//			Require: []golang.Requirement{golang.Require("github.com/google/uuid", "v1.6.0")},
//		})
//	})
func NewModule(p *bob.P, o ModuleOpts) *Module

// BinaryOpts configures a main package.
type BinaryOpts struct {
	Name    string            // default: last element of dir
	Tags    []string          // added to the module's tags
	X       map[string]string // -ldflags -X importpath.name=value (sorted)
	CGo     CGo               // CGoAuto (default: on iff a package in the closure imports "C"), CGoOff
}

// CGo selects cgo for a binary's closure.
type CGo int

const (
	CGoAuto CGo = iota // on iff a package in the closure imports "C"
	CGoOff             // pure Go; importing "C" is an evaluation error
)

// Binary declares the main package in dir (module-relative, "./cmd/api").
// Label //<module dir>/<dir>:<Name>.
//
//	var API = bob.Define(func(p *bob.P) *golang.Binary {
//		return Module(p).Binary("./cmd/api", golang.BinaryOpts{})
//	})
func (m *Module) Binary(dir string, o BinaryOpts) *Binary

// CgoOpts gives a cgo package its typed C/C++/Rust dependencies (R-GO-001, R-RS-001).
type CgoOpts struct {
	Deps []cc.Linkable // -I → CGO_CFLAGS of this package; link info → final external link
}

// Cgo attaches C dependencies to the cgo package in dir. Packages that import "C" without
// a Cgo declaration may use only libc and their own C files.
//
//	var _ = bob.Define(func(p *bob.P) *golang.Package {
//		return Module(p).Cgo("./internal/ffi", golang.CgoOpts{Deps: []cc.Linkable{foo.Lib(p), rcore.Crate(p)}})
//	})
func (m *Module) Cgo(dir string, o CgoOpts) *Package

// Tests declares one test unit per package matching pattern ("./..." or "./pkg/x") that
// has _test.go files. Label //<module dir>:<name> ("tests" by default), units //<pkg dir>:go_test.
//
//	var Tests = bob.Define(func(p *bob.P) *golang.TestSet { return Module(p).Tests("./...", golang.TestOpts{}) })
func (m *Module) Tests(pattern string, o TestOpts) *TestSet

type TestOpts struct {
	Name     string
	Tags     []string
	Services []bob.Service // R-FW-025 (§01)
	Args     []string      // passed to the test binary (-test.run=…)
}

// CArchive / CShared build -buildmode=c-archive / c-shared of the main package in dir;
// the result implements cc.Linkable.
func (m *Module) CArchive(dir string, o BinaryOpts) *CLib
func (m *Module) CShared(dir string, o BinaryOpts) *CLib
```

One constructor per kind (R-FW-014); granularity is not user-configurable.

#### R-GO-001a Go binaries, cgo, tests and c-archives build per package
- **Acceptance:** Given `tests/fixtures/repos/go` (3 modules, a cgo package linking a cc shared
  library and a Rust staticlib, a protobuf codegen package, embeds, a test per package, an
  assembly package, a `.syso`), when `bob build //... && bob test //...` runs natively and
  cross (§04), then all succeed, outputs run under `env -i`, and every Go derivation rebuilds
  bit-identically on a second store.
- **Source:** R-GO-001, R-NFR-006, S2 §3.1.

### §03.2.2 Planning (evaluation time)

1. bob reads Go sources in-process (`go/build` contexts for the target GOOS/GOARCH, module tags,
   cgo on/off; `bob.go` excluded by the `bob` tag) and resolves imports itself: std (from the
   toolchain's `go list -export -deps std` index cached per toolchain), workspace module
   packages, generated packages (§03.6, source = codegen output path), third-party modules
   from the build list (§03.2.4). No `go list`, no network (S2-1).
2. Third-party module trees are realised before planning (two phases: fetch, then plan; S2-2).
3. Groups: one group per third-party **module** (only packages reachable from workspace
   imports), one per workspace **package**. Module groups that import each other cyclically
   are merged into one group (Tarjan SCC over module-level edges).
4. Unsupported in v1 (evaluation error with hint): `#cgo pkg-config:` (hint: use
   `Cgo(…, CgoOpts{Deps: nixpkgs.Lib(…)})`), `//go:linkname` to unexported std symbols in
   workspace code is allowed (it is a compiler matter), `replace` directives (none exist:
   go.mod is generated), `vendor/` directories (ignored with a note).

### §03.2.3 Derivation layout

| Derivation | Name | Job | Key inputs | Output |
|---|---|---|---|---|
| module source | `gomod-<path>-<version>` (`drv.Name`) | bob-imported source path (FOD-identical), recursive sha256 = lock `tree` | — | unpacked, mode-normalised module tree |
| std | `go-std-<goversion>-<goos>-<goarch>[-cgo][-<projection>]` | `go-std` | toolchain, cc (cgo), tags/flags | `$out/pkg/<importpath>.a` for all std + `$out/importcfg` |
| tools | `go-tools-<goversion>` | `go-tools` | toolchain | `bin/pack` |
| third-party group | `go-<module>-<version>[-<projection>]` | `go-compile` / `go-cgo` | module tree, std, deps' groups | `$out/pkg/<importpath>.a` per used package |
| workspace package | `go-<importpath>[-<projection>]` | `go-compile` / `go-cgo` | that package's files only (own source import), std, deps' archives, cgo: cc + `Cgo` deps | `$out/pkg/<importpath>.a` |
| link | `go-link-<name>[-<projection>]` | `go-link` | all transitive archives, C deps | `bin/<name>` only (no `.a`) |
| test compile+link | `go-test-<importpath>` | `go-testmain` | package + `_test.go` files, test deps | `bin/<pkg>.test` |
| test run | `go-test-<importpath>-run` | `test-run` | test binary, services | `test-result.json` |

Exact compile rules (S2 §5.2): `-p <importpath>`, `-lang=go<module go directive major.minor>`,
`-trimpath "<src>=><module>@<version>/<rel>"` (workspace: `<module>/<rel>`), `-nolocalimports`,
`-complete` when no cgo/asm, `-embedcfg` for embeds, `-importcfg`, `-c=min(cores,4)`;
assembly: `asm -gensymabis` → `compile -symabis` → `asm` → `pack r`; `.syso` appended with
`pack r`. Work directories are `$TMPDIR/b<sha256(importpath)[:12]>` (grouping-independent).
cgo sequence = cmd/go's (`cgo` → gcc objects → `_cgo_.o` → `cgo -dynimport` → compile →
`pack r`); `CGO_CFLAGS` per package = `-I` of its `Cgo` deps (no global cgo flags); `-L/-l`
recorded as cgo ldflags and used by the external link. Link: `-buildid=`, `-extld=<cc>`,
`-buildmode=exe|c-archive|c-shared`, modinfo from bob.lock. Job specs larger than 64 KiB use
`passAsFile` (MAX_ARG_STRLEN). Tests: bob writes `_testmain.go` itself (template per Go
minor version, golden-tested against `go test -c -work` output of the pinned toolchain) and
runs `<pkg>.test -test.v=test2json` piped through `go tool test2json` into `TestResult`.
**[open]** If the testmain template proves fragile across Go minors, fall back to
`go test -c` inside the test-compile derivation with a bob-written GOPROXY; recommendation:
own template (fast, no cmd/go loading).

#### R-GO-002a Granularity is hybrid: per third-party module, per workspace package
- **Statement:** A one-file change in a workspace package rebuilds that package, packages whose
  inputs changed transitively, and links; never a third-party module group or std.
- **Acceptance:** Given the Go fixture, when `cmd/api/main.go` changes, then exactly 2 Go
  derivations rebuild (package + link); when `internal/msg/msg.go` changes, exactly the
  package, its importers in the workspace and the link rebuild (counted from the Nix log);
  synthetic 200-package workspace bottom-layer change rebuilds 36 package derivations + link.
- **Source:** R-GO-002, R-FW-020, S2 §4, quiet log `s02-go.log`.

#### R-GO-005 Early cutoff for Go export data (A11)
- **Statement:** The planner supports a `cutoff` mode where dependents compile against export
  data (`-linkobj` split) that bob adds to the store by content, so a change that keeps a
  package's export data byte-identical rebuilds only that package and the link.
- **Acceptance:** Given syn200 with cutoff enabled, when a bottom-layer function body (not
  inlinable) changes, then 1 package derivation + link rebuild and the binary is bit-identical
  to the non-cutoff build.
- **Default:** **[open]** off in v1; recommendation: enable by default when the Stage 3
  measurement on bob's own repo (20 sampled one-file commits, [2c/4G]) shows a median gain
  ≥ 1.3× and cold cost ≤ 1.4× (quiet log: syn200 bottom-layer 3.03 → 1.76 s, but main-only
  1.22 → 1.74 s and cold 10.2 → 13.9 s [8c/15G]).
- **Source:** A11, S2 §4.4, `s02-go.log`.

### §03.2.4 Resolution (`bob lock`) and the lock entry

- Build list per workspace module = Minimal Version Selection over `.mod` files fetched from
  the configured GOPROXY (default `https://proxy.golang.org`, configurable in bob's user
  config, `direct` not supported in v1), with cmd/go's graph pruning for `go ≥ 1.17`
  modules; includes modules needed by tests of workspace packages; excludes nothing (no
  `exclude`/`retract` handling beyond warning on retracted versions).
- For every module of the pruned graph: `.mod` hash (`modH1`). For modules that provide
  packages to the build: the zip, its `h1:` (dirhash of the zip content), and `tree` (NAR
  sha256 of the unpacked tree, files 0444/0555, dirs 0555).
- Errors: `E-RESOLVE` (BOB-E-1015; `✗ cannot resolve Go modules for example.com/api: golang.org/x/net@v0.99.0: not found on proxy.golang.org`),
  toolchain check (BOB-E-1008; `✗ github.com/foo/bar@v1.2.0 needs go 1.27.0, but example.com/api uses go 1.26 (set Go in api/bob.go)`).

#### R-GO-002b bob's build list equals the go command's
- **Acceptance:** On a corpus of ≥ 10 real modules (incl. the fixture, bob itself,
  kubernetes/client-go, grpc-go), with bob's generated go.mod/go.sum and its GOPROXY dir,
  `go list -m all` (GOFLAGS=-mod=readonly, GOSUMDB=off, GOTOOLCHAIN=local) exits 0 and lists
  exactly bob's build list, plus one `<path> v0.0.0 => <dir>` line per workspace module of the
  repository it imports (§03.2.5); `go build ./... && go vet ./...` succeed without network.
- **Source:** R-GO-002, R-GO-003, S2 §3.5.

`lock.GoModule` and the `go` section:

```go
// GoSection is bob.lock's "go" section.
type GoSection struct {
	Inputs     string                 `json:"inputs"`     // §03.0.3
	Proxy      string                 `json:"proxy"`      // "https://proxy.golang.org"
	Workspaces map[string]GoBuildList `json:"workspaces"` // key: workspace module path
	Modules    []GoModule             `json:"modules"`    // sorted by path, then semver
}
type GoBuildList struct {
	Go      string   `json:"go"`      // "1.26.0"
	Modules []string `json:"modules"` // "path@version", sorted; every module of the pruned graph
}
// GoModule is one third-party module version.
type GoModule struct {
	Path     string `json:"path"`
	Version  string `json:"version"`
	ModH1    string `json:"modH1"`              // h1: of the .mod file
	H1       string `json:"h1,omitempty"`       // h1: of the zip (only if Packages)
	Tree     string `json:"tree,omitempty"`     // SRI sha256 NAR hash of the unpacked tree (only if Packages)
	Go       string `json:"go,omitempty"`       // the module's own go directive
	Packages bool   `json:"packages"`           // provides packages to some build
}
```

```json
{"go": {"inputs": "sha256-…", "proxy": "https://proxy.golang.org",
  "workspaces": {"example.com/api": {"go": "1.26.0",
     "modules": ["github.com/google/go-cmp@v0.7.0", "golang.org/x/sync@v0.24.0"]}},
  "modules": [
   {"path": "github.com/google/go-cmp", "version": "v0.7.0",
    "modH1": "h1:pXiqmnSA92OHEEa9HXL2W4E7lf9JzCmGVUdgjX3N/iU=", "packages": false},
   {"path": "golang.org/x/sync", "version": "v0.24.0",
    "modH1": "h1:sUUOizhqBxiL6pEWpqNLUiaJn1ShEbZ6BBqskPbjZm0=",
    "h1": "h1:iqiDrGoDgyuvAKrhLQthUEzU/R378N78gXbBjxed7oA=",
    "tree": "sha256-H9UJMd7zzra4wt24bLTr3y/ooOhJeXlTmULNtrm3gdc=", "go": "1.23.0", "packages": true}]}}
```

The fetcher checks both `h1` and `tree`; proxy zips are not byte-stable across sources, so
the zip is never the imported path (S2 §3.6).

### §03.2.5 go.mod / go.sum generation (R-GO-003)

1. go.mod: the two header lines (§03.0.2), `module <Path>`, `go <X.Y.Z>`, one `require` block
   of direct requirements (Require + codegen runtimes §03.6.4 + the build-definition
   requirements §01 contributes for the module containing `bob.go` files), one block of
   indirect requirements with `// indirect`, both sorted by path. Another workspace module of
   this repository whose packages the module imports (directly or through that module's
   packages) is required at `v0.0.0` in the direct block and replaced by its relative
   directory (`replace example.com/lib => ../lib`, one line each, after the blocks): the only
   `replace` bob writes, so the go command finds it [A133]. No `toolchain`, other
   `replace`, `exclude`, `retract`.
2. go.sum: `<path> <version> <h1>` for each `packages:true` module and `<path> <version>/go.mod <modH1>`
   for every module of the pruned graph, sorted bytewise by line, `\n` endings.
3. User environment (`bob env`, shims, `bob dev`; §02): `GOPROXY=file://$XDG_CACHE_HOME/bob/goproxy`
   (bob writes `<escaped path>/@v/{list,<v>.info,<v>.mod,<v>.zip}` for every lock module, case
   escaping `!x`; zips written by bob from the tree), `GOFLAGS=-mod=readonly`, `GOSUMDB=off`,
   `GOTOOLCHAIN=local`, `GOROOT`/`PATH` → pinned toolchain, `CC` → cc-wrapper,
   `CGO_CFLAGS`/`CGO_LDFLAGS` = union over the module's `Cgo` deps; gopls gets `-tags=bob`
   (ADR 0003).

#### R-GO-003a Go tooling works on generated files, edits are blocked
- **Acceptance:** In the Go fixture via shims as a non-root user: `go build ./...`, `go vet ./...`,
  `gopls check` on a file importing a third-party and a generated package succeed offline;
  `go get golang.org/x/text@latest` fails (read-only go.mod or proxy miss) and leaves files
  unchanged; `go mod tidy` is detected as drift at the next `bob build` (R-FW-061).
- **Source:** R-GO-003, S2 §3.5.

#### R-GO-004a Toolchain per module
- **Acceptance:** Given two modules with `Go: "1.25"` and `Go: "1.26"`, then each package is
  compiled with its module's toolchain (job golden shows the toolchain store path; a package
  imported by a module with another Go is also compiled in the importer's closure with the
  importer's toolchain at its own module's `-lang`, since Go cannot import an archive of
  another toolchain; amended 2026-10-22, A102), std is
  built once per toolchain variant, and a module requirement whose `go` directive exceeds the
  module's Go fails at `bob lock` with the toolchain error above.
- **Source:** R-GO-004.

---

## §03.3 Rust (`pkg/rust`) — ADR 0005

### §03.3.1 API

```go
package rust // github.com/megakuul/bob/pkg/rust

type Edition string

const (
	Edition2018 Edition = "2018"; Edition2021 Edition = "2021"; Edition2024 Edition = "2024"
)

// Dep is a dependency of a crate: a crates.io crate (Crate) or a local crate (*Lib, (*Lib).With).
type Dep interface{ rustDep() }

// DepOpt modifies a crates.io dependency.
type DepOpt func(*depOpts)

// Crate is a crates.io dependency; req is a Cargo version requirement ("1", "^0.13", "=1.38.0").
// Resolved into bob.lock by `bob lock` (R-RS-002).
func Crate(name, req string, opts ...DepOpt) Dep
func Features(fs ...string) DepOpt  // enable features of the dependency
func NoDefaultFeatures() DepOpt     // default-features = false
func Rename(codeName string) DepOpt // `use codeName::…`

// Option is a graph-wide setting attached to any local crate's Options.
type Option interface{ rustOption() }

// Uses gives a crates.io crate's build script native packages (PATH, PKG_CONFIG_PATH,
// include/lib dirs). Applies to every unit of that crate in the graph.
//
//	rust.Uses("openssl-sys", nixpkgs.Lib(p, "openssl"))
func Uses(crate string, libs ...cc.Linkable) Option

// LibOpts configures Library, ProcMacro, StaticLib and CDylib.
type LibOpts struct {
	Name        string              // crate name; default directory name with '-' kept, code name '-'→'_'
	Edition     Edition             // default Edition2021
	Root        string              // default "src/lib.rs"
	Srcs        bob.Sources         // default bob.Glob("src/**", "build.rs")
	Deps        []Dep
	BuildDeps   []Dep               // build-dependencies (host units)
	DevDeps     []Dep               // tests only
	Features    map[string][]string // this crate's feature table (Cargo syntax: "dep:x", "x/f", "x?/f")
	Default     []string            // default features
	BuildScript string              // "build.rs" or "" (default: "build.rs" if the file exists)
	Native      []cc.Linkable       // bob C/C++ libs, cc.External or nixpkgs.Lib linked into this crate
	Headers     string              // StaticLib/CDylib only: include dir exported to C consumers
	Toolchain   *nixpkgs.Package     // default nixpkgs.Tool(p, "rustc") (R-RS-004)
	Options     []Option
}

// BinOpts configures Binary.
type BinOpts struct {
	Name, Root  string               // Root default "src/main.rs"
	Edition     Edition
	Srcs        bob.Sources
	Deps, BuildDeps, DevDeps []Dep
	Features    map[string][]string
	Default     []string
	Enable      []string             // features enabled when built as a root target
	BuildScript string
	Native      []cc.Linkable
	Toolchain   *nixpkgs.Package
	Options     []Option
}

// Library is an rlib, usable as Dep. Label //<dir>:<Name>.
//
//	var Core = bob.Define(func(p *bob.P) *rust.Lib {
//		return rust.Library(p, rust.LibOpts{Deps: []rust.Dep{rust.Crate("serde", "1", rust.Features("derive"))}})
//	})
func Library(p *bob.P, o LibOpts) *Lib

// ProcMacro is a host-compiled proc-macro crate, usable as Dep.
//
//	var Macros = bob.Define(func(p *bob.P) *rust.Lib {
//		return rust.ProcMacro(p, rust.LibOpts{Deps: []rust.Dep{rust.Crate("syn", "2", rust.Features("full")), rust.Crate("quote", "1")}})
//	})
func ProcMacro(p *bob.P, o LibOpts) *Lib

// With enables features of a local crate for this edge.
func (l *Lib) With(features ...string) Dep

// Binary builds bin/<Name>.
//
//	var Demo = bob.Define(func(p *bob.P) *rust.Bin {
//		return rust.Binary(p, rust.BinOpts{Deps: []rust.Dep{rust.Crate("tokio", "1", rust.Features("rt", "macros")), Core(p)}})
//	})
func Binary(p *bob.P, o BinOpts) *Bin

// StaticLib builds lib<name>.a (+ Headers); implements cc.Linkable for C and cgo consumers.
//
//	var RCore = bob.Define(func(p *bob.P) *rust.CLib {
//		return rust.StaticLib(p, rust.LibOpts{Name: "rcore", Headers: "include"})
//	})
func StaticLib(p *bob.P, o LibOpts) *CLib

// CDylib builds lib<name>.so; implements cc.Linkable.
//
//	var Plugin = bob.Define(func(p *bob.P) *rust.CLib { return rust.CDylib(p, rust.LibOpts{Name: "plugin"}) })
func CDylib(p *bob.P, o LibOpts) *CLib

// TestOpts selects what to test: the unit tests of Crate (--test) and every tests/*.rs
// integration test (one unit per file).
type TestOpts struct {
	Crate    interface{ rustCrate() } // *Lib, *Bin or *CLib
	Args     []string
	Services []bob.Service
}

// Test declares the tests of a local crate. Units //<dir>:<crate>_test and
// //<dir>:<crate>_test_<file stem>.
//
//	var CoreTest = bob.Define(func(p *bob.P) *rust.TestSet { return rust.Test(p, rust.TestOpts{Crate: Core(p)}) })
func Test(p *bob.P, o TestOpts) *TestSet // TestSet: Go cannot name a type and a func alike (A122)
```

Features also come from the variant (`bob.Variant.Features`, §04) and are added on top of
`Enable`/edge features.

### §03.3.2 Resolution (`bob lock`) and evaluation-time features

- Index: crates.io sparse index `https://index.crates.io/` (ETag/`Last-Modified` cache under
  `$XDG_CACHE_HOME/bob/crates-index`); alternative registries and git/path-to-outside-repo
  dependencies are rejected in v1.
- Lockfile resolver (S3 §5.4): one resolution for all local crates of the repo; all features
  of local crates; all platforms; dev-deps of local crates; weak `x?/f` counts as a dependency;
  one version per semver-compatibility class (`1.x`, `0.y`, `0.0.z`); highest non-yanked match;
  prefer versions with `rust_version` ≤ toolchain; at most one crate per `links` value; then
  download each crate once, verify the index `cksum`, unpack, compute `narHash`, read `meta`.
- Evaluation (each build): Cargo resolver-2 feature unification per (crate, platform role);
  build-deps, proc-macros and their deps are **host** units; target-specific deps filtered by
  bob's built-in cfg tables per triple; dev-deps only for test units.

#### R-RS-002a bob's resolution equals Cargo's
- **Acceptance:** On a corpus of ≥ 10 workspaces (incl. the 4 S3 graphs: 12, 25, 173, 393
  packages), the Cargo.lock bob generates is byte-identical to `cargo generate-lockfile`
  (pinned cargo, same index snapshot, CI corpus job); cfg tables equal `rustc --print cfg
  --target <t>` of the pinned toolchain for both targets.
- **Source:** R-RS-002, S3 §3.2.

#### R-RS-005 bob.lock carries the crate graph facts (D58)
- **Statement:** Each crate entry stores edges, feature table, `links` and the Cargo.toml facts
  needed to plan, so evaluation needs no network and no crate sources.
- **Acceptance:** Given the Rust fixture locked, with network disabled and an empty crate
  cache, `bob build --dry-run //...` (plan only) succeeds and prints the same derivation set
  as with sources present. *Clarification (Gate H, ruling G24):* `--dry-run` (plan only, no
  build) is added to the `bob build` flags in §02 by the milestone that builds it; "empty crate
  cache" means a fresh `BOB_HOME` in a user+net namespace.
- **Source:** D58, O-S3-1 (a), S3-2.

`lock.Crate` and the `rust` section:

```go
// RustSection is bob.lock's "rust" section.
type RustSection struct {
	Inputs string  `json:"inputs"`
	Index  string  `json:"index"`  // "sparse+https://index.crates.io/"
	Crates []Crate `json:"crates"` // sorted by name, then semver
}
// Crate is one crates.io crate version.
type Crate struct {
	Name     string              `json:"name"`
	Version  string              `json:"version"`
	Checksum string              `json:"checksum"`        // SRI sha256 of the .crate (= index cksum)
	NarHash  string              `json:"narHash"`         // SRI sha256 NAR hash of the unpacked source
	Links    string              `json:"links,omitempty"`
	Deps     []CrateDep          `json:"deps,omitempty"`
	Features map[string][]string `json:"features,omitempty"`
	Meta     CrateMeta           `json:"meta"`
}
type CrateDep struct {
	Name            string   `json:"name"`                      // name used in code (after rename)
	Crate           string   `json:"crate"`                     // resolved "name@version"
	Kind            string   `json:"kind,omitempty"`            // "" (normal) | "build" | "dev"
	Target          string   `json:"target,omitempty"`          // cfg expression or triple
	Optional        bool     `json:"optional,omitempty"`
	NoDefault       bool     `json:"noDefault,omitempty"`       // default-features = false
	Features        []string `json:"features,omitempty"`
}
type CrateMeta struct {
	Edition   string `json:"edition"`
	LibName   string `json:"libName,omitempty"`   // if ≠ name with '-'→'_'
	LibPath   string `json:"libPath,omitempty"`   // if ≠ "src/lib.rs"
	ProcMacro bool   `json:"procMacro,omitempty"`
	Build     string `json:"build,omitempty"`     // build script path
}
```

```json
{"name": "zstd-sys", "version": "2.1.2+zstd.1.5.7",
 "checksum": "sha256-<base64>", "narHash": "sha256-uQTfBw5nXEUrOTW0UY9fbKzVnYOU5x1IwZwZnw2J9D0=",
 "links": "zstd",
 "deps": [{"name": "cc", "crate": "cc@1.2.41", "kind": "build", "features": ["parallel"]},
          {"name": "pkg-config", "crate": "pkg-config@0.3.32", "kind": "build", "optional": true}],
 "features": {"default": ["legacy", "arrays", "zdict_builder"], "pkg-config": ["dep:pkg-config"]},
 "meta": {"edition": "2018", "build": "build.rs"}}
```

Optional deps no workspace feature can enable are omitted. Size guide: 392 crates ≈ 413 KB
indented JSON (S3 §5.3), written in §01 R-FW-004's canonical form (2-space indent, sorted
keys; crates sorted by name, version), which keeps diffs per crate reviewable.

### §03.3.3 Derivation layout per crate unit

Units are keyed by (crate, version or "local", host|target, features, projection).

| Derivation | Name | Job | Inputs | Output |
|---|---|---|---|---|
| source (registry) | `crate-<name>-<version>` | bob-imported source path (FOD-identical, recursive), hash = `narHash` (Fetcher, not a Nix fetch) | — | unpacked crate |
| build-script compile | `<name>-<version>[-host]-build-script` | `rustc` (bin, opt 0) | source, build-deps (host units) | `bin/build_script_build` |
| build-script run | `<name>-<version>[-host]-build-run` | `rust-build-run` | source, script, `links` deps' runs, `Uses`/`Native` packages | `out-dir/`, `output`, `directives.json` |
| compile | `<name>-<version>[-host][-<projection>]` | `rustc` | source, deps' outputs, own run, transitive runs (linking types) | `lib/lib<n>-<H>.rlib` / `.so` (proc-macro) / `lib/lib<n>.{a,so}` + `include/` / `bin/<n>` |
| test compile / run | `<name>-test[-<stem>]` / `…-run` | `rustc --test` / `test-run` | crate + dev-deps | test binary / `test-result.json` |

Toolchain inputs: nixpkgs `rustc`, `stdenv.cc`, `stdenv.cc.bintools`; nothing else (bob-builder
does the rest). rustc flags exactly as S3 §3.4 (`-C metadata=` first 16 hex of sha256(name,
version, source checksum or "local", role, features, toolchain id, deps' hashes, projection
tag); `--cap-lints allow` for registry crates; `--check-cfg` for local crates; host units
opt-level 0; `--remap-path-prefix=<store src>=<crate>-<version>` for registry and
`=<repo-relative dir>` for local crates). Build-script contract (env, directives, consumer side)
exactly as S3 §3.3; `rerun-if-*` ignored; `cargo::error` fails; unknown directives recorded
as `unsupported` and printed as a note. bob-builder is versioned separately from the CLI (it is
an input of every Rust derivation; changing it rebuilds all).

#### R-RS-001a Crates, build scripts, proc-macros and C interop
- **Acceptance:** Given `tests/fixtures/repos/rust` (S3 demo graph: serde+derive, clap+derive,
  tokio+macros, regex, zstd-sys with C, a local proc-macro, a StaticLib linked by the Go cgo
  fixture, openssl-sys via `rust.Uses`), when built natively and cross, then all outputs run
  under `env -i`, the cgo binary calls into the StaticLib, Cargo is never executed (no `cargo`
  in any derivation's inputs), and a second-store rebuild is bit-identical.
- **Source:** R-RS-001, R-RS-002, S3 §3.1.

### §03.3.4 Generated files (R-RS-003, A10)

- `rust-project.json` (repo root): `sysroot_src` = `<rust-src>/lib/rustlib/src/rust/library`;
  `crates[]` in topological order with `root_module`, `edition`, `deps[{crate,name}]`, `cfg`
  (features + build-script cfgs), `env` (`OUT_DIR` = the run's `out-dir`, `CARGO_PKG_*`,
  `rustc-env`), `is_proc_macro` + `proc_macro_dylib_path` (store), `is_workspace_member`
  (local crates true), `source.include_dirs`. Local crates point to the working tree, registry
  crates to their store source. Editor settings (§02) set rust-analyzer `linkedProjects` to it.
- `Cargo.toml` (root: `[workspace] resolver = "3"`, `members` sorted) and per local crate
  (`[package] name, version = "0.0.0", edition, publish = false`, `[lib]/[[bin]]` path,
  `[features]`, `[dependencies]` with `version = "=<locked>"`, `features`, `default-features`,
  `package` for renames, local crates as `path`), `Cargo.lock` (format version 4, from
  bob.lock), `.cargo/config.toml` (`[source.crates-io] replace-with = "bob-vendor"`,
  `[source.bob-vendor] directory = ".bob/vendor"`), `.bob/vendor/<name>-<version>` symlinks to
  store sources with `.cargo-checksum.json`. A user-owned `.cargo/config.toml` is a conflict
  (`E-GEN-CONFLICT`, BOB-E-1016).

#### R-RS-003a rust-analyzer and cargo metadata work
- **Acceptance:** In the Rust fixture after `bob gen`, through shims as non-root on a host
  without `/nix`: `rust-analyzer analysis-stats .` reports 0 unresolved imports and 0
  unresolved macro calls (proc-macros expand from store dylibs); `cargo metadata --locked
  --offline` exits 0.
- **Source:** R-RS-003, A10, S3 §3.5.

#### R-RS-004a Toolchain per package
- **Acceptance:** A crate with `Toolchain: nixpkgs.Tool(p, "<other rustc attr>")` compiles with
  it (job golden); mixing toolchains along one dependency edge fails at evaluation with
  `✗ //a:a (rustc 1.95.0) depends on //b:b built with rustc 1.94.1; Rust crates linked together need the same toolchain`.
  *Decided (2026-10-22, M8b-3):* in v1 `Toolchain` takes rustc attributes of the project's one
  nixpkgs pin: the default `rustc` (1.95.0 at nixos-26.05) or the compiler of a versioned set
  of the pin, `rust_<major>_<minor>.packages.stable.rustc`. The pin has `rust_1_95`, `rust_1_97`
  and `rust_1_98` (no `rust_1_94`); the acceptance's other attribute is
  `rust_1_97.packages.stable.rustc` (rustc 1.97.1, on cache.nixos.org). Cross-compiling
  (R-FW-034) needs the default `rustc`, whose target standard library the target system's set
  provides. A second nixpkgs pin stays out of v1.
- **Source:** R-RS-004.

### §03.3.5 Errors (examples, §02 templates)

`✗ cannot resolve crates: app@0.1.0 depends on tokio 1: no version of tokio satisfies all of: ^1.40, =1.38.0`;
`✗ serde_json@1.0.154 is not in bob.lock (run bob lock)`;
`✗ crates.io:foo@1.2.3: download does not match bob.lock`;
build-script failure: label of the crate unit, the script's stderr excerpt, and for `-sys`
crates without native inputs the hint `rust.Uses("<crate>", nixpkgs.Lib(p, "<attr>"))`.

### §03.3.6 Native-crate defaults table

Shipped Go table mapping `-sys` crates to nixpkgs packages, used when no `rust.Uses` names the
crate (same admission rule as §03.0.6):

```go
type NativeDefault struct {
	Crate string   // exact crate name
	Range string   // semver range
	Libs  []string // nixpkgs attributes (target set)
	Why   string
	Test  string   // crate of bob's CI corpus that must build and run with this entry
}
// v1 entry (verified in S3): {Crate: "openssl-sys", Range: ">=0.9", Libs: ["openssl"], ...}
```

---

## §03.4 TypeScript / npm (`pkg/npm`) — ADR 0006

The npm/pnpm/yarn CLIs are never used (D15). Runtime is Node (nixpkgs `nodejs` for builds,
`nodejs-slim` for runtime outputs); Bun selectable for final outputs.

### §03.4.1 API

```go
package npm // github.com/megakuul/bob/pkg/npm

// Dependency is a registry package, an alias, a peer, or a local *Lib.
type Dependency interface{ npmDep() }
type DepOpt func(*depOpts)

// Dep is a registry dependency; req is a node-semver range or dist-tag ("^19.1.0", "latest").
func Dep(name, req string, opts ...DepOpt) Dependency
func Alias(as, name, req string) Dependency // "lodash4" → npm:lodash@^4
func Peer(name string) Dependency           // only inside Extend
func Dev() DepOpt                           // build/tooling only; never in runtime closures
func Optional() DepOpt

// Option is a package-level fix declared by the user (not a quirk). Options apply to every
// instance of the named package in the importer's graph.
type Option interface{ npmOption() }
func AllowScripts(pkgs ...string) Option            // lifecycle allowlist, exact names (R-NPM-004)
func Extend(pkg string, deps ...Dependency) Option  // phantom deps: npm.Extend("@fastify/cors", npm.Peer("fastify"))
func Hoisted() Option                               // per-importer hoisted layout fallback (D30)

type Runtime int
const ( Node Runtime = iota; Bun )

// TSConfig generates tsconfig.json (R-NPM-008). Zero value = defaults per constructor.
type TSConfig struct {
	Target           string            // default "ES2022"
	Module           string            // default "ESNext" (Vite) / "NodeNext" (Library, App)
	ModuleResolution string            // default "Bundler" (Vite) / "NodeNext"
	Strict           *bool             // default true
	JSX              string            // "", "react-jsx", "preserve"
	Lib, Types       []string
	Extends          string            // e.g. "./.svelte-kit/tsconfig.json" (set automatically for Kit)
	Include, Exclude []string          // default ["src"], []
	Extra            map[string]any    // other compilerOptions; keys validated against tsc's list
}

// LibOpts configures Library.
type LibOpts struct {
	Name     string        // package name ("@demo/ui"); required
	Srcs     bob.Sources   // default bob.Glob("src/**")
	Entry    string        // default "src/index.ts"
	Deps     []Dependency
	Peers    []Dependency  // provided by the consumer
	TSConfig TSConfig
	Generated []Generated // codegen outputs materialised into this importer (§03.6)
	Options  []Option
}

// Library compiles with tsc into dist/ (+ .d.ts) and is linked into consumers' node_modules
// under Name (R-NPM-006). Label //<dir>:<last element of Name>.
//
//	var UI = bob.Define(func(p *bob.P) *npm.Lib {
//		return npm.Library(p, npm.LibOpts{Name: "@demo/ui", Peers: []npm.Dependency{npm.Dep("react", "^19")}})
//	})
func Library(p *bob.P, o LibOpts) *Lib

// AppOpts configures App.
type AppOpts struct {
	Name     string       // default directory name
	Entry    string       // "src/server.ts"; required
	Srcs     bob.Sources
	Deps     []Dependency
	Runtime  Runtime      // Node (default) | Bun (R-NPM-009)
	Bundle   *bool        // default true: esbuild bundle; false: ship node_modules
	TSConfig TSConfig
	Generated []Generated // codegen outputs materialised into this importer (§03.6)
	Options  []Option
}

// App is a Node/Bun program: bin/<Name> wrapper calling the runtime by store path.
//
//	var Server = bob.Define(func(p *bob.P) *npm.App {
//		return npm.NewApp(p, npm.AppOpts{Entry: "src/server.ts", Deps: []npm.Dependency{npm.Dep("fastify", "^5")}})
//	})
func NewApp(p *bob.P, o AppOpts) *App // NewApp: Go cannot name a type and a func alike (A35)

// ViteOpts configures Vite.
type ViteOpts struct {
	Name     string
	Srcs     bob.Sources    // default bob.Glob("src/**", "public/**", "index.html", "vite.config.*", "svelte.config.*")
	Deps     []Dependency
	Config   string         // default "vite.config.ts" (user-owned, D33)
	Kit      bool           // SvelteKit with adapter-static
	Dev      DevServer      // Port (default 5173), Host (default "localhost")
	TSConfig TSConfig
	Generated []Generated // codegen outputs materialised into this importer (§03.6)
	Options  []Option
}

// Vite runs `vite build` hermetically; `bob dev` runs its dev server (R-NPM-007, R-NPM-010).
// Output: the static site directory.
//
//	var Web = bob.Define(func(p *bob.P) *npm.Site {
//		return npm.Vite(p, npm.ViteOpts{Deps: []npm.Dependency{
//			npm.Dep("svelte", "^5"), npm.Dep("vite", "^7.1.0", npm.Dev()),
//			npm.Dep("@sveltejs/vite-plugin-svelte", "^6", npm.Dev()), ui.UI(p)}})
//	})
func Vite(p *bob.P, o ViteOpts) *Site

type Runner int
const ( Vitest Runner = iota; NodeTest )

// TestOpts configures Test. Of names the importer whose deps and sources are used.
type TestOpts struct {
	Of       interface{ npmImporter() } // *Lib, *App, *Site
	Runner   Runner
	Files    bob.Sources               // default bob.Glob("src/**/*.test.ts")
	Services []bob.Service
}

// Test runs vitest (`--reporter=json`) or `node --test --test-reporter=tap` in a derivation.
//
//	var WebTest = bob.Define(func(p *bob.P) *npm.Test { return npm.NewTest(p, npm.TestOpts{Of: Web(p)}) })
func NewTest(p *bob.P, o TestOpts) *Test
```

Each Library/App/Site is an **importer** (one generated `package.json`, `tsconfig.json`,
`node_modules`). git/file/URL specs are rejected in v1 (S4-10).

### §03.4.2 Resolution (`bob lock`)

Against the registry's abbreviated metadata (`Accept: application/vnd.npm.install-v1+json`,
ETag cache `$XDG_CACHE_HOME/bob/npm-meta`; per-version documents only for packages with
`os`/`cpu` to read `libc`). Rules (S4 ADR 0006 §1): node-semver with the prerelease rule;
`latest` dist-tag wins when it satisfies, else highest satisfying non-deprecated version;
reuse an already chosen version when it satisfies; breadth-first, alphabetical per level;
optional deps kept for all platforms; `npm:` aliases; bundled deps not resolved; required
peers auto-installed, optional peers linked only when present; workspace packages link by name.
Instance key = `name@version` + `(peer@version,…)` sorted when it has resolved peers (pnpm-style;
two importers with different peers get two instances). Revalidation is skipped when
`inputs` is unchanged and caches are fresh (≤ 24 h) unless `--refresh`.

#### R-NPM-001a bob's resolution matches npm and is peer-correct
- **Acceptance:** On the six S4 graphs (69–411 packages) plus ≥ 4 more (SvelteKit, Vite+React,
  fastify, a monorepo with two peer-split importers), the name@version set and edges equal
  `npm install --package-lock-only` (pinned npm, same registry snapshot from the harness
  mirror), except peer-split cases where bob's lock gives each importer its own peer (asserted
  explicitly); registry documents over 128 MB, invalid names or unknown integrity algorithms
  are rejected with `E-RESOLVE` (BOB-E-1015).
- **Source:** R-NPM-001, D15, S4 §3.1.

`lock.NpmImporter`, `lock.NpmPackage` and the `npm` section:

```go
// NpmSection is bob.lock's "npm" section.
type NpmSection struct {
	Inputs    string                 `json:"inputs"`
	Registry  string                 `json:"registry"`  // "https://registry.npmjs.org/"
	Importers map[string]NpmImporter `json:"importers"` // key: repo-relative importer dir
	Packages  map[string]NpmPackage  `json:"packages"`  // key: instance key
}
type NpmImporter struct {
	Deps map[string]string `json:"deps"`          // alias → instance key or "link:<dir>"
	Dev  []string          `json:"dev,omitempty"` // aliases that are Dev()
}
type NpmPackage struct {
	Name             string            `json:"name"`
	Version          string            `json:"version"`
	Integrity        string            `json:"integrity"`              // sha512-… (sha256/sha1 legacy accepted)
	Tarball          string            `json:"tarball,omitempty"`      // only if not the default registry URL
	Deps             map[string]string `json:"deps,omitempty"`         // alias → instance key
	Peers            []string          `json:"peers,omitempty"`
	Optional         bool              `json:"optional,omitempty"`
	OS               []string          `json:"os,omitempty"`
	CPU              []string          `json:"cpu,omitempty"`
	Libc             []string          `json:"libc,omitempty"`
	Bundled          []string          `json:"bundled,omitempty"`
	HasInstallScript bool              `json:"hasInstallScript,omitempty"`
	Bin              map[string]string `json:"bin,omitempty"`          // command → relative path
}
```

```json
{"npm": {"inputs": "sha256-…", "registry": "https://registry.npmjs.org/",
  "importers": {"web": {"deps": {"react": "react@19.3.1", "@demo/ui": "link:packages/ui",
                                  "vite": "vite@7.3.7"}, "dev": ["vite"]}},
  "packages": {
    "use-sync-external-store@1.7.0(react@19.3.1)": {"name": "use-sync-external-store",
      "version": "1.7.0", "integrity": "sha512-…", "deps": {"react": "react@19.3.1"}, "peers": ["react"]},
    "@rollup/rollup-linux-arm64-gnu@4.64.12": {"name": "@rollup/rollup-linux-arm64-gnu",
      "version": "4.64.12", "integrity": "sha512-…", "optional": true,
      "os": ["linux"], "cpu": ["arm64"], "libc": ["glibc"]}}}}
```

Platform filtering (`os`/`cpu`/`libc`, `!x` negation) happens at build time per target; a
non-optional package that does not match is an error naming it.

### §03.4.3 Derivation layout

| Derivation | Name | Job | Inputs | Output |
|---|---|---|---|---|
| tarball | `npm-<name>-<version>.tgz` | bob-imported flat path (FOD-identical), hash = integrity | — | tarball |
| package instance | `npm-<instance key>` | `npm-unpack` (+ `npm-script` if allowed) | tarball, dep instances; coreutils/tar/gzip; script toolchain only if allowed | `node_modules/<name>/` + symlinks `node_modules/<dep alias>` → dep instance |
| cycle group | `npm-scc-<hash8>` | `npm-unpack` | members' tarballs | `<i>/node_modules/<name>` per member |
| importer node_modules | `node_modules-<importer>` | `npm-node-modules` | instances, nodejs | direct-dep symlinks, workspace libs, `.bin/<cmd>` wrappers (`exec <node> <realpath>`) |
| hoisted (fallback) | `node_modules-<importer>-hoisted` | `npm-node-modules` | tarballs | npm-style tree |
| workspace lib | `npm-lib-<name>` | `npm-tool` (tsc) | sources, node_modules | `node_modules/<name>/{package.json,dist/}` |
| app / site / test | `<label>` | `npm-tool` | sources, node_modules (copied as a writable symlink dir, `cp -a`), tools | bundle + `bin/` / site dir / `test-result.json` |

- Package instances do not depend on node (a node bump does not rebuild them, S4-4).
- Bundles have `/nix/store/<hash>-npm-` references scrubbed (S4-8).
- The working tree gets `node_modules` as a real directory of symlinks, swapped in by atomic
  rename, keeping `.vite` and `.cache`; `.bob/node_modules.path` records the store path.

#### R-NPM-003a Isolated layout by default, hoisted on request
- **Acceptance:** In the Vite+React fixture, `node_modules/react` is a symlink into the store,
  a package cannot `require` an undeclared package (test asserts `ERR_MODULE_NOT_FOUND`), and
  adding one dependency rebuilds ≤ 4 derivations (S4 quiet: 4); with `npm.Hoisted()` the
  importer gets one hoisted derivation and the same app output runs.
- **Source:** R-NPM-002, R-NPM-003, D30.

### §03.4.4 Lifecycle scripts (R-NPM-004)

- Denied by default; once per lock bob prints `note: better-sqlite3@12.11.1 has an install
  script; bob does not run it. If the package needs it: npm.AllowScripts("better-sqlite3")`.
- Allowed: `preinstall`, `install` (or `node-gyp rebuild` if `binding.gyp` and neither
  install nor preinstall), `postinstall`, run by bob-builder `npm-script` with
  `bash -euo pipefail -c`, cwd = package dir, no network, `HOME=$TMPDIR`, PATH = deps' bins +
  node-gyp wrapper + nodejs, python3, gnumake, cc, sed, grep, coreutils;
  `npm_config_nodedir=<nodejs>`; afterwards only `build/Release/*.node` of the build tree is kept.
- Runtime translations (§02 `E-SCRIPT-DENIED` = BOB-E-2013, `E-PHANTOM-DEP` = BOB-E-2014):
  `Could not locate the bindings file` from a store package with a denied script →
  `✗ better-sqlite3 needs its install script (native addon). Add npm.AllowScripts("better-sqlite3") to //web:web`;
  `TS2307 Cannot find module 'X'` or `ERR_MODULE_NOT_FOUND` raised inside `/nix/store/…-npm-P-…` →
  `✗ P uses X without declaring it. Add npm.Extend("P", npm.Peer("X"))`.

#### R-NPM-004a Scripts denied by default, allowed per package, sandboxed
- **Acceptance:** Given the native fixture (better-sqlite3): without AllowScripts the test
  fails with the `E-SCRIPT-DENIED` text above (golden) and no script ran (log has no
  `node-gyp`); with AllowScripts the addon builds offline, the test opens `:memory:`, and the
  runtime closure contains only the `.node` file, glibc and libgcc beyond node.
  *Clarification (Gate H, G24):* the native fixture gets a `//native:test` target that opens
  `:memory:`, and "builds offline" is observed with the build wrapped in a network namespace; both
  are added by the milestone that builds R-NPM-004a.
- **Source:** R-NPM-004, D31, S4 §3.2.

### §03.4.5 Quirks table (R-NPM-005)

```go
// Quirk is one curated entry; the table is Go data shipped with bob (D32).
type Quirk struct {
	Package string            // exact npm name
	Range   string            // node-semver range
	Why     string            // required justification
	Skip    bool              // never run the script
	Tools   []string          // extra nixpkgs attributes for the script
	Env     map[string]string // extra env
	Test    string            // command run in the package dir after the build (required)
}
```

v1 entries (both verified in S4): `{esbuild, ">=0.15", Skip: postinstall only re-checks the
@esbuild/<platform> binary}`, `{better-sqlite3, ">=8", Env: npm_config_build_from_source=true,
Test: open :memory: and select sqlite_version()}`. Admission: only if the package cannot work
with deny-or-allow alone; phantom dependencies are never quirks (S4-6).

#### R-NPM-005a Every quirk has a justification and a passing test
- **Acceptance:** CI iterates the table: each entry has non-empty `Why` and `Test`, its Test
  passes in a fixture that depends on the package; an entry without them fails CI (`E-QUIRK-TEST`, BOB-E-9006).
- **Source:** R-NPM-005, D32.

### §03.4.6 Generated files (R-NPM-008)

- `package.json`: keys in this order: `"//"` header, `name`, `version` (`"0.0.0"`), `private:
  true`, `type: "module"`, Library only: `exports` (`{".": {"types": "./dist/index.d.ts",
  "import": "./dist/index.js"}}`), `types`, `peerDependencies` (ranges as declared);
  `dependencies` / `devDependencies` (registry: exact locked version; local libs: `"file:<rel dir>"`),
  2-space indent, trailing newline.
- `tsconfig.json`: header comment, `extends` if set, `compilerOptions` (TSConfig defaults +
  Extra, keys sorted), `include`, `exclude`.
- `vite.config.*`, `svelte.config.js` stay user-owned (D33).

#### R-NPM-008a package.json and tsconfig.json come from bob.go
- **Acceptance:** Goldens for each fixture importer; `tsc --noEmit -p .` through the shim
  passes; editing `"strict": false` into tsconfig.json is rejected as drift (R-FW-061).
- **Source:** R-NPM-008, D33.

### §03.4.7 Builders and runtimes

#### R-NPM-007a TypeScript, bundles, Vite and SvelteKit build hermetically
- **Acceptance:** Fixtures build: tsc library (dist + d.ts), fastify App (esbuild bundle,
  runs under `env -i`), Vite React site, Svelte site with a workspace library, SvelteKit with
  adapter-static (`.svelte-kit` written inside the build dir only), vitest and ESLint
  (type-checked) as tests (*clarification, Gate H G24:* the ESLint entry point, a runner or check
  package of `pkg/npm`, is specified by the milestone that builds it; until then only vitest is
  asserted); rebuilds are bit-identical. **[open]** SvelteKit was not built in S4;
  `Kit: true` runs `svelte-kit sync` before `vite build` and in `bob dev` — verify in Stage 3.
- **Source:** R-NPM-007, S4 §3.2.

#### R-NPM-009a Node by default, Bun for final outputs
- **Acceptance:** `npm.App` with `Runtime: npm.Bun` produces `bin/<name>` that runs with
  nixpkgs bun; `Runtime` is not a field of Library/Vite (compile-time); the Node App's runtime
  closure uses `nodejs-slim`.
- **Source:** R-NPM-009, D15, S4-9.

### §03.4.8 `bob dev` contract for Vite (R-NPM-010, A20)

1. If bob.go changed: relock (if `inputs` changed), build importer `node_modules`, swap it
   into the tree atomically.
2. Write `.bob/vite.bob.config.mjs`: imports the user's `vite.config.*` (object or function),
   merges `server.fs.allow: [searchForWorkspaceRoot(cwd), "/nix/store"]`, `server.port`/`host`
   from `Dev`.
3. Start `vite --config .bob/vite.bob.config.mjs` inside the namespace with the dev env;
   workspace libraries link to the **working tree** so their edits hot-reload.
4. On a dependency change in bob.go: relock, rebuild `node_modules`, swap, restart Vite
   (old process gets SIGTERM, 5 s, then SIGKILL).
5. Codegen outputs regenerate into the tree (§03.6) and reach Vite through its watcher.

#### R-NPM-010a Live reload and dependency restarts
- **Acceptance:** In the Vite fixture under `bob dev` as non-root: saving `src/App.svelte` (the fixture is Svelte) emits an
  HMR update over the dev-server websocket within the budget (§03.8); adding a dependency in
  bob.go restarts Vite with the new package importable within 10 s [8c/15G]; editing a
  `.proto` used via `connect.TS` changes the served module.
- **Source:** R-NPM-010, D34, A20, S4 §3.5.

---

## §03.5 OCaml (`pkg/ocaml`) — ADR 0007

Packages come from a pinned opam-repository commit resolved by bob; nixpkgs only for the
toolchain (A21). Toolchain at the pin: OCaml 5.4.1, dune 3.21.1, findlib 1.9.8.

### §03.5.1 API

```go
package ocaml // github.com/megakuul/bob/pkg/ocaml

// Dep is an opam package, a findlib library of one, or a local *Lib.
type Dep interface{ mlDep() }

// Opam is an opam package; req is an opam version formula ("", ">= 5.0", "= 6.1.2").
// As a Dep it means the findlib library of the same name.
func Opam(name, req string) *OpamPkg

// Lib selects one findlib library of the package: ocaml.Opam("lwt", ">= 5.7").Lib("lwt.unix").
func (o *OpamPkg) Lib(findlib string) Dep

// Option is a graph-wide setting attached to a local library or executable.
type Option interface{ mlOption() }

// Conf maps an opam conf-* package to nixpkgs packages for this project (added to the
// shipped table, §03.5.5).
func Conf(opamName string, libs ...cc.Linkable) Option

// LibOpts configures Library.
type LibOpts struct {
	Name    string        // default directory name; module namespace = capitalised name
	Srcs    bob.Sources   // default bob.Glob("*.ml", "*.mli")
	Deps    []Dep
	Stubs   bob.Sources   // C stub files
	Native  []cc.Linkable // linked into the stubs
	Wrapped *bool         // default true (dune semantics: Lib__Mod)
	Flags   []string      // extra ocamlopt flags (validated)
	Dune    bool          // fallback mode: generated dune file built inside the derivation (ppx, menhir)
	Options []Option
}

// Library is a native OCaml library, usable as Dep. Label //<dir>:<Name>.
//
//	var Geom = bob.Define(func(p *bob.P) *ocaml.Lib {
//		return ocaml.Library(p, ocaml.LibOpts{Deps: []ocaml.Dep{ocaml.Opam("fmt", "")}})
//	})
func Library(p *bob.P, o LibOpts) *Lib

// ExeOpts configures Executable and Test.
type ExeOpts struct {
	Name    string
	Main    string        // module, default "main"
	Srcs    bob.Sources
	Deps    []Dep
	Native  []cc.Linkable
	Dune    bool
	Options []Option
}

// Executable builds a native executable bin/<Name> (build references scrubbed).
//
//	var Demo = bob.Define(func(p *bob.P) *ocaml.Exe {
//		return ocaml.Executable(p, ocaml.ExeOpts{Deps: []ocaml.Dep{
//			ocaml.Opam("cmdliner", ""), ocaml.Opam("lwt", ">= 5.7").Lib("lwt.unix"), Geom(p)}})
//	})
func Executable(p *bob.P, o ExeOpts) *Exe

// NewTest builds an executable and runs it; exit 0 = pass; TAP output parsed if present.
// (Named NewTest: Go does not allow a function Test next to the type Test.)
//
//	var GeomTest = bob.Define(func(p *bob.P) *ocaml.Test {
//		return ocaml.NewTest(p, ocaml.ExeOpts{Name: "geom_test", Main: "test", Deps: []ocaml.Dep{Geom(p)}})
//	})
func NewTest(p *bob.P, o ExeOpts) *Test
```

v1 scope (S5 §5.7): native libraries/executables/tests, opam packages, conf-* table, C stubs
linking bob C libs and nixpkgs, `.merlin` + shims, both Linux platforms natively. Deferred
(evaluation error with hint): bytecode, js_of_ocaml/melange, ppx/menhir/ocamllex in direct
mode (hint: `Dune: true`), opam `patches`/`extra-source`, OCaml version other than the pin's,
cross-compilation (`✗ OCaml cross-compilation is not supported in v1`), OCaml libs as
`cc.Linkable`.

### §03.5.2 Resolution (`bob lock`)

- The opam-repository snapshot (`github:ocaml/opam-repository@<commit>` tarball) is a
  bob-imported source path (NAR-hashed in the lock, FOD-identical); bob reads only the opam files it needs.
- Globals: `os=linux`, `arch=arm64|x86_64`, `os-family=os-distribution=nixos`,
  `opam-version=2.5.1`, `ocaml:native=true`; flags `build=post=true`,
  `with-test=with-doc=dev=with-dev-setup=false`.
- Provided (never chosen from the repo): `ocaml`, `ocaml-base-compiler`, `ocaml-compiler`,
  `ocaml-config`, `ocaml-options-vanilla`, `base-{unix,threads,bigarray,domains,nnp,effects}`,
  `dune`, `ocamlfind` at the nixpkgs versions.
- Algorithm: prune versions never installable against the provided set (fixpoint), then a
  conflict-driven search (PubGrub-style) preferring newest versions; `depopts` present in the
  solution become build-order edges and set `%{pkg:installed}%`. The S5 greedy search was
  enough for 8 graphs but blew up before pruning on 5; production uses the conflict-driven
  solver.
- Packages without sha256/sha512 checksum are rejected (`✗ opam:foo.1.0 has no sha256/sha512 checksum; bob cannot pin it`).

#### R-ML-002 bob's opam resolution is valid and close to opam's
- **Acceptance:** On a 20-graph corpus (incl. the 8 S5 graphs), every bob solution satisfies
  all constraints (checked by an independent validator) and equals `opam install --dry-run`
  (pinned opam, same repository commit) on ≥ 70 %; unsatisfiable inputs give `E-RESOLVE` (BOB-E-1015) with
  the conflicting constraint chain, e.g. `✗ cannot resolve opam packages:
  ocaml-lsp-server.1.26.0 needs dune-rpc >= "3.22.0": no available version matches (dune is fixed to 3.21.1 by the toolchain)`.
- **Source:** R-ML-001, A21, S5 §3.2.

`lock.OpamRepository`, `lock.OpamPackage` and the `opam` section:

```go
type OpamSection struct {
	Inputs     string                 `json:"inputs"`
	Repository OpamRepository         `json:"repository"`
	Packages   map[string]OpamPackage `json:"packages"` // key: opam name
}
type OpamRepository struct {
	URL     string `json:"url"`     // "github:ocaml/opam-repository"
	Commit  string `json:"commit"`  // 40 hex
	NarHash string `json:"narHash"` // SRI sha256 of the unpacked snapshot
}
type OpamPackage struct {
	Version string   `json:"version"`
	URL     string   `json:"url"`
	Hash    string   `json:"hash"`           // SRI sha512 or sha256 from the opam file's checksum (flat)
	Opam    string   `json:"opam"`           // sha256 hex of the opam file in the snapshot
	Deps    []string `json:"deps,omitempty"` // depends + present depopts, minus provided packages
}
```

```json
{"opam": {"inputs": "sha256-…",
  "repository": {"url": "github:ocaml/opam-repository",
                 "commit": "af5ddb0addf6e75660ee9d7485b27425459d1d66", "narHash": "sha256-…"},
  "packages": {"fmt": {"version": "0.11.0",
    "url": "https://erratique.ch/software/fmt/releases/fmt-0.11.0.tbz",
    "hash": "sha512-P0AVX8anMVIC5BBYWWQwfWNBbIAB/SQ2Z+2djRoCtn3uyssl6cL+tAnFN7vft4F9kRaN5N3WQ1Mv9R1sHGlqSg==",
    "opam": "02bb57a07d169eef9bde16e0a50a00423a4f756d589f7d1fed0d4d3aa367d11c",
    "deps": ["cmdliner", "ocamlbuild", "topkg"]}}}}
```

### §03.5.3 Derivation layout

| Derivation | Name | Job | Inputs | Output |
|---|---|---|---|---|
| source | `opam-src-<name>-<version>` | bob-imported flat path (FOD-identical), hash from lock | — | archive |
| opam package | `opam-<name>-<version>` | `opam-build`: unpack, render the opam `build:`/`install:` commands in Go (variables substituted, filters applied), then `opam-installer --prefix=$out --libdir=$out/lib --stubsdir=$out/lib/stublibs <name>.install` if present | source, toolchain, closure of dep outputs on `OCAMLPATH`, conf-mapped nixpkgs | `lib/<findlib>/…`, `bin/`, `lib/stublibs/` |
| conf-* | `opam-conf-<x>-<v>` | none (empty output) | mapped nixpkgs packages | empty; consumers get `PKG_CONFIG_PATH`/`C_INCLUDE_PATH`/`LIBRARY_PATH` |
| local library | `ml-<name>` | `ml-compile`: `ocamldep -sort`, alias module, `ocamlfind ocamlopt` per module, stubs + `ar`, `-a`, META | own sources, closure, C libs | `lib/<name>/{*.cmi,*.cmx,*.cmt*,<name>.cmxa,<name>.a,lib<name>_stubs.a,META}` |
| executable | `ml-<name>` | `ml-link`: `ocamlfind ocamlopt -linkpkg`, then scrub build-only refs (ocaml, gcc, glibc-dev, binutils) | own sources, closure | `bin/<name>` |
| dune mode | `ml-<name>` | generated dune file + `dune build` in the sandbox | as above + dune | same as direct |

Toolchain inputs as S5 §5.2. Reference scrubbing is pattern-based (`ocaml-*`, `gcc-*`,
`glibc-*-dev`, `binutils-*`); every fixture executable is run under `env -i` after scrubbing
(R-ML-001a), so a scrubbed path the program needs shows up as a test failure.

#### R-ML-001a OCaml libraries, executables and tests build fine-grained
- **Acceptance:** Given `tests/fixtures/repos/ocaml` (S5 demo: 11 opam packages incl. lwt,
  cmdliner, yojson, fmt; a local library with C stubs linking a bob C library; an executable;
  a test), when built and tested, then the executable runs under `env -i`, its runtime closure
  is ≤ 80 MB (S5: 55 MB), each opam package is its own derivation, a library change rebuilds
  only the library and its dependents, and direct and `Dune: true` modes give working
  executables.
- **Source:** R-ML-001, S5 §3.1, §3.6.

### §03.5.4 Tooling files

Default: `<dir>/.merlin` per local library/executable (`S .`, `B .`, `PKG <findlib>…`,
`B <store>/lib/<locallib>` for local deps, `FLG -open <Lib>__` inside wrapped libraries) and
shims `.bob/bin/{ocamllsp,ocamlmerlin,dot-merlin-reader,dune,ocamlfind,ocaml,ocamlc,ocamlopt}`
with `OCAMLPATH` into the store (§02). Opt-in per project (`ocaml.Editor(ocaml.DuneFiles)`,
an `Option`): `dune-project` (`(lang dune 3.21)` first line), `dune` per directory,
`<lib>.opam`; `bob dev` then runs `.bob/bin/dune build @check` after changes.

#### R-ML-001b ocaml-lsp works without an in-tree build
- **Acceptance:** In the OCaml fixture after `bob gen`, `ocamllsp` through the shim answers
  hover and go-to-definition for an identifier from an opam package and one from a local
  library (scripted LSP client), with no `_build` directory in the tree.
- **Source:** R-ML-001, S5 §3.5.

### §03.5.5 conf-* table

Shipped Go table `{Opam: "conf-gmp", Libs: ["gmp"], Why, Test}` (same admission rule,
§03.0.6); `ocaml.Conf` adds project entries. Unmapped conf-* fails at `bob lock`:
`✗ opam:conf-foo.1: no nixpkgs mapping for this system package (add ocaml.Conf("conf-foo", nixpkgs.Lib(p, "<attr>")) or extend bob's table)`.
v1 entries: exactly those the 20-graph corpus (R-ML-002) needs; each entry's `Test` is a
corpus graph that builds with it.

---

## §03.6 Code generators and materialisation — ADR 0008

### §03.6.1 API

Codegen values are recipes: a typed `golang.Generated` or `npm.Generated` that the consuming
Go module or npm importer realises in its own `Generated` list (the module supplies the Go
import path, the importer the TS output location). A TS output cannot be passed to a Go
module (R-FW-010). Each realised recipe is one package labelled
`//<consumer dir>:gen-<owned dir relative to consumer without a leading "gen/", '/'→'-'>` (e.g. `//svc:gen-proto-greet-v1`
for `gen/proto/greet/v1`). A recipe's package is defined at the line of the recipe call
(`connect.Go(…)`). `connect.Go`/`connect.TS` also realise the libraries the library imports, each
into its default directory.

```go
package proto // github.com/megakuul/bob/pkg/proto

// LibraryOpts configures a protobuf library: the *.proto files of the calling bob.go's directory.
type LibraryOpts struct {
	Deps []*Library // imported libraries
	Root string     // import root, repo-relative; default: nearest ancestor directory named "proto", else repo root
}

// Library is one proto package (one directory, label //<dir>:proto). .proto files need no
// go_package (bob sets M options).
type Library struct{ bob.Node }

// NewLibrary declares the library of the calling bob.go's directory. (A Go function and a type
// cannot share a name, so the constructor is NewLibrary, as npm.NewApp, A66/A123.)
//
//	var Greet = bob.Define(func(p *bob.P) *proto.Library {
//		return proto.NewLibrary(p, proto.LibraryOpts{Deps: []*proto.Library{commonv1.Common(p)}})
//	})
func NewLibrary(p *bob.P, o LibraryOpts) *Library

// Lint and Breaking are check packages (buf lint / buf breaking) usable in pipelines: test
// units (bob test runs them, bob build leaves them out), labelled //<dir>:lint and
// //<dir>:breaking. Breaking compares with the library at a git revision of the same repo.
//
//	var GreetLint = bob.Define(func(p *bob.P) *proto.Check { return proto.Lint(p, Greet(p)) })
func Lint(p *bob.P, lib *Library) *Check
//	var GreetCompat = bob.Define(func(p *bob.P) *proto.Check { return proto.Breaking(p, Greet(p), "origin/main") })
func Breaking(p *bob.P, lib *Library, againstRev string) *Check
```

```go
package connect // github.com/megakuul/bob/pkg/connect

type GoOpts struct {
	Dir    string // owned dir relative to the module; default "gen/proto/<proto dir relative to Root>"
	Simple bool   // connect-go simple API
}
// Go generates protoc-gen-go + protoc-gen-connect-go code for lib and every library it imports.
// Adds google.golang.org/protobuf and connectrpc.com/connect at the generator versions (§03.6.4).
//
//	golang.ModuleOpts{Generated: []golang.Generated{connect.Go(greetv1.Greet(p), connect.GoOpts{})}}
func Go(lib *proto.Library, o GoOpts) golang.Generated

type TSOpts struct{ Dir string } // default "gen/proto/<proto dir>"
// TS generates protoc-gen-es (target=ts, import_extension=js); adds @bufbuild/protobuf at the
// generator version. @connectrpc/connect(-web|-node) are ordinary npm deps.
//
//	npm.ViteOpts{Generated: []npm.Generated{connect.TS(greetv1.Greet(p), connect.TSOpts{})}}
func TS(lib *proto.Library, o TSOpts) npm.Generated
```

```go
package graphql // github.com/megakuul/bob/pkg/graphql

// Schema is a set of .graphqls files.
//
//	var API = bob.Define(func(p *bob.P) *graphql.Schema { return graphql.NewSchema(p, bob.Glob("*.graphqls")) })
func NewSchema(p *bob.P, files bob.Sources) *Schema

type GqlgenOpts struct {
	Dir      string            // default "gen/graph" (+ "gen/graph/model")
	Models   map[string]string // GraphQL type → Go type
	Autobind []string          // Go packages
}
// Gqlgen generates the executable schema and models; never resolver stubs. Adds
// github.com/99designs/gqlgen (exact generator version) and its gqlparser.
//
//	golang.ModuleOpts{Generated: []golang.Generated{graphql.Gqlgen(schema.API(p), graphql.GqlgenOpts{})}}
func Gqlgen(s *Schema, o GqlgenOpts) golang.Generated

type TSOpts struct {
	Dir       string            // default "gen/graphql"
	Documents bob.Sources       // operation documents
	Scalars   map[string]string // GraphQL scalar → TS type
}
// TS runs graphql-codegen (typescript + typescript-operations) through bob's driver and tool lock.
//
//	npm.ViteOpts{Generated: []npm.Generated{graphql.TS(schema.API(p), graphql.TSOpts{Documents: bob.Glob("src/**/*.graphql")})}}
func TS(s *Schema, o TSOpts) npm.Generated
```

```go
package openapi // github.com/megakuul/bob/pkg/openapi

//	var Items = bob.Define(func(p *bob.P) *openapi.Spec { return openapi.NewSpec(p, "items.yaml") })
func NewSpec(p *bob.P, file string) *Spec

type Server int
const ( NoServer Server = iota; StdHTTP )

type GoOpts struct {
	Dir     string // default "gen/<Package>"
	Package string // required
	Server  Server
	Models  *bool  // default true
	Client  bool
}
// Go runs oapi-codegen; adds github.com/oapi-codegen/runtime.
//
//	golang.ModuleOpts{Generated: []golang.Generated{openapi.Go(schema.Items(p), openapi.GoOpts{Package: "rest", Server: openapi.StdHTTP})}}
func Go(s *Spec, o GoOpts) golang.Generated

type TSOpts struct{ Dir string } // default "gen/openapi"
// TS generates TypeScript types with openapi-typescript (bob tool lock). [open: not built in S6]
//
//	npm.ViteOpts{Generated: []npm.Generated{openapi.TS(schema.Items(p), openapi.TSOpts{})}}
func TS(s *Spec, o TSOpts) npm.Generated
```

### §03.6.2 Derivation rules

- One derivation per realised recipe, job `codegen`; generators from the pinned nixpkgs
  (`buf`, `protoc-gen-go`, `protoc-gen-connect-go`, `protoc-gen-es`, `gqlgen`,
  `oapi-codegen`); graphql-codegen/openapi-typescript from bob-shipped tool locks (§03.6.5).
- bob writes `buf.yaml`, `buf.gen.yaml`, `gqlgen.yml` (`skip_mod_tidy: true`,
  `skip_validation: true`), the oapi-codegen config inside the derivation; users never write
  them. Inputs = only files the generator reads, laid out at repo-relative paths (errors need
  no translation): proto files of the library and its imports; schema files; spec + go.mod;
  gqlgen also go.sum + the module proxy dir of the consuming module.
- `HOME`, `BUF_CACHE_DIR` = `$TMPDIR`; no network.
- Output = exactly the owned directory's content: regular files only, no `.gitignore`
  (checked, else bob error naming the generator).

#### R-GEN-001a protobuf/connect for Go and TypeScript
- **Acceptance:** Given `tests/fixtures/repos/codegen` (S6 tree: two proto libraries with an
  import, a Go server and client, a TS web client), `bob build //...` generates, compiles and
  an e2e test calls the Go server from both clients successfully; each codegen derivation
  rebuilds bit-identically; a proto syntax error prints
  `✗ //svc:gen-proto-greet-v1 code generation failed (defined at svc/bob.go:<line>)` +
  ``| proto/greet/v1/greet.proto:9:1:unexpected `}` after definition`` (golden; buf 1.73 of the pin,
  fault F1 of S6: the `;` of `string name = 1;` removed), no raw Nix text.
- **Source:** R-GEN-001, R-GEN-004, S6 §3.1, §3.4.

#### R-GEN-002a GraphQL for Go and TypeScript
- **Acceptance:** In the codegen fixture, gqlgen output compiles with the user's resolvers and
  graphql-codegen output type-checks the TS operations; an undefined type in the schema gives
  `| failed to load schema: schema/schema.graphqls:9:18: Undefined type Nope.` (gqlgen) and
  `| Unknown type: "Nope".` (TS) (excerpt prefix `|` outside a terminal, `│` on one: §02.5), no
  Node stack trace into the store.
- **Source:** R-GEN-002, S6 §3.3–3.4.

#### R-GEN-003a OpenAPI for Go and TypeScript
- **Acceptance:** oapi-codegen `StdHTTP` server + models compile and serve the fixture's
  `items.yaml`; `openapi.TS` types type-check a fetch client in the web fixture; a bad `$ref`
  prints the generator's message with the spec path.
- **Source:** R-GEN-003.

### §03.6.3 Early cutoff for codegen (A11)

Consumers do not depend on the codegen derivation directly: bob builds codegen packages of the
requested closure first, adds each output's content to the store as a content-addressed path
(`gen-<label slug>`, `nix store add --mode nar`), and plans consumers against that path. A
byte-identical output therefore rebuilds nothing downstream.

#### R-GEN-005 Codegen early cutoff
- **Acceptance:** In the codegen fixture, a whitespace-only change to `greet.proto` followed
  by `bob build //...` rebuilds only the codegen derivations (2) and no Go/TS consumer; the
  added two-phase cost on a no-change build is ≤ 0.5 s [8c/15G] (S6 quiet: 14.5 s → 1.43 s
  with cutoff).
- **Source:** A11, ADR 0008 Consequences, `s06-codegen.log`.

### §03.6.4 Generator ↔ runtime pinning

For each recipe bob adds the runtime library at the version matching the generator of the
pin (`google.golang.org/protobuf` ≥ protoc-gen-go, `connectrpc.com/connect` ≥ the version the
connect-go output needs, `github.com/99designs/gqlgen` exact, `@bufbuild/protobuf` = protoc-gen-es,
`github.com/oapi-codegen/runtime`). An explicit conflicting pin fails before any build:
`✗ //svc:gen-graph: generator gqlgen 0.17.90 (bob's nixpkgs pin) does not match its runtime: the generated code needs github.com/99designs/gqlgen 0.17.90, but svc/bob.go requires v0.17.89`
`hint: remove the Require of github.com/99designs/gqlgen; bob adds the matching version`.

#### R-GEN-006 Runtime mismatch is a pre-check
- **Acceptance:** Fixture faults F4 (gqlgen runtime v0.17.89) and F5 (@bufbuild/protobuf
  2.10.0) each exit 3 (BOB-E-1021) before Nix runs, with the golden text.
- **Source:** R-NIX-011, S6 §3.3.

### §03.6.5 Tool locks

npm-based generators not in nixpkgs ship as `lock.ToolLock` documents embedded in bob (one per
tool, pinned at bob release time, same schema as `NpmSection` without `inputs`, one importer
`"tool"`), installed with the §03.4 installer and driven by a bob-owned driver script calling
the plugin API (no CLI, no `codegen.yml`). Users do not declare them; security updates come
with bob releases.

### §03.6.6 Materialisation (R-FW-007, D25)

Owned directories: one per realised recipe (defaults in §03.6.1, relative to the consumer
dir); never nested, never overlapping, never inside `.git` or `.bob`, clean relative paths.
Errors: `✗ //p:b generates into gen/b/, which is inside gen/ owned by //p:a` /
`hint: generated directories may not be nested`; `✗ //p:a and //p:b both generate into gen/` /
`hint: give one of them another output directory`.

Manifest `.bob/gen/manifest.json` (hostile input: ≤ 64 MiB, all paths re-validated):

```go
type Manifest struct {
	Version int                     `json:"version"` // 1
	Roots   map[string]ManifestRoot `json:"roots"`   // key: owned dir, repo-relative
}
type ManifestRoot struct {
	Label         string                  `json:"label"`
	Source        string                  `json:"source"`        // store path of the output
	Files         map[string]ManifestFile `json:"files"`         // path relative to the root
	PendingSource string                  `json:"pendingSource"` // set during a write
	Pending       map[string]ManifestFile `json:"pending"`
}
type ManifestFile struct {
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
	Exec   bool   `json:"exec,omitempty"`
	MTime  int64  `json:"mtime"` // ns; size+mtime only enable the no-hash fast path
}
```

Classification per file on disk (ownership is decided by sha256):

| On disk | Manifest/pending | New content | Action |
|---|---|---|---|
| hash = new | any | present | keep (fix mode if needed) |
| hash = manifest or pending | known | present / absent | overwrite / delete |
| hash ≠ manifest | known | — | conflict **edited** |
| not in manifest | — | — | conflict **foreign** (unless hash = new: adopt) |
| `.bob-tmp-*` temp file | — | — | delete (interrupted write) |
| git-tracked | — | — | conflict **tracked** (not overridable, even with `--force`) |
| missing | known | present | rewrite (not a conflict) |

Write protocol: `flock .bob/gen/lock` → manifest with `pending` → per file: temp in the same
dir, write, fsync, chmod 0444 (0555 if exec), rename, fsync dir → delete stale files and empty
dirs → write the dir's `.gitignore` (`# Generated by bob. Do not edit files in this directory:
change the inputs and run \`bob gen\`.` + `*`) → final manifest (atomic; skipped if unchanged).
A root with conflicts is skipped entirely, others proceed, exit status 3 (`E-GEN-CONFLICT` = BOB-E-1016,
S6 §3.5 text). `--force` renames conflicting files to `.bob/gen/backup/<UTC time>/<path>` first;
nothing is deleted. `.bob/.gitignore` = `*`.

When: `bob gen [patterns]` (all codegen, prunes roots no longer defined), `bob gen --check`
(no build, no write; exit 3 with BOB-E-1011 unless all fresh), `bob build` (codegen in the targets' closure;
never prunes; `note: N generated directories are out of date but not needed by this build … run bob gen`),
`bob dev` (on entry and on every change of a codegen input or bob.go; conflicts printed,
watching continues). Remote workers and pipeline runs never materialise. `bob status` lists
`fresh | stale | missing | edited | orphan` per root. Source import excludes owned dirs, so
materialising never invalidates a build.

#### R-FW-066 Materialisation never overwrites foreign or edited files
- **Acceptance:** In a git copy of the codegen fixture: (a) editing a generated file and adding
  a foreign file to the same root makes `bob gen` exit 3 (BOB-E-1016) with the S6 conflict text (golden),
  leave that root untouched and update other roots; (b) `--force` moves both to the backup dir
  and rewrites; (c) a git-tracked file in a root is refused with and without `--force`;
  (d) removing a recipe from bob.go removes its root (files, `.gitignore`, empty parents);
  (e) deleting the manifest and rerunning adopts identical files without writing; (f) killing
  bob (SIGKILL) at each step of the write protocol (fault injection) and rerunning leaves no
  temp file and a consistent manifest.
- **Source:** R-FW-007, R-GEN-004, D25, D2, S6 §3.5.

#### R-FW-067 Editors see generated code
- **Acceptance:** After `bob gen`, gopls (through the shim, `-tags=bob`) resolves an import of
  `example.com/svc/gen/proto/greet/v1` and tsserver resolves `./gen/proto/greet/v1/greet_pb.js`
  with 0 diagnostics; in `bob dev`, a `.proto` edit reaches the tree within the budget (§03.8).
- **Source:** R-FW-007, R-DEV-002.

---

## §03.7 bob.lock entry schemas (summary for §01)

All types live in `pkg/engine/lock`; JSON keys are exactly the struct tags above; maps are written
with sorted keys, arrays in the documented order; hashes are SRI strings (`sha256-…`,
`sha512-…`) except Go `h1:` and the opam file digest. Every section has `inputs` (§03.0.3).

| Section key | Type | Entry types | Defined in |
|---|---|---|---|
| `go` | `lock.GoSection` | `lock.GoBuildList`, `lock.GoModule` | §03.2.4 |
| `rust` | `lock.RustSection` | `lock.Crate`, `lock.CrateDep`, `lock.CrateMeta` | §03.3.2 |
| `npm` | `lock.NpmSection` | `lock.NpmImporter`, `lock.NpmPackage` | §03.4.2 |
| `opam` | `lock.OpamSection` | `lock.OpamRepository`, `lock.OpamPackage` | §03.5.2 |
| `cc` | `lock.CCSection` | `lock.CExternal` | §03.7.6 |
| (embedded in bob) | `lock.ToolLock` | as `NpmSection` | §03.6.5 |

All lock content is hostile input when read (§5.4 BOOTSTRAP): size cap 64 MiB per bob.lock,
every name, version and hash validated against its ecosystem's grammar before use.

### §03.7.6 `lock.CExternal`

```go
type CCSection struct {
	Inputs    string               `json:"inputs"`
	Externals map[string]CExternal `json:"externals"` // key: label ("//third_party/zstd:zstd")
}
type CExternal struct {
	URL     string `json:"url"`
	Strip   int    `json:"strip"`
	NarHash string `json:"narHash"` // SRI sha256 of the unpacked, stripped, mode-normalised tree
}
```

```json
{"cc": {"inputs": "sha256-…", "externals": {"//third_party/zstd:zstd": {
  "url": "https://github.com/facebook/zstd/releases/download/v1.5.7/zstd-1.5.7.tar.gz",
  "strip": 1, "narHash": "sha256-…"}}}}
```

#### R-FW-065 Lock files round-trip and are deterministic
- **Acceptance:** For every fixture, `bob lock` twice gives byte-identical bob.lock; decoding
  and re-encoding each section is byte-identical; a fuzz test of each section decoder
  (10⁵ iterations in the slow suite) never panics and rejects invalid hashes/names.
- **Source:** R-FW-004, R-FW-008, BOOTSTRAP §5.4.

---

## §03.8 Ecosystem budgets **[proposal]**

From `docs/spikes/measure/quiet/*.log` (2026-10-21) unless noted. Cells: [8c/15G] / [2c/4G].
Stores warm (toolchains, std, third-party outputs substituted or built) unless "cold".

| Budget | [8c/15G] | [2c/4G] | Measured (quiet) |
|---|---|---|---|
| **Go** no-op build, fixture | ≤ 0.5 s | ≤ 0.5 s | hybrid 0.26 / 0.27 s |
| Go one-file change in cgo main package (fixture) | ≤ 4 s | ≤ 4 s | 2.87 / 2.89 s |
| Go library change with dependents (fixture `internal/msg`) | ≤ 4.5 s | ≤ 4.5 s | 3.28 / 2.99 s |
| Go bottom-layer change, 200-pkg workspace (36 dependents) | ≤ 4.5 s | ≤ 4.5 s | 3.03 / 3.12 s |
| Go graph → derivations, 1,000 packages, new / again | ≤ 5 / ≤ 2 s | ≤ 5 / ≤ 2 s | 3.53/1.33, 3.43/1.19 s |
| Go cold fixture incl. std build | ≤ 40 s | ≤ 75 s | 25.0 / 48.7 s |
| Go cold fixture, std cached | ≤ 10 s | ≤ 11 s | 6.11 / 7.15 s |
| Go peak RSS of bob during a build | ≤ 300 MB | ≤ 300 MB | ≤ 160 MB (S2, busy) |
| **Rust** no-op, 49-crate demo | ≤ 1 s | ≤ 1 s | 0.56 / 0.47 s |
| Rust leaf crate change | ≤ 4.5 s | ≤ 7 s | 2.92 / 4.60 s |
| Rust local lib change (with dependents) | ≤ 5.5 s | ≤ 8 s | 3.80 / 5.36 s |
| Rust cold demo vs `cargo build --release` | ≤ 1.3× | ≤ 1.3× | 48.3/40.7 = 1.19×, 130.9/119.3 = 1.10× |
| Rust per-derivation cost (trivial crate chain) | ≤ 0.25 s | ≤ 0.3 s | 0.146 s (chain30 [8c]) |
| Rust plan + derivations, demo (no build) | ≤ 1 s | ≤ 2 s | 0.49 s [8c] |
| Rust `bob lock`, 392 crates, warm index | ≤ 2 s | ≤ 4 s | 0.85 s offline [8c]; 1.9 s live (S3, busy) |
| **npm** `bob lock`, ~200 packages, warm cache (revalidation) | ≤ 4 s | ≤ 5 s | 2.90 s ts-lib [8c]; 3.4 s [2c] (S4, busy) |
| npm `bob lock`, inputs unchanged | ≤ 0.5 s | ≤ 1 s | offline 0.11–0.42 s [8c] |
| npm materialise node_modules, no-op | ≤ 0.75 s | ≤ 0.75 s | 0.58 s [8c]; 0.42 s [2c] (busy) |
| npm materialise after adding one dependency | ≤ 1.5 s | ≤ 1.5 s | 0.87 s [8c] |
| npm cold node_modules, ~66 packages, tarballs fetched | ≤ 6 s | ≤ 8 s | 4.79 s [8c] |
| npm Vite site, one source file changed (`vite build`) | ≤ 7 s | ≤ 12 s **[unmeasured 2c]** | 5.79 s [8c] |
| npm save → HMR update (server side), p95 | ≤ 50 ms | ≤ 50 ms | median 2.4–3.3 ms, max 29 ms (S4, busy) |
| npm tsserver first diagnostics through a shim | ≤ 4 s | ≤ 6 s | 2.1–2.6 s [8c] (S4, busy) |
| **OCaml** cold demo (11 opam + C + lib + exe) | ≤ 25 s | ≤ 40 s | 15.9 / 25.5 s |
| OCaml no-op | ≤ 0.5 s | ≤ 0.5 s | 0.33 / 0.29 s |
| OCaml executable source change | ≤ 2.5 s | ≤ 2.5 s | 1.46 / 1.36 s |
| OCaml library change (lib + exe) | ≤ 3 s | ≤ 3 s | 1.86 / 1.77 s |
| OCaml resolve, ≤ 110-package graph | ≤ 0.5 s | ≤ 1 s | 0.15 s (S5, busy) |
| **codegen** `bob gen` no-op | ≤ 0.3 s | ≤ 0.3 s | 0.16 / 0.16 s |
| codegen `bob dev` proto edit → Go+TS files in tree | ≤ 2.5 s | ≤ 2.5 s | 1.39 / 1.35 s |
| codegen whitespace-only proto change, full build (cutoff) | ≤ 2.5 s | ≤ 2.5 s | 1.43 / 1.36 s |
| codegen `bob status` | ≤ 0.3 s | ≤ 0.3 s | 0.13 / 0.13 s |
| **C/C++** no-op build, fixture | ≤ 0.6 s | ≤ 0.8 s | backend B fixture 0.33 / 0.45 s (`b-quiet.md`) + eval no-op; = §01 §1.17 polyglot row |
| C/C++ synthetic 100-package cold build | ≤ 15 s | ≤ 15 s | 10.0 / 9.4 s (`b-quiet.md`) |

Budgets marked "busy" derive from spike runs on a loaded host and must be re-measured on the
quiet profile in Phase 3.1 before Gate H. The harness runs these with the budget tolerance of
§01/HARNESS.

---

## §03.9 Open points

- **[open] Generated-file ignore mechanism** (§03.0.2): `.git/info/exclude` block; recommended.
- **[open] R-CC-004 per-object C/C++ derivations** (§03.1.3): per-library in v1; measure.
- **[open] R-GO-005 default for Go export-data cutoff**: off until the Stage 3 measurement.
- **[open] Go test main** (§03.2.3): bob's own `_testmain.go` template; fallback `go test -c`.
- **[open] Second toolchain versions** (R-RS-004a, R-GO-004a): need §01 to allow more than one
  nixpkgs pin; recommendation: named extra pins in bob.go, toolchains only from pins.
- **[open] SvelteKit** (R-NPM-007a): `svelte-kit sync` in build and dev, unverified.
- **[open] OpenAPI TypeScript** (R-GEN-003a): openapi-typescript tool lock, unbuilt.
- **[open] `proto.Breaking` against a git revision** needs §01 source import from git objects;
  recommendation: bob reads the revision's files with `git archive` into a store path.
- cc.External from git: same item as §01 Open point 2 (listed once there).
- Rust lock layout: closed at integration — §01 R-FW-004 fixes the canonical form (2-space
  indent, sorted keys, arrays in schema order), so each crate is its own object block.

## Escalations

None. All owner decisions relevant here (D2, D15, D25, D30–D34, D44, D58, D59) are applied;
O-DX-1 is superseded by D59 and the §02 LSP proxy.
