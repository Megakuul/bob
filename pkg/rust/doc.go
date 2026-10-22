// Package rust is the Rust ecosystem API (SPEC §03.3, ADR 0005): local crates (Library,
// ProcMacro, StaticLib, CDylib, Binary), their crates.io dependencies (Crate) and tests (Test).
//
// bob never runs Cargo to build: `bob lock` resolves crates.io dependencies like Cargo's
// lockfile resolver and records every crate's graph facts in bob.lock; each build plans one
// rustc derivation per crate unit (plus build-script compile and run), so a change rebuilds
// only the crates it touches. Cargo.toml, Cargo.lock and rust-project.json are generated for
// tooling.
//
//	var Demo = bob.Define(func(p *bob.P) *rust.Bin {
//		return rust.Binary(p, rust.BinOpts{Deps: []rust.Dep{rust.Crate("serde", "1", rust.Features("derive"))}})
//	})
package rust
