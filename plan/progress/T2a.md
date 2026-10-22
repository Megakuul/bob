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
DONE R-NIX-014 → tests/nix/fetch_test.go::TestRNIX014*
DONE R-NIX-008 (second half) → fetch_test.go::TestRNIX008FetchedPathNeedsNoNetwork
DONE R-NIX-015 → tests/nix/builder_test.go::TestRNIX015*
DONE R-NIX-016 → builder_test.go::TestRNIX016* (+drv_test GoldenATerm); test-run kind NOT covered (args schema is §03)
DONE R-NIX-017 → builder_test.go::TestRNIX017LogsAndExitCodes + faults_test.go::TestRNIX017
PARTIAL R-NIX-019 → builder_test.go::TestRNIX019* (builder-level fixup); polyglot closure/__FILE__ integration still to add
DONE R-NIX-019 → builder_test.go::TestRNIX019*, closure_test.go::TestRNIX019*, faults_test.go (fault 30)
DONE R-NIX-030 → closure_test.go::TestRNIX030ClosureBudgets (3 of 4 rows+path count; B-010..B-013)
DONE R-NIX-024 → tests/nix/embedded_test.go::TestRNIX024*
DONE R-NIX-025 → embedded_test.go::TestRNIX025SystemNixCoexistence
DONE R-NIX-026 → embedded_test.go::TestRNIX026ConcurrentCommands
DONE R-NIX-029 → embedded_test.go::TestRNIX029CredentialPrecheck
PARTIAL R-NIX-020 → embedded_test.go::TestRNIX020* (host part); rootless images part to add in tests/rootless
TODO: R-NIX-027 (nixidx_test.go), R-NIX-021/022 (tests/rootless), R-NIX-011/018 direct extras
DONE R-NIX-027 → tests/nix/nixidx_test.go::TestRNIX027* (+testdata/fakepkgs)
PARTIAL R-NIX-021 → tests/nix/namespace_test.go (bind mode); pivot + rootless to add
DONE R-NIX-020/021/022 (rootless) → tests/rootless/rootless_test.go::TestRNIX020RootlessMatrix,TestRNIX021PivotProbes,TestRNIX022Refusal
ALL 28 ids have tests. Remaining: full-suite verification, ci fmt/vet/lint/layering, final report
