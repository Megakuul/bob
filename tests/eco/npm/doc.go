// Package npm holds the acceptance tests of the TypeScript/npm ecosystem (SPEC §03.4):
// R-NPM-001a, 003a, 004a, 005a, 007a, 008a, 009a, 010a. They drive the bob binary against
// tests/fixtures/repos/npm and the offline registry mirror (tools/regmirror, tests/fixtures/
// registry); they are red until the ecosystem is implemented. The comparison oracle of
// R-NPM-001a is the pinned npm (nodejs in tools/pins.json), used only by the tests.
package npm
