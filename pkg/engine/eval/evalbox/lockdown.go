package evalbox

import (
	"fmt"
	"os"
)

// lockDown applies Landlock and seccomp in stage2 (minus the layer a test build removed,
// DisabledLayer). Without Landlock (kernel < 5.13 or not in lsm=) a local evaluation continues
// with the other layers and says so; a worker (Strict) refuses.
func lockDown(cfg *stageConfig) error {
	if DisabledLayer != "landlock" {
		if err := applyLandlock(cfg.Rules); err != nil {
			if cfg.Strict || LandlockABI() > 0 {
				return wrap(err, "landlock")
			}
			fmt.Fprintf(os.Stderr, "warning: the evaluation sandbox runs without Landlock: %v\n", err)
		}
	}
	if DisabledLayer != "seccomp" {
		if err := applySeccomp(); err != nil {
			return wrap(err, "seccomp")
		}
	}
	return nil
}
