# T2a progress
DONE-STUBS stubs written: bspec,drv,nar,store,fetch,srcs,nixidx; goldens in tests/nix/testdata/drv
DONE R-NIX-006 → tests/nix/drv_test.go::TestRNIX006* (Golden,Escapes,StoreNames,FixedPaths,OutputPathsChangeWithEnv,RecomputeHello,CrossCheckExamples)
DONE R-NIX-009 → tests/nix/srcs_test.go::TestRNIX009*
DONE R-NIX-007 → tests/nix/srcs_test.go::TestRNIX007*
DONE R-NIX-010 → tests/nix/faults_test.go::TestRNIX010NoRawNixText
DONE R-NIX-012 → tests/nix/faults_test.go::TestRNIX012FailuresInBobTerms
DONE R-NIX-028 → tests/nix/faults_test.go::TestRNIX028*
PARTIAL (catalogue wrappers in faults_test.go; direct tests still to add): R-NIX-011, 017, 018, 019, 022
DONE R-NIX-001 → tests/nix/layer_test.go::TestRNIX001OnlyResolveNix
DONE R-NIX-013 → layer_test.go::TestRNIX013OnlyNixpkgsLeaks
DONE R-NIX-005 → layer_test.go::TestRNIX005NixpkgsPin
DONE R-NIX-003 → layer_test.go::TestRNIX003StandardStorePathsAndCaches
DONE R-NIX-002 → layer_test.go::TestRNIX002NixpkgsStaysUsable
PARTIAL R-NIX-008 → layer_test.go::TestRNIX008 (drv scan + dead network); fetch half to come in fetch_test.go
