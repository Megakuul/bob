# Spike S2 — How bob builds Go

Status: **done (indicative numbers)**, 2026-10-20, host `vibe-engine`. Code: `spikes/s02-go/`
(throwaway, module `bobspike/s02`, built on a copy of backend B). ADR: `docs/adr/0004-go-builds.md`.

## 1. Question and requirements

How should bob turn Go code into derivations so builds are correct, reproducible, cacheable and
fast on one-file changes? How do cgo, generated `go.mod`/`go.sum`, drift detection and module
fetching work? Requirements: R-GO-001…004, R-FW-006/007, R-FW-020, R-NFR-001/002/006/008.

## 2. What was built

| Path | What |
|---|---|
| `gopkg/plan.go` | **Planner**: reads Go sources in-process with `go/build` (`ImportDir`, own import→module resolution, build tags, cgo flags, `//go:embed` expansion, `-lang` from each module's `go` directive). No `go` command, no network. |
| `gen/gopkg.go` | **Direct-compile derivations**: std library as one derivation; then one derivation per *group* of Go packages calling `compile` / `cgo` / `gcc` / `pack` / `link` with an `importcfg`. Groups: `package`, `module`, `hybrid` (third-party per module, workspace per package). Packages inside a group compile in parallel by level. `-linkobj` variant for early cutoff. |
| `gen/cacheprog.go`, `cacheprog/main.go` | **GOCACHEPROG variant**: one derivation per package running plain `go build`; `bob-gocacheprog` (157 lines) serves dependency outputs as read-only cache layers and writes new entries to `$out`. Modules laid out as `vendor/` (`-mod=vendor`). |
| `cmd/bob-s02` | Driver (copy of `bob-b`): `-go single|module|hybrid|package|cacheprog|cutoff`. `single` = S0a backend B unchanged. `cutoff` builds level by level and adds export data to the store by content. |
| `gomod/gomod.go`, `cmd/gomodcheck` | go.mod/go.sum generation, `h1:` hashing in Go (no `x/mod`), drift detection with explained diffs. |
| `fixture/` | Copy of the S0a fixture plus one library package `api/internal/msg` (main imports it), so a library change can be measured. |
| `fixturegraph/` `GoSynthetic` | 200-package pure-Go workspace: 10 layers × 20 packages, each imports two of the layer below; `main` imports the top layer. |
| `measure.sh`, `verify.sh` | Measurements (both profiles) and correctness checks. Results: `spikes/s02-go/measure-results.md`. |

Spike code: about 1,700 lines of Go on top of the copied B packages.

## 3. Results

### 3.1 Correctness and reproducibility (`verify.sh`)

| Check | Result |
|---|---|
| api runs under `env -i`, prints `digest=81b637d8… numhex=3432 proto_bytes=116` | OK in module, hybrid, package, cacheprog, cutoff |
| `nix build --rebuild` of every Go derivation (all four modes + std, 72 drvs) | **0 differ** (bit-identical rebuilds) |
| api binary identical across module / hybrid / package grouping | identical (`2593aeec…`) **after one fix**: the per-package work dir was `$TMPDIR/p<index-in-group>`; cgo puts that path into C DWARF, so grouping changed the binary. Now `$TMPDIR/b<hash(importpath)>`. |
| `go version -m` on the bob-built binary | shows path, module, deps, build settings (bob writes the `modinfo` line into the link importcfg like cmd/go) |
| RUNPATH of api | libfoo, glibc, gcc-lib (cc-wrapper, as in S0a) |
| Body-only change in a library: does `go build` itself recompile dependents? | yes (`go build -x` recompiles `main` after editing `msg`): plain cmd/go has no early cutoff either |
| `-linkobj`: export data after a body change in a **non-inlinable** function | byte-identical (`bc872c53…` both) → early cutoff possible |
| same, for an **inlinable** function (`msg.New`) | export data changes (inline bodies are export data) → no cutoff |

### 3.2 Fixture (cgo api + libfoo + rcore + protobuf, 32 Go packages, 3 modules)

"cold" = all Go derivation outputs deleted; std archives, C/C++/Rust deps, downloads stay.
Cells: wall time / derivations built. Peak RSS is ≤ 160 MB everywhere except `single` and the
std build (≤ 303 MB).

| Mode | Go drvs | cold [8c/15G] | cold [2c/4G] | no-op [8c] / [2c] | main.go [8c] / [2c] | internal/msg [8c] / [2c] |
|---|---|---|---|---|---|---|
| single (S0a B) | 1 | 22.7 s | 31.3 s | 0.47 / 0.35 s | 26.2 / **41.9 s** | 19.7 / 42.7 s |
| module | 4 | 7.5 s | 10.5 s | 0.26 / 0.29 s | 3.5 / 3.7 s | 3.8 / 3.6 s (1) |
| hybrid | 6 | 9.1 s | 11.9 s | 0.22 / 0.58 s | 3.5 / 4.3 s | 4.8 / 4.0 s (2) |
| package | 32 | 27.7 s | 12.7 s | 0.95 / 0.50 s | 6.4 / **3.1 s** | 8.1 / 4.6 s (2) |
| cacheprog | 32 | 41.4 s | 19.5 s | 1.02 / 0.58 s | 16.7 / 4.3 s | 9.9 / 4.9 s (2) |
| std library alone (once per toolchain × cgo) | 1 | ≈ 17 s ¹ | ≈ 54 s ¹ | | | |

¹ "cold incl. std" (44.7 s / 66.7 s) minus "cold" of package mode. Std is shared by every Go
binary with the same toolchain and settings and comes from a cache in practice.

Extra, measured once afterwards on a quieter host [8c/15G]: `foo.cpp` changed → hybrid 3.9 s
(libfoo + main package + link) vs. 12.3 s in S0a.

### 3.3 Synthetic 200-package workspace (pure Go)

| Mode | Go drvs | cold [8c] / [2c] | no-op | incr main | top layer (1 dependent) | middle layer | bottom layer |
|---|---|---|---|---|---|---|---|
| single | 1 | 16.8 / 24.6 s | 0.27 / 0.27 | 19.4 / 27.3 | 14.4 / 25.8 | 8.4 / 25.6 | 12.1 / 26.1 |
| module (whole workspace = 1 drv) | 1 | 3.5 / 8.9 s | 0.44 / 0.22 | 3.6 / 5.8 | 4.3 / 5.8 | 4.8 / 5.7 | 5.9 / 5.7 |
| package | 201 | 35.2 / 12.7 s | 2.27 / 0.38 | 3.0 / **1.2** | 4.1 / **1.3** (2) | 7.3 / **2.0** (16) | 14.6 / **3.0** (36) |
| cacheprog | 201 | 85.1 / 32.8 s | 1.70 / 0.63 | 3.3 / 2.4 | 4.8 / 2.9 | 5.9 / 5.2 | 8.3 / 7.8 |

Rerun once afterwards on a quieter host [8c/15G], package vs. cutoff:

| | cold | no-op | main | top | middle | bottom |
|---|---|---|---|---|---|---|
| package | 16.4 s | 0.41 | 1.08 | 1.33 (2 drvs) | 2.12 (16) | 3.31 (36) |
| cutoff | 24.1 s (11 waves) | 0.82 | 2.84 | 2.78 | 2.62 | 3.04 (1 compile + link) |

### 3.4 Evaluation (graph → derivations in store, package mode, includes per-package source import)

| N packages | new [8c] / [2c] | again [8c] / [2c] | peak RSS |
|---|---|---|---|
| 200 | 1.62 / 0.98 s | 0.37 / 0.33 s | 35 MB |
| 1,000 | 6.76 / 3.68 s | 2.10 / 1.14 s | 39 MB |

Planning itself (go/build over 32 / 200 packages) takes 13–34 ms / < 0.2 s; the rest is source
import (`nix-store --add`, one call) and drv computation, as in S0a.

**About the numbers.** Other spikes were building during the run. The [8c/15G] pass ran first,
on the busiest host, and is often *slower* than [2c/4G], which ran later. Compare modes within a
column, not across profiles. Every number depends on rerunning `measure.sh` on a quiet machine.
Where reruns exist (3.3 second table), they match the [2c/4G] ordering.

### 3.5 go.mod / go.sum, tooling, drift (`cmd/gomodcheck`, host tests)

| Check | Result |
|---|---|
| `h1:` of the three module zips and of five go.mod files, computed by bob | identical to the go.sum cmd/go writes, and to sum.golang.org for go-cmp v0.7.0 and golang/protobuf v1.5.0 |
| bob-generated go.sum vs. go.sum written by `go build -mod=mod` | 0 lines missing; bob adds `/go.mod` lines for the two pruned-graph modules |
| `go build ./...`, `go vet ./...`, `gopls check` (cgo main, library, generated `.pb.go`) with bob's env, `GOFLAGS=-mod=readonly`, `GOPROXY=file://<bob proxy>` | all OK, no diagnostics; gopls 0.24.0 uses `go` from PATH (nixpkgs 1.26.8) |
| same, with GOSUMDB at its default | OK (go.sum is complete, so no lookups are needed; not proven that no network request happened) |
| `go list -m all` | **fails** unless the proxy also has `.mod`/`.info` of the pruned-graph modules (go-cmp, golang/protobuf); works once they are added |
| `go mod tidy` | fails: it wants test dependencies of dependencies (go-cmp zip). Not supported with bob. It also rewrites `go 1.26` to `go 1.26.0` → bob writes the canonical `go 1.26.0`. |
| `go get golang.org/x/text@v0.20.0` | fails: module not in bob's proxy |
| `go mod edit -require=… -go=1.25.0` (writable go.mod), then bob's check | drift reported (text below) |
| go.mod/go.sum mode 0444, user `bobtest`: `go mod edit`, `go get`, `go mod tidy` | all fail (`open go.mod: permission denied`), files unchanged. As root, 0444 does not protect. |
| cgo in gopls/vet | needs `CC` (cc-wrapper) and `CGO_CFLAGS`/`CGO_LDFLAGS` with the deps' `-I`/`-L`; without them gopls reports `undefined: C.CString` |

### 3.6 Module fetching

| Check | Result |
|---|---|
| zip from proxy.golang.org vs. `GOPROXY=direct go mod download` (VCS): uuid v1.6.0 | byte-identical |
| same for golang.org/x/sync v0.24.0 | **different bytes** (sha256 `ca3fcf…` vs `99f164…`), same `h1:`, same NAR hash of the unpacked tree (`sha256-H9UJMd7…`) |

So a flat zip hash (S0a's FOD) pins bob to one proxy's bytes. The unpacked tree's NAR hash and
`h1:` are stable across sources.

## 4. Answer

1. **Direct compilation, not cmd/go, inside derivations.** bob plans the Go package graph
   itself (go/build, in-process) and each derivation calls `compile`/`cgo`/`link` with an
   importcfg. Outputs are package archives. This was faster than GOCACHEPROG in every
   incremental case, needs no module cache, no go.mod and no vendor tree in the sandbox, and
   gives exact per-package cgo flags. GOCACHEPROG works and is correct (each drv compiles only its
   own package: `hits=465 misses=3`), but every derivation re-runs `go build` package loading
   and source hashing for the whole closure (2–5× slower incremental, 2.5× slower cold). Also,
   cmd/go hashes `CGO_CFLAGS` into the action ID of *every* cgo package, std included, so the
   cgo flags must be identical in all derivations.
2. **Granularity: `hybrid`.** Third-party modules are one derivation per module (they change
   only on version bumps; fewer derivations: fixture 6 instead of 32 Go drvs, cold 9 s vs 28 s).
   Workspace packages are one derivation per package: the best one-file times (fixture main.go
   3.1 s vs 41.9 s for S0a at [2c/4G]; syn200 1.2–3.0 s vs 5.7 s for a whole-workspace
   derivation). A whole-module derivation is cheaper cold (3.5 s for 200 packages) and stays
   flat at ~5–6 s. Modules that import each other in a cycle must be merged into one group (the
   spike only detects and reports such cycles; merging is not implemented).
3. **std library: one derivation per (toolchain, GOOS/GOARCH, cgo, flags)**, built with
   `go list -export -deps std` (17–54 s once, then cached and substitutable). Plus a tiny
   `go-tools` derivation for `cmd/pack`, which Go 1.24+ no longer ships prebuilt.
4. **Early cutoff (`cutoff`) works but is not the default yet.** With `-linkobj`, dependents
   compile against export data that bob adds to the store by content; a non-API change then
   rebuilds one package + link (verified: bottom-layer change, 1 compile instead of 36). With
   this host's per-wave overhead and tiny synthetic packages it is not faster (2.6–3.0 s flat vs
   1.1–3.3 s), and inlinable-function changes defeat it. It pays off with larger packages and
   deep graphs; that needs a follow-up measurement on real code (see open risks).
5. **cgo**: a cgo package is compiled in its own derivation with the same sequence as cmd/go
   (`cgo` → gcc objects → `_cgo_.o` → `-dynimport` → compile → `pack r`). bob passes `-I` of the
   typed C/C++/Rust deps as CFLAGS and `-L/-l` as cgo `-ldflags` (recorded in the archive, used
   by the final external link). Only cgo packages and the link depend on the C libraries, so a
   `foo.cpp` change rebuilds libfoo + the cgo package + link (3.9 s), not the Go module.
6. **go.mod/go.sum are generated, read-only, and checked**; bob.lock carries `h1:` hashes for
   every module of the pruned module graph; the user's tooling gets a bob-written GOPROXY
   directory and `GOFLAGS=-mod=readonly`. All tooling checks passed.
7. **Fetching**: fixed-output derivation per module whose output is the **unpacked, normalised
   module tree** (recursive hash), not the zip; `h1:` in bob.lock is checked as well.

## 5. SPEC input

### 5.1 API **[proposal]**

Package `bob.build/bob/golang` (`go` is a Go keyword, so the package cannot be named `go`).

```go
// bob.go next to api/go.mod (build tag keeps it out of the program; see open risks)
//go:build bob

package api

import (
	"bob.build/bob"
	"bob.build/bob/golang"
	"example.com/repo/libfoo"
	"example.com/repo/proto"
	"example.com/repo/rcore"
)

// Module describes one Go module. Its go.mod/go.sum are generated from this.
var Module = bob.Memo(func() *golang.Module {
	return golang.NewModule(golang.ModuleOptions{
		Path: "example.com/api",
		Go:   "1.26",                    // language + toolchain (R-GO-004); nixpkgs go_1_26 unless Toolchain is set
		Require: []golang.Requirement{   // direct requirements, exact versions (R-GO-002)
			golang.Require("github.com/google/uuid", "v1.6.0"),
			golang.Require("golang.org/x/sync", "v0.24.0"),
			golang.Require("google.golang.org/protobuf", "v1.36.11"),
		},
		Generated: []golang.Generated{   // materialised into the tree (R-FW-007), built from the derivation
			{Dir: "gen/greetingv1", From: proto.Greeting().Go()},
		},
	})
})

// API is the binary. Library packages need no declaration: bob follows the imports.
var API = bob.Memo(func() *golang.Binary {
	return Module().Binary("./cmd/api", golang.BinaryOptions{
		Name:  "api",
		CDeps: []golang.CDep{libfoo.Lib().Shared(), rcore.Lib().Static()}, // typed; gives -I/-L/-l
		// Tags, Ldflags (-X), Strip, CGo (default: on iff CDeps or cgo files) …
	})
})

// Tests: one package per test derivation (R-FW-024), pattern-based.
var Tests = bob.Memo(func() *golang.TestSet { return Module().Tests("./...") })
```

Rules: one constructor per kind (R-FW-014): `NewModule`, `(*Module).Binary`, `(*Module).Tests`,
later `(*Module).CArchive/CShared`. No `Library` constructor: a Go package becomes a library by
being imported. Granularity is not user-configurable.

### 5.2 Derivation layout **[proposal]**

| Derivation | Key inputs | Output |
|---|---|---|
| `go-std-<variant>` | toolchain, GOOS/GOARCH, cgo + cc, tags/flags | `$out/<importpath>.a` for all std packages + `$out/importcfg` |
| `go-tools` | toolchain | `bin/pack` (and later bob's builder) |
| `go-mod-<module>@<version>` (third-party) | unpacked module FOD, std, direct dep groups | `$out/<importpath>.a` for each *used* package |
| `go-<importpath>` (workspace package) | that package's files only (own store path), std, direct deps' archives; cgo: cc + C deps | `$out/<importpath>.a` |
| `go-link-<binary>` (or inside the main package drv) | all transitive archives, C deps | `bin/<name>` |

- Package source = a store path with exactly that package's files (Go, cgo, C/H, embeds), so an
  edit changes one derivation.
- Every compile: `-p`, `-lang=go<module go directive>`, `-trimpath "<src>=><module>@<version>/<rel>"`
  (workspace: `<module>/<rel>`), `-nolocalimports`, `-complete` when no cgo/asm, `-embedcfg`
  when embeds, `-c=min(cores,4)`; link: `-buildid=`, `-extld=gcc`, `modinfo` from bob.lock.
- Work directories must not depend on grouping (`$TMPDIR/b<hash(importpath)>`).
- Builder scripts > 64 KiB go through `passAsFile` (Linux `MAX_ARG_STRLEN` is 128 KiB; a
  200-package module script hit `Argument list too long`).
- Not yet covered by the spike: assembly (`.s` → `asm -gensymabis`, `-symabis`, `pack`),
  `.syso`, `#cgo pkg-config`, cross-compilation (planner context + std per target), test
  derivations (needs `_testmain.go` generation or `go test` in a derivation).

### 5.3 bob.lock Go entries **[proposal]**

```json
{"go": {"modules": [
  {"path": "golang.org/x/sync", "version": "v0.24.0",
   "tree": "sha256-H9UJMd7zzra4wt24bLTr3y/ooOhJeXlTmULNtrm3gdc=",
   "h1": "h1:iqiDrGoDgyuvAKrhLQthUEzU/R378N78gXbBjxed7oA=",
   "modH1": "h1:sUUOizhqBxiL6pEWpqNLUiaJn1ShEbZ6BBqskPbjZm0=",
   "go": "1.26.0", "direct": true, "packages": true},
  {"path": "github.com/google/go-cmp", "version": "v0.7.0",
   "modH1": "h1:pXiqmnSA92OHEEa9HXL2W4E7lf9JzCmGVUdgjX3N/iU=", "packages": false}
]}}
```

- `bob lock` resolves the full build list with MVS from the declared direct requirements
  (fetching `.mod` files from the configured GOPROXY), and records every module of the pruned
  module graph. `tree` (NAR hash of the unpacked, mode-normalised module) is the FOD hash; `h1`
  is checked by the fetcher too. `packages:false` modules only get their `.mod` fetched.

### 5.4 go.mod / go.sum generation rules **[proposal]**

1. go.mod = header comment (`// Code generated by bob from bob.go. DO NOT EDIT.` + one line
   pointing to bob.go), `module`, `go <X.Y.Z>` (always three parts), one `require` block of direct
   requirements, one block of indirect ones marked `// indirect`, both sorted. No `toolchain`,
   `replace` or `exclude` lines in v1. The bob framework module (for `bob.go` files) is listed too.
2. go.sum = `<path> <version> <h1>` for every package-providing module and `<path> <version>/go.mod
   <modH1>` for every module of the graph, sorted by line. All values come from bob.lock.
3. Both files are written with mode 0444 and their hashes stored in `.bob/state`.
4. bob's env for the user (`bob env`, `.bob/bin` shims, `bob dev`; ADR 0002 Decision 5):
   `GOPROXY=file://$XDG_CACHE_HOME/bob/goproxy` (bob writes `list`, `.info`, `.mod`, `.zip` for
   every lock module; zips written by bob from the tree, valid because `h1:` hashes contents),
   `GOFLAGS=-mod=readonly`, `GOSUMDB=off`, `GOTOOLCHAIN=local`, `GOROOT`/`PATH` to the pinned
   toolchain, `CC` = cc-wrapper, `CGO_CFLAGS`/`CGO_LDFLAGS` = union of the module's typed C deps.
5. Drift check before every build: regenerate in memory and compare bytes. On mismatch, refuse:

```
✗ api/go.mod or go.sum was changed outside bob (they are generated from bob.go)
    ~ go 1.26.0 → 1.25.0 (set GoVersion in bob.go)
    ~ golang.org/x/sync v0.24.0 → v0.25.0  (bob.go declares v0.24.0)
    + golang.org/x/text v0.20.0  (not declared in bob.go)
  hint: declare Go modules in bob.go (e.g. go.Require("golang.org/x/text", "v0.20.0")) and run `bob lock`;
        `go get` and `go mod tidy` are not used with bob. To discard the edit: bob gen --force
```

### 5.5 Budgets **[proposal]** (to be confirmed on a quiet machine; reference profile [2c/4G])

| Budget | Proposal | Measured here [2c/4G] |
|---|---|---|
| no-op build, fixture / 200-package workspace | ≤ 0.6 s / ≤ 0.6 s | 0.58 s (hybrid) / 0.38 s |
| one-file change in a cgo main package (fixture) | ≤ 5 s | 4.3 s (hybrid), 3.1 s (package) |
| one-file change in a library with 1 dependent | ≤ 5 s | 4.0 s (fixture), 1.3 s (syn200) |
| bottom-layer change in the 200-package workspace (36 dependents) | ≤ 5 s | 3.0 s |
| graph → derivations, 1,000 Go packages (new / again) | ≤ 5 s / ≤ 2 s | 3.7 / 1.1 s |
| std library build (once per toolchain variant, normally substituted) | ≤ 90 s | ≈ 54 s |
| peak RSS of bob during a Go build | ≤ 300 MB | ≤ 160 MB (excl. std build) |

## 6. Decisions, risks, follow-ups

### Agent decisions

| # | Decision | Reason |
|---|---|---|
| S2-1 | Planner uses `go/build` in-process with bob's own import resolution, not `go list`. | No toolchain process or network at evaluation; 13–34 ms for the fixture (§2 principle 3, 6). |
| S2-2 | Third-party module zips are unpacked before planning (two phases). | The planner must read their imports; the unpack drvs are cached after the first run (≈1 s once). |
| S2-3 | `vendor/` (not `-mod=mod`) in the cacheprog variant. | No module cache, no go.sum, no network inside the sandbox; worked first time. |
| S2-4 | `go mod tidy` is not supported; bob's go.sum is the minimal set for build/vet/gopls/`go list -m all`. | tidy needs test-only modules of dependencies and rewrites files bob owns. |
| S2-5 | `GOSUMDB=off` in bob's user env. | bob already verifies `h1:` from bob.lock; avoids network lookups from tooling. |
| S2-6 | A modified fixture copy (added `internal/msg`) instead of editing the shared fixture. | Brief: never edit `spikes/gen/`. |

### Owner decision needed

None. The choice of Go granularity is the Stage 2 measurement R-FW-020 asked for (ADR 0004,
Proposed). Early cutoff is a follow-up spike, not a decision yet.

### Open risks

- **Busy host.** All timings are indicative; [8c/15G] was measured under the heaviest load.
- **Per-derivation overhead** dominates small packages: 200 package drvs cold took 12.7–35 s
  against 3.5 s for one module drv. Very large workspaces (thousands of tiny packages) may prefer
  module-level groups; a size heuristic may be needed.
- **bob.go in package directories** (R-FW-002, D22): the planner must exclude `bob.go`
  (`//go:build bob` works with go/build's default tags), and the generated go.mod must then also
  require the bob framework module, which mixes build-definition and program dependencies in one
  module graph. The D22 spike should confirm.
- **Nested module paths / nested packages** (`a` and `a/b` both packages) were not exercised in
  the cacheprog symlink layout (direct compile is not affected).
- **Not verified**: assembly packages, `.syso`, `#cgo pkg-config`, cross-compilation (cgo cross
  needs a cross cc), test derivations, module-level import cycles in practice, bob-written zips
  accepted by the go command (expected from `h1:` semantics), `go list -m all` with GOSUMDB on.
- Closure size: the binary's output also contains `main.a`; production should output only `bin/`
  (closure was 22 paths).

### Still to verify

1. Rerun `spikes/s02-go/measure.sh` and `verify.sh` on a quiet machine (then fill §5.5).
2. Follow-up spike: early cutoff on a real codebase (e.g. bob itself), with waves pipelined and
   inline-aware; measure on [2c/4G].
3. Test derivations: generate `_testmain.go` vs. `go test` inside a derivation.
4. FOD that downloads and unpacks in one step (needs bob-builder with HTTP + unzip) and checks `h1:`.
