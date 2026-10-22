// Package gen implements engine.Ecosystem for pkg/cc (SPEC §03.1): it plans one
// cc-compile-link derivation per library, program and test, a test-run per test, a
// cc-external derivation per External (source pinned in bob.lock's cc section), and generates
// compile_commands.json.
package gen
