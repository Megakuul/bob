# T4b progress
DONE tools/regmirror + tests/fixtures/registry/README.md (support; not an R-id)
SKIP tools/synth (not needed: no R-NPM/ML/GEN acceptance uses a synthetic graph)
DONE stubs pkg/npm (api.go quirk.go); SPEC problem: func App/Test vs type App/Test collide in Go -> stubs NewApp/NewTest
DONE R-NPM-001a → tests/eco/npm/resolve_test.go::TestRNPM001aMatchesNpm,TestRNPM001aPeerSplit,TestRNPM001aHostileRegistryDocuments (+ecotest, graphs_test.go)
DONE R-NPM-003a → layout_test.go::TestRNPM003aIsolatedLayoutAndHoisted
DONE R-NPM-004a → scripts_test.go::TestRNPM004a*
DONE R-NPM-005a → quirks_test.go::TestRNPM005a*
