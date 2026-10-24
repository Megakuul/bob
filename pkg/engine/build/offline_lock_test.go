package build

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/megakuul/bob/pkg/bob/diag"
	"github.com/megakuul/bob/pkg/engine"
	"github.com/megakuul/bob/pkg/engine/ir"
	"github.com/megakuul/bob/pkg/engine/lock"
)

// fakeEco is an ecosystem whose Lock records that it ran (it would download).
type fakeEco struct {
	stale  string
	locked *bool
}

func (fakeEco) Name() string { return lock.SectionGo }
func (f fakeEco) Lock(context.Context, engine.LockRequest) (json.RawMessage, error) {
	*f.locked = true
	return json.RawMessage(`{}`), nil
}
func (fakeEco) Plan(context.Context, engine.PlanRequest) (engine.PlanResult, error) {
	return engine.PlanResult{}, nil
}
func (fakeEco) Native(context.Context, engine.PlanRequest) ([]engine.GeneratedFile, error) {
	return nil, nil
}
func (f fakeEco) Stale(context.Context, engine.LockRequest) (string, ir.Pos, error) {
	return f.stale, ir.Pos{}, nil
}

// TestOfflineSection (A456): bob lock --offline keeps a present, fresh section without running
// its resolver and refuses (BOB-E-3002) what would need the network: a missing or stale
// section, or one --update names. Before, --offline was ignored and the resolvers downloaded.
func TestOfflineSection(t *testing.T) {
	ctx := context.Background()
	present := &lock.File{Sections: map[string]json.RawMessage{lock.SectionGo: json.RawMessage(`{"modules":[]}`)}}
	ran := false
	cases := []struct {
		name   string
		eco    fakeEco
		lf     *lock.File
		update bool
		refuse bool
	}{
		{"fresh", fakeEco{locked: &ran}, present, false, false},
		{"stale", fakeEco{stale: "go golang.org/x/net v0.2.0 is not pinned", locked: &ran}, present, false, true},
		{"missing", fakeEco{locked: &ran}, &lock.File{}, false, true},
		{"update", fakeEco{locked: &ran}, present, true, true},
	}
	for _, c := range cases {
		err := offlineSection(ctx, c.eco, &ir.Graph{}, c.lf, nil, "", c.update)
		var d *diag.Error
		switch {
		case c.refuse && (!errors.As(err, &d) || d.Code != 3002):
			t.Errorf("%s: err = %v, want BOB-E-3002", c.name, err)
		case !c.refuse && err != nil:
			t.Errorf("%s: err = %v, want the section kept", c.name, err)
		}
	}
	if ran {
		t.Errorf("a resolver ran under --offline")
	}
}
