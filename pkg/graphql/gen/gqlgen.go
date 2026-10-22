package gen

import (
	"context"
	"encoding/json"
	"fmt"
	"path"
	"regexp"
	"sort"
	"strings"
	"sync"

	"github.com/megakuul/bob/pkg/engine"
	"github.com/megakuul/bob/pkg/engine/drv"
	"github.com/megakuul/bob/pkg/engine/ir"
	golanggen "github.com/megakuul/bob/pkg/golang/gen"
	"github.com/megakuul/bob/pkg/graphql/internal/gqlspec"
)

// planGqlgen plans the gqlgen derivation of n (§03.6.2). gqlgen loads Go packages (the
// gqlgen runtime, autobind packages) with the go command, so the job is a module root at
// buildSrc: a go.mod with the consuming module's path and Go version, vendor/ with the
// module's third-party build list (the go command runs offline with -mod=vendor), the schema
// at its repository paths, bob's gqlgen.yml (skip_mod_tidy, skip_validation), and the owned
// directory written at its module-relative path, so gqlgen computes the import paths the
// module will use.
func planGqlgen(ctx context.Context, req engine.PlanRequest, jobs *engine.Jobs, n *ir.Node) (engine.Resolved, *engine.Materialisation, error) {
	var s gqlspec.Gqlgen
	if err := json.Unmarshal(n.Spec, &s); err != nil {
		return engine.Resolved{}, nil, fmt.Errorf("%s: graphql.gqlgen spec: %w", n.Label, err)
	}
	c, err := consumerOf(req.Graph, n)
	if err != nil {
		return engine.Resolved{}, nil, err
	}
	if c.Kind != "go.module" {
		return engine.Resolved{}, nil, fmt.Errorf("%s: gqlgen output is realised by %s, not a Go module", n.Label, c.Label)
	}
	rel, owned := c.Dir, c.Owned
	gm, err := golanggen.GeneratorModuleOf(ctx, req, c.Path, c.Go)
	if err != nil {
		return engine.Resolved{}, nil, err
	}
	d := newDeps()
	schemaSrc, err := d.input(req, n, s.Schema)
	if err != nil {
		return engine.Resolved{}, nil, err
	}
	schema, err := sourceFiles(req, schemaSrc)
	if err != nil {
		return engine.Resolved{}, nil, fmt.Errorf("%s: schema: %w", n.Label, err)
	}
	if len(schema) == 0 {
		return engine.Resolved{}, nil, fmt.Errorf("%s: the schema has no files", n.Label)
	}
	gqlgen, err := d.input(req, n, s.Tool, "bin")
	if err != nil {
		return engine.Resolved{}, nil, err
	}
	goTool, err := d.input(req, n, s.Go, "bin")
	if err != nil {
		return engine.Resolved{}, nil, err
	}
	out := drv.Placeholder("out")
	steps := []any{
		step("mkdir", map[string]any{"path": out}),
		step("copy", map[string]any{"from": schemaSrc, "to": buildSrc}),
	}
	steps = append(steps, vendorSteps(gm, d)...)
	steps = append(steps,
		step("write", map[string]any{"path": buildSrc + "/go.mod", "content": goMod(gm)}),
		step("write", map[string]any{"path": buildSrc + "/vendor/modules.txt", "content": modulesTxt(gm)}),
		step("write", map[string]any{"path": buildCfg + "/gqlgen.yml", "content": gqlgenYML(rel, schema, s)}),
		step("exec", map[string]any{"argv": []string{"gqlgen", "generate", "--config", buildCfg + "/gqlgen.yml"},
			"env": goEnv(), "cwd": buildSrc, "path": []string{gqlgen + "/bin", goTool + "/bin"}}),
		step("copy", map[string]any{"from": buildSrc + "/" + rel, "to": out}),
	)
	return codegenJob(ctx, req, jobs, n, owned, steps, d)
}

// goEnv is the go command's environment inside the job: offline, vendored, no cgo (gqlgen only
// loads packages), caches in the build directory.
func goEnv() map[string]string {
	return map[string]string{"HOME": buildTmp, "GOPATH": buildTmp + "/go", "GOCACHE": buildTmp + "/go-cache",
		"GOFLAGS": "-mod=vendor", "GOPROXY": "off", "GOSUMDB": "off", "GOTOOLCHAIN": "local", "GOTELEMETRY": "off",
		"CGO_ENABLED": "0", "GOENV": "off", "GOWORK": "off"}
}

// vendorSteps lay out the build list as vendor/<module path>: one symlink per module tree; a
// module that contains another vendored module (golang.org/x/tools and a nested module) is
// copied instead, so the nested module's link can live inside it.
func vendorSteps(gm golanggen.GeneratorModule, d *deps) []any {
	mods := append([]golanggen.VendorModule(nil), gm.Modules...)
	sort.Slice(mods, func(i, j int) bool { return mods[i].Path < mods[j].Path })
	var steps []any
	for i, m := range mods {
		d.srcs = append(d.srcs, m.Tree)
		to := buildSrc + "/vendor/" + m.Path
		outer := false
		for _, o := range mods[i+1:] {
			outer = outer || strings.HasPrefix(o.Path, m.Path+"/")
		}
		if outer {
			steps = append(steps, step("copy", map[string]any{"from": m.Tree, "to": to}))
		} else {
			steps = append(steps, step("symlink", map[string]any{"target": m.Tree, "link": to}))
		}
	}
	return steps
}

// goMod requires every vendored module (all explicit, so vendor/modules.txt is consistent).
func goMod(gm golanggen.GeneratorModule) string {
	var b strings.Builder
	fmt.Fprintf(&b, "module %s\n\ngo %s\n", gm.Path, gm.Go)
	if len(gm.Modules) > 0 {
		b.WriteString("\nrequire (\n")
		for _, m := range gm.Modules {
			fmt.Fprintf(&b, "\t%s %s\n", m.Path, m.Version)
		}
		b.WriteString(")\n")
	}
	return b.String()
}

// modulesTxt is vendor/modules.txt of the build list.
func modulesTxt(gm golanggen.GeneratorModule) string {
	var b strings.Builder
	for _, m := range gm.Modules {
		fmt.Fprintf(&b, "# %s %s\n## explicit\n", m.Path, m.Version)
		for _, p := range m.Packages {
			b.WriteString(p + "\n")
		}
	}
	return b.String()
}

var goIdent = sync.OnceValue(func() *regexp.Regexp { return regexp.MustCompile(`[^a-z0-9_]`) })

// packageName is the Go package name of a generated directory ("gen/graph" → "graph").
func packageName(dir string) string {
	name := goIdent().ReplaceAllString(strings.ToLower(path.Base(dir)), "")
	if name == "" || name[0] >= '0' && name[0] <= '9' {
		name = "generated"
	}
	return name
}

// gqlgenYML is bob's gqlgen configuration (users never write it): JSON is valid YAML, so the
// values need no YAML quoting.
func gqlgenYML(dir string, schema []string, s gqlspec.Gqlgen) string {
	cfg := map[string]any{
		"schema":          schema,
		"exec":            map[string]string{"filename": dir + "/generated.go", "package": packageName(dir)},
		"model":           map[string]string{"filename": dir + "/model/models_gen.go", "package": "model"},
		"skip_validation": true,
		"skip_mod_tidy":   true,
	}
	if len(s.Autobind) > 0 {
		cfg["autobind"] = s.Autobind
	}
	if len(s.Models) > 0 {
		models := map[string]any{}
		for k, v := range s.Models {
			models[k] = map[string]any{"model": []string{v}}
		}
		cfg["models"] = models
	}
	b, _ := json.MarshalIndent(cfg, "", "  ") // maps of strings and bools: cannot fail
	return string(b) + "\n"
}
