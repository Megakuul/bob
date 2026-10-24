# Spike S0a — backend A′ (graph data → bob-owned Nix library)

Status: done (indicative numbers; main agent reruns `measure.sh` on a quiet host). Host: see
`docs/environment.md`. nixpkgs `nixos-26.05@cd4ea8d5` unless stated. Code: `spikes/gen/aprime/`.

## Design

```
bob.go (Go DSL) ──► ir.Graph JSON ──► lib/default.nix (fixed, ships with bob) ──► lib/bob.nix ──► nixpkgs builders ──► nix build
                       (data only)       reads JSON with builtins.fromJSON
```

- **No per-project Nix code exists.** The only per-project input is the graph JSON, passed as
  `--argstr graphFile /abs/graph.json` to `nix build -f lib/default.nix 'packages."//api:api"'`.
- **Purity:** non-flake evaluation of a fixed library file. The JSON is read by path and repo
  sources are imported with `builtins.path` (content-addressed, so identical sources give identical
  store paths no matter where the repo lives). nixpkgs comes from `builtins.fetchTree` with the exact
  rev from the graph. This is as reproducible as a locked flake but avoids flakes' "copy the whole
  repo into the store" step and needs no generated `flake.nix`. Not pure-eval: `--argstr` of an
  absolute path is an impure read by design; the content hashes make it deterministic.
- **Driver** `cmd/bob-aprime` (`build|eval -graph FILE [-target LABEL] [-v] [-nixpkgs REV]`):
  1. `ir.Load` (validation: ordering, unknown deps, cycles) → bob error with `definedAt`.
  2. **Preflight**: `nix eval checks` (bob-owned, uses `tryEval`): for every nixpkgs attribute —
     exists, broken, insecure, unfree, platform, version. Plus Go version of the module vs nixpkgs
     `go`. Each problem → bob error at the package's `definedAt`.
  3. `nix eval drvIndex` → map every bob-created `.drv` (package, download, Go proxy) to its label.
  4. `nix build --log-format internal-json --keep-going`; parses activities (105 = build),
     build log lines (101) and level-0 messages; on failure prints label, `definedAt`, filtered log
     excerpt with sandbox paths rewritten to repo paths, a hint, and `nix log <drv>`.
  5. Any Nix evaluation error at step 2/3 is by contract a bob bug → "internal error" with the last
     `error:` line only; the trace only with `-v`.

### Kind → nixpkgs mapping

| IR kind | Implementation in `lib/bob.nix` |
|---|---|
| `fetch` | `pkgs.fetchurl` (FOD, SRI hash from lock) + `runCommand` to unpack (tar.gz / zip); passthru `file` for the raw download |
| `cc_library` | `stdenv.mkDerivation` with a hand-written build phase (`$CXX/$CC -std= -fPIC`, `-shared -Wl,-soname` or `ar`); dependency headers and `-L` come from cc-wrapper setup hooks of `buildInputs`; nixpkgs link flags via `pkg-config <attr>` |
| `rust_crate` | `pkgs.buildRustCrate` per crate (no cargo, no Cargo.toml); staticlib gets a `lib<name>.a` symlink and exported headers in `$lib/include` |
| `proto_go` | `runCommand` with nixpkgs `protobuf` + `protoc-gen-go` |
| `go_binary` | `stdenv.mkDerivation` + nixpkgs `go`; third-party modules served via a **file:// GOPROXY** assembled from the per-module FOD zips (`GOSUMDB=off GOFLAGS=-mod=mod`), so **no vendorHash** and one cache entry per module; generated `go.mod`; codegen copied into the module; cgo link deps via `buildInputs` (rpath added by ld-wrapper) |

Result: `api` prints `digest=81b637d8…58ce9 numhex=3432 proto_bytes=116` with `env -i`;
`libfoo.so` resolves via RPATH (`/nix/store/…-libfoo-foo/lib`), no `LD_LIBRARY_PATH`.

## Fault catalogue (`aprime/faults/`, regenerate with `faults/make.sh`)

`raw.txt` = plain `nix build` output, `bob.txt` = driver output (no `-v`).

| Fault | Raw Nix (class) | bob output |
|---|---|---|
| unknown nixpkgs attr `opensssl` | **EVAL** error, 21-line trace through `make-derivation.nix` (`while evaluating attribute 'buildInputs'…`) | `✗ //libfoo:foo nixpkgs has no package "opensssl" at revision cd4ea8d57de1 (defined at fixture.go:101)` + hint — caught in preflight, Nix build never starts |
| wrong fetch hash | build error: `hash mismatch in fixed-output derivation …uuid-v1.6.0.zip.drv` + 2 cascade errors | `✗ //third_party:uuid-v1.6.0.zip downloaded file does not match the hash in bob.lock` + url / expected / got + "re-lock" hint |
| missing link dep (rcore dropped from api) | build error, cgo `fatal error: rcore.h: No such file or directory` | `✗ //api:api package failed (defined at fixture.go:130)`, excerpt with `fixture/api/cmd/api/main.go:7:10`, hint "header rcore.h is not provided by any dependency; add the package that exports it to Deps" |
| C++ compile error | build error with log tail | `✗ //libfoo:foo package failed (defined at fixture.go:101)`, `fixture/libfoo/src/foo.cpp:13:13: error: expected ';'…` (path rewritten to repo path) |
| insecure package (`openssl_1_1`) | **EVAL** error, 25-line trace (`knownVulnerabilities`) | `✗ //libfoo:foo nixpkgs package "openssl_1_1" has known vulnerabilities at this revision` — preflight |
| broken package (`hyper`) | **EVAL** error, 25-line trace | `✗ //libfoo:foo nixpkgs package "hyper" is marked broken at this revision` — preflight |
| cycle (validated) | — | `✗ graph fixture.go:101: //libfoo:foo depends on //api:api which is unknown or defined later (cycle?)` |
| cycle with validation skipped (`-no-validate`, self-dependency) | **EVAL** `infinite recursion encountered` | `✗ bob internal error while preparing the build (this is a bob bug, please report it)` + `error: infinite recursion encountered`, exit 70; trace only with `-v` |
| Go version (nixpkgs 25.11 `go` 1.25.10 vs module `go 1.26`) | build error `go.mod requires go >= 1.26` | `✗ //api:api module needs Go 1.26 but nixpkgs at b6018f87da91 provides go 1.25.10` — preflight |

**Honest assessment:** with preflight + `ir.Validate`, none of the faults leaks raw Nix eval text.
The remaining risk class is a **bug in `bob.nix` or an incompatible nixpkgs builder change**: that
is an evaluation error A′ cannot turn into a precise user error; it can only be labelled "internal
bob error" (the user is blocked until bob ships a fixed library). Error *translation* also depends
on Nix's message formats: Nix 2.34 says `Cannot build '…drv'. Reason: builder failed…` (older:
`builder for '…' failed`); both are matched — this fragility applies equally to B.

## Indicative numbers (`measure.sh --both`, other agents were running concurrently)

Definitions are in the header of `measure.sh`. "cold build" = all derivations built from repo
sources are new; third-party downloads/crates/Go proxy and toolchains already in the store.

| profile | measurement | wall s | peak RSS MiB |
|---|---|---|---|
| 8c/15G | fixture cold build //api:api | 17.49 | 278 |
| 8c/15G | fixture no-op build | 2.11 | 142 |
| 8c/15G | fixture incremental (foo.cpp) | 15.05 | 270 |
| 8c/15G | eval n=10 cold / warm | 0.39 / 0.38 | 87 |
| 8c/15G | eval n=100 cold / warm | 0.65 / 0.60 | 91 |
| 8c/15G | eval n=1000 cold / warm | 2.30 / 0.80 | 107 |
| 8c/15G | eval n=10000 cold / warm | 21.64 / 5.48 | 250 |
| 8c/15G | synthetic n=100 cold build (serial chain) | 43.77 | 118 |
| 8c/15G | synthetic n=100 no-op | 1.23 | 92 |
| 2c/4G | fixture cold build //api:api | 24.64 | 275 |
| 2c/4G | fixture no-op build | 2.14 | 143 |
| 2c/4G | fixture incremental (foo.cpp) | 25.08 | 276 |
| 2c/4G | eval n=1000 cold / warm | 2.31 / 0.88 | 106 |
| 2c/4G | eval n=10000 cold / warm | 20.83 / 5.28 | 250 |
| 2c/4G | synthetic n=100 cold build (serial chain) | 37.55 | 117 |

Other observations:
- First fixture build including all third-party work (crates, unpacks, Go proxy): **29 s** [8c/15G].
- **Per-derivation overhead** of `stdenv.mkDerivation` (setup, phases, fixup/patchelf/strip):
  ~0.4 s per tiny C library (100-step serial chain ≈ 38–44 s). This matters for fine-grained
  graphs (R-FW-020).
- **No early cutoff:** a comment-only change in `foo.cpp` rebuilds `libfoo` *and* `api`
  (input-addressed derivations). Incremental ≈ cold because `go build` of api dominates and every
  build starts with an empty GOCACHE. Content-addressed derivations / per-package Go caching
  (S2) are the levers.
- No-op = 3 Nix invocations (checks, drvIndex, build), each re-importing nixpkgs (~0.35 s); can be
  merged into one.
- nixpkgs bump 26.05 → 25.11: **`bob.nix` needed no change and produced no eval error.** C++,
  Rust (buildRustCrate) and protobuf built. Only failures were legitimate version constraints:
  nixpkgs `go` 1.25.10 < module `go 1.26` (now a preflight error) and, after lowering it,
  `golang.org/x/sync@v0.24.0 requires go >= 1.26.0` (build error; preflight could check it if the
  lock recorded each module's `go` directive). Every derivation hash changed (all depend on
  stdenv), including the unpack steps of third-party sources.
- **Cache compatibility:** api closure (11 paths) signed with a test key, `nix copy` to
  `file://…`, then substituted into a fresh store (`local?root=…`) with only that key trusted:
  signatures verified, 11 paths present. Standard binary cache format, nothing bob-specific.
- Concurrency note: parallel `nix` processes sharing one `XDG_CACHE_HOME` hit "SQLite database …
  eval-cache … is busy" (seen by the main agent); bob must give each process/worker its own cache
  dir or serialise eval.

## Lines of code

| | non-blank lines |
|---|---|
| Go driver (`cmd/bob-aprime/main.go`) | 366 |
| Nix library (`lib/default.nix`, `lib/bob.nix`) | 300 |
| Scripts (`measure.sh`, `faults/make.sh`) | 109 |

## nixpkgs internals A′ depends on (fragility list)

1. `buildRustCrate` argument names (`crateName`, `type`, `libPath`, `features`, `dependencies`),
   its `lib` output and the `lib<name>-<metadata>.a` file naming.
2. `stdenv.mkDerivation` phases and hooks; cc-wrapper / ld-wrapper behaviour: automatic `-isystem`
   / `-L` for `buildInputs` and automatic RPATH for linked libraries; fixup (patchelf shrink, strip).
3. Multiple-output selection (`openssl` → `dev` for headers, `out` for libs).
4. `pkg-config` setup hook, and **pkg-config module names matching attribute names** (works for
   openssl; not in general).
5. `fetchurl`, `runCommand`, `unzip`, `writeText`.
6. Attribute names and versions of `go`, `protobuf`, `protoc-gen-go`; protoc-gen-go version must
   match the Go protobuf runtime pinned in the graph.
7. `meta.broken`, `meta.knownVulnerabilities`, `meta.license.free`, `lib.meta.availableOn`.
8. `builtins.fetchTree` (flakes experimental feature) to fetch nixpkgs.

## IR gaps found (derived inside the backend for now)

1. No explicit link libraries for nixpkgs deps (`-lcrypto`): derived via pkg-config.
2. Go: no transitive module graph, no per-module `go` directive, no go.sum `h1:` hashes
   (`GOSUMDB=off`, go.sum produced inside the build). Module path escaping (`!` for uppercase) not
   handled. Go fetches need both the zip and its `go.mod` (backend reads it from the unpacked zip).
3. No version constraints on nixpkgs attributes; no compiler flags/variants per package yet.

## Limitations of this spike

- Synthetic graphs are only C libraries; eval cost per Rust/Go package is higher.
- Error hints are a small pattern table; unrecognised failures print the first Nix line plus
  "bob does not recognise this (bob bug)".
- Measurements ran while two other agents used the machine; rerun on a quiet host.
