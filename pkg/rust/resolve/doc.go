// Package resolve is bob's crates.io resolver (SPEC §03.3.2, ADR 0005, R-RS-002a, R-RS-005).
// It has no build pipeline and no network of its own beyond the sparse-index client, so the
// rust ecosystem (pkg/rust/gen) can use it at `bob lock` and at evaluation:
//
//   - Index: the crates.io sparse index (ETag / Last-Modified cache, bounded parallel
//     fetches, size caps on files, lines, versions, dependencies and features).
//   - Resolve: Cargo's lockfile resolver (cargo 1.95.0): every feature of every workspace
//     crate, dev-dependencies of workspace crates, all platforms, weak "d?/f" locking d,
//     "dep:" hiding implicit features, one version per semver-compatibility class, one crate
//     per `links` value, yanked versions skipped, Cargo's dependency order (fewest candidates
//     first) and backtracking; with Options.RustVersion, resolver 3's rust-version fallback.
//   - Records and CargoLock: bob.lock's crate entries (the JSON shape of lock.Crate) and a
//     Cargo.lock v4 byte-identical to `cargo generate-lockfile`; CargoLockFromRecords does
//     the same from bob.lock alone.
//   - MetaFromCrate / MetaFromManifest: the Cargo.toml facts of a verified .crate.
//   - CfgTable, Platform, Unify: built-in cfg tables of the pinned rustc and Cargo's
//     resolver-2 feature unification per (crate, host|target role) for evaluation.
//
// Member dependencies are passed in the order Cargo reads a manifest: [dependencies],
// [dev-dependencies], [build-dependencies], then each target table in key order, each table
// sorted by name. The order only matters when two dependencies compete for one semver class.
//
// Written from the Cargo reference (resolver, features, registry index chapters) and Cargo's
// observable behaviour; the equality with Cargo is tested against cargo 1.95.0 itself
// (cargo_compare_test.go, scenario_test.go). No Cargo code is copied.
package resolve
