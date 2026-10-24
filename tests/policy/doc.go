// Package policy holds the S11 policy matrix (HARNESS.md §8.2): 27 S3 operations × 6
// bucket/prefix targets and 8 global/admin operations, run with the shipped worker and
// orchestrator credentials on RustFS, plus the prefix table of R-SEC-002, the credential
// rules of R-SEC-100 and the start-up check R-SEC-102 (positive path).
//
// The expected outcome of every cell is computed by a small reference evaluator written from
// the policy table of SPEC §05.2.3 (model_test.go), never from the shipped JSON files. The
// shipped files are checked against the same model, so a policy that drifts from the SPEC
// fails twice.
//
// tools/devstack applies no policies yet (Info.policies_applied=false): the matrix tests are
// red for exactly that reason until it does, and say so in their failure text.
package policy
