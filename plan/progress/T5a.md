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
