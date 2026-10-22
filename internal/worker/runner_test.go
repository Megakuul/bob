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
