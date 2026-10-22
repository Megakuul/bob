# I-R-crates — pure crate resolver (M8.1 core, A78)

Owner of: pkg/rust/resolve only. Status: **done** (2026-10-22). Integration into `bob lock`
(pkg/rust/gen, M8.1 proper) is not part of this unit.

## Done
- `pkg/rust/resolve` (std only; layer L2):
  - semver.go — Cargo/semver-crate requirement rules (caret, tilde, =, ranges, wildcards,
    pre-release rule), version order, compat classes.
  - index.go, sparse.go — sparse index format, validation and caps (16 MiB file, 4 MiB line,
    50k versions, 2000 deps, 5000 features, schema v ≤ 2); client with ETag/Last-Modified
    cache, offline mode, single flight per crate, bounded parallel Prefetch (recover).
  - features.go, resolve.go — Cargo's lockfile resolver: all features of members, member
    dev-deps, all platforms, weak `d?/f` locks d, `dep:` hides implicit features, missing
    feature rejects a candidate, one version per semver class, one crate per `links`, yanked
    skipped, fewest-candidates-first order, backtracking with Cargo's conflict-age pruning,
    step cap; Options.RustVersion = resolver 3 rust-version fallback.
  - lock.go — bob.lock records (JSON shape of lock.Crate/CrateDep/CrateMeta), Cargo.lock v4,
    CargoLockFromRecords (from bob.lock alone), MatchLocked.
  - toml.go, manifest.go — TOML reader + Cargo.toml facts (edition, lib name/path, proc-macro,
    build script) from a .crate, path-escape checks.
  - cfg.go — cfg tables of rustc 1.95.0 (diffed equal to `rustc --print cfg --target` for both
    Linux triples), cfg() parser/evaluator.
  - unify.go — evaluation-time resolver-2 feature unification per (crate, host|target).
- Evidence (vibe-engine arm64, 2026-10-22, live crates.io, cargo 1.95.0 / rustc 1.95.0 from
  the store):
  - TestCompareWithCargo (online, BOB_TEST_CARGO): 9 workspaces (the 4 S3 graphs as the
    acceptance corpus spells them: 12/25/200/389 packages; the spike's big graph 393; zstd,
    regex, openssl, wasm-bindgen) × resolver 2 and 3 → 18/18 byte-identical Cargo.lock.
    bob resolve through a local mirror: big graphs 60–80 ms warm.
  - TestUnifyEqualsCargoTree (online): (crate, features) sets equal `cargo tree -e
    normal,build` for all 9 workspaces, native aarch64 and cross x86_64 (18/18). One earlier
    run failed once in s3-big/aarch64 and never again in 5 reruns; output was filtered, most
    likely a transient download error of cargo tree.
  - Offline in CI: TestSnapshotEqualsCargo (recorded index snapshot testdata/index.tar.gz,
    200 KB, goldens written by cargo), TestScenariosEqualCargo (12 synthetic Cargo-rule
    scenarios × resolver 2/3, goldens written by cargo: msrv fallback, yanked, weak/dep:,
    implicit features, backtracking, links, missing feature, renames/classes, kinds/targets,
    member features, pre-releases, feature re-activation), plus unit tests. Coverage 88.6 %.
  - MetaFromCrate equals the spike's Cargo.toml facts on all 392 crates of the big graph
    (scratch check, not committed: it downloads 54 MB).
- Checks: gofmt clean, `GOWORK=off go vet` clean, staticcheck + boblint (-nopanic -noexit
  -nolist -safego, -importboundary) clean on the package, TestLayering ok.
  `go run ./tools/ci -only fmt,vet,lint,layering`: fmt ok (with GOTMPDIR on /var/tmp; /tmp
  tmpfs is full), layering ok; vet fails on `out/uicheck` (not mine), lint fails on
  internal/api and pkg/npm/resolve unused code (other units).

## Next
- (integration, M8.1 proper, owner of pkg/rust/gen) see NEEDS.

## DECISIONS
- R1: resolve defines its own Crate/CrateDep/CrateMeta with the exact JSON of lock.Crate:
  L2 (pkg/rust/...) may not import E1 pkg/engine/lock (R-ARCH-003). gen converts field by field.
- R2: a small TOML reader instead of a dependency: L2 has no third-party allowance; checked
  on 392 real published manifests.
- R3: NarHash is not computed here: engine/fetch.Resolve already returns the NAR hash of the
  unpacked crate; gen records it and reads Meta with MetaFromCrate on the verified archive.
- R4: lock records keep the published feature table (features ∪ features2) without implicit
  features; Unify derives implicit ones from the recorded optional deps. Optional deps the
  workspace cannot enable are omitted (SPEC).
- R5: a member dependency resolves at evaluation to the highest locked version matching its
  requirement (same choice Cargo makes); Cargo.lock from bob.lock uses the same rule.
- R6: a proc-macro root activates both target and host units (Cargo does the same).
- R7: index lines Cargo would skip (unparsable version, schema v > 2, dependency on another
  registry, bad checksum) are skipped; malformed JSON fails the crate.

## NEEDS
- pkg/rust (cfg.go, not mine): `CfgTable` should return `resolve.CfgTable(triple)`; then
  TestRRS002aCfgTablesEqualRustc can go green (tables already diffed equal to rustc 1.95.0).
- SPEC conflict for the coordinator/owner: §03.3.2 says "prefer versions with rust_version ≤
  toolchain" (resolver-3 fallback), but §03.3.4 generates `[workspace] resolver = "2"`, under
  which cargo ignores rust_version, and R-RS-002a compares with cargo on that generated
  Cargo.toml (the test drops `.cargo/`, so a config.toml setting does not help). Both modes
  are implemented and cargo-verified (Options.RustVersion). Recommendation: generate
  `resolver = "3"` and pass the toolchain version, so locked crates also compile with the
  pinned rustc; otherwise pass RustVersion "" to stay equal to cargo under resolver 2.
- pkg/rust/gen (M8.1 integration): Members from bob.go in Cargo manifest order (doc.go),
  `Resolve(ctx, NewIndex(CratesIO, $XDG_CACHE_HOME/bob/crates-index), …)`, `Records()` →
  lock.Crate, engine/fetch for each crate (checksum, narHash), `MetaFromCrate` → Meta,
  `CargoLockFromRecords` for the generated Cargo.lock; Unify at evaluation.
- tools/regmirror has no crates.io route yet (tests/eco/rust needs BOB_TEST_CRATES_INDEX).
- Licence check (owner item, AGENTS §0 "restrictive data"): testdata/index.tar.gz holds 32
  crates.io sparse-index files (crate metadata). If kept, it needs an ATTRIBUTION.md row
  (not my file). Without it, TestSnapshotEqualsCargo goes away and the 12 synthetic scenarios
  remain as the offline Cargo-equality test.

## TEST PROBLEM
- none.
