package gen

import (
	"context"
	"encoding/json"
	"fmt"
	"path"
	"slices"
	"sort"
	"strings"

	"github.com/megakuul/bob/pkg/bob"
	"github.com/megakuul/bob/pkg/bob/diag"
	"github.com/megakuul/bob/pkg/cc/internal/ccspec"
	"github.com/megakuul/bob/pkg/engine"
	"github.com/megakuul/bob/pkg/engine/drv"
	"github.com/megakuul/bob/pkg/engine/ir"
	"github.com/megakuul/bob/pkg/engine/srcs"
)

// buildTop is the build directory inside Nix's sandbox; objects live there, not in the output.
const buildTop = "/build"

// planner plans the C/C++ nodes of one PlanRequest. Planning is a pure function of the request
// and the source files it lists (R-FW-008).
type planner struct {
	ctx   context.Context
	req   engine.PlanRequest
	byID  map[string]*ir.Node
	cur   *ir.Node
	own   map[string]*drv.Derivation // drv path → derivation planned here
	out   engine.PlanResult
	files func(n *ir.Node) ([]string, error) // the repo-relative source files of a node
}

func newPlanner(ctx context.Context, req engine.PlanRequest) *planner {
	pl := &planner{ctx: ctx, req: req, byID: map[string]*ir.Node{}, own: map[string]*drv.Derivation{},
		out: engine.PlanResult{Nodes: map[string]engine.Resolved{}}}
	if req.Graph != nil {
		for _, n := range req.Graph.Nodes {
			pl.byID[n.ID] = n
		}
	}
	pl.files = pl.sourceFiles
	return pl
}

// Modulo resolves the derivations planned here first, then the engine's.
func (pl *planner) Modulo(p string) ([32]byte, error) {
	if d, ok := pl.own[p]; ok {
		return d.Modulo(pl)
	}
	if pl.req.Resolver == nil {
		return [32]byte{}, fmt.Errorf("no resolver for input derivation %s", p)
	}
	return pl.req.Resolver.Modulo(p)
}

func (pl *planner) plan() (engine.PlanResult, error) {
	nodes := slices.Clone(pl.req.Nodes)
	sort.Slice(nodes, func(i, j int) bool { return nodes[i].ID < nodes[j].ID })
	var errs []error
	for _, n := range nodes {
		pl.cur = n
		var err error
		switch n.Kind {
		case ccspec.KindLibrary, ccspec.KindBinary, ccspec.KindTest:
			err = pl.planTarget(n)
		case ccspec.KindExternal:
			err = pl.planExternal(n)
		default:
			err = engine.Internal(n, "unknown C/C++ kind %s", n.Kind)
		}
		if err != nil {
			errs = append(errs, err)
		}
	}
	if len(errs) > 0 {
		return pl.out, engine.JoinErrors(errs)
	}
	return pl.out, nil
}

// ---- configuration ----

// config is the projected configuration of a node.
type config struct {
	system     string        // Nix system of the derivations: always the host's (R-PLAT-004)
	tc         bob.Toolchain // the node's C/C++ toolchain (cross for another platform, R-FW-033)
	profile    string        // release | debug
	sanitizers []string
}

func configOf(n *ir.Node) (config, error) {
	var c config
	tc, err := engine.CToolchain(n)
	if err != nil {
		return c, err
	}
	if _, ok := n.Proj["platform"]; !ok {
		return c, fmt.Errorf("%s has no platform", n.Label)
	}
	c.system, c.tc = hostSystem(), tc
	c.profile = string(bob.Release)
	if raw, ok := n.Proj["profile"]; ok {
		var p string
		if json.Unmarshal(raw, &p) == nil && p != "" {
			c.profile = p
		}
	}
	if raw, ok := n.Proj["sanitizers"]; ok {
		_ = json.Unmarshal(raw, &c.sanitizers)
	}
	return c, nil
}

// name is a derivation name of the node: base, then the platform when cross and the
// projection tag (dbg, address, undefined), R-FW-038.
func (c config) name(base ...string) string {
	parts := append([]string{}, base...)
	if c.tc.Cross() {
		parts = append(parts, string(c.tc.Target))
	}
	if c.profile == string(bob.Debug) {
		parts = append(parts, "dbg")
	}
	parts = append(parts, c.sanitizeNames()...)
	return drv.Name(parts...)
}

// cmd is a command of the toolchain ("gcc" → "x86_64-unknown-linux-gnu-gcc" when cross).
func (c config) cmd(name string) string { return c.tc.Prefix + name }

// profileFlags are the profile's and the sanitisers' compile flags (§04 table), in order.
func (c config) profileFlags() []string {
	var out []string
	if c.profile == string(bob.Debug) {
		out = append(out, "-O0", "-g")
	} else {
		out = append(out, "-O2")
	}
	return append(out, c.sanitizeFlags()...)
}

// sanitizeNames are the -fsanitize names of the configuration, in canonical order.
func (c config) sanitizeNames() []string {
	var names []string
	for _, s := range c.sanitizers {
		switch s {
		case string(bob.ASan):
			names = append(names, "address")
		case string(bob.UBSan):
			names = append(names, "undefined")
		}
	}
	return names
}

// sanitizeFlags are needed at compile and link time.
func (c config) sanitizeFlags() []string {
	names := c.sanitizeNames()
	if len(names) == 0 {
		return nil
	}
	return []string{"-fsanitize=" + strings.Join(names, ","), "-fno-omit-frame-pointer"}
}

// ---- inputs and jobs ----

// job finalises one bob-builder job and records it; it returns its path and derivation.
func (pl *planner) job(name, system, kind, label string, args any, fx *engine.Fixup, in *engine.Inputs) (string, *drv.Derivation, error) {
	p, d, err := engine.BuilderJob{Name: name, System: system, Builder: pl.req.Builder, Kind: kind, Label: label,
		Args: args, Fixup: fx, Inputs: in}.Finalize(pl)
	if err != nil {
		return "", nil, err
	}
	if _, ok := pl.own[p]; !ok {
		pl.own[p] = d
		pl.out.Drvs = append(pl.out.Drvs, d)
	}
	return p, d, nil
}

// dep returns the resolved dep i of n.
func (pl *planner) dep(n *ir.Node, i int) (*ir.Node, engine.Resolved, error) {
	if i < 0 || i >= len(n.Deps) {
		return nil, engine.Resolved{}, engine.Internal(n, "dep index %d out of range", i)
	}
	rv, ok := pl.req.Inputs[n.Deps[i]]
	if !ok {
		return nil, engine.Resolved{}, engine.Internal(n, "dependency %s is not planned", n.Deps[i])
	}
	return pl.byID[n.Deps[i]], rv, nil
}

// toolset is the compiler, binutils and patchelf of a C/C++ job.
type toolset struct {
	path    []string // bin directories (compiler first)
	cc      string   // cc-wrapper output
	bin     string   // binutils output
	fixup   engine.FixupTools
	forbids []string
	ccLib   string // the cross compiler's lib output (sanitised cross builds), "" otherwise
}

func (pl *planner) tools(n *ir.Node, t ccspec.Tools, c config, in *engine.Inputs) (toolset, error) {
	var ts toolset
	if t.CCLib > 0 {
		_, rv, err := pl.dep(n, t.CCLib)
		if err != nil {
			return ts, err
		}
		ts.ccLib = in.Add(rv, engine.PickOutput(rv.Outputs, "lib", "out"))
	}
	for i, ix := range []int{t.CC, t.Binutils, t.Patchelf} {
		_, rv, err := pl.dep(n, ix)
		if err != nil {
			return ts, err
		}
		o := engine.PickOutput(rv.Outputs, "bin", "out")
		p := in.Add(rv, o)
		switch i {
		case 0:
			ts.cc = p
			ts.path = append(ts.path, p+"/bin")
		case 1:
			ts.bin = p
			ts.path = append(ts.path, p+"/bin")
			ts.fixup.Strip, ts.fixup.Objcopy = p+"/bin/"+c.cmd("strip"), p+"/bin/"+c.cmd("objcopy")
		case 2:
			ts.fixup.Patchelf = p + "/bin/patchelf"
		}
		ts.forbids = append(ts.forbids, p)
	}
	return ts, nil
}

// sourceFiles lists the files of n's source set exactly as bob imported them (R-FW-021).
func (pl *planner) sourceFiles(n *ir.Node) ([]string, error) {
	if pl.req.Root == "" {
		return nil, engine.Internal(n, "the planner has no repository root (PlanRequest.Root)")
	}
	if !strings.HasPrefix(n.Label, "//") {
		return nil, diag.NotImplemented("C/C++ packages of dependency repositories (" + n.Label + ")")
	}
	return srcs.ResolveAll(pl.req.Root, n.Srcs, n.DefinedAt.String(), nil)
}

// labelDir is the repository-relative directory of a label ("//libs/foo:foo" → "libs/foo").
func labelDir(label string) string {
	d, _, _ := strings.Cut(strings.TrimPrefix(label, "//"), ":")
	return d
}

// joinDir joins a label directory and a relative path ("" directory = repo root).
func joinDir(dir, rel string) string {
	if dir == "" {
		return rel
	}
	if rel == "" || rel == "." {
		return dir
	}
	return path.Join(dir, rel)
}

// hostSystem is the Nix system bob runs on: the system of every C/C++ derivation.
func hostSystem() string { return engine.HostSystem() }
