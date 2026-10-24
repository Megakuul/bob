package worker

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/megakuul/bob/pkg/engine/eval"
)

func TestEvalRequestIsUntrusted(t *testing.T) {
	if !EvalRequest(eval.Request{Root: "/src"}).Untrusted {
		t.Fatal("worker evaluations must be untrusted (A98)")
	}
}

func TestRunJobNotBuilt(t *testing.T) {
	var out bytes.Buffer
	if err := RunJob(context.Background(), strings.NewReader(`{"id":"j1","epoch":1}`), &out, NotBuilt{}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `"status":"failed"`) || !strings.Contains(out.String(), "not implemented yet") {
		t.Fatalf("outcome %s", out.String())
	}
}

type recOut struct{ lines, ends []string }

func (r *recOut) Line(pkg string, stream int32, text string) { r.lines = append(r.lines, pkg+":"+text) }
func (r *recOut) EndPackage(pkg string, e PackageEnd)        { r.ends = append(r.ends, pkg+":"+e.Status) }

// lineRunner reports two lines and a package end.
type lineRunner struct{}

func (lineRunner) Run(_ context.Context, _ Job, out Output) Outcome {
	out.Line("//a:a", 1, "hello")
	out.Line("//a:a", 2, "world")
	out.EndPackage("//a:a", PackageEnd{Status: "ok"})
	return Outcome{Status: "ok"}
}

func TestJobEventsRoundTrip(t *testing.T) {
	var buf bytes.Buffer
	if err := RunJob(context.Background(), strings.NewReader(`{"id":"j1"}`), &buf, lineRunner{}); err != nil {
		t.Fatal(err)
	}
	var r recOut
	o, err := readEvents(&buf, &r)
	if err != nil || o == nil || o.Status != "ok" {
		t.Fatalf("outcome %+v, %v", o, err)
	}
	if strings.Join(r.lines, ",") != "//a:a:hello,//a:a:world" || strings.Join(r.ends, ",") != "//a:a:ok" {
		t.Fatalf("events %+v", r)
	}
	if _, err := readEvents(strings.NewReader("not json\n"), &r); err == nil {
		t.Fatalf("a malformed event was accepted")
	}
}
