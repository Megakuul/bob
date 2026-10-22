package gen

import (
	"context"
	"fmt"

	"github.com/megakuul/bob/pkg/engine"
	"github.com/megakuul/bob/pkg/engine/lock"

	// The Rust API registers the rust.* kinds the IR names.
	_ "github.com/megakuul/bob/pkg/rust"
)

func init() { engine.Register(Ecosystem{}) }

// Ecosystem is the Rust ecosystem (SPEC §03.3, ADR 0005): engine.Ecosystem and
// engine.LockFreshness.
type Ecosystem struct{}

// Name is the bob.lock section key and the kind prefix.
func (Ecosystem) Name() string { return lock.SectionRust }

// Plan turns the Rust nodes into per-crate derivations: registry sources (fetched by bob),
// build-script compile and run, rustc per crate unit, roots and test units.
func (Ecosystem) Plan(_ context.Context, req engine.PlanRequest) (res engine.PlanResult, err error) {
	defer func() { // a planner bug must not crash bob
		if v := recover(); v != nil {
			err = fmt.Errorf("rust planner: internal error: %v", v)
		}
	}()
	if req.Graph == nil {
		return engine.PlanResult{}, fmt.Errorf("rust planner: no graph")
	}
	pl, err := newPlanner(req)
	if err != nil {
		return engine.PlanResult{}, err
	}
	return pl.plan()
}

// Native returns the Cargo files of the repository's crates (§03.3.4).
func (Ecosystem) Native(_ context.Context, req engine.PlanRequest) ([]engine.GeneratedFile, error) {
	if req.Graph == nil {
		return nil, nil
	}
	ws, err := readWorkspace(req.Graph)
	if err != nil {
		return nil, err
	}
	var sec *lock.RustSection
	if len(req.Lock) > 0 {
		v, err := lock.DecodeSection(lock.SectionRust, req.Lock)
		if err != nil {
			return nil, err
		}
		sec = v.(*lock.RustSection)
	}
	return nativeFiles(ws, sec)
}
