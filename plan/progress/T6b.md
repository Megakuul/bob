# T6b progress
SETUP tests/serverenv/regenv/regenv.go (shared stack+Forgejo+Connect helper), stubs internal/cache/sign.go internal/forge/{forge.go,errors.go,github,forgejo,gitlab}
DONE R-REG-001 → tests/cache/cache_test.go::TestRREG001NixSubstitution
DONE R-REG-154 → tests/cache/cache_test.go::TestRREG154DownloadTokenScope
DONE R-REG-150 → tests/cache/visibility_test.go::TestRREG150VisibilityDocuments
DONE R-REG-151 → tests/cache/proxy_test.go::TestRREG151ProxyNeverRedirectsToS3 (+ slow: big_slow_test.go::TestRREG151BigNARStreaming)
DONE R-REG-002 → tests/cache/sign_test.go + promote_test.go (3 tests)
DONE R-REG-152 → tests/cache/promote_test.go (3 tests) + slow big_slow_test.go::TestRREG152KilledPromotion
DONE R-REG-005 → tests/cache/promote_test.go (2 tests)
DONE R-REG-153 → tests/cache/rotation_test.go::TestRREG153SigningKeyRotation
DONE R-REG-003 → tests/oci/oci_test.go::TestRREG003PullRegistry (podman rootless only)
DONE R-REG-004 → tests/oci/release_test.go::TestRREG004Releases
DONE R-PIPE-001 (fixtures contract) + R-PIPE-008 (adapter part) → tests/forge/adapters_test.go::TestFixtures{Contract,Refused,BaseURLMustBeHTTPS,CheckEvent}; stubs internal/forge
