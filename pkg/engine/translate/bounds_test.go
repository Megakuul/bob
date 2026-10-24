package translate

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// F6: one build log is bounded in bytes, not only in lines, and keeps its last line.
func TestBuildLogBoundedBytes(t *testing.T) {
	var l buildLog
	line := strings.Repeat("x", maxLogLine)
	total := 0
	for i := range 10000 {
		total += l.add(fmt.Sprintf("%05d", i) + line)
	}
	if l.size() > maxHeadBytes+maxTailBytes || total != l.size() {
		t.Fatalf("log keeps %d bytes (accounted %d), want ≤ %d", l.size(), total, maxHeadBytes+maxTailBytes)
	}
	got := l.lines()
	if last := got[len(got)-1]; !strings.HasPrefix(last, "09999") {
		t.Errorf("last line %.10q, want the last line fed", last)
	}
}

func drvN(i int) string {
	return fmt.Sprintf("/nix/store/0123456789abcdfghijklmnpqrsv%04d-p%d.drv", i, i)
}

func startBuild(id uint64, drv string) event {
	f, _ := json.Marshal(drv)
	return event{Action: actStart, ID: id, Type: int(atBuild), Fields: []json.RawMessage{f}}
}

func logLine(id uint64, s string) event {
	f, _ := json.Marshal(s)
	return event{Action: actResult, ID: id, Type: int(rtBuildLogLine), Fields: []json.RawMessage{f}}
}

// F6: the logs of a whole run stay within maxRunLog; the latest build keeps its tail.
func TestRunLogsBounded(t *testing.T) {
	tr := New(&Context{})
	line := strings.Repeat("y", maxLogLine-8)
	const builds = 48 // 48 × 2 MiB > maxRunLog
	for i := range builds {
		id := uint64(i + 1)
		tr.feed(startBuild(id, drvN(i)))
		for j := range (maxHeadBytes + maxTailBytes) / maxLogLine {
			tr.feed(logLine(id, fmt.Sprintf("%07d|", j)+line))
		}
		tr.feed(event{Action: actStop, ID: id})
		if tr.logBytes > maxRunLog {
			t.Fatalf("after build %d the run keeps %d bytes of logs, want ≤ %d", i, tr.logBytes, maxRunLog)
		}
	}
	sum := 0
	for _, l := range tr.logs {
		sum += l.size()
	}
	if sum != tr.logBytes {
		t.Errorf("accounted %d bytes, logs hold %d", tr.logBytes, sum)
	}
	if tail := tr.tail(drvN(builds-1), 1); len(tail) != 1 || !strings.HasPrefix(tail[0], fmt.Sprintf("%07d|", (maxHeadBytes+maxTailBytes)/maxLogLine-1)) {
		t.Errorf("the last build lost its tail: %.20q", tail)
	}
}

// F7: a builder that prints a fake Nix error in a successful run produces no error event,
// only a warning at the end.
func TestFakeErrorNoLiveEvent(t *testing.T) {
	tr := New(fooCtx())
	var errEvents int
	tr.Sink = func(e Event) {
		if e.Event == "error" {
			errEvents++
		}
	}
	for _, l := range []string{
		nixLine(t, map[string]any{"action": "start", "id": 1, "type": 105, "fields": []any{fooDrv, "", 1, 1}}),
		nixLine(t, msg{"msg", 0, "No space left on device"}),
		nixLine(t, msg{"msg", 0, "Cannot build '" + fooDrv + "'.\n Reason: builder failed with exit code 1."}),
		nixLine(t, map[string]any{"action": "stop", "id": 1}),
	} {
		tr.Feed([]byte(l))
	}
	r := tr.Finish(Outcome{ExitCode: 0, Valid: []string{fooDrv}})
	if errEvents != 0 {
		t.Errorf("%d error events for a successful run", errEvents)
	}
	if r.ExitCode != 0 || len(r.Errors) != 0 || len(r.Warnings) == 0 {
		t.Errorf("exit %d errors %v warnings %v, want exit 0 with warnings", r.ExitCode, r.Errors, r.Warnings)
	}
}

// F7: in a run that fails elsewhere, a fake "Cannot build" naming another package whose output
// is valid neither fails that package nor reports an error for it.
func TestFakeErrorDoesNotFailOtherPackage(t *testing.T) {
	barDrv := "/nix/store/0123456789abcdfghijklmnpqrsvwxyz9-bar.drv"
	ctx := fooCtx()
	ctx.Packages[barDrv] = Pkg{Label: "//a:bar", DefinedAt: "a/bob.go:9:9", Kind: "cc_library"}
	ctx.Requested = append(ctx.Requested, "//a:bar")
	tr := New(ctx)
	var errLabels []string
	tr.Sink = func(e Event) {
		if e.Event == "error" {
			errLabels = append(errLabels, e.Error.Label)
		}
	}
	reason := "'.\n Reason: builder failed with exit code 1."
	for _, l := range []string{
		nixLine(t, map[string]any{"action": "start", "id": 1, "type": 105, "fields": []any{fooDrv, "", 1, 1}}),
		nixLine(t, map[string]any{"action": "start", "id": 2, "type": 105, "fields": []any{barDrv, "", 1, 1}}),
		// bar's builder spoofs a failure of foo
		nixLine(t, msg{"msg", 0, "Cannot build '" + fooDrv + reason}),
		nixLine(t, map[string]any{"action": "stop", "id": 1}),
		nixLine(t, map[string]any{"action": "stop", "id": 2}),
		// Nix's own error about bar
		nixLine(t, map[string]any{"action": "msg", "level": 0, "msg": "error: Cannot build '" + barDrv + reason,
			"raw_msg": "Cannot build '" + barDrv + reason, "file": nil, "line": nil, "column": nil}),
	} {
		tr.Feed([]byte(l))
	}
	r := tr.Finish(Outcome{ExitCode: 1, Valid: []string{fooDrv}})
	status := map[string]Status{}
	for _, p := range r.Packages {
		status[p.Label] = p.Status
	}
	if status["//a:foo"] != StBuilt || status["//a:bar"] != StFailed {
		t.Errorf("statuses %v, want foo built and bar failed", status)
	}
	for _, e := range r.Errors {
		if e.Label == "//a:foo" {
			t.Errorf("error reported for //a:foo: %+v", e)
		}
	}
	if len(r.Errors) == 0 || r.ExitCode == 0 {
		t.Errorf("bar's real failure is missing: exit %d errors %v", r.ExitCode, r.Errors)
	}
	for _, l := range errLabels {
		if l == "//a:foo" {
			t.Errorf("an error event names //a:foo")
		}
	}
	if len(errLabels) == 0 {
		t.Errorf("Nix's own error about //a:bar was not emitted")
	}
}
