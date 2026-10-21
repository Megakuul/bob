# ADR 0009 — Platforms, cross-compilation and build variants

Status: **Accepted (Gate S pending)** (spike S7, 2026-10-21; SPEC §04 cross part, §01 model part). Evidence: `docs/spikes/s07-cross.md`, code `spikes/s07-cross/`.

## Context

R-FW-030/031, R-GO-001, R-RS-001, D3, D28: packages are platform-generic and built for
`linux/amd64` or `linux/arm64`, natively or cross-compiled; variants (debug/release, features,
sanitisers) change a package's identity. Backend B (ADR 0001) computes derivations in Go.

## Decision

1. **Build on the host, compile for the target.** Every bob derivation has the *host* Nix
   system. The target only changes which compiler, flags and target libraries are inputs.
   No emulated builders, no binfmt, no remote builder of the other architecture is needed.
2. **Toolchains per (host, target) pair, from nixpkgs at the pin:**
   - C/C++: `stdenv.cc` natively, `pkgsCross.<gnu64|aarch64-multiplatform>.stdenv.cc` for cross
     (cached on cache.nixos.org for aarch64→x86_64; measured).
   - Go: the host's nixpkgs `go`, with `GOARCH`; cgo uses the cross cc. std is one derivation per
     (toolchain, target, cgo, Go-relevant variant).
   - Rust: the host's nixpkgs `rustc` with `--target`; the target std comes from the **target
     system's own nixpkgs `rustc`** (copied into a small `rust-std-<triple>` derivation). Upstream
     `rust-std` tarballs are **incompatible** with nixpkgs rustc (E0514, measured).
   - Target nixpkgs libraries default to the **target system's own nixpkgs package** (Hydra-cached),
     with `pkgsCross.<x>.<attr>` as the alternative (often not cached; openssl: 369 s build).
3. **Host units.** Proc-macros, build-script compiles and their dependencies are always built by a
   separate host-role generator, also in native builds, so one host unit has one derivation for
   every target. Build-script *runs* are per target (they get `TARGET`, `CARGO_CFG_*`).
4. **Variant projection.** A variant is a typed configuration (profile, sanitisers, Go race,
   per-crate features). Each package kind sees only its projection; only the projection enters
   the derivation (env, args, name suffix). Unaffected packages keep their derivation and share
   the cache (fetches, codegen, host units, Rust under C sanitisers, pure-Go packages under UBSan).
5. **Labels:** `//pkg:name@<platform>[+<variant>...]`, e.g. `//api:api@linux-amd64+debug+asan`;
   omitted parts = host platform, release, no extras.
6. **v1 matrix:** native arm64/amd64 and cross arm64↔amd64 for C, C++, Go (cgo and pure), Rust;
   variants debug/release, Rust features, ASan/UBSan (C/C++/cgo; Go `-asan`), Go `-race`.
   Rust sanitisers are out of v1 (stable rustc has none).

## Consequences

- Cross builds need no extra machines; test execution of foreign binaries needs either a worker
  of that architecture or qemu-user (works for normal binaries; ASan/TSan fail under qemu-user).
- Each extra (platform × variant) costs a Go std build (~20–35 s) and rebuilds only the packages
  whose projection changed.
- Mixing the target's native nixpkgs libraries with the cross toolchain puts two glibc builds
  of the same version into the closure (+~38 MB); acceptable, documented, switchable.
- Open: nixpkgs cross `gcc-lib`'s `libasan.so` lacks a RUNPATH to its own directory, so bob must
  add the cross gcc-lib dir to the RUNPATH (or use `-static-libasan`) for cross ASan binaries.

## Owner amendments (cross part, SPEC §04)

- No owner decision changes the cross-compilation choice. D3/D4/D28 confirm the scope; D47
  (low-power target) supports defaulting to cached target libraries.
- O-S7-1 settled by agent decision **A18**: foreign-architecture tests run on a worker of that
  architecture, otherwise under qemu-user; sanitiser and race tests only natively (SPEC
  R-FW-039). Scheduling of such tests follows D53 (§05).
- O-S7-2 settled by **A19**: target C/C++ libraries from the target system's own nixpkgs by
  default, `cc.CrossLibs` per package (R-FW-036); bob pre-checks glibc/rustc version equality
  (R-PLAT-007).
- Open item resolved: bob adds the cross gcc-lib dir to the RUNPATH of sanitised cross binaries
  (R-FW-037). Go std is compiled package by package by bob, so cmd/go's prefixed-compiler check
  no longer applies (R-FW-033).
- nixpkgs dependencies are typed `nixpkgs.Tool` (host set) vs `nixpkgs.Lib` (target set)
  (R-FW-032).

## Owner amendments (model part, SPEC §01)

No owner decision changes the model. Agent decisions applied:
- **A18:** tests of a non-host platform run on a worker of that platform, else under qemu-user;
  sanitiser/race tests only on native machines (SPEC §04 R-FW-039).
- **A19:** target C/C++ libraries default to the target system's own nixpkgs set, `nixpkgs.Cross()`
  per package selects pkgsCross (SPEC §04 R-FW-036).
- Projection is made structural (SPEC §01 R-FW-046): a node kind declares the variant axes it reads
  and can only read those; the per-kind table is SPEC §04 R-FW-038.
- `Race` is a `bool` field of `Variant`, not a `Sanitizer` value (Go-only), and labels use the
  canonical token order of SPEC §01 R-FW-050.
