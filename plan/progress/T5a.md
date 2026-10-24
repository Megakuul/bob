# T5a progress
DONE R-STORE-104 → tests/store/keys_test.go::TestRSTORE104KeySegmentBuilders (stubs internal/store/{store,keys}.go; env tests/store/env)
DONE R-STORE-005 → tests/store/panic_test.go::TestRSTORE005CallRecoversMisusePanics,TestRSTORE005PrimitivesReturnErrPanic
DONE R-STORE-103 → tests/store/records_test.go::TestRSTORE103RecordRules,TestRSTORE103SourceRules (single rec type; see report)
DONE R-STORE-106 → tests/store/primitives_test.go::TestRSTORE106{SeqLog,PagedIndex,ShardSet,Lease} + static_test.go::TestRSTORE106OnlyStoreImportsDynamitedbAndS3
DONE R-STORE-102 → tests/store/backend_test.go::TestRSTORE102{BackendHonoursTheSevenChecks,RefusesBackendThatIgnoresIfNoneMatch}
DONE R-SEC-102 → tests/store/selfcheck_test.go::TestRSEC102OrchestratorRefusesReadableControlBucket (positive path in tests/policy)
DONE R-STORE-100 → tests/store/stack_test.go::TestRSTORE100NoListOnControlAndFleet + static_test.go::TestRSTORE100NoListOutsideSweep
DONE R-STORE-101 → tests/store/stack_test.go::TestRSTORE101ListsAreSweepsOnly + soak_test.go (slow tag)
DONE R-STORE-004 → tests/store/stack_test.go::TestRSTORE004ReadPlan (+ PagedIndex/ShardSet costs in primitives_test.go)
DONE R-STORE-105 → tests/store/retention_test.go::TestRSTORE105StagingCleanup
DONE R-STORE-007 → tests/store/retention_test.go::TestRSTORE007Retention
DONE R-STORE-150 → tests/store/retention_test.go::TestRSTORE150ReleasesAreNeverCollected (knobs: BOB_TEST_CLOCK_OFFSET)
DONE R-STORE-002 → tests/policy/matrix_test.go::TestRSEC002PolicyMatrix,TestRSEC002PrefixTable,TestRSTORE002GlobalOperations
DONE R-SEC-002 → tests/policy/{matrix,sec}_test.go::TestRSEC002PolicyMatrix,TestRSEC002PrefixTable,TestRSEC002ShippedPolicyFiles
DONE R-SEC-100 → tests/policy/sec_test.go::TestRSEC100NoCredentialMintingInSource,TestRSEC100RPCsWithoutWorkerTokenReturnNoCredentials
DONE R-SEC-102(+) → tests/policy/sec_test.go::TestRSEC102PositivePathAndRefusal
SKIP R-SEC-101 (manual: runbook docs/operations/worker-compromise.md)
DONE R-FLEET-005 → tests/store/fleet/planner_test.go::TestRFLEET005Planner (real-run 4-worker/73-drv bullet blocked: no raw-graph submit API/fixture)
DONE R-FLEET-004 → tests/store/fleet/class_test.go::TestRFLEET004ClassEligibility (+ worker-side in queue_test.go, pending)
DONE R-FLEET-011 → tests/store/fleet/queue_test.go::TestRFLEET011{QueueLiveness,SlotFirstLosesAJob,OrphanAndStaleSlots}
DONE R-FLEET-004 (worker side) → queue_test.go::TestRFLEET004WorkerIgnoresOtherClasses
DONE R-FLEET-003 → tests/store/fleet/lease_test.go::TestRFLEET003{LeaseAndRequeue,ClaimGrace,Cancel,Reslot,Chaos} (process kill -9 half: tests/chaos, T5b)
DONE R-FLEET-012 → tests/store/fleet/dispatch_test.go::TestRFLEET012{WakeLatency,PollingOnlyLatency,SelectionPolicy,NoHerdAndNoDoubleClaim,BacklogSpread}
DONE R-FLEET-009 → tests/store/fleet/budget_test.go::TestRFLEET009RequestBudgetPerJob
DONE R-FLEET-010 → budget_test.go::TestRFLEET010IdleRequestMix + idle_slow_test.go (slow tag, 100 workers x 10 min)
DONE R-FLEET-002 → tests/store/fleet/identity_test.go::TestRFLEET002{Registration,TokenLifecycle,ConcurrentRegistrars,RegistrationBacklog,ForgedResultsAndSpecs,AttachIsolation} (S11's 31 checks: 19 behaviours of docs/spikes/s11 §3.4 ported)
DONE R-FLEET-001(part) → identity_test.go::TestRFLEET002ConcurrentRegistrars; e2e p99 pending
DONE R-FLEET-006 → tests/store/fleet/checkout_test.go::TestRFLEET006{CheckoutOnlyForTheHeldJob,RevokedAtJobEndAndExpiry} (live Forgejo bullets: forge-forgejo-live, T6b)
DONE R-FLEET-001 → tests/store/fleet/e2e_test.go::TestRFLEET001ZeroSetupRegistration
DONE R-FLEET-008/R-SEC-000 → e2e_test.go::TestRFLEET008{WorkerRefusesNonRoot,WorkerRefusesWithoutCgroupV2,BuildsAreIsolated}
DONE R-FLEET-013 → e2e_test.go::TestRFLEET013ConcurrentJobsShareOneStore
ALL DONE 2026-10-22: 28 R-ids covered, SEC-101 manual. go vet/lint/layering/parity ok for T5a packages.
