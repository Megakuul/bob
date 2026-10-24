// Package core holds the acceptance tests of SPEC §01 (framework core) and the R-FW
// requirements of §03.0, §03.6–§03.7 and §04: one test per requirement id, registered with
// parity.Covers, grouped by SPEC section (layout, bob.go convention, labels, core API, ext
// API and IR, bob.lock, pipeline, ecosystem-common rules, platforms and variants).
//
// The tests drive the bob binary (tests/harness) on scratch copies of fixture repositories,
// or call the engine packages where SPEC names a library entry point. Repositories that only
// one requirement needs live in testdata/repos.
package core
