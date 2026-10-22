// Package gen implements engine.Ecosystem for pkg/rust (SPEC §03.3): Lock (crates.io
// resolution with pkg/rust/resolve, one download per crate for its checksum, NAR hash and
// Cargo.toml facts), Plan (per-crate-unit derivations from bob.lock alone, R-RS-005), Native
// (Cargo.toml, Cargo.lock, .cargo/config.toml, .bob/vendor) and PrepareDev (rustc, cargo,
// rust-analyzer, rust-project.json).
package gen
