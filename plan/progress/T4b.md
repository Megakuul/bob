# T4b progress
DONE tools/regmirror + tests/fixtures/registry/README.md (support; not an R-id)
SKIP tools/synth (not needed: no R-NPM/ML/GEN acceptance uses a synthetic graph)
DONE stubs pkg/npm (api.go quirk.go); SPEC problem: func App/Test vs type App/Test collide in Go -> stubs NewApp/NewTest
DONE R-NPM-001a → tests/eco/npm/resolve_test.go::TestRNPM001aMatchesNpm,TestRNPM001aPeerSplit,TestRNPM001aHostileRegistryDocuments (+ecotest, graphs_test.go)
DONE R-NPM-003a → layout_test.go::TestRNPM003aIsolatedLayoutAndHoisted
DONE R-NPM-004a → scripts_test.go::TestRNPM004a*
DONE R-NPM-005a → quirks_test.go::TestRNPM005a*
DONE R-NPM-008a → gen_test.go::TestRNPM008a*
DONE R-NPM-007a → build_test.go::TestRNPM007a*
DONE R-NPM-009a → build_test.go::TestRNPM009a*
DONE R-NPM-010a → dev_test.go::TestRNPM010a*
DONE R-ML-001a → tests/eco/ocaml/build_test.go::TestRML001aFixtureBuildsFineGrained
DONE R-ML-001b → tests/eco/ocaml/lsp_test.go::TestRML001bLSPWithoutInTreeBuild
DONE R-ML-002 → tests/eco/ocaml/corpus_test.go::TestRML002*
DONE R-GEN-001a,002a,003a,005,006 → tests/eco/gen/generate_test.go
DONE all; lint/vet clean for T4b packages
