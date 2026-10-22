//go:build bob_noseccomp && !bob_nolandlock

package evalbox

// DisabledLayer: this test build runs the evaluator sandbox without seccomp.
const DisabledLayer = "seccomp"
