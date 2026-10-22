//go:build !bob_nolandlock && !bob_noseccomp

package evalbox

// DisabledLayer names the evaluator sandbox layer a test build removed (HARNESS.md §7.4
// layer-removal runs, build tags bob_nolandlock and bob_noseccomp). Empty in real builds.
const DisabledLayer = ""
