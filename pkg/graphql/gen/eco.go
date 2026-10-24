package gen

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"

	"github.com/megakuul/bob/pkg/engine"
	"github.com/megakuul/bob/pkg/engine/drv"
	"github.com/megakuul/bob/pkg/engine/ir"
	"github.com/megakuul/bob/pkg/graphql/internal/gqlspec"
)

func init() {
	engine.Register(Ecosystem{})
	engine.RegisterCodegenKind(gqlspec.KindGqlgen)
	engine.RegisterCodegenKind(gqlspec.KindTS)
}

// Ecosystem plans the GraphQL generators. It has no lock section and no native files: gqlgen
// comes from the nixpkgs pin, graphql-codegen from a tool lock embedded in bob (§03.6.5).
type Ecosystem struct{}

// Name is the kind prefix "graphql".
func (Ecosystem) Name() string { return "graphql" }

// Lock has nothing to resolve.
func (Ecosystem) Lock(context.Context, engine.LockRequest) (json.RawMessage, error) { return nil, nil }

// Native writes no files.
func (Ecosystem) Native(context.Context, engine.PlanRequest) ([]engine.GeneratedFile, error) {
	return nil, nil
}

// Materialises reports whether g realises a GraphQL generator (bob gen builds it then).
func (Ecosystem) Materialises(g *ir.Graph) bool {
	for _, n := range g.Nodes {
		if n.Kind == gqlspec.KindGqlgen || n.Kind == gqlspec.KindTS {
			return true
		}
	}
	return false
}

// Plan plans one codegen derivation per realised recipe; schema and document nodes are their
// imported sources.
func (Ecosystem) Plan(ctx context.Context, req engine.PlanRequest) (res engine.PlanResult, err error) {
	defer func() { // a planner bug must not crash bob
		if v := recover(); v != nil {
			err = fmt.Errorf("graphql planner: internal error: %v", v)
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
		case gqlspec.KindSchema, gqlspec.KindDocuments:
			src, ok := req.Sources[n.ID]
			if !ok {
				err = fmt.Errorf("%s: the GraphQL files were not imported", n.Label)
			}
			rv = engine.Resolved{Outputs: map[string]string{"out": src}}
		case gqlspec.KindGqlgen:
			rv, m, err = planGqlgen(ctx, req, jobs, n)
		case gqlspec.KindTS:
			rv, m, err = planTS(ctx, req, &res, n)
		default:
			err = fmt.Errorf("%s: unknown GraphQL kind %s", n.Label, n.Kind)
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

// Where a codegen job lays out its inputs: at their repository paths below buildSrc, so the
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

// deps collects a job's inputs.
type deps struct {
	drvs map[string][]string
	srcs []string
}

func newDeps() *deps { return &deps{drvs: map[string][]string{}} }

// input returns output o of n's dependency i (a tool: "bin" before "out").
func (d *deps) input(req engine.PlanRequest, n *ir.Node, i int, prefer ...string) (string, error) {
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
			d.drvs[rv.Drv] = append(d.drvs[rv.Drv], o)
		} else {
			d.srcs = append(d.srcs, p)
		}
		return p, nil
	}
	return "", fmt.Errorf("%s: dependency %s has no output %v", n.Label, n.Deps[i], prefer)
}

// maxSchemaFiles bounds the files bob lists of one imported source tree.
const maxSchemaFiles = 10000

// sourceFiles lists the repository paths of the files of an imported source tree (store path
// src), sorted.
func sourceFiles(req engine.PlanRequest, src string) ([]string, error) {
	if req.Store == nil {
		return nil, fmt.Errorf("listing %s needs the store", src)
	}
	root := req.Store.HostPath(src)
	var out []string
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		out = append(out, filepath.ToSlash(rel))
		if len(out) > maxSchemaFiles {
			return fmt.Errorf("more than %d files", maxSchemaFiles)
		}
		return nil
	})
	sort.Strings(out)
	return out, err
}

// storeName is the derivation name of codegen node n ("svc-gen-graph").
func storeName(n *ir.Node) string {
	dir, name, _ := strings.Cut(strings.TrimPrefix(n.Label, "//"), ":")
	if dir == "" {
		return drv.Name(name)
	}
	return drv.Name(strings.ReplaceAll(dir, "/", "-"), name)
}

// codegenJob finalises a codegen "run" job: built while planning and imported by content for
// the consumers (engine.PlanCodegen, R-GEN-005), materialised into dir.
func codegenJob(ctx context.Context, req engine.PlanRequest, jobs *engine.Jobs, n *ir.Node, dir string, steps []any, d *deps) (engine.Resolved, *engine.Materialisation, error) {
	dp, out, err := jobs.Add(engine.Job{Name: storeName(n), Kind: "run", Label: n.Label, Drvs: d.drvs, Srcs: d.srcs,
		Args: map[string]any{"steps": steps}})
	if err != nil {
		return engine.Resolved{}, nil, fmt.Errorf("%s: %w", n.Label, err)
	}
	var job *drv.Derivation
	for _, x := range jobs.Drvs {
		if p, err := x.Path(); err == nil && p == dp {
			job = x
		}
	}
	rv, m := engine.PlanCodegen(ctx, req, engine.Codegen{Node: n, Drv: job, Path: dp, Out: out, Dir: dir})
	return rv, &m, nil
}
