// Package toyeco is the self-contained test ecosystem "toy" of R-FW-051: a generator package
// that registers itself with engine.Register from init and plans one trivial derivation per
// node of kind toy.thing. pkg/engine knows nothing about it.
package toyeco

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/megakuul/bob/pkg/builder/bspec"
	"github.com/megakuul/bob/pkg/engine"
	"github.com/megakuul/bob/pkg/engine/drv"
	"github.com/megakuul/bob/pkg/engine/store"
)

func init() { engine.Register(toy{}) }

type toy struct{}

func (toy) Name() string { return "toy" }

func (toy) Lock(context.Context, engine.LockRequest) (json.RawMessage, error) {
	return json.RawMessage(`{"inputs":"toy"}`), nil
}

func (toy) Native(context.Context, engine.PlanRequest) ([]engine.GeneratedFile, error) {
	return nil, nil
}

// Plan writes "<label> built" into the output of each node and orders the derivations by the
// node's deps. Derivations are finalised against the engine's resolver (for deps planned in
// earlier waves) and this call's own derivations.
func (toy) Plan(_ context.Context, req engine.PlanRequest) (engine.PlanResult, error) {
	res := engine.PlanResult{Nodes: map[string]engine.Resolved{}}
	own := resolver{own: map[string]*drv.Derivation{}, rest: req.Resolver}
	for _, n := range req.Nodes {
		write, err := json.Marshal(bspec.WriteArgs{Path: drv.Placeholder("out") + "/result.txt", Content: n.Label + " built\n"})
		if err != nil {
			return res, err
		}
		args, err := json.Marshal(bspec.RunArgs{Steps: []bspec.Step{{Op: "write", Args: write}}})
		if err != nil {
			return res, err
		}
		job := drv.Job{
			Name:      drv.Name("toy", n.Label),
			System:    store.HostSystem(),
			Builder:   req.Builder,
			InputSrcs: []string{req.Builder},
			Spec:      bspec.Job{V: 1, Kind: "run", Label: n.Label, Outputs: []string{"out"}, Args: args},
		}
		for _, dep := range n.Deps {
			in, ok := req.Inputs[dep]
			if !ok {
				return res, fmt.Errorf("toy: dependency %s of %s was not planned", dep, n.ID)
			}
			job.InputDrvs = append(job.InputDrvs, drv.InputDrv{Path: in.Drv, Outputs: []string{"out"}})
		}
		d, err := job.Derivation()
		if err != nil {
			return res, err
		}
		if err := d.Finalize(own); err != nil {
			return res, err
		}
		path, err := d.Path()
		if err != nil {
			return res, err
		}
		own.own[path] = d
		res.Drvs = append(res.Drvs, d)
		res.Nodes[n.ID] = engine.Resolved{Drv: path, Outputs: map[string]string{"out": d.Outputs[0].Path}}
	}
	return res, nil
}

// resolver resolves this ecosystem's own derivations, everything else through the engine.
type resolver struct {
	own  map[string]*drv.Derivation
	rest drv.Resolver
}

func (r resolver) Modulo(p string) ([32]byte, error) {
	if d, ok := r.own[p]; ok {
		return d.Modulo(r)
	}
	if r.rest == nil {
		return [32]byte{}, fmt.Errorf("toy: unknown derivation %s", p)
	}
	return r.rest.Modulo(p)
}
