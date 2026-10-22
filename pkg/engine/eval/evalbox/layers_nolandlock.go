//go:build bob_nolandlock

package evalbox

// DisabledLayer: this test build runs the evaluator sandbox without Landlock.
const DisabledLayer = "landlock"
