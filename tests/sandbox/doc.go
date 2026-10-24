// Package sandbox holds the evaluator sandbox suite (SPEC R-FW-045, HARNESS.md §7.4): the 15
// hostile bob.go cases of tests/fixtures/evil run through eval.Evaluate. Layer-removal runs
// use the build tags bob_nolandlock and bob_noseccomp (eval.DisabledLayer).
package sandbox
