# T6a progress
DONE-INFRA authenv, tests/rbac/ref (proto scanner + evaluator); no R-id done yet
DONE R-AUTH-006 → tests/rbac/rules_test.go::TestRAUTH006EveryRPCHasRule, tests/rbac/matrix_test.go::TestRBACMatrix (proto problem: UpdateProject/UpdateRelease project_field "id" not a top-level field)
DONE R-AUTH-002 → tests/rbac/handwritten_test.go::TestRAUTH002Table
DONE R-AUTH-003 → tests/rbac/handwritten_test.go::TestRAUTH003AdminInvariants
DONE R-AUTH-152 → tests/rbac/handwritten_test.go::TestRAUTH152NoEscalation
DONE R-AUTH-001 → tests/auth/oidc_test.go::TestRAUTH001LocalAndOIDC
DONE R-AUTH-004 → tests/auth/oidc_test.go::TestRAUTH004MappingsReevaluated
DONE R-AUTH-005 → tests/auth/sa_test.go::TestRAUTH005ServiceAccounts
DONE R-AUTH-007 → tests/auth/tokens_test.go::TestRAUTH007ServerIssuedTokens (needs BOB_TEST_CLOCK_FILE hook in bobtest build)
DONE R-AUTH-150 → tests/auth/tokens_test.go::TestRAUTH150OAuthFlows
DONE R-AUTH-151 → tests/auth/hygiene_test.go::TestRAUTH151CredentialHygiene + zz_logs_test.go::TestRAUTH151LogsStaySecretFree
DONE R-API-001 → tests/auth/api_test.go::TestRAPI001ConnectAndBuf
DONE R-API-002 → tests/auth/api_test.go::TestRAPI002Resources
DONE R-API-003 → tests/auth/api_test.go::TestRAPI003BufBreaking
DONE R-API-004 → tests/auth/uipaths_test.go::TestRAPI004UIUsesOnlyPublicAPI
DONE R-API-150 → tests/auth/api_test.go::TestRAPI150ErrorModel (needs devstack seed.projects/seed.builds)
DONE R-API-151 → tests/auth/api_test.go::TestRAPI151ListsAreGetOnly, zz_logs_test.go::TestRAPI151WholeSuite
TODO self-tests of authenv plumbing; lint/ci stages; stubs in internal/{api,auth,rbac,http}
DONE all 16 R-ids; self-tests tests/auth/authenv/authenv_test.go, tests/rbac/ref/ref_test.go; staticcheck/vet clean for tests/auth, tests/rbac; no stubs needed in internal/*
