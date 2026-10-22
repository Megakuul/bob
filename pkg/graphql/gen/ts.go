package gen

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/megakuul/bob/pkg/engine"
	"github.com/megakuul/bob/pkg/engine/drv"
	"github.com/megakuul/bob/pkg/engine/ir"
	"github.com/megakuul/bob/pkg/graphql/internal/gqlspec"
	npmgen "github.com/megakuul/bob/pkg/npm/gen"
)

// buildTool is where a job puts the tool's driver next to its node_modules.
const buildTool = "/build/tool"

// tsFile is the file graphql.TS generates in its owned directory.
const tsFile = "graphql.ts"

// planTS plans the graphql-codegen derivation of n: the tool lock's node_modules, bob's driver,
// the schema and the operation documents at their repository paths. The output is one file,
// graphql.ts (types of the schema and of every operation).
func planTS(ctx context.Context, req engine.PlanRequest, res *engine.PlanResult, n *ir.Node) (engine.Resolved, *engine.Materialisation, error) {
	var s gqlspec.TS
	if err := json.Unmarshal(n.Spec, &s); err != nil {
		return engine.Resolved{}, nil, fmt.Errorf("%s: graphql.ts spec: %w", n.Label, err)
	}
	owned, err := ownedDir(n, s.Dir)
	if err != nil {
		return engine.Resolved{}, nil, err
	}
	tl, err := toolLock()
	if err != nil {
		return engine.Resolved{}, nil, err
	}
	tool, err := npmgen.ToolModules(ctx, req, n, toolName, tl, s.Node, s.Bash)
	if err != nil {
		return engine.Resolved{}, nil, err
	}
	res.Drvs = append(res.Drvs, tool.Drvs...)
	res.Fetches = append(res.Fetches, tool.Fetches...)
	treq := req
	treq.Resolver = tool
	jobs := engine.NewJobs(treq)

	d := newDeps()
	d.drvs[tool.Drv] = []string{"out"}
	node, err := d.input(req, n, s.Node, "bin")
	if err != nil {
		return engine.Resolved{}, nil, err
	}
	schemaSrc, err := d.input(req, n, s.Schema)
	if err != nil {
		return engine.Resolved{}, nil, err
	}
	schema, err := sourceFiles(req, schemaSrc)
	if err != nil {
		return engine.Resolved{}, nil, fmt.Errorf("%s: schema: %w", n.Label, err)
	}
	out := drv.Placeholder("out")
	steps := []any{
		step("mkdir", map[string]any{"path": out}),
		step("copy", map[string]any{"from": schemaSrc, "to": buildSrc}),
	}
	docs := []string{}
	if s.Documents >= 0 {
		docSrc, err := d.input(req, n, s.Documents)
		if err != nil {
			return engine.Resolved{}, nil, err
		}
		if docs, err = sourceFiles(req, docSrc); err != nil {
			return engine.Resolved{}, nil, fmt.Errorf("%s: documents: %w", n.Label, err)
		}
		steps = append(steps, step("copy", map[string]any{"from": docSrc, "to": buildSrc}))
	}
	scalars := s.Scalars
	if scalars == nil {
		scalars = map[string]string{}
	}
	job, err := json.Marshal(map[string]any{"out": out + "/" + tsFile, "schema": schema, "documents": docs, "scalars": scalars})
	if err != nil {
		return engine.Resolved{}, nil, err
	}
	steps = append(steps,
		step("symlink", map[string]any{"target": tool.Dir, "link": buildTool + "/node_modules"}),
		step("write", map[string]any{"path": buildTool + "/driver.mjs", "content": driverJS}),
		step("write", map[string]any{"path": buildCfg + "/job.json", "content": string(job)}),
		step("exec", map[string]any{"argv": []string{"node", buildTool + "/driver.mjs", buildCfg + "/job.json"},
			"env": map[string]string{"HOME": buildTmp}, "cwd": buildSrc, "path": []string{node + "/bin"}}),
	)
	tool.Prepare(ctx, req.Store)
	rv, m, err := codegenJob(ctx, req, jobs, n, owned, steps, d)
	res.Drvs = append(res.Drvs, jobs.Drvs...)
	return rv, m, err
}
