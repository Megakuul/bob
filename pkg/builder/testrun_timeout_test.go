package builder

import (
	"strings"
	"testing"
	"time"

	"github.com/megakuul/bob/pkg/builder/bspec"
)

// F11 / A92: a test unit that hangs is stopped at its timeout and fails with the BOB-E-2007
// diagnostic "<label> timed out after <n>s"; the result still goes to the build log.
func TestTestRunTimeout(t *testing.T) {
	e := newEnv(t, "out")
	start := time.Now()
	code, log := e.run(t, "test-run", testRunJob(t, bspec.TestRunArgs{Argv: helper(t, "sleeper"), TimeoutS: 1}))
	if d := time.Since(start); d > 30*time.Second {
		t.Errorf("the job took %s, want it stopped after about 1 s", d)
	}
	if code != ExitStep || !strings.Contains(log, `"msg":"//it:it timed out after 1s"`) || !strings.Contains(log, `bob:: {"type":"test-result"`) {
		t.Fatalf("exit %d, want %d with the timeout diagnostic and the result:\n%s", code, ExitStep, log)
	}
}

// A negative timeout is an invalid job; 0 means the 10-minute default.
func TestTestRunTimeoutArgs(t *testing.T) {
	if err := checkTestRun(bspec.TestRunArgs{Argv: []string{"t"}, TimeoutS: -1}); err == nil {
		t.Errorf("negative timeout accepted")
	}
	if bspec.DefaultTestTimeoutS != 600 {
		t.Errorf("default test timeout %d s, want 600 (10 min)", bspec.DefaultTestTimeoutS)
	}
}
