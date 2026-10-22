package gen

import (
	"context"
	"encoding/json"
	"fmt"
	"path"
	"runtime"
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
			err = internal(n, "unknown C/C++ kind %s", n.Kind)
		}
		if err != nil {
			errs = append(errs, err)
		}
	}
	if len(errs) > 0 {
		return pl.out, joinErrs(errs)
	}
	return pl.out, nil
}

// internal is an error that is bob's bug (the engine reports it as such).
func internal(n *ir.Node, format string, a ...any) error {
	return fmt.Errorf("%s: %s", n.Label, fmt.Sprintf(format, a...))
}

// invalid is BOB-E-1020 for a node: an option that is wrong for the files or deps it names.
func invalid(n *ir.Node, option, reason string) error {
	e := diag.New(1020, "label", n.Label, "option", option, "reason", reason)
	e.DefinedAt = diag.Pos{File: n.DefinedAt.File, Line: n.DefinedAt.Line}
	return e
}

func joinErrs(errs []error) error {
	if len(errs) == 1 {
		return errs[0]
	}
	return errorList(errs)
}

// errorList joins errors so that the engine still sees each diagnostic (Unwrap []error).
type errorList []error

func (l errorList) Error() string {
	s := make([]string, len(l))
	for i, e := range l {
		s[i] = e.Error()
	}
	return strings.Join(s, "\n")
}

func (l errorList) Unwrap() []error { return l }

// ---- configuration ----

// config is the projected configuration of a node.
type config struct {
	system     string // Nix system
	profile    string // release | debug
	sanitizers []string
}

func configOf(n *ir.Node) (config, error) {
	var c config
	var plat string
	if raw, ok := n.Proj["platform"]; !ok || json.Unmarshal(raw, &plat) != nil {
		return c, fmt.Errorf("%s has no platform", n.Label)
	}
	goos, goarch, _ := strings.Cut(plat, "-")
	arch := map[string]string{"amd64": "x86_64", "arm64": "aarch64"}[goarch]
	if arch == "" || goos != "linux" {
		return c, fmt.Errorf("%s: unsupported platform %s", n.Label, plat)
	}
	c.system = arch + "-" + goos
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

// hostSystem is the Nix system bob runs on: the C/C++ tools come from its package set.
func hostSystem() string {
	return map[string]string{"amd64": "x86_64-linux", "arm64": "aarch64-linux"}[runtime.GOARCH]
}

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

// sanitizeFlags are needed at compile and link time.
func (c config) sanitizeFlags() []string {
	var names []string
	for _, s := range c.sanitizers {
		switch s {
		case string(bob.ASan):
			names = append(names, "address")
		case string(bob.UBSan):
			names = append(names, "undefined")
		}
	}
	if len(names) == 0 {
		return nil
	}
	return []string{"-fsanitize=" + strings.Join(names, ","), "-fno-omit-frame-pointer"}
}

// ---- inputs and jobs ----

// inputs collects a job's input derivations and sources.
type inputs struct {
	drvs map[string]map[string]bool
	srcs map[string]bool
}

func newInputs(builder string) *inputs {
	return &inputs{drvs: map[string]map[string]bool{}, srcs: map[string]bool{builder: true}}
}

// add records output o of rv and returns its path.
func (in *inputs) add(rv engine.Resolved, o string) string {
	sp := rv.Outputs[o]
	if sp == "" {
		return ""
	}
	if rv.Drv == "" {
		in.srcs[sp] = true
		return sp
	}
	if in.drvs[rv.Drv] == nil {
		in.drvs[rv.Drv] = map[string]bool{}
	}
	in.drvs[rv.Drv][o] = true
	return sp
}

func (in *inputs) list() ([]drv.InputDrv, []string) {
	var ds []drv.InputDrv
	for p, outs := range in.drvs {
		d := drv.InputDrv{Path: p}
		for o := range outs {
			d.Outputs = append(d.Outputs, o)
		}
		sort.Strings(d.Outputs)
		ds = append(ds, d)
	}
	sort.Slice(ds, func(i, j int) bool { return ds[i].Path < ds[j].Path })
	srcs := make([]string, 0, len(in.srcs))
	for s := range in.srcs {
		srcs = append(srcs, s)
	}
	sort.Strings(srcs)
	return ds, srcs
}

// fixup is the fixup contract of a C/C++ job (R-NIX-019): strip in release, shrink RUNPATH,
// never reference the compiler, binutils or the sources.
type fixup struct {
	Strip       bool       `json:"strip"`
	DebugOut    bool       `json:"debugOut"`
	ShrinkRPath bool       `json:"shrinkRpath"`
	Forbidden   []string   `json:"forbidden"`
	Tools       fixupTools `json:"tools"`
}

type fixupTools struct {
	Strip    string `json:"strip"`
	Objcopy  string `json:"objcopy"`
	Patchelf string `json:"patchelf"`
}

// job finalises one bob-builder job and records it; it returns its path and derivation.
func (pl *planner) job(name, system, kind, label string, args any, fx *fixup, in *inputs) (string, *drv.Derivation, error) {
	var fixupSpec any = map[string]any{"strip": false, "debugOut": false, "shrinkRpath": false, "forbidden": []any{},
		"tools": map[string]any{"strip": "", "objcopy": "", "patchelf": ""}}
	var disallowed map[string][]string
	if fx != nil {
		fx.Forbidden = sortedUnique(fx.Forbidden)
		fixupSpec = fx
		disallowed = map[string][]string{"out": fx.Forbidden}
	}
	spec := map[string]any{"v": 1, "kind": kind, "label": label, "outputs": []any{"out"}, "args": args, "fixup": fixupSpec}
	ds, ss := in.list()
	j := drv.Job{Name: name, System: system, Builder: pl.req.Builder, Spec: spec, InputDrvs: ds, InputSrcs: ss,
		Disallowed: disallowed, ExtraAttribute: map[string]any{"allowSubstitutes": false}}
	d, err := j.Derivation()
	if err != nil {
		return "", nil, err
	}
	if err := d.Finalize(pl); err != nil {
		return "", nil, err
	}
	p, err := d.Path()
	if err != nil {
		return "", nil, err
	}
	if _, ok := pl.own[p]; !ok {
		pl.own[p] = d
		pl.out.Drvs = append(pl.out.Drvs, d)
	}
	return p, d, nil
}

func outPath(d *drv.Derivation) string {
	for _, o := range d.Outputs {
		if o.Name == "out" {
			return o.Path
		}
	}
	return ""
}

func pickOutput(outs map[string]string, prefer ...string) string {
	for _, o := range prefer {
		if _, ok := outs[o]; ok {
			return o
		}
	}
	names := make([]string, 0, len(outs))
	for o := range outs {
		names = append(names, o)
	}
	sort.Strings(names)
	if len(names) > 0 {
		return names[0]
	}
	return ""
}

func sortedUnique(s []string) []string {
	out := slices.Clone(s)
	slices.Sort(out)
	out = slices.Compact(out)
	if out == nil {
		out = []string{}
	}
	return out
}

// dep returns the resolved dep i of n.
func (pl *planner) dep(n *ir.Node, i int) (*ir.Node, engine.Resolved, error) {
	if i < 0 || i >= len(n.Deps) {
		return nil, engine.Resolved{}, internal(n, "dep index %d out of range", i)
	}
	rv, ok := pl.req.Inputs[n.Deps[i]]
	if !ok {
		return nil, engine.Resolved{}, internal(n, "dependency %s is not planned", n.Deps[i])
	}
	return pl.byID[n.Deps[i]], rv, nil
}

// toolset is the compiler, binutils and patchelf of a C/C++ job.
type toolset struct {
	path    []string // bin directories (compiler first)
	cc      string   // cc-wrapper output
	bin     string   // binutils output
	fixup   fixupTools
	forbids []string
}

func (pl *planner) tools(n *ir.Node, t ccspec.Tools, in *inputs) (toolset, error) {
	var ts toolset
	for i, ix := range []int{t.CC, t.Binutils, t.Patchelf} {
		_, rv, err := pl.dep(n, ix)
		if err != nil {
			return ts, err
		}
		o := pickOutput(rv.Outputs, "bin", "out")
		p := in.add(rv, o)
		switch i {
		case 0:
			ts.cc = p
			ts.path = append(ts.path, p+"/bin")
		case 1:
			ts.bin = p
			ts.path = append(ts.path, p+"/bin")
			ts.fixup.Strip, ts.fixup.Objcopy = p+"/bin/strip", p+"/bin/objcopy"
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
		return nil, internal(n, "the planner has no repository root (PlanRequest.Root)")
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
