//go:build bob_nolandlock

package eval

// DisabledLayer: this test build runs the evaluator sandbox without Landlock.
const DisabledLayer = "landlock"
