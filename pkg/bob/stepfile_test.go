package bob

import (
	"strings"
	"testing"
)

// TestStepFileOnlyDeclaredOutputs is the regression test of review finding F10 (SPEC §1.4.2,
// R-FW-023): Step.File accepts exactly the step's declared Outputs; anything else is a
// definition error at evaluation, not a failure in the consumer's build.
func TestStepFileOnlyDeclaredOutputs(t *testing.T) {
	for _, p := range []string{"../../etc", "other.txt", "/etc/passwd", "", "./v.txt", "gen/../v.txt", "gen"} {
		t.Run(p, func(t *testing.T) {
			isolate(t)
			step := Define(func(p *P) *Step {
				return Run(p, "version", RunOpts{Cmd: []string{"true"}, Outputs: []string{"v.txt", "gen/a.h"}})
			})
			Define(func(pp *P) *thing { return mk(pp, "test.prof", "lib", step(pp).File(p)) })
			msgs := evalErr(t, "//:lib")
			if len(msgs) == 0 || !strings.Contains(strings.Join(msgs, "\n"), "is not one of the step's declared Outputs") {
				t.Errorf("Step.File(%q): errors %q", p, msgs)
			}
		})
	}
	for _, p := range []string{"v.txt", "gen/a.h"} {
		t.Run("ok "+p, func(t *testing.T) {
			isolate(t)
			step := Define(func(p *P) *Step {
				return Run(p, "version", RunOpts{Cmd: []string{"true"}, Outputs: []string{"v.txt", "./gen/a.h"}})
			})
			Define(func(pp *P) *thing { return mk(pp, "test.prof", "lib", step(pp).File(p)) })
			evalOK(t, "//:lib")
		})
	}
}
