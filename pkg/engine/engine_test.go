package engine

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/megakuul/bob/pkg/engine/ir"
)

type fake struct{ name string }

func (f fake) Name() string { return f.name }
func (fake) Lock(context.Context, LockRequest) (json.RawMessage, error) {
	return nil, nil
}
func (fake) Plan(context.Context, PlanRequest) (PlanResult, error)        { return PlanResult{}, nil }
func (fake) Native(context.Context, PlanRequest) ([]GeneratedFile, error) { return nil, nil }

// R-FW-051: an ecosystem registers itself; its kinds become known to ir.Parse; broken
// registrations are reported, not panicked on.
func TestRegister(t *testing.T) {
	Register(fake{"enginetest"})
	if e, ok := ForKind("enginetest.thing"); !ok || e.Name() != "enginetest" {
		t.Errorf("ForKind = %v, %v", e, ok)
	}
	if !ir.KnownKind("enginetest.thing") || ir.KnownKind("enginetestx.thing") {
		t.Error("ir.KnownKind does not follow the registry")
	}
	if es, err := Ecosystems(); err != nil || len(es) == 0 {
		t.Errorf("Ecosystems() = %v, %v", es, err)
	}
	Register(fake{"enginetest"})
	Register(fake{"Bad"})
	if _, err := Ecosystems(); err == nil {
		t.Error("a duplicate and an invalid registration are not reported")
	}
	regMu.Lock()
	problems = nil
	regMu.Unlock()
}
