package gen

import (
	"context"
	"fmt"
	"os"

	"github.com/megakuul/bob/pkg/engine"
	"github.com/megakuul/bob/pkg/engine/lock"

	// The OCaml API registers the opam.* kinds the IR names.
	_ "github.com/megakuul/bob/pkg/ocaml"
)

func init() { engine.Register(Ecosystem{}) }

// Ecosystem is the OCaml ecosystem (SPEC §03.5, ADR 0007): engine.Ecosystem,
// engine.LockFreshness and engine.DevPreparer. Its name is the bob.lock section key "opam",
// which is also the prefix of its node kinds.
type Ecosystem struct{}

// Name is the bob.lock section key and the kind prefix.
func (Ecosystem) Name() string { return lock.SectionOpam }

// Plan turns the OCaml nodes into derivations: one per opam package of their closure (sources
// fetched by bob), one per local library and executable, plus a test-run per test.
func (Ecosystem) Plan(ctx context.Context, req engine.PlanRequest) (res engine.PlanResult, err error) {
	defer func() { // a planner bug must not crash bob
		if v := recover(); v != nil {
			err = fmt.Errorf("ocaml planner: internal error: %v", v)
		}
	}()
	if req.Graph == nil {
		return engine.PlanResult{}, fmt.Errorf("ocaml planner: no graph")
	}
	pl, err := newPlanner(ctx, req)
	if err != nil {
		return engine.PlanResult{}, err
	}
	return pl.plan()
}

// Native returns the editor files of the OCaml packages (§03.5.4): a .merlin per directory.
func (Ecosystem) Native(_ context.Context, req engine.PlanRequest) (files []engine.GeneratedFile, err error) {
	defer func() {
		if v := recover(); v != nil {
			err = fmt.Errorf("ocaml planner: internal error: %v", v)
		}
	}()
	if req.Graph == nil {
		return nil, nil
	}
	return merlinFiles(req)
}

// envOr is an environment variable (planning reads only mirror settings from it, which never
// change a derivation).
func envOr(name string) string { return os.Getenv(name) }
