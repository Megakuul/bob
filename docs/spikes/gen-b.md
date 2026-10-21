# Spike S0a — backend B: direct derivations

Status: **done (indicative numbers)**, 2026-10-20, host `vibe-engine` (see `docs/environment.md`).
Code: `spikes/gen/b/` (throwaway). Requirement: R-NIX-001 (D1). Counterpart: `docs/spikes/gen-aprime.md`.

All numbers below were measured **while two other agents were building on the same host**. They
are indicative only. The final numbers come from rerunning `spikes/gen/b/measure.sh` on a quiet
machine.

## 1. What B is

```
graph.json ─► own loader + cycle check ─► nixpkgs index (targeted lookup, cached per rev)
          ─► Go pre-checks (attr exists, broken/insecure/unfree/platform, Go version, lock hash)
          ─► import local sources (one `nix-store --add` for all)
          ─► compute derivations in Go (ATerm, hashDerivationModulo, output + .drv paths)
          ─► one `nix-store --import` stream puts all .drv files into the store
          ─► `nix build --log-format internal-json -L` ─► translate to bob errors
```

- **There is no Nix language for bob's own packages.** The only Nix expression is
  `b/nixidx/resolve.nix` (29 lines). It looks up nixpkgs attributes and reports their status, and
  it contains no build logic. Everything it can fail on (missing attribute, broken, insecure,
  refused evaluation) is caught with `tryEval`/`attrByPath` and returned as **data**.
- nixpkgs derivations are real `inputDrvs` of bob's derivations. If an output isn't cached,
  Nix builds it from the nixpkgs recipe as usual (R-NIX-002 holds).
- bob computes the modulo hashes of nixpkgs inputs by parsing their `.drv` files
  recursively. That's 564 drvs for the whole toolchain set, done in 0.3 s and cached in the index.

### Proof that Go-computed paths agree with Nix

| Check | Result |
|---|---|
| Unit test: output path of a toy drv vs path Nix 2.34.8 demands (`nix derivation add` error message) | identical (`b/drv/drv_test.go`) |
| hashDerivationModulo of nixpkgs `hello-2.12.3.drv` recomputed from 187 parsed input drvs → output path | identical to the path recorded in the .drv |
| All 13 fixture drvs re-submitted with `nix derivation add` (`bob-b drvs -add add`, which errors on any mismatch) | all accepted, same paths |
| Fixed-output fetches (`builtin:fetchurl`, flat sha256 = the hash in `lock.json`) | accepted, fetched, verified by Nix |
| Binary cache round trip: sign → `nix copy --to file://` → `nix copy --from` into a fresh chroot store → `nix store verify` (sigs) → run `api` from that store | 12 paths, signatures valid, `deriver` = bob's drv, program prints the expected output |

`nix derivation add` is marked experimental, and its JSON format is at version 4 and has changed
before. B therefore uses `nix-store --import`, a stable format: the NAR of the ATerm plus references. `derivation add` is used only as the consistency oracle in tests.

## 2. Builder approach (decision + evidence)

**Chosen:** bash scripts generated in Go, with the builder being nixpkgs `bash`. They call the
compilers from nixpkgs directly: `gcc-wrapper-15.3.0`, `rustc` 1.95.0, `go` 1.26.8, `protoc`,
`protoc-gen-go`, `pkg-config`. No `stdenv`, no setup hooks.

- **Evidence:** the nixpkgs cc-wrapper works without stdenv. On its own it injects
  glibc and the dynamic linker, and it adds a RUNPATH for every `-L` store directory. `readelf -d` on
  `api` shows RUNPATH = `libfoo/lib`, `glibc/lib`, `gcc-lib/lib`. That means rpath handling comes free
  from the wrapper, and `api` runs under `env -i` with no `LD_LIBRARY_PATH`.
- Every script starts with an ERR trap that names the failing command. bob's error
  translation relies on it. Without it, `set -e` failures were silent: the first libfoo failure
  had an empty log.

Rejected for the spike:

- **stdenv `setup` as a library.** It couples bob to stdenv internals and hooks, which are the
  same upgrade surface A′ has.
- **A static Go "bob-builder" binary as the builder.** This is the better production choice:
  structured logs, no shell quoting, and one place for fixup logic. But it must itself be
  bootstrapped (built by nixpkgs `go`, or shipped as a fixed-output download), and the spike
  didn't need it to answer the question.

## 3. Fault catalogue (`b/faults/run.sh`; outputs in `b/faults/out/`)

| Fault | Where caught | Raw Nix would show (first lines) | bob prints (default, no `-v`) |
|---|---|---|---|
| unknown nixpkgs attr `opensll` | Go pre-check (names index) | eval error: `flake … does not provide attribute 'packages.aarch64-linux.opensll'…` | `✗ nixpkgs has no package "opensll"` · in //libfoo:foo (defined at fixture.go:101) · hint: did you mean openslp, openssl, openal? |
| wrong fetch hash | build (FOD) | `error: hash mismatch in fixed-output derivation '/nix/store/…-itoa-1.0.18.crate.drv': specified … got …` | `✗ //third_party:itoa-1.0.18.crate download does not match bob.lock` + url, lock hash, downloaded hash, hint |
| missing dependency (rcore dropped from api deps) | build | `error: Cannot build '/nix/store/…-api-api.drv'. Reason: builder failed with exit code 1.` | `✗ //api:api build failed (defined at fixture.go:130)` · `cmd/api/main.go:7:10: fatal error: rcore.h: No such file or directory` · hint: add the package that provides it to Deps |
| link error (symbol removed from libfoo) | build | same "Cannot build … builder failed" | `undefined reference to 'foo_sha256_hex'` · hint: symbol not provided by any linked library, a dependency is probably missing |
| C++ compile error | build | same "Cannot build … builder failed" | `fixture/libfoo/src/foo.cpp:13:13: error: expected ';' before '}' token` (store paths mapped back to repo paths, ANSI stripped) |
| dependency cycle (hand-edited JSON) | Go (own topo sort) | N/A: a cycle **cannot be expressed** as derivations (paths are hashes of inputs) | `✗ dependency cycle: //api:api → //libfoo:foo → //api:api` with each definedAt |
| broken package (`aranym`) | Go pre-check (resolve.nix status) | eval error trace into `lib/customisation.nix:428` / `check-meta.nix:702` | `✗ nixpkgs package "aranym" (1.1.0) is marked broken` + hint |
| insecure package (`bzip2_1_1`) | Go pre-check | same check-meta eval trace | `✗ … is marked insecure: CVE-2026-42250` + hint |
| Go version newer than toolchain | Go pre-check | (build-time `go.mod requires go >=`) | `✗ //api:api needs Go 1.27 but nixpkgs … provides Go 1.26.8` + hint |
| dependency needs newer Go (bump case) | build, log pattern | `go: golang.org/x/sync@v0.24.0 requires go >= 1.26.0 …` | translated with a hint (see §5) |

**Result: none of the 10 faults leaks raw Nix evaluation text by default.** Evaluation errors in
B can only come from `resolve.nix`, and it turns them into data. Build failures are mapped from
`.drv` back to label and definedAt. `definedAt` currently points into `fixturegraph/fixture.go`
because the spike's helper DSL records the caller one frame up. A real DSL records the user's
`bob.go` line.

## 4. Numbers (indicative, busy host; `b/measure-results.md`)

| Measurement (backend B) | [8c/15G] | [2c/4G] |
|---|---|---|
| fixture cold (all bob outputs deleted except downloads; toolchains in store) | 11.80 s / 285 MB | 21.67 s / 276 MB |
| fixture no-op | 0.21 s / 34 MB | 0.33 s / 34 MB |
| fixture incremental (foo.cpp changed) | 12.27 s / 295 MB | 22.15 s / 270 MB |
| graph→drvs in store, N=10 new / again | 0.31 s / 0.12 s | 0.35 s / 0.27 s |
| graph→drvs N=100 new / again | 0.58 s / 0.23 s | 0.58 s / 0.33 s |
| graph→drvs N=1,000 new / again | 2.86 s (36 MB) / 1.07 s | 2.92 s (44 MB) / 1.13 s |
| graph→drvs N=10,000 new / again | 25.2 s (157 MB) / 8.0 s | 25.3 s (182 MB) / 8.5 s |
| synthetic N=100 cold build (100-deep chain) / no-op | 9.99 s / 0.38 s | 9.33 s / 0.51 s |
| nixpkgs index cold (source already fetched) / warm | 1.53 s / 131 MB, 0.02 s / 7 MB | — |

Peak RSS = the largest single process (GNU time `%M`). "graph→drvs" includes importing sources, so
evaluation memory stays **far below the 4 GiB small-machine budget at 10,000 packages**.

Observations:

- **The incremental build is dominated by Go.** When foo.cpp changes, libfoo rebuilds in 1.5 s,
  but `api` takes 11.6 s, because the Go module is one derivation with an empty build cache, so
  protobuf and the other module deps recompile every time. Changing only `main.go` also costs
  11.4 s. **Per-package Go builds (S2) are mandatory, whatever strategy wins.**
- graph→drvs at N=10,000 is about 25 s, split between importing sources (3–10 s: one `nix-store
  --add` over 10k directories) and computing and importing drvs. The second run still recomputes
  everything (8 s). Caching drv results by input hash, and computing NAR hashes in Go instead of
  copying sources, are straightforward next steps.
- **A first version was O(N²):** 17 s to compute 1,000 drvs, because static libraries pulled
  their transitive libs in as inputs. Fixed by giving static libraries only their direct deps'
  headers. Link closure is a consumer concern. Worth a regression test in the real harness.
- **Index:** a full `nix-env -qaP --json --meta` index costs **56 s and 6.4 GB RSS** (397 MB
  JSON, 100,119 attrs). That's unusable on small machines, so B uses targeted lookups instead:
  0.4–0.8 s and 86–130 MB per evaluation, plus a top-level names list (27,258 names) for "did you
  mean". Fetching a new nixpkgs revision the first time (the 25.11 bump) took 16.5 s.

## 5. nixpkgs bump to nixos-25.11 (`b6018f87`)

1. The Go pre-check refuses straight away: `//api:api needs Go 1.26 but nixpkgs b6018f87da91
   provides Go 1.25.10`.
2. With GoVersion lowered to 1.25, everything else in the graph builds unchanged: C++ with
   openssl, both crates, the Rust staticlib, protoc-gen-go. Only `api` fails, because
   `golang.org/x/sync@v0.24.0 requires go >= 1.26.0`. bob translates this with a hint, but it's
   caught at build time. The pre-check doesn't yet read the `go` directive of dependency modules,
   though it could, since the `.mod` files are in the fetched zips.
3. **No bob builder code had to change.** B depends only on nixpkgs package outputs (compilers,
   libraries), not on nixpkgs build functions.

## 6. Size of the implementation

| Part | Lines |
|---|---|
| `drv/` ATerm, store paths, modulo, .drv parser | 519 |
| `nixidx/` index + lookup | 220 (+29 lines `resolve.nix`) |
| `gen/` builders for 5 kinds | 503 |
| `run/` store import, build, log parsing | 284 |
| `cmd/bob-b` driver, pre-checks, error translation | 434 |
| **Go total (non-test)** | **1,960** (+69 test) |
| shell (`measure.sh`, `faults/run.sh`) | 147 |

## 7. nixpkgs builder logic B had to reimplement (and what is still missing)

Done in the spike: compile and link commands per language; include and link-flag propagation
(direct vs transitive, static vs shared); pkg-config usage, a crude version that links every `.pc`
in a dep; unpacking fetched archives; Go module proxy from per-module zips plus a generated
`go.mod`; rustc invocation per crate (editions, features, `--extern`, `-L dependency=`); log
visibility (ERR trap).

Missing before production:

- **Fixup:** stripping, `remove-references-to` / `--remap-path-prefix`, and patchelf shrinking.
  The `api` closure is 75.9 MiB because it pulls in the itoa *source* (Rust panic paths) and
  nixpkgs Go's `tzdata` and `iana-etc` references.
- Output splitting (`out`/`dev`/`lib`); a setup-hook equivalent for things like Python or
  pkg-config paths when ecosystems need them; cross-compilation flags; Rust build scripts and
  proc-macros; cargo feature resolution; a Go per-package build cache; C++ header dependency
  tracking; `compile_commands.json`.
- **A real builder binary.** Shell quoting is fragile. The spike already had to work around
  `set -e` inside command substitutions.

## 8. IR gaps found

- Which libraries of a nixpkgs dependency to link (openssl: `-lcrypto` vs `-lssl`). The spike
  links every pkg-config module of the dep.
- cgo `LDFLAGS` are hardcoded in `main.go` (`-lfoo -lrcore`). bob should generate cgo flags from
  the typed deps.
- Go dependencies' own `go` directives and transitive requirements aren't in the IR (see §5).
- The unpack subdirectory of fetched archives is known only by convention (`Src.Subdir`).
- No per-package Go granularity; one `go build` per module.

## 9. Other findings

- Concurrent `nix` invocations that share `XDG_CACHE_HOME` hit `SQLite database … eval-cache … is
  busy` (the main agent saw this). bob must own its cache directory and serialise access.
- `nix-store --import` of unsigned `.drv` files works for root on single-user Nix. In S0b's
  rootless store the user owns the store, so this should hold; check it there.
- Builds run as children of the single-user `nix` process, so a systemd scope (cgroup) limits
  them. That's how the [2c/4G] numbers were taken. With a daemon, builds would escape the scope.

## 10. How to reproduce

```
cd spikes/gen && CGO_ENABLED=0 go build -o b/bob-b ./b/cmd/bob-b
b/bob-b index                                           # nixpkgs index for the pin
b/bob-b build -graph fixture.graph.json -target //api:api   # prints the api store path
b/faults/run.sh                                         # fault catalogue → b/faults/out/
b/measure.sh                                            # full measurements (quiet machine!)
```
