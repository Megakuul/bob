package worker

import (
	"testing"
	"time"

	"github.com/megakuul/bob/pkg/engine/build"
)

type recOutput struct {
	lines []string
	ends  map[string]string
}

func (r *recOutput) Line(pkg string, _ int32, text string) { r.lines = append(r.lines, pkg+" "+text) }
func (r *recOutput) EndPackage(pkg string, e PackageEnd)   { r.ends[pkg] = e.Status }

// TestEventsLogIsBuildOutput: a package's log holds what its build printed, no start marker
// and no bob-builder diagnostics (A296, A299: R-LOG-102, R-LOG-003 compare it with the build's
// lines).
func TestEventsLogIsBuildOutput(t *testing.T) {
	out := &recOutput{ends: map[string]string{}}
	e := &events{out: out, start: map[string]time.Time{}}
	e.handle(build.Event{Event: "package.start", Label: "//a:t", Action: "build"})
	e.handle(build.Event{Event: "log", Label: "//a:t", Line: "hello"})
	e.handle(build.Event{Event: "log", Label: "//a:t", Line: "bob-builder: //a:t: step 3 exec bash failed: exit status 1"})
	e.handle(build.Event{Event: "log", Label: "//a:t", Line: `bob:: {"code":2000}`})
	e.handle(build.Event{Event: "package.finish", Label: "//a:t", Status: "built"})
	if len(out.lines) != 1 || out.lines[0] != "//a:t hello" {
		t.Errorf("log lines = %q, want only the build's line", out.lines)
	}
	if out.ends["//a:t"] != "ok" {
		t.Errorf("package end status = %q, want ok", out.ends["//a:t"])
	}
}
