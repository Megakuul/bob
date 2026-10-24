package gen

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/megakuul/bob/pkg/engine"
	"github.com/megakuul/bob/pkg/engine/drv"
	"github.com/megakuul/bob/pkg/engine/ir"
	"github.com/megakuul/bob/pkg/openapi/internal/oaspec"
)

func init() {
	engine.Register(Ecosystem{})
	engine.RegisterCodegenKind(oaspec.KindGo)
	engine.RegisterCodegenKind(oaspec.KindTS)
}

// Ecosystem plans the OpenAPI generators. It has no lock section and no native files: the
// generators come from the nixpkgs pin (oapi-codegen) or from a tool lock embedded in bob
// (openapi-typescript, §03.6.5).
type Ecosystem struct{}

// Name is the kind prefix "openapi".
func (Ecosystem) Name() string { return "openapi" }

// Lock has nothing to resolve.
func (Ecosystem) Lock(context.Context, engine.LockRequest) (json.RawMessage, error) { return nil, nil }

// Native writes no files.
func (Ecosystem) Native(context.Context, engine.PlanRequest) ([]engine.GeneratedFile, error) {
	return nil, nil
}

// Materialises reports whether g realises an OpenAPI generator (bob gen builds it then).
func (Ecosystem) Materialises(g *ir.Graph) bool {
	for _, n := range g.Nodes {
		if n.Kind == oaspec.KindGo || n.Kind == oaspec.KindTS {
			return true
		}
	}
	return false
}

// Plan plans one codegen derivation per realised recipe; a spec node is its imported source.
func (Ecosystem) Plan(ctx context.Context, req engine.PlanRequest) (res engine.PlanResult, err error) {
	defer func() { // a planner bug must not crash bob
		if v := recover(); v != nil {
			err = fmt.Errorf("openapi planner: internal error: %v", v)
		}
	}()
	jobs := engine.NewJobs(req)
	res.Nodes = map[string]engine.Resolved{}
	var errs []error
	for _, n := range req.Nodes {
		var rv engine.Resolved
		var m *engine.Materialisation
		var err error
		switch n.Kind {
		case oaspec.KindSpec:
			src, ok := req.Sources[n.ID]
			if !ok {
				err = fmt.Errorf("%s: the OpenAPI document was not imported", n.Label)
			}
			rv = engine.Resolved{Outputs: map[string]string{"out": src}}
		case oaspec.KindGo:
			rv, m, err = planGo(ctx, req, jobs, n)
		case oaspec.KindTS:
			rv, m, err = planTS(ctx, req, &res, n)
		default:
			err = fmt.Errorf("%s: unknown OpenAPI kind %s", n.Label, n.Kind)
		}
		if err != nil {
			errs = append(errs, err)
			continue
		}
		res.Nodes[n.ID] = rv
		if m != nil {
			res.Materialise = append(res.Materialise, *m)
		}
	}
	if len(errs) > 0 {
		return engine.PlanResult{}, errors.Join(errs...)
	}
	res.Drvs = append(res.Drvs, jobs.Drvs...)
	return res, nil
}

// buildSrc is where a codegen job lays out its inputs at their repository paths, so the
// generators' messages name repository files (§03.6.2).
const (
	buildSrc = "/build/src"
	buildCfg = "/build/cfg"
	buildTmp = "/build/tmp"
)

// step is one operation of a bob-builder "run" job.
func step(op string, args map[string]any) map[string]any {
	return map[string]any{"op": op, "args": args}
}

// input returns output o of n's dependency i (a tool: "bin" before "out").
func input(req engine.PlanRequest, n *ir.Node, i int, drvs map[string][]string, srcs *[]string, prefer ...string) (string, error) {
	if i < 0 || i >= len(n.Deps) {
		return "", fmt.Errorf("%s: dependency index %d out of range", n.Label, i)
	}
	rv, ok := req.Inputs[n.Deps[i]]
	if !ok {
		return "", fmt.Errorf("%s: dependency %s is not planned", n.Label, n.Deps[i])
	}
	for _, o := range append(prefer, "out") {
		p, ok := rv.Outputs[o]
		if !ok || p == "" {
			continue
		}
		if rv.Drv != "" {
			drvs[rv.Drv] = append(drvs[rv.Drv], o)
		} else {
			*srcs = append(*srcs, p)
		}
		return p, nil
	}
	return "", fmt.Errorf("%s: dependency %s has no output %v", n.Label, n.Deps[i], prefer)
}

// storeName is the derivation name of codegen node n ("gen-svc-gen-rest").
func storeName(n *ir.Node) string {
	dir, name, _ := strings.Cut(strings.TrimPrefix(n.Label, "//"), ":")
	if dir == "" {
		return drv.Name(name)
	}
	return drv.Name(strings.ReplaceAll(dir, "/", "-"), name)
}
