// Package gen holds the acceptance tests of code generation (SPEC §03.6): R-GEN-001a, 002a,
// 003a, 005 and 006. They drive the bob binary against tests/fixtures/repos/codegen (the S6
// tree: two proto libraries, a Go server and client, a TS client) and are red until the
// generators are implemented. Resolution of the generators' runtimes uses the offline registry
// mirror (tools/regmirror).
package gen
