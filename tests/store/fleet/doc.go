// Package fleet holds the acceptance tests of SPEC §05 fleet behaviour: registration and
// tokens, queues and claims, leases and requeue, dispatch, request budgets, scheduling,
// worker isolation and checkout credentials (R-FLEET-001…013, R-SEC-000).
//
// Most tests drive the library level (internal/orchestrator, internal/worker, internal/store)
// on a real RustFS behind the counting proxy with an injected clock, so lease and token
// timing is exact and fast. Process-level behaviour (kill -9, devstack, hostile builds) is
// tested through tools/devstack. The Go API of the controllers is the harness's proposal
// (the SPEC fixes behaviour, not the API of internal/orchestrator and internal/worker); the
// owner confirms it at Gate H together with the tests.
package fleet
