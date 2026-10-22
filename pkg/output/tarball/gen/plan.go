// Package gen implements engine.Ecosystem for pkg/output/tarball (SPEC R-OUT-004).
package gen

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"regexp"
	"sort"
	"strings"

	"github.com/megakuul/bob/pkg/bob/diag"
	"github.com/megakuul/bob/pkg/engine"
	"github.com/megakuul/bob/pkg/engine/drv"
	"github.com/megakuul/bob/pkg/engine/ir"
	"github.com/megakuul/bob/pkg/output/tarball/internal/tarspec"
)

func init() { engine.Register(Ecosystem{}) }

// Ecosystem plans tarballs. It has no lock section and no native files.
type Ecosystem struct{}

// Name is the kind prefix "tarball".
func (Ecosystem) Name() string { return "tarball" }

// Lock has nothing to resolve.
func (Ecosystem) Lock(context.Context, engine.LockRequest) (json.RawMessage, error) { return nil, nil }

// Native writes no files.
func (Ecosystem) Native(context.Context, engine.PlanRequest) ([]engine.GeneratedFile, error) {
	return nil, nil
}

// args is the job of kind "tarball" (pkg/output/tarball/bop.Args).
type args struct {
	File     string   `json:"file"`
	Kind     string   `json:"kind"`
	Paths    []string `json:"paths"`
	Roots    []string `json:"roots"`
	Program  string   `json:"program"`
	Name     string   `json:"name"`
	Licences string   `json:"licences"`
}

// Plan plans one tarball derivation per node. Its program's closure is read while planning, so
// a Static tarball of a program with run-time dependencies is refused before anything else is
// built (BOB-E-1022).
func (Ecosystem) Plan(ctx context.Context, req engine.PlanRequest) (res engine.PlanResult, err error) {
	defer func() { // a planner bug must not crash bob
		if v := recover(); v != nil {
			err = fmt.Errorf("tarball planner: internal error: %v", v)
		}
	}()
	byID := map[string]*ir.Node{}
	if req.Graph != nil {
		for _, n := range req.Graph.Nodes {
			byID[n.ID] = n
		}
	}
	jobs := engine.NewJobs(req)
	res.Nodes = map[string]engine.Resolved{}
	var errs []error
	for _, n := range req.Nodes {
		rv, err := plan(ctx, req, jobs, byID, n)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		res.Nodes[n.ID] = rv
	}
	if len(errs) > 0 {
		return engine.PlanResult{}, errors.Join(errs...)
	}
	res.Drvs = jobs.Drvs
	return res, nil
}

func plan(ctx context.Context, req engine.PlanRequest, jobs *engine.Jobs, byID map[string]*ir.Node, n *ir.Node) (engine.Resolved, error) {
	var s tarspec.Tarball
	if err := json.Unmarshal(n.Spec, &s); err != nil {
		return engine.Resolved{}, fmt.Errorf("%s: tarball spec: %w", n.Label, err)
	}
	if len(n.Deps) != 1 {
		return engine.Resolved{}, fmt.Errorf("%s: a tarball has exactly one program", n.Label)
	}
	pn := byID[n.Deps[0]]
	prog, ok := req.Inputs[n.Deps[0]]
	if !ok || pn == nil {
		return engine.Resolved{}, fmt.Errorf("%s: program %s is not planned", n.Label, n.Deps[0])
	}
	roots := engine.PrimaryOutputs(pn, prog)
	if len(roots) == 0 {
		return engine.Resolved{}, fmt.Errorf("%s: program %s has no output", n.Label, pn.Label)
	}
	name := drv.Name("tarball", s.Name, s.Platform)
	drvs := map[string][]string{}
	var srcs []string
	for _, o := range roots {
		if o.Drv == "" {
			srcs = append(srcs, o.Path)
		} else {
			drvs[o.Drv] = append(drvs[o.Drv], o.Name)
		}
	}
	closer, ok := req.Store.(engine.PlanCloser)
	if !ok {
		return engine.Resolved{}, fmt.Errorf("%s: tarballs need the store while planning (engine.PlanCloser)", n.Label)
	}
	refs, err := closer.Closure(ctx, roots)
	if errors.Is(err, engine.ErrClosureBuild) {
		// The program does not build: plan a tarball that waits for it, so the build stage
		// reports the program's failure (this job never runs then).
		dp, out, err := jobs.Add(engine.Job{Name: name, Kind: "tarball", Label: n.Label, Drvs: drvs, Srcs: srcs,
			Args: args{File: "unplannable", Kind: "unplannable", Paths: []string{}, Roots: []string{}}})
		return engine.Resolved{Drv: dp, Outputs: map[string]string{"out": out}}, err
	}
	if err != nil {
		return engine.Resolved{}, err
	}
	own := engine.OwnLicences(req.Graph, req.Inputs)
	a := args{File: s.Name + "-" + s.Platform + ".tar.gz", Kind: s.Kind, Paths: []string{}, Roots: []string{}}
	var rootPaths []string
	for _, o := range roots {
		rootPaths = append(rootPaths, o.Path)
	}
	switch s.Kind {
	case tarspec.Static:
		main := roots[0].Path
		var needs []string
		for _, r := range refs[main] {
			needs = append(needs, short(r))
		}
		if len(needs) > 0 {
			sort.Strings(needs)
			return engine.Resolved{}, diag.New(1022, "label", pn.Label, "deps", strings.Join(needs, ", "))
		}
		if prog.MainProgram == "" {
			return engine.Resolved{}, diag.New(1020, "label", n.Label, "option", "Program",
				"reason", pn.Label+" has no main program")
		}
		a.Program, a.Name = path.Join(main, prog.MainProgram), s.Name
		lj, err := json.Marshal(engine.Licences(ctx, req.Nixpkgs, []string{main}, own))
		if err != nil {
			return engine.Resolved{}, err
		}
		a.Licences = string(lj) + "\n"
	case tarspec.Closure:
		a.Paths = engine.ClosureOf(refs, rootPaths...)
		a.Roots = rootPaths
		lj, err := json.Marshal(engine.Licences(ctx, req.Nixpkgs, a.Paths, own))
		if err != nil {
			return engine.Resolved{}, err
		}
		a.Licences = string(lj) + "\n"
	default:
		return engine.Resolved{}, fmt.Errorf("%s: unknown tarball kind %q", n.Label, s.Kind)
	}
	dp, out, err := jobs.Add(engine.Job{Name: name, Kind: "tarball", Label: n.Label, Args: a, Drvs: drvs, Srcs: srcs})
	if err != nil {
		return engine.Resolved{}, err
	}
	return engine.Resolved{Drv: dp, Outputs: map[string]string{"out": out}}, nil
}

// versionDigits is the leading numeric part of a version ("2.42" of "2.42-47").
var versionDigits = regexp.MustCompile(`^[0-9][0-9.]*`)

// short names a store path for messages: "glibc-2.42" (name and numeric version).
func short(p string) string {
	name, version := engine.StoreName(p)
	if v := strings.TrimRight(versionDigits.FindString(version), "."); v != "" {
		return name + "-" + v
	}
	return name
}
