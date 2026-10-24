package gen

import (
	"context"
	"encoding/json"
	"fmt"
	"path"
	"strings"

	"github.com/megakuul/bob/pkg/engine"
	"github.com/megakuul/bob/pkg/engine/drv"
	"github.com/megakuul/bob/pkg/engine/ir"
	golanggen "github.com/megakuul/bob/pkg/golang/gen"
	npmgen "github.com/megakuul/bob/pkg/npm/gen"
	"github.com/megakuul/bob/pkg/openapi/internal/oaspec"
)

// planGo plans the oapi-codegen derivation of n: the document at its repository path, a
// go.mod with the consuming module's path and Go version (oapi-codegen reads the go directive
// for the std-http server), bob's config, and the output written straight into $out.
func planGo(ctx context.Context, req engine.PlanRequest, jobs *engine.Jobs, n *ir.Node) (engine.Resolved, *engine.Materialisation, error) {
	var s oaspec.Go
	if err := json.Unmarshal(n.Spec, &s); err != nil {
		return engine.Resolved{}, nil, fmt.Errorf("%s: openapi.go spec: %w", n.Label, err)
	}
	if s.Module == "" {
		return engine.Resolved{}, nil, fmt.Errorf("%s: no Go module realises it", n.Label)
	}
	owned, err := ownedDir(n, s.Dir)
	if err != nil {
		return engine.Resolved{}, nil, err
	}
	goVersion, err := golanggen.ModuleGoVersion(req, s.Module)
	if err != nil {
		return engine.Resolved{}, nil, err
	}
	file, err := specFile(req, n, s.Spec)
	if err != nil {
		return engine.Resolved{}, nil, err
	}
	drvs, srcs := map[string][]string{}, []string{}
	src, err := input(req, n, s.Spec, drvs, &srcs)
	if err != nil {
		return engine.Resolved{}, nil, err
	}
	tool, err := input(req, n, s.Tool, drvs, &srcs, "bin")
	if err != nil {
		return engine.Resolved{}, nil, err
	}
	out := drv.Placeholder("out")
	gen := []string{}
	if s.Server == oaspec.ServerStdHTTP {
		gen = append(gen, "  std-http-server: true")
	}
	if s.Models {
		gen = append(gen, "  models: true")
	}
	if s.Client {
		gen = append(gen, "  client: true")
	}
	base := strings.TrimSuffix(path.Base(file), path.Ext(file))
	cfg := fmt.Sprintf("package: %s\ngenerate:\n%s\noutput: %s/%s.gen.go\n", s.Package, strings.Join(gen, "\n"), out, base)
	steps := []any{
		step("mkdir", map[string]any{"path": out}),
		step("copy", map[string]any{"from": src, "to": buildSrc}),
		step("write", map[string]any{"path": buildSrc + "/go.mod", "content": fmt.Sprintf("module %s\n\ngo %s\n", s.Module, goVersion)}),
		step("write", map[string]any{"path": buildCfg + "/oapi-codegen.yaml", "content": cfg}),
		step("exec", map[string]any{"argv": []string{"oapi-codegen", "-config", buildCfg + "/oapi-codegen.yaml", file},
			"env": map[string]string{"HOME": buildTmp}, "cwd": buildSrc, "path": []string{tool + "/bin"}}),
	}
	return codegenJob(ctx, req, jobs, n, owned, steps, drvs, srcs)
}

// specFile is the repository path of the OpenAPI document of dependency i.
func specFile(req engine.PlanRequest, n *ir.Node, i int) (string, error) {
	if i < 0 || i >= len(n.Deps) {
		return "", fmt.Errorf("%s: spec index %d out of range", n.Label, i)
	}
	for _, d := range req.Graph.Nodes {
		if d.ID != n.Deps[i] {
			continue
		}
		var s oaspec.Spec
		if err := json.Unmarshal(d.Spec, &s); err != nil || s.File == "" {
			return "", fmt.Errorf("%s: openapi.spec without a file", d.Label)
		}
		return s.File, nil
	}
	return "", fmt.Errorf("%s: spec %s is not in the graph", n.Label, n.Deps[i])
}

// codegenJob finalises a codegen "run" job: built while planning and imported by content for
// the consumers (engine.PlanCodegen, R-GEN-005), materialised into dir.
func codegenJob(ctx context.Context, req engine.PlanRequest, jobs *engine.Jobs, n *ir.Node, dir string, steps []any,
	drvs map[string][]string, srcs []string) (engine.Resolved, *engine.Materialisation, error) {
	dp, out, err := jobs.Add(engine.Job{Name: storeName(n), Kind: "run", Label: n.Label, Drvs: drvs, Srcs: srcs,
		Args: map[string]any{"steps": steps}})
	if err != nil {
		return engine.Resolved{}, nil, fmt.Errorf("%s: %w", n.Label, err)
	}
	var d *drv.Derivation
	for _, x := range jobs.Drvs {
		if p, err := x.Path(); err == nil && p == dp {
			d = x
		}
	}
	rv, m := engine.PlanCodegen(ctx, req, engine.Codegen{Node: n, Drv: d, Path: dp, Out: out, Dir: dir})
	return rv, &m, nil
}

// buildTool is where a job puts the tool's driver next to its node_modules.
const buildTool = "/build/tool"

// tsFile is the file openapi.TS generates in its owned directory (type declarations only).
const tsFile = "index.d.ts"

// planTS plans the openapi-typescript derivation of n: the tool lock's node_modules, bob's
// driver and the document at its repository path; the output is index.d.ts.
func planTS(ctx context.Context, req engine.PlanRequest, res *engine.PlanResult, n *ir.Node) (engine.Resolved, *engine.Materialisation, error) {
	var s oaspec.TS
	if err := json.Unmarshal(n.Spec, &s); err != nil {
		return engine.Resolved{}, nil, fmt.Errorf("%s: openapi.ts spec: %w", n.Label, err)
	}
	owned, err := ownedDir(n, s.Dir)
	if err != nil {
		return engine.Resolved{}, nil, err
	}
	file, err := specFile(req, n, s.Spec)
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
	drvs, srcs := map[string][]string{tool.Drv: {"out"}}, []string{}
	src, err := input(req, n, s.Spec, drvs, &srcs)
	if err != nil {
		return engine.Resolved{}, nil, err
	}
	node, err := input(req, n, s.Node, drvs, &srcs, "bin")
	if err != nil {
		return engine.Resolved{}, nil, err
	}
	out := drv.Placeholder("out")
	job, err := json.Marshal(map[string]string{"out": out + "/" + tsFile, "spec": file})
	if err != nil {
		return engine.Resolved{}, nil, err
	}
	steps := []any{
		step("mkdir", map[string]any{"path": out}),
		step("copy", map[string]any{"from": src, "to": buildSrc}),
		step("symlink", map[string]any{"target": tool.Dir, "link": buildTool + "/node_modules"}),
		step("write", map[string]any{"path": buildTool + "/driver.mjs", "content": driverJS}),
		step("write", map[string]any{"path": buildCfg + "/job.json", "content": string(job)}),
		step("exec", map[string]any{"argv": []string{"node", buildTool + "/driver.mjs", buildCfg + "/job.json"},
			"env": map[string]string{"HOME": buildTmp}, "cwd": buildSrc, "path": []string{node + "/bin"}}),
	}
	tool.Prepare(ctx, req.Store)
	rv, m, err := codegenJob(ctx, req, jobs, n, owned, steps, drvs, srcs)
	res.Drvs = append(res.Drvs, jobs.Drvs...)
	return rv, m, err
}
