# ADR 0005 — Rust: per-crate derivations, bob's own crate resolver, bob-builder runs build scripts

- **Status:** Accepted (locked) — owner at Gate S, 2026-10-21
- **Requirements:** R-RS-001…004, R-FW-004/006/020, R-CC-001, R-NFR-001/002/008, R-NIX-010…012
- **Evidence:** `docs/spikes/s03-rust.md`, code in `spikes/s03-rust/`, numbers in
  `spikes/s03-rust/measure-results.md`.

## Context

bob replaces Cargo as the build driver (§7 principle) but must still use crates.io, build
scripts and proc-macros, and keep rust-analyzer working. Backend B (ADR 0001) computes
derivations in Go. The spike built real graphs with it: a 49-unit graph (serde+derive,
clap+derive, tokio+macros, regex, zstd-sys with C code, 13 build scripts, 4 proc-macros), a
129-unit web graph (axum, reqwest+rustls with `ring`, tracing, tokio full) and openssl via
nixpkgs + pkg-config.

## Decision (proposed)

1. **Resolution is bob's, in Go, at `bob lock` time.** bob reads the crates.io sparse index
   (`index.crates.io`, ETag-cached), resolves like Cargo's lockfile resolver (one version per
   semver-compatibility class, highest match, all features of local crates, all platforms,
   weak `x?/f` counted as a dependency) and writes bob.lock. Cargo is never run. The spike
   resolver produced Cargo.lock files **identical** to `cargo generate-lockfile` for four
   graphs (12, 25, 173, 393 packages).
2. **bob.lock stores per crate:** name, version, `.crate` checksum, NAR hash of the unpacked
   source, resolved dependency edges with their kind/target/optional/features, the feature
   table, `links`, and four facts from Cargo.toml (edition, lib name/path, proc-macro, build
   script). Evaluation then needs no network and no crate sources.
3. **Feature resolution happens at evaluation** (not in the lock), with Cargo resolver-2 rules:
   features unify per (crate, platform); build-dependencies, proc-macros and their dependencies
   are *host* units; target-specific dependencies are filtered by built-in cfg tables.
4. **Derivations per crate unit:** one recursive fixed-output source derivation (download +
   checksum + unpack, verified by the NAR hash), one compile derivation, and for crates with a
   build script one *build-script compile* (host) and one *build-script run* derivation. Rust
   derivations call `rustc` directly; Cargo is not involved.
5. **A static Go `bob-builder` is the builder of every Rust derivation** (no shell). It reads a
   JSON job from the derivation environment. The build-script run writes `out-dir/`, raw
   `output` and a parsed `directives.json`; consumers read it at build time
   (`rustc-cfg`, `rustc-env`, `rustc-link-lib/search`, `rustc-flags`, link args,
   `cargo:KEY=VALUE` metadata → `DEP_<LINKS>_<KEY>`).
6. **rust-analyzer gets a generated `rust-project.json`** from the same plan (store sources,
   build-script cfgs/`OUT_DIR`, proc-macro dylibs from the store). Generated
   `Cargo.toml`/`Cargo.lock` plus a `.bob/vendor` directory source are also written, so
   `cargo metadata --locked --offline` and rust-analyzer's cargo mode work too (R-RS-003).
7. **Native libraries** of `-sys` crates come from nixpkgs through a per-crate declaration in
   `bob.go` (`rust.Crate("openssl-sys").Uses(nixpkgs.Pkg("openssl"))`), which adds the
   packages to the build-script run (PATH, PKG_CONFIG_PATH).

## Consequences

- Every bob-builder change changes every Rust derivation (the builder is an input). bob-builder
  must be versioned and pinned separately from the `bob` CLI and change rarely.
- One derivation per crate costs about 0.15 s per derivation including a trivial rustc run
  (measured), and Cargo-style pipelining (start dependents on `.rmeta`) is not possible across
  derivations. Measured cold build of a 49-crate graph: 65 s vs cargo 71 s on 8 cores, 148 s
  vs 124 s on 2 cores (busy host). Cache hits and incremental rebuilds are what this design
  is for.
- Cargo resolver changes (a new resolver version, MSRV rules) have to be followed by hand. A CI
  job compares bob's lock with `cargo generate-lockfile` on a corpus.
- No early cutoff: a local crate change rebuilds every dependent crate.

## Alternatives rejected

- **Cargo inside one derivation per workspace** (crane-style): coarse; one change rebuilds
  everything; Cargo becomes the build driver, against §7.
- **Cargo as resolver at lock time** (`cargo generate-lockfile` + `cargo metadata`): needs the
  Cargo binary and a fake workspace on every developer machine. bob already has to ship its own
  resolver for npm (R-NPM-001), and the Go resolver matched Cargo on all four test graphs.
- **Unpacking sources in every consumer derivation:** saves one derivation per crate, but
  build-script metadata (for example zstd-sys `include=`) points into the source directory, so
  the source needs a stable store path. rust-analyzer needs the same sources.
- `builtin:fetchurl` with `unpack`: it unpacks NAR archives only, not tarballs (tried; failed).

## Owner amendments

- **D58** (O-S3-1 a): bob.lock may contain crate dependency edges, feature tables and the
  needed Cargo.toml facts. Decision 2 stands as written; schema `lock.Crate` /
  `lock.CrateDep` / `lock.CrateMeta` in SPEC §03.3.2 (R-RS-005).
- **A10** (O-S3-2 a): Decision 6 confirmed: rust-project.json **and** Cargo.toml/Cargo.lock +
  `.bob/vendor` are generated; editor settings point rust-analyzer at rust-project.json.
- **D59**: rust-analyzer runs through the `.bob/bin` shim and the §02 LSP path proxy.
- **S13**: crate source FODs are realised by bob's fetcher, never as Nix FOD builds.
- Added: a small shipped native-crate defaults table (v1: openssl-sys → openssl) with the
  common admission rule (SPEC §03.3.6). Quiet numbers: cold 1.19× cargo [8c/15G], 1.10×
  [2c/4G] (the Consequences' busy-host numbers are superseded).
