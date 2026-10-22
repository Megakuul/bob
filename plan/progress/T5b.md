# T5b progress
SKIP R-NFR-150 (blocked: benchmark rows B-065..B-074 owned by the budgets stage; no SPEC acceptance beyond the budget file)
DONE R-LOG-001 → tests/server/log_commit_test.go::TestRLOG001SizeTriggeredCommit,TestRLOG001KillLosesOnlyUncommitted; tests/soak/log_scale_test.go::TestRLOG001Scale (slow)
DONE R-LOG-100 → tests/soak/lake_test.go::TestRLOG100LakeWorkarounds (slow, ~20 min; e2e form, no in-process lake: no lake dep in go.mod)
helper: tests/server/srvtest/srvtest.go (devstack contract in its package doc)
DONE R-LOG-002 → tests/server/log_live_test.go::TestRLOG002LivePull
DONE R-LOG-101 → tests/server/log_merge_test.go::TestRLOG101MatrixLive,TestRLOG101MatrixProcess (6+6 runs)
DONE R-LOG-003 → tests/server/log_attrib_test.go::TestRLOG003Attribution (100M-line data set not reachable: SPEC problem)
DONE R-LOG-102 (PARTIAL: hostile build output only; forged LiveChunk cases need an Attach client) → tests/server/log_hostile_test.go::TestRLOG102HostileOutput
DONE R-SRV-100 (PARTIAL: checkout bullet needs forge in devstack) → tests/server/fleet_test.go::TestRSRV100WorkersWorkWithoutAttach
DONE R-SRV-102 → tests/server/fleet_test.go::TestRSRV102Membership (needs attach proxy contract /streams /redirect)
DONE R-NFR-005 (PARTIAL: running-builds row only; projects/workers/jobs rows infeasible black-box) → tests/soak/scale_test.go::TestRNFR005RunningBuildsLimit
DONE R-SRV-004 → tests/chaos/chaos_test.go::TestRSRV004OrchestratorKill
DONE R-NFR-007 → tests/chaos/chaos_test.go::TestRNFR007CrashSafety (fsck as record validator; R-SRV-101 injection folded in as proxy faults)
DONE R-SRV-101 (slow; payloads of CreateProject/CreateRoleBinding are best-effort, see report) → tests/soak/intent_test.go::TestRSRV101IntentFirst
