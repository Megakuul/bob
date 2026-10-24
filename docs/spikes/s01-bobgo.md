# Spike S1 — `bob.go` next to the code

Status: done 2026-10-20. ADR: [`docs/adr/0003-bobgo-next-to-code.md`](../adr/0003-bobgo-next-to-code.md) (Proposed).
Code: `spikes/s01-bobgo/` (module `bobspike/s01`). Raw output: `spikes/s01-bobgo/results/`.
Tools: Go 1.26.8 and gopls 0.22.0 from the pinned nixpkgs (`cd4ea8d5`), linked in `.tools/`.

## 1. Question and requirements

Can build definitions live in a `bob.go` file next to the code (R-FW-002, D22) while staying
ordinary typed Go that works with gopls (R-FW-001)? Covers package-name collisions, build tags,
how bob compiles and evaluates all `bob.go` files, labels and typed cross-references
(R-FW-010/011/015/016), cross-repo use (R-FW-022), determinism (R-FW-008), gopls through the
`.bob/bin` shim (ADR 0002 Decision 5, R-DEV-001/002) and evaluation speed (R-DEV-006, R-NFR).

## 2. What was built

| Path | What |
|---|---|
| `fw/` | Mock framework, module `github.com/megakuul/bob`: `pkg/bob` (`P`, `Define`, labels, `Main`), `pkg/cc`, `pkg/golang`, `pkg/rust`, `pkg/npm`, `pkg/nixpkgs`. Just enough API to type-check and to emit a graph. |
| `examples/acme/` | Example repo: C++ lib `libs/foo` (sources in `src/`), Rust crate `crates/rcore`, Go library `internal/greet` (with `greet_test` external tests), Go binary `cmd/api` (`package main`, plus `main_test.go`), Vite app `web/`. |
| `examples/acme-tree/` | The same repo in the fallback layout (`bob/` module + `go.work`). |
| `cmd/bobeval/` | The evaluator generator (scan, rule checks, overlay, generated main, `go build`, run). |
| `cmd/lspc/` | LSP JSON-RPC client that drives real gopls over stdio (open, diagnostics, completion, definition, hover, edit). |
| `cmd/synth/` | Synthetic repos with N `bob.go` files and typed imports between them. |
| `cmd/bobexec/` | Copy of S0b's `bobns` plus `-env FILE`: stand-in for `bob exec <tool>`. |
| `tests/` | `variants.sh` (problem cases), `run-all.sh`, `shim.sh`, step files for `lspc`. |
| `measure.sh` | Evaluation timing, `./measure.sh` [8c/15G] and `./measure.sh low` [2c/4G]. |

## 3. Results

### 3.1 Go rules (go 1.26.8)

| Case | `go build/vet/test ./...` (no tag) | with `-tags bob` |
|---|---|---|
| `bob.go` (`//go:build bob`) same package as the Go code (`greet`, `main`) | ignored ✓ | merged into that package |
| `bob.go` with a different package name (`package bob` in dir `greet`) | ignored ✓ | `found packages bob (bob.go) and greet (greet.go)` ✗ |
| dir with only `x_test` files + `bob.go package x` | ✓ (`go test` passes) | ✓ |
| dir with `foo.c` / `x.cpp` next to `bob.go`, CGO_ENABLED=1 | ignored ✓ | `C source files not allowed when not using cgo or SWIG` ✗ |
| same, CGO_ENABLED=0 | ✓ | ✓ (C files are ignored) |
| `.s`/`.S` files next to `bob.go` | ✓ | ✓ for gopls |
| `bob.go` imports the `bob.go` of a `package main` dir | – | `is a program, not an importable package` ✗ |
| overlay replacing a file below GOMODCACHE | – | refused: `Files beneath GOMODCACHE … must not be replaced` |
| overlay adding a virtual package dir (`<root>/_bobeval/main.go`) | – | works |
| `runtime.Caller` in an overlay-replaced file | – | reports the original path; with `-trimpath` it is `example.com/acme/cmd/api/bob.go` |
| compiler errors in an overlay-replaced file | – | name the replacement file; bob maps them back (done in `bobeval`) |
| `-modfile` in a directory with `go.work` | – | `-modfile cannot be used in workspace mode` → evaluator runs with `GOWORK=off` |
| `go mod tidy` | keeps `require github.com/megakuul/bob` (tidy ignores tags) ✓ | |

So the tag is required, the package name must match the directory's Go code, and the
evaluator must not compile `bob.go` directories as they are.

### 3.2 gopls 0.22.0, driven over LSP (`results/gopls-*.txt`)

`examples/acme`, one session, buildFlags `-tags=bob` [8c/15G]:

| Step | Result |
|---|---|
| diagnostics `cmd/api/bob.go` | none (correct) |
| completion `foo.` in `cmd/api/bob.go` | `Lib` |
| completion in an options struct literal (`golang.BinaryOpts{ C…`) | `CGo` |
| definition `foo.Lib` | `libs/foo/bob.go:12` |
| definition `cc.SharedLibrary` | framework source `fw/pkg/cc/cc.go:47` |
| hover `rcore.Crate` | type + doc comment incl. the label |
| edit: pass a `*golang.Binary` where `cc.Linkable` is expected | `cannot use Binary(p) (value of type *golang.Binary) as cc.Linkable value … (missing method LinkInfo)` + `initialization cycle` |
| edit undone | diagnostics clear |
| normal code `cmd/api/main.go`: diagnostics / `greet.` / definition `greet.Hello` | none / `Hello`, **`Test`** / `internal/greet/greet.go:5` |
| `internal/greet/greet_test.go` (package `greet_test`) | no diagnostics |
| request latency (completion, definition, hover) | 0–4 ms; 28–65 ms for unimported-package completion |

Other modes (`results/gopls-acme-modes.txt`):

| gopls configuration | bob.go |
|---|---|
| no tag | warning `No packages found for open file … excluded due to its build tags`; no completion; definition fails `no package metadata` |
| `GOFLAGS=-tags=bob` in gopls's environment, no settings | same results as buildFlags ✓ |
| `standaloneTags: ["ignore","bob"]` | file loads as a standalone main, but imports of other `bob.go` packages fail (`build constraints exclude all Go files`) ✗ |

Problem cases (`tests/variants.sh`, `results/gopls-variants.txt`):

| Variant | gopls with `-tags=bob` |
|---|---|
| V1 flat C dir (`flat.c` next to `bob.go`), CGO_ENABLED=0 | clean; completion and cross-refs work |
| V1 same, CGO_ENABLED=1 | one permanent error `1:1 go list: C source files not allowed when not using cgo or SWIG: flat.c`; completion still works |
| V1 + generated `zz_bob_cgo.go` (`//go:build bob && cgo`, `import "C"`), CGO_ENABLED=1, gcc from the pin in PATH | clean ✓ |
| V1b `x.cpp` | same as V1 (`C++ source files not allowed`) |
| V1b `.S` + `.s` | clean |
| V2 `package bob` next to `package greet` | errors in all three files, `greet.` completion empty in importers ✗ |
| V3 bob.go imports the main package's bob.go | `import "example.com/acme/cmd/api" is a program, not an importable package` ✗ |
| V4 real code declares `Test`, bob.go declares `Test` | `Test redeclared in this block` in both files (editor only; untagged `go build/vet` pass) |
| V5 only `x_test.go` files + `bob.go` | clean |
| V6 real greet imports util; util's bob.go imports greet's bob.go | `import cycle not allowed in test`, `could not import …/util` in greet.go ✗ |

Scale (`tmp/synth1000`, 1,000 bob.go, 500 dirs with real Go code) [8c/15G], indicative:

| | time to diagnostics of an open bob.go (incl. 1.5 s settle wait) | gopls max RSS |
|---|---|---|
| `-tags=bob` | 6.8 s | 239 MB |
| no tag (bob.go not loaded) | 3.1 s | 90 MB |

### 3.3 gopls through the `.bob/bin/gopls` shim, as `bobtest` (`results/shim-*.txt`)

Setup: bobtest's own store (`/home/bobtest/s01/state/nix`), go + gopls substituted from
cache.nixos.org into it inside the namespace (`nix-store -r`, 6.9 s, 334 MB store). The shim:

```sh
#!/bin/sh
# Code generated by bob. DO NOT EDIT.
exec bobexec -state … -env <repo>/.bob/env-gopls ${BOBEXEC_FLAGS:-} -- gopls "$@"
```

`.bob/env-gopls` = the `bob dev` environment (`PATH=<store go>/bin:<store gopls>/bin:…`,
`GOTOOLCHAIN=local`, `GOPROXY=off`, `CGO_ENABLED=0`) plus `GOFLAGS=-tags=bob`. bobtest's login
PATH has no go, gopls or nix.

| Check | bind mode (host has /nix) | pivot mode (`-force-pivot`, host without /nix) |
|---|---|---|
| full `acme-tagged` LSP script (same steps as 3.2) | identical results ✓ | identical results ✓ |
| `go` subprocesses started by gopls | 34 calls per session (154 log lines over all runs, every one in a bob mount namespace, none in the host's `mnt:[4026531832]`) (`go env`, `go list …`), all with `uid_map = 0 1000 1` and the namespace's mount ns — i.e. inside the namespace | ✓ |

Shim start overhead (as bobtest, 20 runs each, 2 rounds) [8c/15G], indicative:

| | via shim | same binary inside an entered namespace | overhead |
|---|---|---|---|
| `/bin/true` (pure shim cost) | 7.0–8.0 ms | 0.16–0.26 ms | **6.8–7.8 ms** (5 rounds) |
| `go version` | 23–27 ms | 14–15 ms | 9–11 ms |
| `gopls version` | 90–101 ms | 67–90 ms | noisy (0.4–34 ms) |

The cost is paid once per gopls start; gopls's own `go` calls are already inside the namespace.

### 3.4 Evaluation speed (`measure.sh`, `results/measure-{8c,2c}.txt`)

Synthetic repo: N `bob.go`, package i imports packages i-1, i/2, i/3 (a chain: the worst case
for parallel compilation), half of the dirs also contain real Go code. Scenarios: **cold** =
empty GOCACHE (includes compiling the needed std packages and the framework); **noop** =
nothing changed; **leaf-body** / **root-body** = a body change in the last / first package;
**root-api** = new exported identifier in the first package; **new-repo** = a different repo of
the same size with warm std+framework cache. Times are wall clock of the whole evaluation
(scan + generate + `go build` + run). Run time of the evaluator binary itself is in brackets.

| N | cold | noop | leaf-body | root-body | root-api | new-repo | GOCACHE after cold |
|---|---|---|---|---|---|---|---|
| 10 [8c/15G] | 6.4 s | 0.11 s | 0.31 s | 0.41 s | 0.53 s | 0.53 s | 39 MB |
| 100 [8c/15G] | 8.8 s | 0.13 s | 0.37 s | 0.46 s | 0.77 s | 2.8 s | 45 MB |
| 1,000 [8c/15G] | 37.5 s | 0.50 s | 0.87 s | 0.97 s | 1.27 s | 31.0 s | 133 MB |
| 10 [2c/4G] | 10.3 s | 0.10 s | 0.31 s | 0.38 s | 0.51 s | 0.49 s | 39 MB |
| 100 [2c/4G] | 12.3 s | 0.14 s | 0.39 s | 0.47 s | 0.72 s | 2.7 s | 45 MB |
| 1,000 [2c/4G] | 39.4 s | 0.55 s | 0.88 s | 0.95 s | 1.24 s | 30.0 s | 133 MB |

Evaluator run time (all definitions called, graph sorted, JSON written): 4 ms (N=10), 6 ms
(100), 20 ms (1,000) [8c/15G]. Scan of 1,000 dirs: 67 ms (a first, loaded run).

Wide graph (`SYNTH_FLAGS=-wide`: package i imports i/2 and i/3, import depth ≈ log2 N),
N = 1,000, measured on a quiet machine (load < 1, `results/measure-wide-*.txt`):

| N=1,000 wide | cold | noop | leaf-body | root-body | root-api |
|---|---|---|---|---|---|
| [8c/15G] | 10.8 s | 0.34 s | 0.69 s | 0.76 s | 0.81 s |
| [2c/4G] | 23.9 s | 0.47 s | 0.78 s | 0.81 s | 1.02 s |

The chain graph is depth-bound: its first compile takes ~30 s on both profiles (`new-repo`
≈ 30–31 s at 8c and 2c), i.e. about 30 ms per package on the critical path, because the go
command compiles a package only after everything it imports. CPU per package is 35–45 ms
(user+sys / N). Real repos are wide, so the wide numbers are the better estimate; the chain is
the upper bound. (In the wide run the `new-repo` row used a chain repo by mistake; ignore it.)

The 8c and 2c rows were measured back to back while other spikes were running (load 3–14);
cold and new-repo numbers depend most on that and must be rerun with `measure.sh` on a quiet
machine. A first run under load 20 gave 83 s for N=1,000 cold [8c/15G].

Toolchain needed for evaluation: Go 1.26.8 from the pin, closure 279 MB (`nix path-info -S`).

### 3.5 Other verifications

- **Determinism (R-FW-008):** two checkouts of `synth100` at different paths give byte-identical
  graphs (`-trimpath` positions).
- **Cross-repo (R-FW-022):** module `example.com/other` served from a `file://` GOPROXY, its real
  code does not compile (`var X = generatedByBob`). The consumer's `cmd/api/bob.go` imports
  `example.com/other/libs/bar`. Evaluation works through the `-modfile` copy; label
  `example.com/other//libs/bar:bar`, position `example.com/other@v1.0.0/libs/bar/bob.go:12`.
  gopls in the consumer: completion `bar.` → `Lib`, `X`; definition into the module cache;
  no diagnostics in the consumer file.
- **Error positions (R-NIX-012, R-FW-016):** a type error in `cmd/api/bob.go` (renamed main
  package) is reported as `cmd/api/bob.go:17:51: cannot use Binary(p) …`; a duplicate label as
  `example.com/acme/cmd/api/bob.go:24: package //cmd/api:api is already defined at …:15`.
- **Rule checks** in `bobeval` (texts in §5.6) fire on V2, V3, V4.
- **Fallback tree** (`examples/acme-tree`): root `go build/vet` clean, `bob/` module vet clean,
  evaluation gives the same labels; gopls without any tag: completion/definition in
  `bob/cmd/api/bob.go` work, and `greet.` in real code offers only `Hello`.
- **Name clash found:** the framework's own `pkg/bob/bob.go` was picked up by the scanner as a
  build definition of the dependency module. Framework files must not be called `bob.go`.

## 4. Answer

**Yes, `bob.go` next to the code works with gopls**, as a file with `//go:build bob` in the
directory's own package, provided bob enforces five rules (§5.6) and generates the evaluator
with an overlay. Measured: completion, go-to-definition, hover and type errors work in
`bob.go` and in normal code in the same gopls session, also through the shim as a non-root
user, in both namespace modes.

The costs are real but small and checkable: bob identifiers show up in real code's completion
of the same package, definitions in `package main` directories are leaves, flat C/C++
directories need a generated cgo stub, and tag-only import cycles must be rejected. The
fallback tree has none of these costs but separates definitions from code. Recommendation:
next to code (option a).

## 5. SPEC input

### 5.1 File convention [proposal]

- Exactly `bob.go`; at most one per directory; the file's directory is the label directory.
- Line 1: `//go:build bob`. Nothing else may be in the constraint.
- Package clause: the package name of the non-test `.go` files in the directory; with none, the
  directory name lowercased with every character outside `[a-z0-9_]` removed, prefixed `x` if it
  then starts with a digit or is empty (bob's error text proposes the name).
- Directories skipped by the scan: names starting with `.` or `_`, `node_modules`, `testdata`,
  nested Go modules (they are their own bob repo).
- No file called `bob.go` anywhere else, including the framework (`pkg/bob/core.go`).
- One bob-generated `go.mod` at the repo root (also for repos without Go code) that requires
  `github.com/megakuul/bob`. Multi-module repos: unverified, see §6.

### 5.2 Build tag and editor/gopls settings bob writes [proposal]

- `.bob/bin/gopls` shim runs `bob exec gopls`; its environment adds `GOFLAGS=-tags=bob` (gopls
  and its `go` children only; `bob exec go` and `bob dev` do **not** set it).
- `.vscode/settings.json` (written on request, merged):

```json
{
  "go.alternateTools": { "go": "${workspaceFolder}/.bob/bin/go", "gopls": "${workspaceFolder}/.bob/bin/gopls" },
  "go.toolsManagement.autoUpdate": false,
  "gopls": { "build.buildFlags": ["-tags=bob"], "build.directoryFilters": ["-.bob"] }
}
```

- Other editors: run `.bob/bin/gopls` (or `bob env` puts `.bob/bin` first in PATH) and set
  gopls `buildFlags = ["-tags=bob"]`. Neovim lspconfig:
  `settings = { gopls = { buildFlags = { "-tags=bob" } } }`.
- The VS Code JSON itself was not tested in VS Code; only the gopls behaviour behind it was.

### 5.3 Evaluator generation [proposal]

1. Scan (§5.1), parse each `bob.go` (imports, top-level names), check rules R1–R5.
2. Key = hash(all `bob.go` contents and paths, `go.mod`, `go.sum`, bob version, Go version,
   platform/variant set). Same key as last time → reuse `.bob/eval/bob-eval`, skip step 3–5
   (no-op cost then = scan + hash, not 0.1–0.5 s of `go build`). [not built in the spike]
3. Write `.bob/eval/overlay.json`:
   - every other compilable file (`.go .c .cc .cpp .cxx .m .f .F .for .f90 .s .S .sx .swig
     .swigcxx .syso`) in a `bob.go` directory → `""` (hidden);
   - `bob.go` of a `package main` directory → `.bob/eval/rename/<n>.go`, same text with the
     package clause renamed to `bobmain<n>` on the same line;
   - `<root>/_bobeval/main.go` → `.bob/eval/main.go`:

```go
//go:build bob

// Code generated by bob. DO NOT EDIT.
package main

import (
	"github.com/megakuul/bob/pkg/bob"
	_ "example.com/acme/cmd/api"
	_ "example.com/acme/libs/foo"
	// … every bob.go package of the repo, sorted by directory
)

func main() {
	bob.MainModule = "example.com/acme"
	bob.Main()
}
```

4. For each dependency module (`go list -m -f '{{if not .Main}}{{.Dir}}{{end}}' all`) that
   contains `bob.go` files: copy its `go.mod` and `bob.go` files to
   `.bob/eval/mods/<module path with / → !>/` and write `.bob/eval/go.mod` = root `go.mod` +
   `replace <module> => <copy>` lines (and a copy of `go.sum`).
5. `GOWORK=off CGO_ENABLED=0 GOFLAGS= GOCACHE=<bob cache>/go-build go build -tags=bob -trimpath
   -modfile=<root or .bob/eval>/go.mod -overlay=.bob/eval/overlay.json -o .bob/eval/bob-eval
   ./_bobeval`, inside the namespace with the pinned Go. Compiler output: replace
   `./.bob/eval/rename/<n>.go` with the original `bob.go` path, then format as a bob error.
6. Run `bob-eval` (stdout: graph; stderr: bob-formatted errors; output size-capped, §5.4 of
   BOOTSTRAP: hostile input). Positions are `<module>[@version]/<dir>/bob.go:<line>`; bob shows
   them repo-relative.

### 5.4 Labels [proposal]

| Package from | Label | Example |
|---|---|---|
| this repo | `//<dir>:<name>` (`//:name` at the root) | `//libs/foo:foo` |
| another bob repo (Go module) | `<module path>//<dir>:<name>` | `example.com/other//libs/bar:bar` |
| nixpkgs | `nixpkgs:<attr>` | `nixpkgs:openssl` |

- `<dir>` = directory of the `bob.go` that made the builder call (`runtime.Caller` of the
  builder, `-trimpath` path minus module path). `<name>` = the builder's name argument,
  `[a-z0-9][a-z0-9_.+-]*` [proposal]. A builder called from a helper in another file gets the
  label of the helper's directory, so builders take the caller depth into account.
- Patterns: `//...`, `//libs/...`, `//libs/foo:all` (R-FW-015).
- Duplicate label → error naming both positions.

### 5.5 API shape used by the examples [proposal]

```go
// Define turns a constructor into a memoised, registered constructor (R-FW-011).
// All registered constructors are evaluated for every (platform, variant) P.
func Define[T any](fn func(p *P) T) func(p *P) T
```

`var X = bob.Define(func(p *bob.P) T {…})` is the one way to declare a package; exported for
cross-references, unexported for local ones. Registration happens at package initialisation
(the only global state, R-FW-011 exception in BOOTSTRAP §5.3).

Example — C++ library (`libs/foo/bob.go`):

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
		Hdrs: "include",
		Std:  cc.CXX20,
		Deps: []cc.Linkable{nixpkgs.Lib(p, "openssl")},
	})
})
```

Example — Go binary (`cmd/api/bob.go`, next to `main.go`):

```go
//go:build bob

package main

import (
	"example.com/acme/crates/rcore"
	"example.com/acme/libs/foo"
	"github.com/megakuul/bob/pkg/bob"
	"github.com/megakuul/bob/pkg/cc"
	"github.com/megakuul/bob/pkg/golang"
)

// Binary is the api server (label //cmd/api:api).
var Binary = bob.Define(func(p *bob.P) *golang.Binary {
	return golang.NewBinary(p, "api", golang.BinaryOpts{
		CGo:  true,
		Link: []cc.Linkable{foo.Lib(p), rcore.Crate(p)},
		Requires: []golang.Module{
			{Path: "github.com/google/uuid", Version: "v1.6.0"},
		},
	})
})
```

Example — npm app (`web/bob.go`):

```go
//go:build bob

package web

import (
	"github.com/megakuul/bob/pkg/bob"
	"github.com/megakuul/bob/pkg/npm"
)

// App is the Svelte frontend (label //web:web). package.json and tsconfig.json are generated.
var App = bob.Define(func(p *bob.P) *npm.App {
	return npm.ViteApp(p, "web", npm.AppOpts{
		Srcs:    bob.Glob("src/**", "vite.config.ts", "svelte.config.js"),
		Deps:    npm.Deps{"svelte": "^5.0.0"},
		DevDeps: npm.Deps{"vite": "^6.0.0", "@sveltejs/vite-plugin-svelte": "^5.0.0"},
		Runtime: npm.Node,
	})
})
```

Type safety measured in gopls: passing `Binary(p)` where `cc.Linkable` is expected →
`*golang.Binary does not implement cc.Linkable (missing method LinkInfo)`.

### 5.6 Rules and error texts [proposal]

Checked by bob before `go build`, all errors listed together, sorted by path:

| Rule | Error text |
|---|---|
| R1 tag | `<dir>/bob.go: the first line must be "//go:build bob" so that go build, go vet and go test ignore it` |
| R2 package name | `<dir>/bob.go: package <x>, but the Go code in this directory is package <y>; bob.go must use package <y>` |
| R3 main is a leaf | `<dir>/bob.go: imports the build definitions of <maindir>/, which is a main package and cannot be imported; define the shared package in a bob.go of a non-main directory` |
| R4 no shared identifiers | `<dir>/bob.go:<line>: <Name> is also declared at <file>:<line>; bob.go shares its package with that code, rename one of them` |
| R5 C-family sources next to bob.go | not an error: bob materialises `<dir>/zz_bob_cgo.go` (gitignored) with `//go:build bob && cgo` / `package <x>` / `import "C"` when generating native files (R-DEV-002) |
| R6 tag-only import cycle | `<dir>/bob.go imports <dir2>, whose Go code imports <dir> (cycle exists only for build definitions); move the definition` [proposal, not implemented] |

### 5.7 Budgets [proposal, rerun on a quiet machine first]

- Evaluation no-op (unchanged bob.go): ≤ 50 ms at 1,000 files through the step-2 key (scan
  67 ms measured under load → needs an mtime/size index to reach that).
- One bob.go changed: ≤ 1.5 s at 1,000 files [8c/15G], ≤ 3 s [2c/4G].
- First evaluation of a 1,000-file repo, empty cache, wide graph: ≤ 15 s [8c/15G], ≤ 30 s [2c/4G] (measured 10.8 s / 23.9 s); deep chains are depth-bound (~30 ms per level).
- Shim start: ≤ 15 ms.

## 6. Decisions, risks, open points

### Agent decisions

| # | Decision | Reason |
|---|---|---|
| S1-1 | Tag name `bob`, constraint exactly `//go:build bob`. | Short, obvious; any extra term would hide files on some platforms. |
| S1-2 | `bob.go` uses the directory's package name, never a fixed `package bob`. | Only working option (V2). |
| S1-3 | Registration through `bob.Define` at init, generated main only blank-imports. | No AST analysis of user code needed to find definitions; deterministic init order. |
| S1-4 | Evaluator hides real code via overlay instead of compiling it. | User code may need codegen/cgo that only exists after evaluation. |
| S1-5 | Dependency bob repos via `-modfile` copies. | Overlays below GOMODCACHE are refused (measured). |
| S1-6 | `-trimpath`, labels from caller positions. | Checkout-independent graph (verified); no label strings besides the name. |
| S1-7 | `GOFLAGS=-tags=bob` only in the gopls shim environment, plus buildFlags in editor settings. | Editor-agnostic; normal `go` invocations must not merge bob.go into real builds. |
| S1-8 | Flat C/C++ dirs: generated cgo stub, not a layout rule. | Keeps "bob replaces CMake" layouts working; verified clean in gopls. |
| S1-9 | Main-package `bob.go` is a leaf, with an explicit error. | Matches what gopls shows; no hidden eval-only behaviour. |

### Owner decision needed (D22 confirmation)

- **(a) Next to code, rules R1–R6 (recommended).** Meets D22 as written; costs listed in §4.
- **(b) Fallback `bob/` tree:** `bob/<dir>/bob.go`, separate `bob/` Go module, root `go.work`,
  no tag, no rules R2–R6, no completion pollution; sources referenced from another tree, and
  moving a directory means moving two places. Verified working (§3.5).
- **(c) Support both:** not recommended (two ways for one task, R-FW-014).

### Open risks

1. Completion pollution in real Go code (bob.go identifiers of the same package) can't be
   avoided under (a); size: one item per definition.
2. Pipelines/releases (R-FW-040) that want typed handles of a Go binary defined in a `main`
   directory can't import it. Options for the SPEC: define releases/images in the binary's
   own `bob.go`, or let pipelines select by label pattern. Not decided here.
3. Cold first evaluation of large repos is dominated by compiling one Go package per `bob.go`
   (~35–60 ms CPU each) plus std/framework. A remote GOCACHE (`GOCACHEPROG`) from the bob
   server could remove it; not tested.
4. The cgo stub needs a C compiler in gopls's environment when CGO_ENABLED=1 (the `bob dev`
   toolchain has one; a repo without C has no such dirs).
5. gopls memory grows with the tag (239 MB vs 90 MB at 1,000 files).

### Still to verify

- Repos with several Go modules / `go.work` (only single-module + dependency modules tested).
- Real editors (VS Code, Neovim) with the generated settings; only gopls over LSP was tested.
- Rule R6 (tag-only cycle) detection, the step-2 eval key, and the mtime index.
- Quiet-machine rerun of `measure.sh` (both profiles).
