# ADR 0001 — Generation strategy: A′ (data + bob-owned Nix library) vs B (direct derivations)

- **Status:** Accepted (locked) — owner at Gate 0, 2026-10-20: "use B" (D49)
- **Date:** 2026-10-20
- **Requirements:** R-NIX-001 (choose by measurement), R-NIX-002 (nixpkgs stays usable),
  R-NIX-003 (standard store paths/caches), R-NIX-010…013 (Nix never leaks), R-FW-020
  (fine-grained), R-NFR-001/002/008 (eval/build speed, low-power machines)
- **Evidence:** `docs/spikes/gen-aprime.md`, `docs/spikes/gen-b.md`, quiet-machine measurements
  in `docs/spikes/measure/{aprime,b}-quiet.md` (run back to back, nothing else running, 2026-10-20), code in `spikes/gen/{aprime,b}` on the shared
  fixture/IR in `spikes/gen/{fixture,ir,fixturegraph}`.

## Context

Both strategies were implemented by separate agents on **the same IR and the same fixture**:
a C++ shared library using nixpkgs `openssl`, a Rust staticlib with two crates.io crates, a
protobuf → Go codegen step and a cgo Go binary linking both, with three Go modules. Both builds
produce a working binary (`digest=81b637d8… numhex=3432 proto_bytes=116`, `libfoo.so` via RPATH,
runs under `env -i`).

- **A′:** Go emits only the graph JSON; a fixed Nix library shipped with bob (`bob.nix`, 300
  lines) maps each kind onto nixpkgs builders (`stdenv.mkDerivation`, `buildRustCrate`,
  `fetchurl`, `runCommand`, nixpkgs `go`).
- **B:** Go computes every derivation itself (ATerm, `hashDerivationModulo`, output and `.drv`
  paths) and imports them with `nix-store --import`; builders are Go-generated bash scripts calling
  nixpkgs compilers (through the nixpkgs cc-wrapper) without stdenv. The only Nix expression is a
  29-line attribute lookup that returns data.

## Comparison

### Measured (quiet machine, `[8c/15G]` / `[2c/4G]`)

| Measurement | A′ | B |
|---|---|---|
| fixture cold build ¹ | 14.4 s / 24.0 s | 11.8 s / 22.0 s |
| fixture no-op | 1.96 s / 2.02 s | 0.33 s / 0.45 s |
| fixture incremental (`foo.cpp` changed) | 14.1 s / 23.4 s | 12.5 s / 22.8 s |
| graph → derivations, N=1,000 (new / again) [8c/15G] | 2.2 s / 0.85 s | 3.0 s / 1.1 s |
| graph → derivations, N=10,000 (new / again) [8c/15G] | 19.6 s / 5.4 s | 24.9 s / 7.7 s |
| peak RSS at N=10,000 | 250 MiB | 175 MB (217 MB at [2c/4G]) |
| synthetic N=100 cold build (100-deep chain) | 41.7 s / 37.3 s | 10.0 s / 9.4 s |
| synthetic N=100 no-op | 1.13 s / 1.19 s | 0.47 s / 0.59 s |

¹ "Cold" differs slightly: A′ keeps the already-built third-party crates (itoa, hex), B rebuilds
them (downloads kept in both). The two crates compile in about a second, so the comparison is
fair to within that.

### Qualitative

| Criterion | A′ | B |
|---|---|---|
| Raw Nix eval text reaching the user (10 faults) | none, **after** a Nix-evaluated preflight (exists/broken/insecure/unfree/platform/Go version) | none; pre-checks run in Go on data from the lookup; a cycle cannot even be expressed |
| Residual risk class | a bug in `bob.nix` or an incompatible nixpkgs **builder** change → only "internal bob error" with a Nix trace under `-v` | a bug in bob's derivation computation; Nix's drv format/hashing changes — mitigated because bob **pins and embeds its own Nix** (ADR 0002) |
| nixpkgs bump 26.05 → 25.11 | `bob.nix` unchanged, no eval error; only real version constraints failed (caught/translated) | builders unchanged; same real version constraints |
| What of nixpkgs it depends on | builder functions and their argument names, setup hooks, cc/ld-wrapper behaviour, multiple outputs, pkg-config hook, `buildRustCrate` naming (8-item fragility list) | package outputs and the cc-wrapper (glibc/rpath injection); attribute names |
| Per-derivation overhead | ~0.4 s (`stdenv` setup, phases, fixup) | ~0.1 s (plain builder script) |
| Fine-grained graphs (R-FW-020) | overhead grows linearly with package count | overhead small; suits per-package Go/C++ object granularity |
| Implementation size (spike) | ~670 lines (366 Go + 300 Nix) | ~2,100 lines (1,960 Go + 147 shell) |
| Missing before production | per-package Go cache, early cutoff, link-lib selection | **fixup** (strip, reference removal, patchelf shrink; closure 75.9 MiB today), output splitting, Rust build scripts/proc-macros, cross flags, compile_commands, a real builder binary instead of bash, per-package Go cache |
| Nixpkgs metadata | evaluated per run (≈0.35 s per invocation, 3 invocations today) | targeted lookups cached per revision (0.4–1.5 s cold, 20 ms warm); a full index is unusable (56 s, 6.4 GB) |
| Cache compatibility | standard store paths; signed `file://` round trip verified | identical; paths cross-checked against Nix four ways (unit test, `hello` recomputation, `derivation add`, cache round trip) |
| Works with Lix | yes | no (`derivation add`/drv JSON diverged; irrelevant if ADR 0002 picks Nix) |

### Findings that hold for both

- **Go dominates incremental builds.** A one-module `go build` with an empty cache costs
  11–12 s every time; per-package Go builds (spike S2) are needed under either strategy.
- **No early cutoff**: a comment change rebuilds all dependents (input-addressed derivations).
- **Parallel `nix` processes must not share `XDG_CACHE_HOME`** (SQLite "busy").
- **Error translation depends on Nix's log/message formats** — another reason bob pins its Nix.
- Both stayed far below the 4 GiB small-machine budget (≤ 300 MiB peak).

## Recommendation

**B — direct derivations**, for these reasons, in the order of BOOTSTRAP §2:

1. **Nix stays invisible structurally, not by preflight.** B has no Nix language for bob's
   packages, so there is no class of "internal Nix evaluation error" that can block a user. A′
   reaches the same user-visible result only by adding a Nix-evaluated preflight in front of a Nix
   library whose failures remain opaque.
2. **Speed where it matters for bob's design:** fine-grained graphs (R-FW-020) multiply the
   per-derivation overhead; B's ~0.1 s vs A′'s ~0.4 s per derivation and the much cheaper no-op
   are decisive on small machines (R-NFR-008): the 100-deep chain builds in 10 s vs 42 s, the
   fixture no-op takes 0.33 s vs 1.96 s, the fixture cold build 11.8 s vs 14.4 s. A′ is faster at
   turning very large graphs into derivations (19.6 s vs 24.9 s at N=10,000); B's time is mostly
   source import and recomputation, both with known fixes (compute NAR hashes in Go, cache drvs by
   input hash).
3. **Dependency surface:** B depends on nixpkgs *outputs* (+ cc-wrapper), A′ on nixpkgs *builder
   functions*. Both survived one release bump unchanged, but A′'s fragility list is longer and
   sits exactly where nixpkgs changes most.
4. bob already pins and embeds Nix (ADR 0002), so B's coupling to Nix's derivation format is under
   bob's control.

**Cost the owner accepts with B:** roughly 3× the code, and bob must own the builder logic that
stdenv gives A′ for free — above all a fixup phase and a real builder (a static Go
`bob-builder` binary instead of bash scripts) — before production. Those become Phase 3 tasks
with their own tests (closure-size budgets, strip/reference checks).

**If the owner picks A′:** it is viable — fewer lines, faster to first results, no eval errors
leaked on the fault catalogue. The plan would add: merge the three Nix invocations, a
bob-owned `bob.nix` CI matrix against every supported nixpkgs revision, and the per-derivation
overhead accepted or reduced by coarser packages.

## Consequences

- The `pkg/` core gets a derivation layer (ATerm, store paths, modulo hashes) with golden and
  cross-check tests against the embedded Nix.
- A static `bob-builder` (Go) becomes the builder of every bob derivation; it is itself a
  fixed-output input (bootstrap path documented, R-ARCH-005).
- nixpkgs is consulted through cached, targeted lookups per revision; never a full index.
- Fixup, output splitting and closure-size budgets are first-class Stage 3 tasks.
