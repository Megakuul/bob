// Package gen implements engine.Ecosystem "connect" (SPEC §03.6, R-GEN-001a, R-GEN-005):
// one codegen derivation per realised connect.Go / connect.TS recipe (buf generate with the
// protoc plugins of the nixpkgs pin, offline), built while planning so consumers depend on its
// content (early cutoff), and materialised into the consumer's owned directory.
package gen

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/megakuul/bob/pkg/connect/internal/connspec"
	"github.com/megakuul/bob/pkg/engine"
	"github.com/megakuul/bob/pkg/engine/drv"
	"github.com/megakuul/bob/pkg/engine/ir"
	protogen "github.com/megakuul/bob/pkg/proto/gen"
)

func init() {
	engine.Register(Ecosystem{})
	engine.RegisterCodegenKind(connspec.KindGo)
	engine.RegisterCodegenKind(connspec.KindTS)
}

// Ecosystem plans the connect code generators: engine.Ecosystem and engine.Materialiser.
type Ecosystem struct{}

// Name is the kind prefix "connect".
func (Ecosystem) Name() string { return "connect" }

// Lock has nothing to resolve: the runtimes are the consumers' requirements (§03.6.4).
func (Ecosystem) Lock(context.Context, engine.LockRequest) (json.RawMessage, error) { return nil, nil }

// Native writes no files: generated code is materialised, not a native tooling file.
func (Ecosystem) Native(context.Context, engine.PlanRequest) ([]engine.GeneratedFile, error) {
	return nil, nil
}

// Materialises reports whether g has connect recipes.
func (Ecosystem) Materialises(g *ir.Graph) bool {
	for _, n := range g.Nodes {
		if n.Kind == connspec.KindGo || n.Kind == connspec.KindTS {
			return true
		}
	}
	return false
}

const (
	buildSrc  = "/build/src"  // the .proto files at their repository paths (messages name repo files)
	buildHome = "/build/home" // HOME and buf's cache
)

// Plan plans one codegen derivation per node and builds it while planning (early cutoff).
func (Ecosystem) Plan(ctx context.Context, req engine.PlanRequest) (res engine.PlanResult, err error) {
	defer func() { // a planner bug must not crash bob
		if v := recover(); v != nil {
			err = fmt.Errorf("connect planner: internal error: %v", v)
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
	var gens []engine.Codegen
	for _, n := range req.Nodes {
		c, err := plan(req, jobs, byID, n)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		gens = append(gens, c)
	}
	if len(errs) > 0 {
		return engine.PlanResult{}, errors.Join(errs...)
	}
	res.Drvs = jobs.Drvs
	rvs, ms := engine.PlanCodegens(ctx, req, gens)
	for i, c := range gens {
		res.Nodes[c.Node.ID] = rvs[i]
	}
	res.Materialise = append(res.Materialise, ms...)
	return res, nil
}

// step is one step of a bob-builder "run" job (pkg/builder/bspec.Step).
func step(op string, args map[string]any) map[string]any {
	return map[string]any{"op": op, "args": args}
}

func plan(req engine.PlanRequest, jobs *engine.Jobs, byID map[string]*ir.Node, n *ir.Node) (engine.Codegen, error) {
	var s connspec.Gen
	if err := json.Unmarshal(n.Spec, &s); err != nil {
		return engine.Codegen{}, fmt.Errorf("%s: connect spec: %w", n.Label, err)
	}
	if len(n.Deps) < 2 {
		return engine.Codegen{}, fmt.Errorf("%s: a connect recipe has a library and its tools", n.Label)
	}
	owned, err := engine.OwnedDir(engine.LabelDir(n.Label), s.Dir)
	if err != nil {
		return engine.Codegen{}, fmt.Errorf("%s: %w", n.Label, err)
	}
	libs, err := protogen.Closure(byID, n.Deps[0])
	if err != nil {
		return engine.Codegen{}, fmt.Errorf("%s: %w", n.Label, err)
	}
	lib := libs[len(libs)-1]
	in := map[string][]string{}
	var srcPaths []string
	add := func(rv engine.Resolved, o string) string {
		if rv.Drv == "" {
			srcPaths = append(srcPaths, rv.Outputs[o])
		} else {
			in[rv.Drv] = append(in[rv.Drv], o)
		}
		return rv.Outputs[o]
	}
	out := drv.Placeholder("out")
	steps := []any{step("mkdir", map[string]any{"path": out}), step("mkdir", map[string]any{"path": buildSrc}),
		step("mkdir", map[string]any{"path": buildHome})}
	hostSrc := map[string]string{} // library id → host path of its sources
	for _, l := range libs {
		rv, ok := req.Inputs[l.Node.ID]
		if !ok || rv.Outputs["out"] == "" {
			return engine.Codegen{}, fmt.Errorf("%s: library %s is not planned", n.Label, l.Node.Label)
		}
		sp := add(rv, "out")
		steps = append(steps, step("copy", map[string]any{"from": sp, "to": buildSrc}))
		if req.Store != nil {
			hostSrc[l.Node.ID] = req.Store.HostPath(sp)
		}
	}
	var bins []string
	for _, id := range n.Deps[1:] {
		rv, ok := req.Inputs[id]
		if !ok || rv.Outputs["out"] == "" {
			return engine.Codegen{}, fmt.Errorf("%s: tool %s is not planned", n.Label, id)
		}
		o := "out"
		if rv.Outputs["bin"] != "" {
			o = "bin"
		}
		bins = append(bins, add(rv, o)+"/bin")
	}
	var plugins []any
	switch n.Kind {
	case connspec.KindGo:
		opts, err := goOptions(n, s, libs, hostSrc)
		if err != nil {
			return engine.Codegen{}, err
		}
		connectOpts := opts
		if s.Simple {
			connectOpts = append(append([]string{}, opts...), "simple")
		}
		plugins = []any{
			map[string]any{"local": "protoc-gen-go", "out": "gen", "opt": opts},
			map[string]any{"local": "protoc-gen-connect-go", "out": "gen", "opt": connectOpts},
		}
	case connspec.KindTS:
		plugins = []any{map[string]any{"local": "protoc-gen-es", "out": "gen", "opt": []string{"target=ts", "import_extension=js"}}}
	default:
		return engine.Codegen{}, fmt.Errorf("%s: unknown connect kind %s", n.Label, n.Kind)
	}
	tmpl, err := json.Marshal(map[string]any{"version": "v2", "plugins": plugins})
	if err != nil {
		return engine.Codegen{}, err
	}
	libDir := lib.Spec.Dir
	if libDir == "" {
		libDir = "."
	}
	steps = append(steps,
		step("exec", map[string]any{"argv": []string{"buf", "generate", "--config", protogen.BufConfig(lib.Spec.Root),
			"--template", string(tmpl), "--path", libDir},
			"env": map[string]string{"HOME": buildHome, "BUF_CACHE_DIR": buildHome + "/buf"}, "cwd": buildSrc, "path": bins}),
		step("mkdir", map[string]any{"path": buildSrc + "/gen/" + relToRoot(lib.Spec)}),
		step("copy", map[string]any{"from": buildSrc + "/gen/" + relToRoot(lib.Spec), "to": out}))
	slug := strings.NewReplacer("//", "", "/", "-", ":", "-").Replace(n.Label)
	dp, o, err := jobs.Add(engine.Job{Name: drv.Name("gen", slug), Kind: "run", Label: n.Label,
		Args: map[string]any{"steps": steps}, Drvs: in, Srcs: srcPaths})
	if err != nil {
		return engine.Codegen{}, err
	}
	var d *drv.Derivation
	for _, x := range jobs.Drvs {
		if p, _ := x.Path(); p == dp {
			d = x
		}
	}
	return engine.Codegen{Node: n, Drv: d, Path: dp, Out: o, Dir: owned}, nil
}

// relToRoot is a library's directory relative to its import root (where buf writes its files
// below the plugin's out directory with paths=source_relative).
func relToRoot(l protogen.LibSpec) string {
	if l.Root == "" {
		return l.Dir
	}
	return strings.TrimPrefix(strings.TrimPrefix(l.Dir, l.Root), "/")
}

// goOptions are the protoc-gen-go options: source-relative paths and one M option per .proto
// file of the closure, so bob (not go_package) decides each library's Go import path: the
// module path plus the directory of its Go package in the module (connspec.Gen.Imports).
func goOptions(n *ir.Node, s connspec.Gen, libs []protogen.Lib, hostSrc map[string]string) ([]string, error) {
	if s.Module == "" {
		return nil, fmt.Errorf("%s: no Go module realises it", n.Label)
	}
	opts := []string{"paths=source_relative"}
	for _, l := range libs {
		dir, ok := s.Imports[l.Spec.Dir]
		if !ok {
			dir = "gen/proto/" + relToRoot(l.Spec)
		}
		ip := path.Join(s.Module, dir)
		files, err := protoFiles(hostSrc[l.Node.ID], l.Spec.Dir)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", l.Node.Label, err)
		}
		for _, f := range files {
			opts = append(opts, fmt.Sprintf("M%s=%s;%s", path.Join(relToRoot(l.Spec), f), ip, goPackage(relToRoot(l.Spec))))
		}
	}
	return opts, nil
}

// protoFiles lists the .proto files of dir (repo-relative) in an imported source tree.
func protoFiles(host, dir string) ([]string, error) {
	if host == "" {
		return nil, fmt.Errorf("its sources cannot be read while planning")
	}
	ents, err := os.ReadDir(filepath.Join(host, filepath.FromSlash(dir)))
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range ents {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".proto") {
			out = append(out, e.Name())
		}
	}
	sort.Strings(out)
	return out, nil
}

// goPackage is the Go package name of a proto directory: its last two elements joined
// ("greet/v1" → greetv1, buf's convention), letters and digits only.
func goPackage(rel string) string {
	parts := strings.Split(rel, "/")
	if len(parts) > 2 {
		parts = parts[len(parts)-2:]
	}
	var b strings.Builder
	for _, r := range strings.ToLower(strings.Join(parts, "")) {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '_' {
			b.WriteRune(r)
		}
	}
	name := b.String()
	if name == "" || name[0] >= '0' && name[0] <= '9' {
		name = "pb" + name
	}
	return name
}
