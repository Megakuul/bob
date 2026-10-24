package cli

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/megakuul/bob/pkg/bob/diag"
	"github.com/megakuul/bob/pkg/engine/build"
	"github.com/megakuul/bob/pkg/engine/ir"
	"github.com/megakuul/bob/pkg/engine/translate"
)

// pipelineDef is the IR form of a bob.Pipeline (pkg/bob pipelineJSON).
type pipelineDef struct {
	Name     string   `json:"name"`
	Targets  []string `json:"targets"`
	Tests    []string `json:"tests"`
	Releases []string `json:"releases"`
}

// pipelineStep is one step of a local pipeline run.
type pipelineStep struct {
	name     string
	cmd      build.Command
	patterns []string
}

// runPipelineCmd is `bob pipeline run <name>` (R-CLI-012): the named pipeline's steps run
// locally in order, build (targets and release packages) then test, each with the output of
// bob build / bob test under a `== step <name>` header. The first failing step ends the run
// with its exit status.
func runPipelineCmd(e *env) int {
	if len(e.a.pos) != 2 || e.a.pos[0] != "run" {
		return e.out.fail(diag.New(4, "flag", "bob pipeline", "what", "run <name>"))
	}
	name := e.a.pos[1]
	if e.a.bool("remote") {
		// The framework's RemoteExecutor submits patterns, not pipelines (M16).
		return e.out.fail(diag.NotImplemented("bob pipeline run --remote"))
	}
	root, err := e.root(false)
	if err != nil {
		return e.out.fail(err)
	}
	st, caches, warns, exit, done := e.openStore(true)
	if done {
		return exit
	}
	g, err := build.Evaluate(e.ctx, build.Options{Root: root, Patterns: []string{"//..."}, Store: st, Caches: caches, Offline: e.a.bool("offline")})
	if err != nil {
		return e.out.fail(err)
	}
	steps, found, err := pipelineSteps(g, name)
	if err != nil {
		return e.out.fail(err)
	}
	if !found {
		e.out.diagnose(diag.New(1, "kind", "pipeline", "name", name), "bob graph --format json lists the pipelines")
		return e.out.finish(2, translate.Counts{}, nil)
	}
	var total translate.Counts
	var drift []string
	exit = 0
	for i, s := range steps {
		e.out.printf("== step %s\n", s.name)
		opts := build.Options{Command: s.cmd, Root: root, Patterns: s.patterns, Offline: e.a.bool("offline"),
			NoMaterialise: true, JSONLog: e.out.jsonLog, Store: st, Caches: caches}
		w := warns
		if i > 0 {
			w = nil // reported once, with the first step
		}
		code, c, d := runStep(e, root, opts, w)
		total = addCounts(total, c)
		drift = append(drift, d...)
		if code != 0 {
			exit = code
			break
		}
	}
	if e.out.json {
		return e.out.finish(exit, total, drift)
	}
	return exit
}

// pipelineSteps returns the steps of the named pipeline of g. A pipeline that names no
// targets, tests or releases builds and tests the whole repository (decision A152).
func pipelineSteps(g *ir.Graph, name string) ([]pipelineStep, bool, error) {
	for _, raw := range g.Pipelines {
		var p pipelineDef
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, false, fmt.Errorf("pipeline definition unreadable: %w", err)
		}
		if p.Name != name {
			continue
		}
		if len(p.Targets) == 0 && len(p.Tests) == 0 && len(p.Releases) == 0 {
			p.Targets, p.Tests = []string{"//..."}, []string{"//..."}
		}
		targets := slices.Clone(p.Targets)
		for _, entry := range p.Releases {
			for _, n := range g.Nodes {
				if n.Kind != build.KindRelease {
					continue
				}
				_, short, _ := strings.Cut(n.Label, ":")
				if (n.Label == entry || !strings.HasPrefix(entry, "//") && short == entry) && !slices.Contains(targets, n.Label) {
					targets = append(targets, n.Label)
				}
			}
		}
		var steps []pipelineStep
		if len(targets) > 0 {
			steps = append(steps, pipelineStep{"build", build.CmdBuild, targets})
		}
		if len(p.Tests) > 0 {
			steps = append(steps, pipelineStep{"test", build.CmdTest, p.Tests})
		}
		return steps, true, nil
	}
	return nil, false, nil
}

func addCounts(a, b translate.Counts) translate.Counts {
	a.Built += b.Built
	a.Cached += b.Cached
	a.Failed += b.Failed
	a.Skipped += b.Skipped
	a.Cancelled += b.Cancelled
	a.Upstream += b.Upstream
	a.Downloaded += b.Downloaded
	return a
}
