package gen

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/megakuul/bob/pkg/bob"
	"github.com/megakuul/bob/pkg/bob/diag"
	"github.com/megakuul/bob/pkg/engine"
	"github.com/megakuul/bob/pkg/engine/drv"
	"github.com/megakuul/bob/pkg/engine/ir"
	"github.com/megakuul/bob/pkg/engine/lock"
	"github.com/megakuul/bob/pkg/engine/srcs"
	"github.com/megakuul/bob/pkg/golang/internal/gospec"
)

// buildTop is the build directory inside Nix's sandbox; intermediate archives of a test
// job live there, not in its output.
const buildTop = "/build"

// planner plans the Go nodes of one PlanRequest. Planning is a pure function of the request
// and the files it reads (R-FW-008): derivations shared by several nodes come out identical
// and the engine keeps one.
type planner struct {
	ctx  context.Context
	req  engine.PlanRequest
	cur  *ir.Node // the node being planned (error attribution)
	mods map[string]*wsModule
	disk map[string]string // module path → repo-relative dir of go.mod files found on disk
	lock *lock.GoSection
	// third-party modules that provide packages, by path
	third   map[string]*thirdParty
	trees   map[string]string // module path → host path of its tree
	storeOf map[string]string // host path → store path (trees and generated outputs)

	own     map[string]*drv.Derivation // drv path → derivation planned here
	byName  map[string]string          // cache key → drv path (std, tools, groups)
	srcPath map[string]string          // source tree key → imported store path
	out     engine.PlanResult
}

func newPlanner(ctx context.Context, req engine.PlanRequest) *planner {
	return &planner{ctx: ctx, req: req, trees: map[string]string{}, storeOf: map[string]string{},
		own: map[string]*drv.Derivation{}, byName: map[string]string{}, srcPath: map[string]string{},
		out: engine.PlanResult{Nodes: map[string]engine.Resolved{}}}
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
	if err := pl.workspace(); err != nil {
		return pl.out, err
	}
	if err := pl.lockModules(); err != nil {
		return pl.out, err
	}
	nodes := slices.Clone(pl.req.Nodes)
	sort.Slice(nodes, func(i, j int) bool { return nodes[i].ID < nodes[j].ID })
	var errs []error
	for _, n := range nodes {
		pl.cur = n
		var err error
		switch n.Kind {
		case gospec.KindBinary:
			err = pl.planBinary(n)
		case gospec.KindTest:
			err = pl.planTest(n)
		case gospec.KindModule, gospec.KindPackage, gospec.KindTests:
			pl.out.Nodes[n.ID] = engine.Resolved{} // declarations: nothing to build of their own
		default:
			err = internal(n, "unknown Go kind %s", n.Kind)
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

// defError is a definition error of the node being planned (BOB-E-1015, exit 3).
func (pl *planner) defError(_ string, reason string) error {
	e := diag.New(1015, "ecosystem", "Go", "root", pl.cur.Label, "reason", reason, "label", pl.cur.Label)
	e.DefinedAt = diag.Pos{File: pl.cur.DefinedAt.File, Line: pl.cur.DefinedAt.Line}
	return e
}

// invalid is BOB-E-1020 for the node being planned.
func (pl *planner) invalid(option, reason string) error {
	e := diag.New(1020, "label", pl.cur.Label, "option", option, "reason", reason)
	e.DefinedAt = diag.Pos{File: pl.cur.DefinedAt.File, Line: pl.cur.DefinedAt.Line}
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

// toolchain is a Go toolchain from nixpkgs.
type toolchain struct {
	rv      engine.Resolved
	out     string // store path
	goroot  string // <out>/share/go
	version string // "1.26.8"
	minor   int    // 26
}

var goStoreName = regexp.MustCompile(`-go-(1\.([0-9]+)(\.[0-9]+)?)$`)

// toolchainOf returns the toolchain of module m and checks it against the module's Go.
func (pl *planner) toolchainOf(m *wsModule) (*toolchain, error) {
	if m.node == nil || m.spec.Toolchain < 0 || m.spec.Toolchain >= len(m.node.Deps) {
		return nil, internal(pl.cur, "module %s has no toolchain", m.spec.Path)
	}
	rv, ok := pl.req.Inputs[m.node.Deps[m.spec.Toolchain]]
	if !ok {
		return nil, internal(pl.cur, "toolchain of %s is not planned", m.spec.Path)
	}
	out := rv.Outputs["out"]
	mm := goStoreName.FindStringSubmatch(out)
	if mm == nil {
		return nil, pl.invalid("Go toolchain", fmt.Sprintf("%s is not a Go toolchain (want a store path named go-<version>)", path.Base(out)))
	}
	minor, _ := strconv.Atoi(mm[2])
	if "1."+mm[2] != m.spec.Go {
		e := diag.New(1008, "label", m.node.Label, "want", m.spec.Go, "rev", "pinned", "have", mm[1])
		e.DefinedAt = diag.Pos{File: m.node.DefinedAt.File, Line: m.node.DefinedAt.Line}
		return nil, e
	}
	return &toolchain{rv: rv, out: out, goroot: out + "/share/go", version: mm[1], minor: minor}, nil
}

// platformOf returns GOOS/GOARCH and the Nix system of a node's platform.
func platformOf(n *ir.Node) (goos, goarch, system string, err error) {
	raw, ok := n.Proj["platform"]
	if !ok {
		return "", "", "", fmt.Errorf("%s has no platform", n.Label)
	}
	var pl string
	if err := json.Unmarshal(raw, &pl); err != nil {
		return "", "", "", err
	}
	goos, goarch, _ = strings.Cut(pl, "-")
	arch := map[string]string{"amd64": "x86_64", "arm64": "aarch64"}[goarch]
	if arch == "" {
		return "", "", "", fmt.Errorf("%s: unsupported platform %s", n.Label, pl)
	}
	return goos, goarch, arch + "-" + goos, nil
}

// target is everything a binary or test unit is built with.
type target struct {
	node   *ir.Node
	mod    *wsModule
	build  gospec.Build
	ctx    buildCtx
	system string
	cc     *engine.Resolved // C compiler (nil: cgo off)
	cgoOK  bool
}

// newTarget prepares the configuration of a binary or test node.
func (pl *planner) newTarget(n *ir.Node, b gospec.Build) (*target, error) {
	if b.Module < 0 || b.Module >= len(n.Deps) {
		return nil, internal(n, "module dep out of range")
	}
	var mod *wsModule
	for _, m := range pl.mods {
		if m.node != nil && m.node.ID == n.Deps[b.Module] {
			mod = m
		}
	}
	if mod == nil {
		return nil, internal(n, "its module node is not in the graph")
	}
	tc, err := pl.toolchainOf(mod)
	if err != nil {
		return nil, err
	}
	goos, goarch, system, err := platformOf(n)
	if err != nil {
		return nil, internal(n, "%v", err)
	}
	t := &target{node: n, mod: mod, build: b, system: system}
	if b.CC >= 0 && b.CC < len(n.Deps) {
		rv, ok := pl.req.Inputs[n.Deps[b.CC]]
		if !ok {
			return nil, internal(n, "C compiler is not planned")
		}
		t.cc = &rv
		t.cgoOK = true
	}
	tags := append(slices.Clone(mod.spec.Tags), b.Tags...)
	t.ctx = buildCtx{goos: goos, goarch: goarch, tags: sortedUnique(tags), cgo: t.cgoOK, tc: tc}
	return t, nil
}

// loadTarget loads the closure of roots for t, deciding cgo: on iff a non-std package of the
// closure imports "C" (CGoAuto), never with CGoOff.
func (pl *planner) loadTarget(t *target, roots []string, tests map[string]bool) (*closure, error) {
	cl, err := pl.load(t.ctx, roots, tests)
	if err != nil {
		return nil, err
	}
	var cgoPkgs []string
	for ip, p := range cl.pkgs {
		if p.cgo() {
			cgoPkgs = append(cgoPkgs, ip)
		}
	}
	sort.Strings(cgoPkgs)
	if t.build.CGo == gospec.CgoOff {
		// Loaded with cgo off: files importing "C" were ignored; name the first one.
		for _, ip := range sortedKeys(cl.pkgs) {
			p := cl.pkgs[ip]
			for _, f := range p.bp.IgnoredGoFiles {
				if importsC(filepath.Join(p.dir, f)) {
					return nil, pl.invalid("CGo", fmt.Sprintf("%s imports \"C\" (%s) but the package sets golang.CGoOff", ip, f))
				}
			}
		}
		return cl, nil
	}
	if len(cgoPkgs) > 0 || !t.ctx.cgo {
		return cl, nil
	}
	// No cgo in the closure: build it pure Go (static binary, std without cgo).
	t.ctx.cgo = false
	return pl.load(t.ctx, roots, tests)
}

var importCRe = regexp.MustCompile(`(?m)^\s*import\s+(\(\s*)?"C"`)

func importsC(file string) bool {
	b, err := os.ReadFile(file)
	return err == nil && importCRe.Match(b)
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
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

func (in *inputs) addDrv(d *drv.Derivation, path string) {
	if in.drvs[path] == nil {
		in.drvs[path] = map[string]bool{}
	}
	for _, o := range d.Outputs {
		in.drvs[path][o.Name] = true
	}
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
	return ds, sortedKeys(in.srcs)
}

// emptyFixup: Go kinds skip the fixup (byte-exact archives); the contract still names it.
func emptyFixup() map[string]any {
	return map[string]any{"strip": false, "debugOut": false, "shrinkRpath": false, "forbidden": []any{},
		"tools": map[string]any{"strip": "", "objcopy": "", "patchelf": ""}}
}

// job finalises one bob-builder job and records it; it returns its path and derivation.
func (pl *planner) job(name, system, kind, label string, args any, in *inputs) (string, *drv.Derivation, error) {
	spec := map[string]any{"v": 1, "kind": kind, "label": label, "outputs": []any{"out"}, "args": args, "fixup": emptyFixup()}
	ds, ss := in.list()
	j := drv.Job{Name: name, System: system, Builder: pl.req.Builder, Spec: spec, InputDrvs: ds, InputSrcs: ss,
		ExtraAttribute: map[string]any{"allowSubstitutes": false}}
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

// toolchainArgs is the Toolchain part of a job.
type toolchainArgs struct {
	GoRoot string   `json:"goroot"`
	GOOS   string   `json:"goos"`
	GOARCH string   `json:"goarch"`
	Path   []string `json:"path"`
	CC     string   `json:"cc"`
}

func (pl *planner) toolchainArgs(c buildCtx, cc *engine.Resolved, in *inputs) toolchainArgs {
	in.add(c.tc.rv, "out")
	a := toolchainArgs{GoRoot: c.tc.goroot, GOOS: c.goos, GOARCH: c.goarch, Path: []string{}}
	if c.cgo && cc != nil {
		o := pickOutput(cc.Outputs, "bin", "out")
		a.Path = append(a.Path, in.add(*cc, o)+"/bin")
		a.CC = "gcc"
	}
	return a
}

func pickOutput(outs map[string]string, prefer ...string) string {
	for _, o := range prefer {
		if _, ok := outs[o]; ok {
			return o
		}
	}
	names := sortedKeys(outs)
	if len(names) > 0 {
		return names[0]
	}
	return ""
}

// std plans the standard library for c (one derivation per toolchain, target and cgo).
func (pl *planner) std(c buildCtx, cc *engine.Resolved, system string) (string, *drv.Derivation, error) {
	in := newInputs(pl.req.Builder)
	tca := pl.toolchainArgs(c, cc, in)
	name := drv.Name("go-std", c.tc.version, c.goos, c.goarch)
	if c.cgo {
		name += "-cgo"
	}
	args := map[string]any{"goroot": tca.GoRoot, "goos": tca.GOOS, "goarch": tca.GOARCH, "path": tca.Path, "cc": tca.CC,
		"out": drv.Placeholder("out")}
	return pl.job(name, system, "go-std", "go std "+c.tc.version, args, in)
}

// tools plans cmd/pack for the toolchain (host).
func (pl *planner) tools(c buildCtx, system string) (string, *drv.Derivation, error) {
	in := newInputs(pl.req.Builder)
	in.add(c.tc.rv, "out")
	goos, goarch := hostGo()
	args := map[string]any{"goroot": c.tc.goroot, "goos": goos, "goarch": goarch, "path": []string{}, "cc": "",
		"out": drv.Placeholder("out")}
	return pl.job(drv.Name("go-tools", c.tc.version), system, "go-tools", "go tools "+c.tc.version, args, in)
}

// ---- packages and groups ----

// group is one compile derivation: a workspace package, or the used packages of one
// third-party module (modules that import each other cyclically are merged).
type group struct {
	key  string
	pkgs []*pkg // dependency order
	deps []*group
	drv  string
	d    *drv.Derivation
	out  string
}

// archive is the path of ip's archive in a group output.
func archive(out, ip string) string { return out + "/pkg/" + ip + ".a" }

// groups forms the groups of a closure and orders them dependencies first.
func (pl *planner) groups(cl *closure) ([]*group, map[string]*group, error) {
	key := func(p *pkg) string {
		if p.ref.kind == refThird {
			return "mod:" + p.ref.third.Path
		}
		return "pkg:" + p.ip
	}
	byKey := map[string]*group{}
	byPkg := map[string]*group{}
	order := cl.order()
	for _, p := range order {
		k := key(p)
		g := byKey[k]
		if g == nil {
			g = &group{key: k}
			byKey[k] = g
		}
		g.pkgs = append(g.pkgs, p)
		byPkg[p.ip] = g
	}
	// Merge cycles between module groups (Tarjan's SCC over group edges).
	edges := map[*group][]*group{}
	for _, g := range byKey {
		seen := map[*group]bool{}
		for _, p := range g.pkgs {
			for _, d := range p.imports {
				dg := byPkg[d]
				if dg != nil && dg != g && !seen[dg] {
					seen[dg] = true
					edges[g] = append(edges[g], dg)
				}
			}
		}
	}
	for _, scc := range tarjan(sortedGroups(byKey), edges) {
		if len(scc) < 2 {
			continue
		}
		sort.Slice(scc, func(i, j int) bool { return scc[i].key < scc[j].key })
		merged := &group{key: scc[0].key}
		for _, g := range scc {
			delete(byKey, g.key)
		}
		for _, p := range order {
			for _, g := range scc {
				if byPkg[p.ip] == g {
					merged.pkgs = append(merged.pkgs, p)
				}
			}
		}
		for _, p := range merged.pkgs {
			byPkg[p.ip] = merged
		}
		byKey[merged.key] = merged
	}
	// Group dependencies and topological order.
	for _, g := range byKey {
		seen := map[*group]bool{}
		for _, p := range g.pkgs {
			for _, d := range p.imports {
				dg := byPkg[d]
				if dg != nil && dg != g && !seen[dg] {
					seen[dg] = true
					g.deps = append(g.deps, dg)
				}
			}
		}
		sort.Slice(g.deps, func(i, j int) bool { return g.deps[i].key < g.deps[j].key })
	}
	var out []*group
	state := map[*group]int{}
	var visit func(g *group) error
	visit = func(g *group) error {
		switch state[g] {
		case 1:
			return fmt.Errorf("import cycle through %s", g.key)
		case 2:
			return nil
		}
		state[g] = 1
		for _, d := range g.deps {
			if err := visit(d); err != nil {
				return err
			}
		}
		state[g] = 2
		out = append(out, g)
		return nil
	}
	for _, g := range sortedGroups(byKey) {
		if err := visit(g); err != nil {
			return nil, nil, pl.defError("", err.Error())
		}
	}
	return out, byPkg, nil
}

func sortedGroups(m map[string]*group) []*group {
	out := make([]*group, 0, len(m))
	for _, k := range sortedKeys(m) {
		out = append(out, m[k])
	}
	return out
}

// tarjan returns the strongly connected components of the graph.
func tarjan(nodes []*group, edges map[*group][]*group) [][]*group {
	index, low := map[*group]int{}, map[*group]int{}
	onStack := map[*group]bool{}
	var stack []*group
	var out [][]*group
	next := 0
	var strong func(v *group)
	strong = func(v *group) {
		index[v], low[v] = next, next
		next++
		stack = append(stack, v)
		onStack[v] = true
		for _, w := range edges[v] {
			if _, seen := index[w]; !seen {
				strong(w)
				low[v] = min(low[v], low[w])
			} else if onStack[w] {
				low[v] = min(low[v], index[w])
			}
		}
		if low[v] == index[v] {
			var scc []*group
			for {
				w := stack[len(stack)-1]
				stack = stack[:len(stack)-1]
				onStack[w] = false
				scc = append(scc, w)
				if w == v {
					break
				}
			}
			out = append(out, scc)
		}
	}
	for _, v := range nodes {
		if _, seen := index[v]; !seen {
			strong(v)
		}
	}
	return out
}

// compiled is the planned archives of a closure.
type compiled struct {
	std     string // std importcfg path
	stdDrv  string
	stdD    *drv.Derivation
	pack    string // pack path ("" = not needed)
	packDrv string
	packD   *drv.Derivation
	groups  []*group
	byPkg   map[string]*group
	cgo     bool // a package of the closure uses cgo
}

// sourceTree returns the source tree of a workspace package (its own files only).
func sourceTree(p *pkg, test bool) srcs.Source {
	bp := p.bp
	var files []string
	files = append(files, bp.GoFiles...)
	files = append(files, bp.CgoFiles...)
	files = append(files, bp.CFiles...)
	files = append(files, bp.HFiles...)
	files = append(files, bp.SFiles...)
	files = append(files, bp.SysoFiles...)
	if p.embeds != nil {
		files = append(files, p.embeds.files...)
	}
	name := drv.Name("gosrc", p.ip)
	if test {
		files = append(files, bp.TestGoFiles...)
		files = append(files, bp.XTestGoFiles...)
		name = drv.Name("gosrc", p.ip, "test")
	}
	return srcs.Source{Name: name, Tree: srcs.Tree{Root: p.dir, Files: sortedUnique(files)}}
}

// importSources imports the source trees of the closure's workspace packages in one batch
// and returns ip → store path. Third-party and generated packages live in store paths
// already.
func (pl *planner) importSources(cl *closure, tests map[string]bool) (map[string]string, error) {
	out := map[string]string{}
	var trees []srcs.Source
	var want []string
	for _, ip := range sortedKeys(cl.pkgs) {
		p := cl.pkgs[ip]
		switch p.ref.kind {
		case refThird, refGen:
			sp, ok := pl.storeOf[pl.storeRoot(p)]
			if !ok {
				return nil, fmt.Errorf("%s: its store path is unknown", ip)
			}
			out[ip] = sp + strings.TrimPrefix(p.dir, pl.storeRoot(p))
			continue
		}
		tr := sourceTree(p, tests[ip])
		key := tr.Name + "\x00" + tr.Tree.Root + "\x00" + strings.Join(tr.Tree.Files, "\x00")
		if sp, ok := pl.srcPath[key]; ok {
			out[ip] = sp
			continue
		}
		trees = append(trees, tr)
		want = append(want, ip)
	}
	if len(trees) == 0 {
		return out, nil
	}
	if pl.req.Store == nil {
		return nil, fmt.Errorf("planning Go packages needs the store to import their sources (engine.PlanRequest.Store)")
	}
	paths, err := pl.req.Store.ImportSources(pl.ctx, trees)
	if err != nil {
		return nil, err
	}
	if len(paths) != len(trees) {
		return nil, fmt.Errorf("source import returned %d paths for %d trees", len(paths), len(trees))
	}
	for i, ip := range want {
		tr := trees[i]
		pl.srcPath[tr.Name+"\x00"+tr.Tree.Root+"\x00"+strings.Join(tr.Tree.Files, "\x00")] = paths[i]
		out[ip] = paths[i]
	}
	return out, nil
}

// storeRoot is the host path of the store path a third-party or generated package lives in.
func (pl *planner) storeRoot(p *pkg) string {
	if p.ref.kind == refThird {
		return p.ref.modDir
	}
	return p.dir
}

// lang is the -lang of a package: its module's go directive (major.minor).
func (pl *planner) lang(p *pkg, main *wsModule) string {
	switch p.ref.kind {
	case refThird:
		if v := p.ref.third.Go; v != "" {
			parts := strings.SplitN(v, ".", 3)
			if len(parts) >= 2 {
				return "go" + parts[0] + "." + parts[1]
			}
		}
		return "go1.16"
	case refWorkspace:
		if p.mod != nil && p.mod.spec.Go != "" {
			return "go" + p.mod.spec.Go
		}
	case refGen:
		if p.ref.ws.spec.Go != "" {
			return "go" + p.ref.ws.spec.Go
		}
	}
	return "go" + main.spec.Go
}

// trim is what a package's source directory becomes in recorded paths.
func trim(p *pkg) string {
	if p.ref.kind == refThird {
		t := p.ref.third.Path + "@" + p.ref.third.Version
		if p.ref.rel != "" {
			t += "/" + p.ref.rel
		}
		return t
	}
	return p.ip
}

// pkgJob is the bop.Package JSON of one package.
type pkgJob struct {
	ImportPath string            `json:"importPath"`
	Name       string            `json:"name"`
	Flags      []string          `json:"flags"`
	Dir        string            `json:"dir"`
	Write      map[string]string `json:"write,omitempty"`
	GoFiles    []string          `json:"goFiles"`
	CgoFiles   []string          `json:"cgoFiles,omitempty"`
	CFiles     []string          `json:"cFiles,omitempty"`
	SFiles     []string          `json:"sFiles,omitempty"`
	SysoFiles  []string          `json:"sysoFiles,omitempty"`
	CFlags     []string          `json:"cflags,omitempty"`
	LDFlags    []string          `json:"ldflags,omitempty"`
	Imports    map[string]string `json:"imports"`
	Embed      *embedJob         `json:"embed,omitempty"`
	Trim       string            `json:"trim"`
	Out        string            `json:"out"`
	Export     string            `json:"export,omitempty"`
	Header     string            `json:"header,omitempty"`
}

type embedJob struct {
	Patterns map[string][]string `json:"patterns"`
	Files    []string            `json:"files"`
}

// compileFlags are the planner's compile flags of a package: -p and -lang.
func compileFlags(p, lang string) []string { return []string{"-p", p, "-lang=" + lang} }

// cgoFlags returns the cgo CFLAGS/LDFLAGS of p: its #cgo directives, then the include and
// link flags of the C libraries its Module.Cgo declaration gives it (R-GO-001).
func (pl *planner) cgoFlags(t *target, p *pkg, in *inputs) ([]string, []string, error) {
	cflags := slices.Clone(p.bp.CgoCFLAGS)
	ldflags := slices.Clone(p.bp.CgoLDFLAGS)
	if p.mod == nil {
		return cflags, ldflags, nil
	}
	rel := strings.TrimPrefix(strings.TrimPrefix(p.ip, p.mod.spec.Path), "/")
	if rel == "" {
		rel = "."
	}
	for _, n := range pl.req.Graph.Nodes {
		if n.Kind != gospec.KindPackage {
			continue
		}
		var s gospec.Package
		if err := json.Unmarshal(n.Spec, &s); err != nil {
			return nil, nil, internal(n, "package spec: %v", err)
		}
		if s.Dir != rel || s.Module < 0 || s.Module >= len(n.Deps) || p.mod.node == nil || n.Deps[s.Module] != p.mod.node.ID {
			continue
		}
		// The Cgo declaration's own platform must match the target's.
		if _, _, sys, err := platformOf(n); err != nil || sys != t.system {
			continue
		}
		for _, li := range s.Libs {
			if li < 0 || li >= len(n.Deps) {
				return nil, nil, internal(n, "library dep out of range")
			}
			rv, ok := pl.req.Inputs[n.Deps[li]]
			if !ok {
				return nil, nil, fmt.Errorf("%s: its C library %s is not planned yet (declare Module.Cgo before the binaries and tests that use it)", n.Label, n.Deps[li])
			}
			c, l := linkFlags(rv, in)
			cflags = append(cflags, c...)
			ldflags = append(ldflags, l...)
		}
	}
	return cflags, ldflags, nil
}

// linkFlags turns a C library's link info into cgo flags; a library without link info (a
// nixpkgs library) contributes its include and lib directories.
func linkFlags(rv engine.Resolved, in *inputs) ([]string, []string) {
	var cflags, ldflags []string
	for o := range rv.Outputs {
		in.add(rv, o)
	}
	li := rv.LinkInfo
	if li == nil {
		li = &bob.LinkInfo{}
		if o := pickOutput(rv.Outputs, "dev", "out"); o != "" {
			li.IncludeDirs = []string{rv.Outputs[o] + "/include"}
		}
		if o := pickOutput(rv.Outputs, "lib", "out"); o != "" {
			li.LinkDirs = []string{rv.Outputs[o] + "/lib"}
			li.RuntimeDirs = []string{rv.Outputs[o] + "/lib"}
		}
	}
	for _, d := range li.IncludeDirs {
		cflags = append(cflags, "-I"+d)
	}
	for _, d := range li.LinkDirs {
		ldflags = append(ldflags, "-L"+d)
	}
	ldflags = append(ldflags, li.Archives...)
	for _, l := range li.Libs {
		ldflags = append(ldflags, "-l"+l)
	}
	for _, d := range li.RuntimeDirs {
		ldflags = append(ldflags, "-Wl,-rpath,"+d)
	}
	ldflags = append(ldflags, li.LinkArgs...)
	return cflags, ldflags
}

// compileClosure plans std, pack (when needed) and the group derivations of a closure.
// testPkg, when set, is left out of the groups: the test job compiles it itself.
func (pl *planner) compileClosure(t *target, cl *closure, tests map[string]bool) (*compiled, error) {
	c := cl.ctx
	cc := t.cc
	stdDrv, stdD, err := pl.std(c, cc, t.system)
	if err != nil {
		return nil, internal(t.node, "%v", err)
	}
	res := &compiled{std: outPath(stdD) + "/importcfg", stdDrv: stdDrv, stdD: stdD}
	for _, p := range cl.pkgs {
		if p.cgo() {
			res.cgo = true
		}
		if len(p.bp.SFiles)+len(p.bp.SysoFiles)+len(p.bp.CgoFiles) > 0 {
			res.pack = "need"
		}
	}
	if res.pack != "" || res.cgo {
		res.packDrv, res.packD, err = pl.tools(c, t.system)
		if err != nil {
			return nil, internal(t.node, "%v", err)
		}
		res.pack = outPath(res.packD) + "/bin/pack"
	}
	// Embeds and sources.
	for _, ip := range sortedKeys(cl.pkgs) {
		p := cl.pkgs[ip]
		pats := p.bp.EmbedPatterns
		if tests[ip] {
			pats = append(append(slices.Clone(pats), p.bp.TestEmbedPatterns...), p.bp.XTestEmbedPatterns...)
		}
		es, err := resolveEmbeds(p.dir, sortedUnique(pats))
		if err != nil {
			return nil, pl.loadError(ip, "", err)
		}
		p.embeds = es
	}
	srcPaths, err := pl.importSources(cl, tests)
	if err != nil {
		return nil, err
	}
	groups, byPkg, err := pl.groups(cl)
	if err != nil {
		return nil, err
	}
	res.groups, res.byPkg = groups, byPkg
	for _, g := range groups {
		if err := pl.groupDrv(t, cl, res, g, srcPaths, tests); err != nil {
			return nil, err
		}
	}
	return res, nil
}

// groupDrv plans one group's compile derivation. Only cgo groups depend on the C compiler.
func (pl *planner) groupDrv(t *target, cl *closure, res *compiled, g *group, srcPaths map[string]string, tests map[string]bool) error {
	kind := "go-compile"
	for _, p := range g.pkgs {
		if p.cgo() {
			kind = "go-cgo"
		}
	}
	in := newInputs(pl.req.Builder)
	var cc *engine.Resolved
	if kind == "go-cgo" {
		cc = t.cc
	}
	tca := pl.toolchainArgs(cl.ctx, cc, in)
	in.addDrv(res.stdD, res.stdDrv)
	var pkgs []pkgJob
	self := drv.Placeholder("out")
	for _, p := range g.pkgs {
		j, err := pl.pkgJob(t, cl, res, g, p, srcPaths[p.ip], self, in)
		if err != nil {
			return err
		}
		pkgs = append(pkgs, j)
	}
	pack := ""
	if res.packD != nil && (kind == "go-cgo" || hasObjects(g.pkgs)) {
		in.addDrv(res.packD, res.packDrv)
		pack = res.pack
	}
	args := map[string]any{"goroot": tca.GoRoot, "goos": tca.GOOS, "goarch": tca.GOARCH, "path": tca.Path, "cc": tca.CC,
		"std": res.std, "pack": pack, "packages": pkgs}
	p, d, err := pl.job(groupName(g), t.system, kind, t.node.Label, args, in)
	if err != nil {
		return internal(t.node, "%v", err)
	}
	g.drv, g.d, g.out = p, d, outPath(d)
	return nil
}

func hasObjects(pkgs []*pkg) bool {
	for _, p := range pkgs {
		if len(p.bp.SFiles)+len(p.bp.SysoFiles) > 0 {
			return true
		}
	}
	return false
}

// groupName is go-<importpath> for a workspace package, go-<module>-<version> for a module.
func groupName(g *group) string {
	p := g.pkgs[0]
	if p.ref.kind == refThird {
		return drv.Name("go", p.ref.third.Path, p.ref.third.Version)
	}
	return drv.Name("go", p.ip)
}

// pkgJob is the compile of one package inside a group derivation whose output is self.
func (pl *planner) pkgJob(t *target, cl *closure, res *compiled, g *group, p *pkg, src, self string, in *inputs) (pkgJob, error) {
	if src == "" {
		return pkgJob{}, internal(t.node, "%s has no source path", p.ip)
	}
	if p.ref.kind == refWorkspace {
		in.srcs[src] = true
	} else {
		pl.addStoreInput(p, in)
	}
	bp := p.bp
	pflag := p.ip
	if bp.Name == "main" {
		pflag = "main"
	}
	j := pkgJob{ImportPath: p.ip, Name: bp.Name, Flags: compileFlags(pflag, pl.lang(p, t.mod)), Dir: src,
		GoFiles: sortedUnique(bp.GoFiles), CgoFiles: bp.CgoFiles, CFiles: bp.CFiles, SFiles: bp.SFiles,
		SysoFiles: bp.SysoFiles, Imports: map[string]string{}, Trim: trim(p), Out: archive(self, p.ip)}
	if p.embeds != nil && len(bp.EmbedPatterns) > 0 {
		pats := map[string][]string{}
		files := map[string]bool{}
		for _, pat := range bp.EmbedPatterns {
			pats[pat] = p.embeds.patterns[pat]
			for _, f := range pats[pat] {
				files[f] = true
			}
		}
		j.Embed = &embedJob{Patterns: pats, Files: sortedKeys(files)}
	}
	if p.cgo() {
		cf, lf, err := pl.cgoFlags(t, p, in)
		if err != nil {
			return pkgJob{}, err
		}
		j.CFlags, j.LDFlags = cf, lf
	}
	for _, imp := range p.imports {
		dg := res.byPkg[imp]
		if dg == nil {
			return pkgJob{}, internal(t.node, "%s imports %s, which has no group", p.ip, imp)
		}
		if dg == g {
			j.Imports[imp] = archive(self, imp)
			continue
		}
		in.addDrv(dg.d, dg.drv)
		j.Imports[imp] = archive(dg.out, imp)
	}
	return j, nil
}

// addStoreInput records the store path a third-party or generated package is read from.
func (pl *planner) addStoreInput(p *pkg, in *inputs) {
	if p.ref.kind == refGen {
		g := p.ref.ws.spec.Generated[p.ref.gen]
		in.add(pl.req.Inputs[p.ref.ws.node.Deps[g.Dep]], "out")
		return
	}
	if sp, ok := pl.storeOf[pl.storeRoot(p)]; ok {
		in.srcs[sp] = true
	}
}

// ---- binaries ----

func (pl *planner) planBinary(n *ir.Node) error {
	var s gospec.Binary
	if err := json.Unmarshal(n.Spec, &s); err != nil {
		return internal(n, "binary spec: %v", err)
	}
	t, err := pl.newTarget(n, s.Build)
	if err != nil {
		return err
	}
	ip := importPath(t.mod, s.Dir)
	cl, err := pl.loadTarget(t, []string{ip}, nil)
	if err != nil {
		return err
	}
	if p := cl.pkgs[ip]; p.bp.Name != "main" {
		return pl.invalid("main package", fmt.Sprintf("%s is package %s, not a main package", ip, p.bp.Name))
	}
	res, err := pl.compileClosure(t, cl, nil)
	if err != nil {
		return err
	}
	in := newInputs(pl.req.Builder)
	tca := pl.toolchainArgs(cl.ctx, t.cc, in)
	in.addDrv(res.stdD, res.stdDrv)
	link := map[string]string{}
	for _, dep := range cl.transitive(ip) {
		g := res.byPkg[dep]
		in.addDrv(g.d, g.drv)
		link[dep] = archive(g.out, dep)
	}
	out := drv.Placeholder("out")
	l := map[string]any{"main": link[ip], "packages": link, "mode": s.Mode, "x": xFlags(s.X),
		"modinfo": pl.modinfo(cl, ip, t, s.Mode), "external": res.cgo}
	var linkInfo *bob.LinkInfo
	switch s.Mode {
	case gospec.ModeCArchive, gospec.ModeCShared:
		ext := ".a"
		if s.Mode == gospec.ModeCShared {
			ext = ".so"
		}
		l["out"] = out + "/lib/lib" + s.Name + ext
		linkInfo = &bob.LinkInfo{Libs: []string{s.Name}}
	default:
		l["out"] = out + "/bin/" + s.Name
	}
	args := map[string]any{"goroot": tca.GoRoot, "goos": tca.GOOS, "goarch": tca.GOARCH, "path": tca.Path, "cc": tca.CC,
		"std": res.std, "pack": "", "packages": []any{}, "link": l}
	p, d, err := pl.job(drv.Name("go-link", s.Name), t.system, "go-link", n.Label, args, in)
	if err != nil {
		return internal(n, "%v", err)
	}
	rv := engine.Resolved{Drv: p, Outputs: map[string]string{"out": outPath(d)}}
	if linkInfo != nil {
		linkInfo.IncludeDirs = []string{outPath(d) + "/include"}
		linkInfo.LinkDirs = []string{outPath(d) + "/lib"}
		if s.Mode == gospec.ModeCShared {
			linkInfo.RuntimeDirs = []string{outPath(d) + "/lib"}
		}
		rv.LinkInfo = linkInfo
	} else {
		rv.MainProgram = "bin/" + s.Name
	}
	pl.out.Nodes[n.ID] = rv
	return nil
}

// importPath is the import path of module-relative directory dir of m.
func importPath(m *wsModule, dir string) string {
	if dir == "." || dir == "" {
		return m.spec.Path
	}
	return m.spec.Path + "/" + dir
}

func xFlags(x map[string]string) []string {
	out := make([]string, 0, len(x))
	for _, k := range sortedKeys(x) {
		out = append(out, k+"="+x[k])
	}
	return out
}

// modinfo is the build information cmd/go embeds (so `go version -m` works on bob's binaries):
// the main package and module, the third-party modules of the closure, the build settings.
func (pl *planner) modinfo(cl *closure, mainIP string, t *target, mode string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "path\t%s\nmod\t%s\t(devel)\t\n", mainIP, t.mod.spec.Path)
	deps := map[string]*thirdParty{}
	for _, p := range cl.pkgs {
		if p.ref.kind == refThird {
			deps[p.ref.third.Path] = p.ref.third
		}
	}
	for _, mp := range sortedKeys(deps) {
		d := deps[mp]
		fmt.Fprintf(&b, "dep\t%s\t%s\t%s\n", d.Path, d.Version, d.H1)
	}
	if mode == "" {
		mode = gospec.ModeExe
	}
	cgo := 0
	if cl.ctx.cgo {
		cgo = 1
	}
	fmt.Fprintf(&b, "build\t-buildmode=%s\nbuild\t-compiler=gc\nbuild\t-trimpath=true\nbuild\tCGO_ENABLED=%d\nbuild\tGOARCH=%s\nbuild\tGOOS=%s\n",
		mode, cgo, cl.ctx.goarch, cl.ctx.goos)
	if len(cl.ctx.tags) > 0 {
		fmt.Fprintf(&b, "build\t-tags=%s\n", strings.Join(cl.ctx.tags, ","))
	}
	// cmd/go frames the information with 16 magic bytes on each side.
	return "0w\xaf\f\x92t\b\x02A\xe1\xc1\a\xe6\xd6\x18\xe6" + b.String() + "\xf92C1\x86\x18 r\x00\x82B\x10A\x16\xd8\xf2"
}

// hostGo returns the GOOS/GOARCH of the host system: tools run where bob runs.
func hostGo() (string, string) { return runtime.GOOS, runtime.GOARCH }
