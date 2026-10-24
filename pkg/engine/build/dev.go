package build

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/megakuul/bob/pkg/bob/diag"
	"github.com/megakuul/bob/pkg/engine"
	"github.com/megakuul/bob/pkg/engine/eval"
	"github.com/megakuul/bob/pkg/engine/fetch"
	"github.com/megakuul/bob/pkg/engine/ir"
	"github.com/megakuul/bob/pkg/engine/lock"
	"github.com/megakuul/bob/pkg/engine/nixidx"
	"github.com/megakuul/bob/pkg/engine/store"
	gogen "github.com/megakuul/bob/pkg/golang/gen"
)

// Developer environments (SPEC §02.7, R-DEV-001, R-DEV-002): what bob dev, bob env, the shell
// hook and bob editor need from the engine. The CLI (pkg/cli/devenv) turns a Dev result into
// .bob/env.json, shims and the namespace environment.

// DevShellKind is the IR kind of bob.DevShell.
const DevShellKind = "bob.devshell"

// DevResult is one dev shell of a repository, resolved but not realised.
type DevResult struct {
	Shell    string   // the selected shell's name
	Shells   []string // every declared shell, sorted
	Declared bool     // the shell is declared in bob.go (false: the implicit default of a repo without one)
	EvalKey  string   // "sha256:<hex>" of the evaluation (§01 eval key)
	// Packages are the top-level packages in PATH order: the shell's own Packages, then each
	// ecosystem's (sorted by ecosystem name).
	Packages  []engine.DevPackage
	Env       map[string]string // ecosystem Env, overridden by the shell's own Env
	ShellEnv  map[string]string // the shell's own Env (DevShell.Env)
	InsideEnv map[string]string // ecosystem InsideEnv
	ToolEnv   map[string]map[string]string
	Tools     map[string]string // in-tree tools: name → absolute path
	Hooks     []string
	Servers   []engine.DevServerSpec
	Ecosystem []string // names of the ecosystems the repository uses, sorted
	// Realise are derivations (in the store) the CLI realises with the packages: outputs the
	// working-tree files name (proc-macro dylibs, OUT_DIR, local libraries).
	Realise   []string
	Generated []string // native files written that stay out of git (as Result.Generated)
}

// Dev evaluates the whole repository (o.Patterns are ignored), checks bob.lock as bob build does
// (missing pins are added), resolves the nixpkgs packages and asks every ecosystem that
// implements engine.DevPreparer for its share of the shell named shell ("" = the shell named
// "default", else the only one; a repository without a DevShell gets an implicit "default").
// It also prepares the working tree: native files (R-FW-060) and the ecosystems' DevPrep.Files.
// The whole graph is planned and its derivations are written to the store (as bob build
// does), so ecosystems may name any of them; nothing is built here: the CLI realises
// DevResult.Packages and DevResult.Realise.
func Dev(ctx context.Context, o Options, shell string) (*DevResult, error) {
	if o.Root == "" {
		return nil, errors.New("build: Options.Root is empty")
	}
	o.Patterns, o.Platform, o.Variants = nil, "", nil
	st, err := openStore(ctx, o.Store, o.Home, o.Caches)
	if err != nil {
		return nil, err
	}
	r := &runner{o: o}
	f := fetch.New(st, fetch.Policy{})

	goRoot, err := r.evalGoRoot(ctx, st, f)
	if err != nil {
		return nil, err
	}
	ev, err := eval.Evaluate(ctx, evalRequest(o, goRoot))
	if err != nil {
		return nil, err
	}
	g, err := ir.Parse(ev.IR, eval.BobVersion())
	if err != nil {
		return nil, err
	}
	shells, err := devShells(g)
	if err != nil {
		return nil, err
	}
	res := &DevResult{Env: map[string]string{}, ShellEnv: map[string]string{}, InsideEnv: map[string]string{},
		ToolEnv: map[string]map[string]string{}, Tools: map[string]string{}}
	for name := range shells {
		res.Shells = append(res.Shells, name)
	}
	sort.Strings(res.Shells)
	sh, err := pickShell(shells, res.Shells, shell)
	if err != nil {
		return nil, err
	}
	res.Shell = shell
	if sh != nil {
		res.Shell, res.Declared = shellName(sh), true
	} else if res.Shell == "" {
		res.Shell = "default"
	}
	if key, err := os.ReadFile(filepath.Join(o.Root, ".bob", "eval", "key")); err == nil {
		res.EvalKey = "sha256:" + strings.TrimSpace(string(key))
	}

	lf, err := r.lockStage(ctx, st, f, g)
	if err != nil {
		return nil, err
	}
	idx, entries, err := r.indexStage(ctx, st, f, g, lf)
	if err != nil {
		return nil, err
	}
	if idx == nil && lf.Nixpkgs.Rev != "" {
		// Every dev shell has the Go share (go and gopls for the bob.go files, R-FW-001),
		// which takes its toolchain from nixpkgs even when the graph itself needs none.
		src, err := f.Ensure(ctx, lock.NixpkgsRequest(lf.Nixpkgs))
		if err != nil {
			return nil, err
		}
		if idx, err = nixidx.Open(ctx, st, nixidx.Config{Source: src, Rev: lf.Nixpkgs.Rev, System: store.HostSystem()}); err != nil {
			return nil, err
		}
	}
	// The bob.Import modules in the Go module cache (also when the evaluation was cached):
	// the root go.sum records them and the shell's go and gopls are served them (R-FW-022).
	// Best effort: offline, a module that cannot be downloaded is reported by go and gopls in
	// the shell, which must still start.
	if imports, err := gogen.BobImports(o.Root); err == nil && len(imports) > 0 {
		mods := map[string]string{}
		for _, m := range imports {
			mods[m.Path] = m.Version
		}
		_ = eval.DownloadModules(ctx, goRoot, mods)
	}
	gen, err := generate(ctx, o.Root, g, lf, idx, false, time.Now())
	if err != nil {
		return nil, err
	}
	res.Generated = gen

	// The whole graph is planned as bob build plans it, and its derivations are in the store,
	// so an ecosystem's dev share may name any of them (language servers see local libraries
	// and generated code: R-ML-001b, R-RS-003a proc-macro dylibs and OUT_DIR).
	srcPaths, err := importSources(ctx, st, o.Root, g)
	if err != nil {
		return nil, err
	}
	builder, err := st.Builder(ctx)
	if err != nil {
		return nil, err
	}
	p := newPlanner(g, st, builder, srcPaths, entries, lf, idx)
	p.root, p.fetcher, p.offline = o.Root, f, o.Offline
	if err := p.plan(ctx); err != nil {
		return nil, err
	}
	if err := ensureAll(ctx, f, p.fetches); err != nil {
		return nil, err
	}
	if _, err := st.ImportDrvs(ctx, p.drvs); err != nil {
		return nil, err
	}
	recordDrvCache(st, g, p)

	var spec devShellSpec
	if sh != nil {
		if err := json.Unmarshal(sh.Spec, &spec); err != nil {
			return nil, internalf(sh.Label, "dev shell spec: %v", err)
		}
		if spec.Packages < 0 || spec.Servers < 0 || spec.Packages+spec.Servers > len(sh.Deps) {
			return nil, internalf(sh.Label, "dev shell spec does not match its dependencies")
		}
		byID := nodesByID(g)
		for _, id := range sh.Deps[:spec.Packages] {
			n := byID[id]
			rv, ok := p.nodes[id]
			if n == nil || !ok || n.Nix == nil {
				label := id
				if n != nil {
					label = n.Label
				}
				return nil, diag.NotImplemented("dev shell packages other than nixpkgs tools (" + label + ")")
			}
			out := rv.Outputs[pickOutput(rv.Outputs, "bin", "out")]
			res.Packages = append(res.Packages, engine.DevPackage{Name: n.Nix.Attr, Version: entries[n.Nix.Attr].Version, Path: out, Drv: rv.Drv})
		}
		for k, v := range spec.Env {
			res.ShellEnv[k] = v
		}
		res.Hooks = append(res.Hooks, spec.Hooks...)
	}

	ecos, err := ecosystemsOf(g)
	if err != nil {
		return nil, err
	}
	for _, eco := range ecos {
		res.Ecosystem = append(res.Ecosystem, eco.Name())
	}
	ecos = withGoEcosystem(ecos)
	var servers map[string]bool
	if sh != nil {
		servers = map[string]bool{}
		for _, id := range sh.Deps[spec.Packages : spec.Packages+spec.Servers] {
			servers[id] = true
		}
	}
	for _, eco := range ecos {
		dp, ok := eco.(engine.DevPreparer)
		if !ok {
			continue
		}
		var nodes []*ir.Node
		var srv []string
		for _, n := range g.Nodes {
			if name, _, _ := strings.Cut(n.Kind, "."); name == eco.Name() {
				nodes = append(nodes, n)
				if servers[n.ID] {
					srv = append(srv, n.ID)
				}
			}
		}
		prep, err := prepareDev(ctx, dp, eco.Name(), engine.DevRequest{
			PlanRequest: engine.PlanRequest{Graph: g, Nodes: nodes, Lock: lf.Sections[eco.Name()], Inputs: p.nodes,
				Sources: srcPaths, Nixpkgs: idx, Builder: builder, Resolver: p.res, Root: o.Root,
				Store: planStore{st: st, f: f}},
			Shell: res.Shell, Servers: srv})
		if err != nil {
			return nil, err
		}
		if len(prep.Drvs) > 0 {
			if _, err := st.ImportDrvs(ctx, prep.Drvs); err != nil {
				return nil, err
			}
		}
		if err := res.add(o.Root, prep); err != nil {
			return nil, err
		}
	}
	if proxy, ok := strings.CutPrefix(res.InsideEnv["GOPROXY"], "file://"); ok && proxy != "" {
		// The shell's go and gopls resolve the framework and the dependency bob repositories the
		// bob.go files import from there.
		if err := eval.InstallFrameworkProxy(proxy); err != nil {
			return nil, err
		}
		if err := gogen.ServeImports(o.Root, proxy); err != nil {
			return nil, err
		}
	}
	for k, v := range res.ShellEnv {
		res.Env[k] = v
	}
	return res, nil
}

// withGoEcosystem adds the Go ecosystem to the ecosystems of a dev shell: the bob.go files are
// Go, so every repository's shell gets go and gopls (R-FW-001, R-FW-022, §02.7.2 "The Go
// package always gets gopls"), also one without Go nodes of its own. ecos stays sorted by name.
func withGoEcosystem(ecos []engine.Ecosystem) []engine.Ecosystem {
	name := gogen.Ecosystem{}.Name()
	for _, e := range ecos {
		if e.Name() == name {
			return ecos
		}
	}
	e, ok := engine.Lookup(name)
	if !ok {
		return ecos
	}
	out := append(slices.Clone(ecos), e)
	sort.SliceStable(out, func(i, j int) bool { return out[i].Name() < out[j].Name() })
	return out
}

// prepareDev calls one ecosystem's PrepareDev; a panic is a bob bug, not a crash.
func prepareDev(ctx context.Context, dp engine.DevPreparer, name string, req engine.DevRequest) (prep engine.DevPrep, err error) {
	defer func() {
		if v := recover(); v != nil {
			err = internalf("//...", "ecosystem %s panicked preparing the dev shell: %v", name, v)
		}
	}()
	prep, err = dp.PrepareDev(ctx, req)
	if err != nil {
		if _, ok := diagErrors(err); ok {
			return prep, err
		}
		return prep, internalf("//...", "%s dev shell: %v", name, err)
	}
	return prep, nil
}

// add merges one ecosystem's share and writes its working-tree files.
func (res *DevResult) add(root string, prep engine.DevPrep) error {
	res.Packages = append(res.Packages, prep.Packages...)
	for k, v := range prep.Env {
		res.Env[k] = v
	}
	for k, v := range prep.InsideEnv {
		res.InsideEnv[k] = v
	}
	for tool, env := range prep.ToolEnv {
		if res.ToolEnv[tool] == nil {
			res.ToolEnv[tool] = map[string]string{}
		}
		for k, v := range env {
			res.ToolEnv[tool][k] = v
		}
	}
	for name, rel := range prep.Tools {
		if !nativePathOK(rel) {
			return internalf("//...", "in-tree tool %s has an invalid path %q", name, rel)
		}
		if _, ok := res.Tools[name]; !ok {
			res.Tools[name] = filepath.Join(root, filepath.FromSlash(rel))
		}
	}
	for _, f := range prep.Files {
		if !nativePathOK(f.Path) && !strings.HasPrefix(f.Path, ".bob/") {
			return internalf(f.Owner, "dev file has an invalid path %q", f.Path)
		}
		if err := writeNative(root, f); err != nil {
			return err
		}
	}
	res.Servers = append(res.Servers, prep.Servers...)
	res.Realise = append(res.Realise, prep.Realise...)
	return nil
}

// devShellSpec is the IR spec of kind bob.devshell (pkg/bob shellSpec): deps are the Packages
// followed by the Servers.
type devShellSpec struct {
	Packages int               `json:"packages"`
	Servers  int               `json:"servers"`
	Env      map[string]string `json:"env"`
	Hooks    []string          `json:"hooks"`
}

// shellName is the name of a dev shell node: its label's name.
func shellName(n *ir.Node) string {
	_, name, _ := strings.Cut(n.Label, ":")
	return name
}

// devShells returns the dev shells of g by name (one node per label: the host configuration).
// Two shells with one name are BOB-E-1020 (SPEC §02.7.0).
func devShells(g *ir.Graph) (map[string]*ir.Node, error) {
	out := map[string]*ir.Node{}
	var errs []error
	for _, n := range g.Nodes {
		if n.Kind != DevShellKind {
			continue
		}
		name := shellName(n)
		if prev, ok := out[name]; ok {
			if prev.Label != n.Label {
				e := diag.New(1020, "label", n.Label, "option", "Name",
					"reason", fmt.Sprintf("the dev shell name %q is also used by %s", name, prev.Label))
				e.DefinedAt = diag.Pos{File: n.DefinedAt.File, Line: n.DefinedAt.Line}
				errs = append(errs, e)
			}
			continue
		}
		out[name] = n
	}
	return out, errors.Join(errs...)
}

// pickShell selects the shell (SPEC §02.7.7 step 1): want, else "default", else the only one.
// nil without error: the repository declares no shell and none was asked for.
func pickShell(shells map[string]*ir.Node, names []string, want string) (*ir.Node, error) {
	switch {
	case want != "":
		if n, ok := shells[want]; ok {
			return n, nil
		}
		if want == "default" && len(shells) == 0 {
			return nil, nil
		}
	case shells["default"] != nil:
		return shells["default"], nil
	case len(shells) == 1:
		return shells[names[0]], nil
	case len(shells) == 0:
		return nil, nil
	}
	have := strings.Join(names, ", ")
	if have == "" {
		have = "none"
	}
	if want == "" {
		want = "default"
	}
	return nil, diag.New(1014, "name", want, "names", have)
}

func nodesByID(g *ir.Graph) map[string]*ir.Node {
	m := make(map[string]*ir.Node, len(g.Nodes))
	for _, n := range g.Nodes {
		m[n.ID] = n
	}
	return m
}

// HostPath is where the host sees store path p (outside bob's namespace).
func HostPath(st *store.Store, p string) string { return hostPath(st, p) }
