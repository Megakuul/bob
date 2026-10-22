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
DONE R-PIPE-007 pieces: GitHub replay → tests/forge/github_replay_test.go; gitlab launcher tests/serverenv/gitlab (untested start: heavy)
DONE R-PIPE-001/-007 live Forgejo → tests/forge/forgejo_live_test.go::TestForgejoLive (live tag; verified it starts and reaches red)
DONE R-PIPE-007 GitLab live → tests/forge/gitlab_live_test.go::TestGitLabLive (live+slow; launcher untested, heavy)
DONE R-PIPE-002 → tests/forge/intake_live_test.go::TestPIPE002OneRepositoryPerProject; R-PIPE-008 → TestPIPE008WebhookIntake (+ adapters_test.go TestFixturesCheckEvent); both need `live` tag
DONE R-PIPE-003 (forged-evaluation part NOT written: needs worker Attach client) → pipelines_live_test.go::TestPIPE003PipelinesRunAndReport; R-PIPE-004 → TestPIPE004Triggers; R-PIPE-005 → TestPIPE005CancelRetryRerunConcurrency
DONE R-PIPE-006 → notify_live_test.go; R-PIPE-009 → snapshots_live_test.go; R-PIPE-007 → suites_test.go + replay/live/gitlab. ALL R-ids done; final: lint/layering check
