package gen

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/megakuul/bob/pkg/bob"
	"github.com/megakuul/bob/pkg/bob/diag"
	"github.com/megakuul/bob/pkg/engine"
	"github.com/megakuul/bob/pkg/engine/drv"
	"github.com/megakuul/bob/pkg/engine/fetch"
	"github.com/megakuul/bob/pkg/engine/ir"
	"github.com/megakuul/bob/pkg/engine/lock"
	"github.com/megakuul/bob/pkg/rust/internal/cfgtab"
	"github.com/megakuul/bob/pkg/rust/internal/rsspec"
	"github.com/megakuul/bob/pkg/rust/resolve"
)

// planner plans the Rust nodes of one PlanRequest. Planning is a pure function of the request
// (R-FW-008): bob.lock carries every crate fact (R-RS-005), so no source is read.
type planner struct {
	req     engine.PlanRequest
	ws      *workspace
	members []resolve.Member
	crates  []resolve.Crate
	byKey   map[string]*resolve.Crate // name@version → record
	ids     map[string]*ir.Node

	own     map[string]*drv.Derivation
	modulo  map[string][32]byte
	fetched map[string]bool
	out     engine.PlanResult
}

func newPlanner(req engine.PlanRequest) (*planner, error) {
	ws, err := readWorkspace(req.Graph)
	if err != nil {
		return nil, err
	}
	members, err := ws.resolveMembers()
	if err != nil {
		return nil, err
	}
	pl := &planner{req: req, ws: ws, members: members, byKey: map[string]*resolve.Crate{}, ids: map[string]*ir.Node{},
		own: map[string]*drv.Derivation{}, modulo: map[string][32]byte{}, fetched: map[string]bool{},
		out: engine.PlanResult{Nodes: map[string]engine.Resolved{}}}
	for _, n := range req.Graph.Nodes {
		pl.ids[n.ID] = n
	}
	if len(req.Lock) > 0 {
		v, err := lock.DecodeSection(lock.SectionRust, req.Lock)
		if err != nil {
			return nil, err
		}
		if pl.crates, err = fromLockCrates(v.(*lock.RustSection).Crates); err != nil {
			return nil, err
		}
	}
	for i := range pl.crates {
		c := &pl.crates[i]
		pl.byKey[c.Name+"@"+c.Version] = c
	}
	return pl, nil
}

// Modulo resolves the derivations planned here first, then the engine's.
func (pl *planner) Modulo(p string) ([32]byte, error) {
	if h, ok := pl.modulo[p]; ok {
		return h, nil
	}
	if d, ok := pl.own[p]; ok {
		h, err := d.Modulo(pl)
		if err == nil {
			pl.modulo[p] = h
		}
		return h, err
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
		var err error
		switch n.Kind {
		case rsspec.KindCrate:
			err = pl.planCrate(n)
		case rsspec.KindTest:
			err = pl.planTest(n)
		case rsspec.KindTests:
			pl.out.Nodes[n.ID] = engine.Resolved{}
		default:
			err = fmt.Errorf("%s: unknown Rust kind %s", n.Label, n.Kind)
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

// invalid is BOB-E-1020 for node n.
func invalid(n *ir.Node, option, reason string) error {
	e := diag.New(1020, "label", n.Label, "option", option, "reason", reason)
	e.DefinedAt = diag.Pos{File: n.DefinedAt.File, Line: n.DefinedAt.Line}
	return e
}

// ---- toolchain and platform ----

// toolchain is what every job of one root runs with. Host units (build scripts, proc-macros
// and their dependencies, R-FW-035) use only the host side, so their derivations are the same
// for every target; target units of a cross build add the target's linker and standard library
// (R-FW-033 Rust row, R-FW-034).
type toolchain struct {
	rustc      engine.Resolved
	cc         engine.Resolved // host C compiler
	bin        engine.Resolved // host binutils
	out        string          // rustc store path
	version    string          // "1.95.0"
	system     string          // Nix system of the jobs (always the host's, R-PLAT-004)
	hostTriple string
	triple     string // target triple
	host       *resolve.Platform
	target     *resolve.Platform

	cross   *rsspec.Cross
	xcc     engine.Resolved // cross C compiler wrapper
	xbin    engine.Resolved // its binutils
	sysroot engine.Resolved // rust-std-<triple>
}

var rustcName = regexp.MustCompile(`-rustc(?:-wrapper)?-([0-9]+\.[0-9]+\.[0-9]+)[^/]*$`)

// toolchainOf returns the rustc of crate node n (its Toolchain dep).
func (pl *planner) rustcOf(n *ir.Node, s rsspec.Crate) (engine.Resolved, string, error) {
	if s.Toolchain < 0 || s.Toolchain >= len(n.Deps) {
		return engine.Resolved{}, "", fmt.Errorf("%s: no Rust toolchain", n.Label)
	}
	rv, ok := pl.req.Inputs[n.Deps[s.Toolchain]]
	if !ok {
		return engine.Resolved{}, "", fmt.Errorf("%s: its Rust toolchain is not planned", n.Label)
	}
	m := rustcName.FindStringSubmatch(rv.Outputs["out"])
	if m == nil {
		return engine.Resolved{}, "", invalid(n, "Toolchain", fmt.Sprintf("%s is not a rustc (want a store path named rustc-<version>)", path.Base(rv.Outputs["out"])))
	}
	return rv, m[1], nil
}

// depInput returns the planned dependency i of node n.
func (pl *planner) depInput(n *ir.Node, i int, what string) (engine.Resolved, error) {
	if i < 0 || i >= len(n.Deps) {
		return engine.Resolved{}, fmt.Errorf("%s: %s index out of range", n.Label, what)
	}
	r, ok := pl.req.Inputs[n.Deps[i]]
	if !ok {
		return engine.Resolved{}, fmt.Errorf("%s: its %s is not planned", n.Label, what)
	}
	return r, nil
}

// newToolchain prepares the toolchain and platform of root crate node n.
func (pl *planner) newToolchain(n *ir.Node, s rsspec.Crate) (*toolchain, error) {
	rv, version, err := pl.rustcOf(n, s)
	if err != nil {
		return nil, err
	}
	tc := &toolchain{rustc: rv, out: rv.Outputs["out"], version: version, system: engine.HostSystem()}
	if tc.cc, err = pl.depInput(n, s.CC, "C toolchain"); err != nil {
		return nil, err
	}
	if tc.bin, err = pl.depInput(n, s.Binutils, "C toolchain"); err != nil {
		return nil, err
	}
	plat, err := platformOf(n)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", n.Label, err)
	}
	host := engine.HostPlatform()
	rt := engine.RustFor(host, plat)
	tc.hostTriple, tc.triple = host.Triple(), rt.Triple
	if tc.triple == "" || tc.hostTriple == "" || tc.system == "" {
		return nil, fmt.Errorf("%s: unsupported platform %s", n.Label, plat)
	}
	if tc.host, err = resolve.NewPlatform(tc.hostTriple); err != nil {
		return nil, err
	}
	if tc.target, err = resolve.NewPlatform(tc.triple); err != nil {
		return nil, err
	}
	if !rt.Cross {
		return tc, nil
	}
	x := s.Cross
	if x == nil || x.Triple != rt.Triple {
		return nil, fmt.Errorf("%s: the crate was not evaluated for %s (no cross toolchain in its definition)", n.Label, plat)
	}
	tc.cross = x
	if tc.xcc, err = pl.depInput(n, x.CC, "cross C compiler"); err != nil {
		return nil, err
	}
	if tc.xbin, err = pl.depInput(n, x.Binutils, "cross binutils"); err != nil {
		return nil, err
	}
	std, err := pl.depInput(n, x.Std, "target standard library")
	if err != nil {
		return nil, err
	}
	if m := rustcName.FindStringSubmatch(std.Outputs["out"]); m == nil || m[1] != version {
		e := diag.New(1021, "label", n.Label, "reason", fmt.Sprintf("the standard library for %s comes from %s, but the crate compiles with rustc %s; cross-compiling needs the default rustc of the nixpkgs pin",
			plat, path.Base(std.Outputs["out"]), version))
		e.DefinedAt = diag.Pos{File: n.DefinedAt.File, Line: n.DefinedAt.Line}
		return nil, e
	}
	if tc.sysroot, err = pl.rustStd(tc, std); err != nil {
		return nil, err
	}
	return tc, nil
}

// rustStd plans rust-std-<triple> (R-FW-034): exactly the files the target rustc's
// manifest-rust-std-<triple> lists, laid out as a sysroot. One derivation per target rustc.
func (pl *planner) rustStd(tc *toolchain, std engine.Resolved) (engine.Resolved, error) {
	in := newInputs(pl.req.Builder)
	from := in.add(std, "out")
	return pl.job(drv.Name("rust-std", tc.triple), tc.system, "rust-std", "rust-std-"+tc.triple, rustStdJSON{From: from, Triple: tc.triple}, in)
}

// rustStdJSON is bop.RustStdArgs.
type rustStdJSON struct {
	From   string `json:"from"`
	Triple string `json:"triple"`
}

func platformOf(n *ir.Node) (bob.Platform, error) {
	raw, ok := n.Proj["platform"]
	if !ok {
		return "", fmt.Errorf("no platform")
	}
	var p string
	if err := json.Unmarshal(raw, &p); err != nil {
		return "", err
	}
	return bob.Platform(p), nil
}

// path is the PATH of a job: rustc, the host C compiler (rustc's linker, build scripts' cc) and
// binutils (ar); target units of a cross build put the cross tools first.
func (tc *toolchain) path(in *inputs, host bool) []string {
	p := []string{in.add(tc.rustc, "out") + "/bin"}
	if tc.cross != nil && !host {
		p = append(p, in.add(tc.xcc, pickOutput(tc.xcc.Outputs, "bin", "out"))+"/bin",
			in.add(tc.xbin, pickOutput(tc.xbin.Outputs, "bin", "out"))+"/bin")
	}
	return append(p, in.add(tc.cc, pickOutput(tc.cc.Outputs, "bin", "out"))+"/bin",
		in.add(tc.bin, pickOutput(tc.bin.Outputs, "bin", "out"))+"/bin")
}

// roleTriple is the triple a unit compiles for: the host's for host units.
func (tc *toolchain) roleTriple(host bool) string {
	if host {
		return tc.hostTriple
	}
	return tc.triple
}

// targetArgs sets --target, --sysroot and the linker of a cross target unit.
func (tc *toolchain) targetArgs(in *inputs, a *rustcArgsJSON, host bool) {
	if tc.cross == nil || host {
		return
	}
	a.Target, a.Sysroot, a.Linker = tc.triple, in.add(tc.sysroot, "out"), tc.cross.Prefix+"cc"
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

// ---- inputs and jobs ----

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

func emptyFixup() map[string]any {
	return map[string]any{"strip": false, "debugOut": false, "shrinkRpath": false, "forbidden": []any{},
		"tools": map[string]any{"strip": "", "objcopy": "", "patchelf": ""}}
}

// job finalises one bob-builder job and returns its resolved output.
func (pl *planner) job(name, system, kind, label string, args any, in *inputs) (engine.Resolved, error) {
	spec := map[string]any{"v": 1, "kind": kind, "label": label, "outputs": []any{"out"}, "args": args, "fixup": emptyFixup()}
	ds, ss := in.list()
	j := drv.Job{Name: name, System: system, Builder: pl.req.Builder, Spec: spec, InputDrvs: ds, InputSrcs: ss,
		ExtraAttribute: map[string]any{"allowSubstitutes": false}}
	d, err := j.Derivation()
	if err != nil {
		return engine.Resolved{}, err
	}
	if err := d.Finalize(pl); err != nil {
		return engine.Resolved{}, err
	}
	p, err := d.Path()
	if err != nil {
		return engine.Resolved{}, err
	}
	if _, ok := pl.own[p]; !ok {
		pl.own[p] = d
		pl.out.Drvs = append(pl.out.Drvs, d)
	}
	o := ""
	for _, x := range d.Outputs {
		if x.Name == "out" {
			o = x.Path
		}
	}
	return engine.Resolved{Drv: p, Outputs: map[string]string{"out": o}}, nil
}

// ---- units ----

// unitOut is a planned crate unit.
type unitOut struct {
	unit     *resolve.Unit
	name     string // package name
	version  string
	local    *member
	rec      *resolve.Crate
	meta     string // -C metadata
	codeName string
	typ      string // crate type
	compile  engine.Resolved
	libFile  string // file name in lib/ for --extern
	run      *engine.Resolved
	links    string
	deps     []*unitOut // normal (and dev) dependencies, by UnitDep order
	depNames []string   // their code names
	src      string     // crate directory (store path)
	sum      string     // source checksum, "local" for local crates
	features []string
}

// rootCtx is the planning context of one root node: its unification and toolchain.
type rootCtx struct {
	node  *ir.Node
	tc    *toolchain
	units map[resolve.UnitKey]*resolve.Unit
	done  map[resolve.UnitKey]*unitOut
	nodes map[string]string // crate name → node id of the local crate in this configuration
}

// localNodes maps the local crates reachable from n to their node ids in n's configuration.
func (pl *planner) localNodes(n *ir.Node) (map[string]string, error) {
	out := map[string]string{}
	var walk func(n *ir.Node) error
	walk = func(n *ir.Node) error {
		m := pl.ws.byID[n.ID]
		if m == nil {
			return fmt.Errorf("%s is not a Rust crate of the graph", n.Label)
		}
		if _, ok := out[m.spec.Name]; ok {
			return nil
		}
		out[m.spec.Name] = n.ID
		var s rsspec.Crate
		if err := json.Unmarshal(n.Spec, &s); err != nil {
			return err
		}
		for _, ds := range [][]rsspec.Dep{s.Deps, s.BuildDeps, s.DevDeps} {
			for _, d := range ds {
				if d.Local < 0 || d.Local >= len(n.Deps) {
					continue
				}
				if dn := pl.ids[n.Deps[d.Local]]; dn != nil {
					if err := walk(dn); err != nil {
						return err
					}
				}
			}
		}
		return nil
	}
	return out, walk(n)
}

// newRoot unifies the features of crate node n (dev: its tests are built).
func (pl *planner) newRoot(n *ir.Node, s rsspec.Crate, dev bool) (*rootCtx, error) {
	tc, err := pl.newToolchain(n, s)
	if err != nil {
		return nil, err
	}
	nodes, err := pl.localNodes(n)
	if err != nil {
		return nil, err
	}
	// Every local crate linked into n needs n's toolchain (R-RS-004a).
	for _, name := range sortedKeys(nodes) {
		dn := pl.ids[nodes[name]]
		if dn == nil || dn.ID == n.ID {
			continue
		}
		var ds rsspec.Crate
		if err := json.Unmarshal(dn.Spec, &ds); err != nil {
			return nil, err
		}
		rv, v, err := pl.rustcOf(dn, ds)
		if err != nil {
			return nil, err
		}
		if rv.Outputs["out"] != tc.out {
			return nil, invalid(n, "Toolchain", fmt.Sprintf("%s (rustc %s) depends on %s built with rustc %s; Rust crates linked together need the same toolchain",
				n.Label, tc.version, dn.Label, v))
		}
	}
	if len(pl.crates) == 0 && pl.hasRegistryDeps() {
		return nil, diag.New(1012, "what", "the rust section is missing; run bob lock")
	}
	root := resolve.Root{Member: s.Name, Features: append(slices.Clone(s.Enable), variantFeatures(n)...), Default: true, Dev: dev}
	units, err := resolve.Unify(pl.members, pl.crates, []resolve.Root{root}, tc.host, tc.target)
	if err != nil {
		return nil, invalid(n, "Deps", err.Error())
	}
	rc := &rootCtx{node: n, tc: tc, units: map[resolve.UnitKey]*resolve.Unit{}, done: map[resolve.UnitKey]*unitOut{}, nodes: nodes}
	for i := range units {
		rc.units[units[i].Key] = &units[i]
	}
	return rc, nil
}

// variantFeatures are the features the configuration's variant enables on crate node n
// (bob.Variant.Features, projected into the node as "features": label → features).
func variantFeatures(n *ir.Node) []string {
	raw, ok := n.Proj["features"]
	if !ok {
		return nil
	}
	var m map[string][]string
	if json.Unmarshal(raw, &m) != nil {
		return nil
	}
	return m[n.Label]
}

func (pl *planner) hasRegistryDeps() bool {
	for _, m := range pl.members {
		for _, d := range m.Deps {
			if !d.Local {
				return true
			}
		}
	}
	return false
}

// rootUnit returns the unit of local crate name in rc: the host unit of a proc-macro (it runs
// inside the compiler), the target unit of every other crate type.
func (rc *rootCtx) rootUnit(name, typ string) *resolve.Unit {
	host := typ == rsspec.TypeProcMacro
	for k, u := range rc.units {
		if k.Local && k.Host == host && strings.HasPrefix(k.Crate, name+"@") {
			return u
		}
	}
	for k, u := range rc.units {
		if k.Local && strings.HasPrefix(k.Crate, name+"@") {
			return u
		}
	}
	return nil
}

// codeName is the name Rust code uses for a crate ('-' → '_').
func codeName(s string) string { return strings.ReplaceAll(s, "-", "_") }

// metadata is -C metadata of a unit: the first 16 hex digits of sha256 over its identity.
func metadata(parts ...string) string {
	h := sha256.New()
	for _, p := range parts {
		h.Write([]byte(p))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// unitPlan plans unit k (memoised per root) with its dependencies.
func (pl *planner) unitPlan(rc *rootCtx, k resolve.UnitKey) (*unitOut, error) {
	if u, ok := rc.done[k]; ok {
		if u == nil {
			return nil, fmt.Errorf("%s: dependency cycle at %s", rc.node.Label, k)
		}
		return u, nil
	}
	rc.done[k] = nil
	un := rc.units[k]
	if un == nil {
		return nil, fmt.Errorf("%s: unit %s is not part of the unification", rc.node.Label, k)
	}
	name, version, _ := strings.Cut(k.Crate, "@")
	u := &unitOut{unit: un, name: name, version: version, features: un.Features}
	var normal, build []resolve.UnitDep
	for _, d := range un.Deps {
		if d.Kind == "build" {
			build = append(build, d)
		} else {
			normal = append(normal, d)
		}
	}
	var depMetas []string
	for _, d := range normal {
		du, err := pl.unitPlan(rc, d.To)
		if err != nil {
			return nil, err
		}
		u.deps = append(u.deps, du)
		u.depNames = append(u.depNames, codeName(d.Name))
		depMetas = append(depMetas, d.Name+"="+du.meta)
	}
	var bdeps []*unitOut
	var bnames []string
	for _, d := range build {
		du, err := pl.unitPlan(rc, d.To)
		if err != nil {
			return nil, err
		}
		bdeps = append(bdeps, du)
		bnames = append(bnames, codeName(d.Name))
		depMetas = append(depMetas, "build:"+d.Name+"="+du.meta)
	}
	sort.Strings(depMetas)
	var edition, root, script, sum string
	if k.Local {
		m := pl.ws.members[name]
		if m == nil {
			return nil, fmt.Errorf("%s: unknown local crate %s", rc.node.Label, name)
		}
		u.local = m
		u.typ = rsspec.TypeLib // as a dependency a local crate is a library
		if m.spec.Type == rsspec.TypeProcMacro {
			u.typ = rsspec.TypeProcMacro
		}
		edition, root, script, sum = m.spec.Edition, m.spec.Root, m.spec.BuildScript, "local"
		u.codeName = codeName(m.spec.Name)
		id, ok := rc.nodes[name]
		if !ok {
			return nil, fmt.Errorf("%s: local crate %s has no node in this configuration", rc.node.Label, name)
		}
		src, ok := pl.req.Sources[id]
		if !ok {
			return nil, fmt.Errorf("%s: the sources of %s are not imported", rc.node.Label, name)
		}
		u.src = src
		if m.dir != "" {
			u.src = src + "/" + m.dir
		}
	} else {
		c := pl.byKey[k.Crate]
		if c == nil {
			return nil, diag.New(1012, "what", k.Crate+" is not in bob.lock")
		}
		u.rec = c
		u.links = c.Links
		u.typ = rsspec.TypeLib
		if c.Meta.ProcMacro {
			u.typ = rsspec.TypeProcMacro
		}
		edition, root, script, sum = c.Meta.Edition, c.Meta.LibPath, c.Meta.Build, c.Checksum
		if root == "" {
			root = "src/lib.rs"
		}
		if edition == "" {
			edition = "2015"
		}
		u.codeName = codeName(name)
		if c.Meta.LibName != "" {
			u.codeName = c.Meta.LibName
		}
		src, err := pl.registrySource(c)
		if err != nil {
			return nil, err
		}
		u.src = src
	}
	role := "target"
	if k.Host {
		role = "host"
	}
	u.sum = sum
	u.meta = metadata(append([]string{name, version, sum, role, u.typ, strings.Join(un.Features, ","), rc.tc.out, rc.tc.roleTriple(k.Host)}, depMetas...)...)
	if u.typ == rsspec.TypeProcMacro {
		u.libFile = "lib" + u.codeName + "-" + u.meta + ".so"
	} else {
		u.libFile = "lib" + u.codeName + "-" + u.meta + ".rlib"
	}
	base := drv.Name(name, version)
	if k.Local {
		base = drv.Name(name)
	}
	if k.Host {
		base = drv.Name(base, "host")
	}
	if script != "" {
		if err := pl.buildScript(rc, u, base, edition, script, bdeps, bnames); err != nil {
			return nil, err
		}
	}
	a := pl.rustcArgs(u, edition, root)
	in := newInputs(pl.req.Builder)
	pl.commonInputs(rc, u, in, &a)
	cr, err := pl.job(base, rc.tc.system, "rustc", pl.label(u), a, in)
	if err != nil {
		return nil, err
	}
	u.compile = cr
	rc.done[k] = u
	return u, nil
}

// label names a unit in logs and errors.
func (pl *planner) label(u *unitOut) string {
	if u.local != nil {
		return u.local.node.Label
	}
	return "crates.io:" + u.name + "@" + u.version
}

// registrySource is the unpacked source of a registry crate: a store path bob fetches and
// imports itself (FOD-identical path, R-FW-063).
func (pl *planner) registrySource(c *resolve.Crate) (string, error) {
	hexNar, err := resolve.HexOfSRI(c.NarHash)
	if err != nil {
		return "", diag.New(1012, "what", fmt.Sprintf("%s@%s has no valid narHash in bob.lock", c.Name, c.Version))
	}
	name := sourceName(c.Name, c.Version)
	p, err := drv.FixedOutputPath("sha256", hexNar, true, name)
	if err != nil {
		return "", err
	}
	if !pl.fetched[p] {
		pl.fetched[p] = true
		hexSum, err := resolve.HexOfSRI(c.Checksum)
		if err != nil {
			return "", err
		}
		pl.out.Fetches = append(pl.out.Fetches, fetch.Request{URLs: []string{cratesIODL.url(c.Name, c.Version, hexSum)},
			Kind: fetch.Archive, Hash: c.Checksum, NarHash: c.NarHash, Name: name, Strip: 1, Label: "crates.io:" + c.Name + "@" + c.Version})
	}
	return p, nil
}

// rustcArgsJSON is bop.RustcArgs (generators write the JSON themselves, R-ARCH-003).
type rustcArgsJSON struct {
	Rustc      string            `json:"rustc"`
	Path       []string          `json:"path"`
	Src        string            `json:"src"`
	Remap      string            `json:"remap"`
	CrateName  string            `json:"crateName"`
	CrateType  string            `json:"crateType"`
	Test       bool              `json:"test"`
	Edition    string            `json:"edition"`
	Root       string            `json:"root"`
	Target     string            `json:"target"`
	Sysroot    string            `json:"sysroot,omitempty"`
	Linker     string            `json:"linker,omitempty"`
	Features   []string          `json:"features"`
	Metadata   string            `json:"metadata"`
	OptLevel   string            `json:"optLevel"`
	CapLints   bool              `json:"capLints"`
	CheckCfg   bool              `json:"checkCfg"`
	FeatureSet []string          `json:"featureSet"`
	Externs    []externJSON      `json:"externs"`
	DepDirs    []string          `json:"depDirs"`
	BuildRun   string            `json:"buildRun"`
	LinkFrom   []string          `json:"linkFrom"`
	LinkArgs   []string          `json:"linkArgs"`
	Libs       []string          `json:"libs"`
	Search     []string          `json:"search"`
	Env        map[string]string `json:"env"`
	Headers    string            `json:"headers"`
	OutName    string            `json:"outName"`
}

type externJSON struct {
	Name string `json:"name"`
	File string `json:"file"`
}

type buildRunJSON struct {
	Rustc    string            `json:"rustc"`
	Path     []string          `json:"path"`
	Src      string            `json:"src"`
	Script   string            `json:"script"`
	Env      map[string]string `json:"env"`
	DepLinks []depLinksJSON    `json:"depLinks"`
}

type depLinksJSON struct {
	Links string `json:"links"`
	Run   string `json:"run"`
}

func optLevel(host bool) string {
	if host {
		return "0" // Cargo's release build-override: build scripts, proc-macros and their deps
	}
	return "3"
}

// remap is what a unit's source directory is called in diagnostics: the repository directory
// of a local crate, <name>-<version> of a registry crate.
func remap(u *unitOut) string {
	if u.local != nil {
		if u.local.dir == "" {
			return "."
		}
		return u.local.dir
	}
	return u.name + "-" + u.version
}

// pkgEnv is CARGO_PKG_* of a unit.
func pkgEnv(u *unitOut) map[string]string {
	v, _ := resolve.ParseVersion(u.version)
	m := map[string]string{"CARGO_PKG_NAME": u.name, "CARGO_PKG_VERSION": u.version,
		"CARGO_PKG_VERSION_MAJOR": strconv.FormatUint(v.Major, 10), "CARGO_PKG_VERSION_MINOR": strconv.FormatUint(v.Minor, 10),
		"CARGO_PKG_VERSION_PATCH": strconv.FormatUint(v.Patch, 10), "CARGO_PKG_VERSION_PRE": strings.Join(v.Pre, "."),
		"CARGO_PKG_AUTHORS": "", "CARGO_PKG_DESCRIPTION": "", "CARGO_PKG_HOMEPAGE": "", "CARGO_PKG_REPOSITORY": "",
		"CARGO_PKG_LICENSE": "", "CARGO_PKG_LICENSE_FILE": "", "CARGO_PKG_RUST_VERSION": "", "CARGO_PKG_README": ""}
	if u.local != nil {
		m["CARGO_PRIMARY_PACKAGE"] = "1"
	}
	return m
}

// rustcArgs is the compile of unit u as a library (or proc-macro); roots adjust it.
func (pl *planner) rustcArgs(u *unitOut, edition, root string) rustcArgsJSON {
	a := rustcArgsJSON{Src: u.src, Remap: remap(u), CrateName: u.codeName, CrateType: u.typ, Edition: edition, Root: root,
		Features: nonNil(u.features), Metadata: u.meta, OptLevel: optLevel(u.unit.Key.Host), CapLints: u.local == nil,
		Env: pkgEnv(u), Externs: []externJSON{}, DepDirs: []string{}, LinkFrom: []string{}, LinkArgs: []string{},
		Libs: []string{}, Search: []string{}, FeatureSet: []string{}}
	if u.typ != rsspec.TypeLib && u.typ != rsspec.TypeProcMacro {
		a.CrateType = rsspec.TypeLib // dependencies are libraries; planCrate sets a root's own type
	}
	if u.local != nil {
		a.CheckCfg = true
		a.FeatureSet = sortedKeys(featureTable(u.local.spec))
	}
	return a
}

// commonInputs adds what every compile of u needs: toolchain, source, externs of its normal
// dependencies, the transitive library directories, its build-script run and, for linking
// types, the transitive runs' link search paths.
func (pl *planner) commonInputs(rc *rootCtx, u *unitOut, in *inputs, a *rustcArgsJSON) {
	host := u.unit.Key.Host
	a.Path = rc.tc.path(in, host)
	a.Rustc = rc.tc.out + "/bin/rustc"
	rc.tc.targetArgs(in, a, host)
	pl.addSource(u, in)
	for i, d := range u.deps {
		a.Externs = append(a.Externs, externJSON{Name: u.depNames[i], File: in.add(d.compile, "out") + "/lib/" + d.libFile})
	}
	a.DepDirs = pl.depDirs(u.deps, in)
	if u.run != nil {
		a.BuildRun = in.add(*u.run, "out")
	}
	for _, r := range transitiveRuns(u.deps) {
		a.LinkFrom = append(a.LinkFrom, in.add(*r, "out"))
	}
}

func (pl *planner) addSource(u *unitOut, in *inputs) {
	src := u.src
	if u.local != nil {
		// The imported tree's root (the crate directory is a subdirectory).
		if u.local.dir != "" {
			src = strings.TrimSuffix(src, "/"+u.local.dir)
		}
	}
	in.srcs[src] = true
}

// depDirs are the lib/ directories of the transitive dependencies (-L dependency=).
func (pl *planner) depDirs(deps []*unitOut, in *inputs) []string {
	seen := map[*unitOut]bool{}
	var out []string
	var walk func(u *unitOut)
	walk = func(u *unitOut) {
		if seen[u] {
			return
		}
		seen[u] = true
		out = append(out, in.add(u.compile, "out")+"/lib")
		for _, d := range u.deps {
			walk(d)
		}
	}
	for _, d := range deps {
		walk(d)
	}
	sort.Strings(out)
	return slices.Compact(out)
}

// transitiveRuns are the build-script runs of deps and everything below them.
func transitiveRuns(deps []*unitOut) []*engine.Resolved {
	seen := map[*unitOut]bool{}
	var out []*engine.Resolved
	var walk func(u *unitOut)
	walk = func(u *unitOut) {
		if seen[u] {
			return
		}
		seen[u] = true
		if u.run != nil {
			out = append(out, u.run)
		}
		for _, d := range u.deps {
			walk(d)
		}
	}
	for _, d := range deps {
		walk(d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Drv < out[j].Drv })
	return out
}

// buildScript plans the compile (host) and the run of u's build script. The compile is a host
// unit: it depends only on the host, the crate and its features, never on the target
// (R-FW-035); the run is per target.
func (pl *planner) buildScript(rc *rootCtx, u *unitOut, base, edition, script string, bdeps []*unitOut, bnames []string) error {
	in := newInputs(pl.req.Builder)
	metas := []string{u.name, u.version, u.sum, "build-script", strings.Join(u.features, ","), rc.tc.out, rc.tc.hostTriple}
	for i, d := range bdeps {
		metas = append(metas, bnames[i]+"="+d.meta)
	}
	a := rustcArgsJSON{Src: u.src, Remap: remap(u), CrateName: "build_script_build", CrateType: rsspec.TypeBin, Edition: edition,
		Root: script, Features: nonNil(u.features), Metadata: metadata(metas...), OptLevel: "0", CapLints: u.local == nil,
		Env: pkgEnv(u), Externs: []externJSON{}, LinkFrom: []string{}, LinkArgs: []string{}, Libs: []string{}, Search: []string{},
		FeatureSet: []string{}, OutName: "build_script_build"}
	a.Path = rc.tc.path(in, true)
	a.Rustc = rc.tc.out + "/bin/rustc"
	pl.addSource(u, in)
	for i, d := range bdeps {
		a.Externs = append(a.Externs, externJSON{Name: bnames[i], File: in.add(d.compile, "out") + "/lib/" + d.libFile})
	}
	a.DepDirs = pl.depDirs(bdeps, in)
	for _, r := range transitiveRuns(bdeps) {
		a.LinkFrom = append(a.LinkFrom, in.add(*r, "out"))
	}
	sc, err := pl.job(drv.Name(base, "build-script"), rc.tc.system, "rustc", pl.label(u)+" (build script)", a, in)
	if err != nil {
		return err
	}
	// The run: Cargo's build-script environment for the unit's platform.
	rin := newInputs(pl.req.Builder)
	host := u.unit.Key.Host
	triple := rc.tc.roleTriple(host)
	env := pkgEnv(u)
	for k, v := range map[string]string{"TARGET": triple, "HOST": rc.tc.hostTriple, "PROFILE": "release", "OPT_LEVEL": optLevel(host),
		"DEBUG": "false"} {
		env[k] = v
	}
	if x := rc.tc.cross; x != nil && !host {
		// What Cargo users set for cross builds: the cc and pkg-config crates find the target's
		// compiler and accept target libraries.
		for k, v := range map[string]string{"TARGET_CC": x.Prefix + "cc", "TARGET_CXX": x.Prefix + "c++", "TARGET_AR": x.Prefix + "ar",
			"HOST_CC": "cc", "HOST_CXX": "c++", "HOST_AR": "ar", "PKG_CONFIG_ALLOW_CROSS": "1"} {
			env[k] = v
		}
	}
	if u.links != "" {
		env["CARGO_MANIFEST_LINKS"] = u.links
	}
	for _, f := range u.features {
		env["CARGO_FEATURE_"+envName(f)] = "1"
	}
	cfg, _ := cfgtab.Lookup(triple)
	vals := map[string][]string{}
	for _, l := range cfg {
		k, v, ok := strings.Cut(l, "=")
		switch {
		case k == "debug_assertions":
			// release profile
		case !ok:
			env["CARGO_CFG_"+envName(k)] = ""
		default:
			vals[k] = append(vals[k], strings.Trim(v, `"`))
		}
	}
	for k, vs := range vals {
		env["CARGO_CFG_"+envName(k)] = strings.Join(vs, ",")
	}
	r := buildRunJSON{Src: u.src, Env: env, DepLinks: []depLinksJSON{}}
	r.Path = rc.tc.path(rin, host)
	r.Rustc = rc.tc.out + "/bin/rustc"
	pl.addSource(u, rin)
	if err := pl.nativeForBuildScript(rc, u, rin, &r); err != nil {
		return err
	}
	r.Script = rin.add(sc, "out") + "/bin/build_script_build"
	for _, d := range u.deps {
		if d.links != "" && d.run != nil {
			r.DepLinks = append(r.DepLinks, depLinksJSON{Links: d.links, Run: rin.add(*d.run, "out")})
		}
	}
	run, err := pl.job(drv.Name(base, "build-run"), rc.tc.system, "rust-build-run", pl.label(u)+" (build script run)", r, rin)
	if err != nil {
		return err
	}
	u.run = &run
	return nil
}

// nativeForBuildScript gives a registry crate's build script the packages rust.Uses names
// (PATH, PKG_CONFIG_PATH) and pkg-config.
func (pl *planner) nativeForBuildScript(rc *rootCtx, u *unitOut, in *inputs, r *buildRunJSON) error {
	if u.local != nil {
		return nil
	}
	var pcs []string
	for _, name := range pl.ws.names {
		m := pl.ws.members[name]
		id, ok := rc.nodes[name]
		if !ok {
			continue
		}
		n := pl.ids[id]
		for _, use := range m.spec.Uses {
			if use.Crate != u.name {
				continue
			}
			if m.spec.PkgConfig >= 0 && m.spec.PkgConfig < len(n.Deps) {
				if rv, ok := pl.req.Inputs[n.Deps[m.spec.PkgConfig]]; ok {
					r.Path = append(r.Path, in.add(rv, pickOutput(rv.Outputs, "bin", "out"))+"/bin")
				}
			}
			for _, li := range use.Libs {
				if li < 0 || li >= len(n.Deps) {
					continue
				}
				rv, ok := pl.req.Inputs[n.Deps[li]]
				if !ok {
					return fmt.Errorf("%s: native package of rust.Uses(%q) is not planned", n.Label, use.Crate)
				}
				for _, o := range sortedKeys(rv.Outputs) {
					if o != "out" && o != "dev" && o != "lib" && o != "bin" {
						continue
					}
					p := in.add(rv, o)
					r.Path = append(r.Path, p+"/bin")
					pcs = append(pcs, p+"/lib/pkgconfig", p+"/share/pkgconfig")
				}
			}
		}
	}
	if len(pcs) > 0 {
		r.Env["PKG_CONFIG_PATH"] = strings.Join(pcs, ":")
	}
	return nil
}

func envName(s string) string { return strings.ToUpper(strings.ReplaceAll(s, "-", "_")) }

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// ---- roots ----

// planCrate plans a local crate node as a root: a library, proc-macro, binary, staticlib or
// cdylib.
func (pl *planner) planCrate(n *ir.Node) error {
	var s rsspec.Crate
	if err := json.Unmarshal(n.Spec, &s); err != nil {
		return fmt.Errorf("%s: crate spec: %w", n.Label, err)
	}
	rc, err := pl.newRoot(n, s, false)
	if err != nil {
		return err
	}
	ru := rc.rootUnit(s.Name, s.Type)
	if ru == nil {
		return fmt.Errorf("%s: the crate is not part of its unification", n.Label)
	}
	switch s.Type {
	case rsspec.TypeLib, rsspec.TypeProcMacro:
		u, err := pl.unitPlan(rc, ru.Key)
		if err != nil {
			return err
		}
		pl.out.Nodes[n.ID] = u.compile
		return nil
	}
	// A linking root: its dependencies as units, then its own link.
	u, err := pl.depsOf(rc, ru)
	if err != nil {
		return err
	}
	edition, root := s.Edition, s.Root
	if s.BuildScript != "" {
		var bdeps []*unitOut
		var bnames []string
		for _, d := range ru.Deps {
			if d.Kind == "build" {
				du, err := pl.unitPlan(rc, d.To)
				if err != nil {
					return err
				}
				bdeps = append(bdeps, du)
				bnames = append(bnames, codeName(d.Name))
			}
		}
		if err := pl.buildScript(rc, u, drv.Name(s.Name), edition, s.BuildScript, bdeps, bnames); err != nil {
			return err
		}
	}
	a := pl.rustcArgs(u, edition, root)
	a.CrateType = s.Type
	in := newInputs(pl.req.Builder)
	pl.commonInputs(rc, u, in, &a)
	if err := pl.native(n, s, in, &a); err != nil {
		return err
	}
	rv := engine.Resolved{}
	switch s.Type {
	case rsspec.TypeBin:
		a.OutName = s.Name
		rv.MainProgram = "bin/" + s.Name
	case rsspec.TypeStaticLib, rsspec.TypeCDylib:
		a.Headers = s.Headers
	}
	cr, err := pl.job(drv.Name(s.Name), rc.tc.system, "rustc", n.Label, a, in)
	if err != nil {
		return err
	}
	rv.Drv, rv.Outputs = cr.Drv, cr.Outputs
	if s.Type == rsspec.TypeStaticLib || s.Type == rsspec.TypeCDylib {
		rv.LinkInfo = linkInfo(s, codeName(s.Name), cr.Outputs["out"])
	}
	pl.out.Nodes[n.ID] = rv
	return nil
}

// depsOf prepares a linking root's unit: its dependencies planned, metadata and source set.
func (pl *planner) depsOf(rc *rootCtx, ru *resolve.Unit) (*unitOut, error) {
	name, version, _ := strings.Cut(ru.Key.Crate, "@")
	m := pl.ws.members[name]
	u := &unitOut{unit: ru, name: name, version: version, local: m, typ: m.spec.Type, codeName: codeName(m.spec.Name), features: ru.Features, sum: "local"}
	var metas []string
	for _, d := range ru.Deps {
		if d.Kind == "build" {
			continue
		}
		du, err := pl.unitPlan(rc, d.To)
		if err != nil {
			return nil, err
		}
		u.deps = append(u.deps, du)
		u.depNames = append(u.depNames, codeName(d.Name))
		metas = append(metas, d.Name+"="+du.meta)
	}
	sort.Strings(metas)
	u.meta = metadata(append([]string{name, version, "local", "root", m.spec.Type, strings.Join(ru.Features, ","), rc.tc.out, rc.tc.roleTriple(ru.Key.Host)}, metas...)...)
	id := rc.nodes[name]
	src, ok := pl.req.Sources[id]
	if !ok {
		return nil, fmt.Errorf("%s: the sources of %s are not imported", rc.node.Label, name)
	}
	u.src = src
	if m.dir != "" {
		u.src = src + "/" + m.dir
	}
	return u, nil
}

// native adds the crate's Native libraries (bob C/C++ libraries, nixpkgs libraries) to a
// linking compile.
func (pl *planner) native(n *ir.Node, s rsspec.Crate, in *inputs, a *rustcArgsJSON) error {
	for _, i := range s.Native {
		if i < 0 || i >= len(n.Deps) {
			return fmt.Errorf("%s: native library index out of range", n.Label)
		}
		rv, ok := pl.req.Inputs[n.Deps[i]]
		if !ok {
			return fmt.Errorf("%s: native library %s is not planned", n.Label, n.Deps[i])
		}
		for _, o := range sortedKeys(rv.Outputs) {
			in.add(rv, o)
		}
		if li := rv.LinkInfo; li != nil {
			for _, d := range li.LinkDirs {
				a.Search = append(a.Search, "native="+d)
			}
			for _, ar := range li.Archives {
				a.Search = append(a.Search, "native="+path.Dir(ar))
				a.Libs = append(a.Libs, "static="+strings.TrimSuffix(strings.TrimPrefix(path.Base(ar), "lib"), ".a"))
			}
			a.Libs = append(a.Libs, li.Libs...)
			for _, d := range li.RuntimeDirs {
				a.LinkArgs = append(a.LinkArgs, "-Wl,-rpath,"+d)
			}
			a.LinkArgs = append(a.LinkArgs, li.LinkArgs...)
			continue
		}
		// A nixpkgs library without link info: its lib/ directory, on the search path and
		// the runtime path.
		o := pickOutput(rv.Outputs, "lib", "out")
		a.Search = append(a.Search, "native="+rv.Outputs[o]+"/lib")
		a.LinkArgs = append(a.LinkArgs, "-Wl,-rpath,"+rv.Outputs[o]+"/lib")
	}
	return nil
}

// nativeStaticLibs are the system libraries a Rust staticlib needs at the final link on
// linux-gnu (rustc --print native-static-libs).
var nativeStaticLibs = []string{"gcc_s", "util", "rt", "pthread", "m", "dl", "c"}

// linkInfo is what C and cgo consumers of a staticlib or cdylib get.
func linkInfo(s rsspec.Crate, code, out string) *bob.LinkInfo {
	li := &bob.LinkInfo{}
	if s.Headers != "" {
		li.IncludeDirs = []string{out + "/include"}
	}
	if s.Type == rsspec.TypeStaticLib {
		li.Archives = []string{out + "/lib/lib" + code + ".a"}
		li.Libs = slices.Clone(nativeStaticLibs)
		return li
	}
	li.LinkDirs = []string{out + "/lib"}
	li.Libs = []string{code}
	li.RuntimeDirs = []string{out + "/lib"}
	return li
}

// planTest plans one test unit: the test harness of the crate (unit tests) or of one
// integration test, then its test-run job.
func (pl *planner) planTest(n *ir.Node) error {
	var t rsspec.Test
	if err := json.Unmarshal(n.Spec, &t); err != nil {
		return fmt.Errorf("%s: test spec: %w", n.Label, err)
	}
	if t.Crate < 0 || t.Crate >= len(n.Deps) {
		return fmt.Errorf("%s: crate index out of range", n.Label)
	}
	cn := pl.ids[n.Deps[t.Crate]]
	if cn == nil {
		return fmt.Errorf("%s: its crate is not in the graph", n.Label)
	}
	var s rsspec.Crate
	if err := json.Unmarshal(cn.Spec, &s); err != nil {
		return fmt.Errorf("%s: crate spec: %w", cn.Label, err)
	}
	rc, err := pl.newRoot(cn, s, true)
	if err != nil {
		return err
	}
	ru := rc.rootUnit(s.Name, s.Type)
	if ru == nil {
		return fmt.Errorf("%s: the crate is not part of its unification", n.Label)
	}
	u, err := pl.depsOf(rc, ru)
	if err != nil {
		return err
	}
	// The test unit's own sources (crate + tests/).
	if src, ok := pl.req.Sources[n.ID]; ok {
		u.src = src
		if u.local.dir != "" {
			u.src = src + "/" + u.local.dir
		}
	}
	if s.BuildScript != "" {
		var bdeps []*unitOut
		var bnames []string
		for _, d := range ru.Deps {
			if d.Kind == "build" {
				du, err := pl.unitPlan(rc, d.To)
				if err != nil {
					return err
				}
				bdeps = append(bdeps, du)
				bnames = append(bnames, codeName(d.Name))
			}
		}
		if err := pl.buildScript(rc, u, drv.Name(s.Name, "test"), s.Edition, s.BuildScript, bdeps, bnames); err != nil {
			return err
		}
	}
	name := path.Base(strings.SplitN(n.Label, ":", 2)[1])
	root := s.Root
	crateName := u.codeName
	if t.File != "" {
		root = t.File
		crateName = codeName(strings.TrimSuffix(path.Base(t.File), ".rs"))
	}
	a := pl.rustcArgs(u, s.Edition, root)
	a.CrateName, a.Test, a.OutName, a.CrateType = crateName, true, name, rsspec.TypeBin
	a.Metadata = metadata(u.meta, "test", t.File)
	in := newInputs(pl.req.Builder)
	if t.File != "" && (s.Type == rsspec.TypeLib || s.Type == rsspec.TypeProcMacro) {
		// An integration test links the crate's library (built with the test features).
		lu, err := pl.unitPlan(rc, ru.Key)
		if err != nil {
			return err
		}
		u.deps = append([]*unitOut{lu}, u.deps...)
		u.depNames = append([]string{lu.codeName}, u.depNames...)
	}
	pl.commonInputs(rc, u, in, &a)
	if err := pl.native(cn, s, in, &a); err != nil {
		return err
	}
	tb, err := pl.job(drv.Name(s.Name, "test", strings.TrimSuffix(path.Base(t.File), ".rs")), rc.tc.system, "rustc", n.Label, a, in)
	if err != nil {
		return err
	}
	run, err := pl.testRun(n, t, tb, name, rc.tc.system, rc.tc.cross != nil && !ru.Key.Host)
	if err != nil {
		return err
	}
	pl.out.Nodes[n.ID] = run
	return nil
}

// defaultTestTimeoutS is the timeout of a test unit that sets none (A92: 10 minutes).
const defaultTestTimeoutS = 600

type testRunArgs struct {
	Argv     []string          `json:"argv"`
	Path     []string          `json:"path,omitempty"`
	Env      map[string]string `json:"env,omitempty"`
	Format   string            `json:"format,omitempty"`
	Services []serviceJob      `json:"services,omitempty"`
	TimeoutS int64             `json:"timeoutSeconds,omitempty"`
}

type serviceJob struct {
	Name      string            `json:"name"`
	Argv      []string          `json:"argv"`
	Path      []string          `json:"path,omitempty"`
	Env       map[string]string `json:"env,omitempty"`
	Ready     probeJob          `json:"ready"`
	TimeoutMs int64             `json:"timeoutMs"`
}

type probeJob struct {
	Port int      `json:"port,omitempty"`
	Argv []string `json:"argv,omitempty"`
}

// testRun plans the test-run job of a unit (§03.0.5): the harness runs once with its services;
// a test binary of another platform (emulate) runs under the user-mode emulator (R-FW-039).
func (pl *planner) testRun(n *ir.Node, t rsspec.Test, tb engine.Resolved, bin, system string, emulate bool) (engine.Resolved, error) {
	in := newInputs(pl.req.Builder)
	timeout := t.TimeoutS
	if timeout <= 0 {
		timeout = defaultTestTimeoutS
	}
	argv := []string{in.add(tb, "out") + "/bin/" + bin}
	if emulate {
		if t.Emulator <= 0 || t.EmulatorCmd == "" || strings.ContainsAny(t.EmulatorCmd, "/\x00") {
			return engine.Resolved{}, fmt.Errorf("%s: no emulator for a test of another platform", n.Label)
		}
		q, err := pl.depInput(n, t.Emulator, "emulator")
		if err != nil {
			return engine.Resolved{}, err
		}
		argv = append([]string{in.add(q, pickOutput(q.Outputs, "bin", "out")) + "/bin/" + t.EmulatorCmd}, argv...)
	}
	argv = append(argv, t.Args...)
	argv = append(argv, pl.req.TestArgs...)
	a := testRunArgs{Argv: argv, Env: map[string]string{"RUST_BACKTRACE": "1"}, TimeoutS: timeout}
	for _, sv := range t.Services {
		job := serviceJob{Name: sv.Name, Env: sv.Env, TimeoutMs: sv.TimeoutMs, Ready: probeJob{Port: sv.ReadyPort, Argv: sv.ReadyCmd}}
		var first string
		for _, ti := range sv.Tools {
			if ti < 0 || ti >= len(n.Deps) {
				return engine.Resolved{}, fmt.Errorf("%s: service tool out of range", n.Label)
			}
			rv, ok := pl.req.Inputs[n.Deps[ti]]
			if !ok {
				return engine.Resolved{}, fmt.Errorf("%s: service tool %s is not planned", n.Label, n.Deps[ti])
			}
			root := in.add(rv, pickOutput(rv.Outputs, "bin", "out"))
			if first == "" {
				first = root
			}
			job.Path = append(job.Path, root+"/bin")
		}
		job.Argv = slices.Clone(sv.Start)
		if len(job.Argv) > 0 && strings.Contains(job.Argv[0], "/") && !path.IsAbs(job.Argv[0]) && first != "" {
			job.Argv[0] = first + "/" + job.Argv[0]
		}
		a.Services = append(a.Services, job)
	}
	return pl.job(drv.Name(bin, "run"), system, "test-run", n.Label, a, in)
}
