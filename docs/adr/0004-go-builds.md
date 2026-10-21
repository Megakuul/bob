# ADR 0004 — How bob builds Go

- **Status:** Accepted (locked) — owner at Gate S, 2026-10-21
- **Requirements:** R-GO-001…004, R-FW-006/007, R-FW-020, R-NFR-001/002/006/008
- **Evidence:** `docs/spikes/s02-go.md`, code and `measure-results.md` in `spikes/s02-go/`
- **Builds on:** ADR 0001 (strategy B: bob computes derivations), ADR 0002 (embedded Nix, `bob dev` env)

## Context

S0a showed that one `go build` per Go binary with an empty cache costs 11–12 s on every change
and dominates incremental builds. R-FW-020 asks Stage 2 to pick Go granularity by measurement.
Three options were built on backend B and compared on the S0a fixture (cgo binary, 3 modules,
32 packages) and on a 200-package workspace: per-module derivations, per-package derivations
(direct `go tool compile`/`link`), and per-package derivations running `go build` with a
store-backed `GOCACHEPROG`.

## Decision

1. **Planning in bob, compiling without cmd/go.** bob reads Go sources in-process (`go/build`,
   own import→module resolution) and emits derivations that call `compile`, `cgo`, `gcc`,
   `pack` and `link` with an importcfg. Package archives are the outputs. cmd/go is not used
   inside build derivations, except to build the std library.
2. **Granularity "hybrid":** one derivation per third-party module (only the used packages),
   one per workspace package (its own files as its own store path), one link derivation (or
   inside the main package's derivation). Modules in an import cycle are merged into one group.
3. **std library**: one derivation per toolchain × target × cgo × flags (`go list -export -deps std`),
   substitutable like any other output; plus a `go-tools` derivation (`cmd/pack`).
4. **cgo** packages get their own derivation with cmd/go's step sequence; C/C++/Rust deps are
   typed (`CDeps`) and become `-I` CFLAGS and cgo `-ldflags`. Only cgo packages and the link
   depend on C libraries.
5. **go.mod/go.sum are generated** from bob.go + bob.lock (canonical `go X.Y.Z`, sorted
   requires with `// indirect`, go.sum with `h1:` for package modules and `/go.mod` for the pruned
   graph), written read-only, checked byte-for-byte before every build; drift is refused with a
   diff in bob.go terms. User tooling gets a bob-written `GOPROXY=file://…`,
   `GOFLAGS=-mod=readonly`, `GOSUMDB=off`, the pinned toolchain, `CC` and cgo flags.
6. **Module fetching**: one fixed-output derivation per module whose output is the unpacked,
   mode-normalised module tree (NAR hash in bob.lock); the fetcher also checks `h1:`.
   bob.lock records `tree`, `h1`, `modH1`, `go` for every module of the pruned graph.

## Evidence (indicative; busy host)

| | S0a (`go build`, 1 drv) | hybrid | package | GOCACHEPROG per package |
|---|---|---|---|---|
| fixture: main.go changed [2c/4G] | 41.9 s | 4.3 s | 3.1 s | 4.3 s |
| fixture: library changed [2c/4G] | 42.7 s | 4.0 s | 4.6 s | 4.9 s |
| fixture: cold (std cached) [2c/4G] | 31.3 s | 11.9 s | 12.7 s | 19.5 s |
| 200-pkg workspace, bottom-layer change [2c/4G] | 26.1 s | (= package) | 3.0 s | 7.8 s |
| Go derivations (fixture) | 1 | 6 | 32 | 32 |

All 72 Go derivations rebuild bit-identically; the binary is identical whichever grouping is used.
`go build`, `go vet` and `gopls` work on the generated go.mod/go.sum; `go get`/`go mod edit`
are blocked (read-only + bob-only proxy) or detected (drift).

## Rejected

- **GOCACHEPROG per package** (as in gobuild.nix; idea studied, no code taken): correct, but each
  derivation re-loads and re-hashes the whole closure with cmd/go (2–5× slower incremental,
  ~2.5× slower cold), needs a vendor tree or module cache in every sandbox, and cmd/go hashes
  `CGO_CFLAGS` into every cgo package's action ID, forcing global cgo flags.
- **Per-module for the workspace**: cheapest cold build and flat ~5–6 s, but every one-file
  change recompiles the whole module; per-package is 2–5× faster for typical edits.
- **Zip-hash FODs** (S0a): proxy zips are not byte-stable across sources (x/sync v0.24.0 differs
  between proxy.golang.org and VCS; the unpacked tree and `h1:` are equal).

## Consequences

- bob owns a Go "builder" (std, compile, cgo, link, embed; later asm, syso, tests, cross).
  It should live in the static bob-builder binary rather than bash (ADR 0001 consequence).
- `go mod tidy`/`go get` are unsupported by design; `bob lock` implements MVS over `.mod` files.
- Per-derivation overhead becomes the floor for wide changes in big workspaces; early cutoff
  (`-linkobj`, export data added to the store by content, verified working in the spike) is the
  planned improvement and needs a follow-up spike on real code before it becomes the default.

## Owner amendments

- **D49** (strategy B) confirms Decision 1: bob computes every Go derivation itself.
- **D51 / D59** (dev environment, "cd + editor just works"): the user env of Decision 5
  (`GOPROXY=file://…`, `GOFLAGS=-mod=readonly`, `GOSUMDB=off`, pinned toolchain) is delivered
  through `bob env`, the direnv-style hook and `.bob/bin` shims (SPEC §02); gopls runs through
  the shim with `-tags=bob`.
- **S13 / D56** (workers never build user FODs): module FODs are realised by bob's Go fetcher
  and `nix store add`, never by a Nix FOD build (SPEC R-FW-063).
- **A11** (bob-level early cutoff): the `-linkobj` export-data cutoff is specified as
  R-GO-005, default **off** until a Stage 3 measurement on bob's own code (quiet log:
  bottom-layer change 3.03 → 1.76 s, but main-only 1.22 → 1.74 s and cold 10.2 → 13.9 s).
- Clarified: generated go.mod/go.sum are committed (Go module consumers need them,
  R-FW-022); tests use a bob-generated `_testmain.go` ([open] fallback `go test -c`).
- Exact API, layout, lock schema `lock.GoModule`, budgets: SPEC §03.2, §03.8.
