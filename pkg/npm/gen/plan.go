package gen

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/megakuul/bob/pkg/bob/diag"
	"github.com/megakuul/bob/pkg/engine"
	"github.com/megakuul/bob/pkg/engine/drv"
	"github.com/megakuul/bob/pkg/engine/fetch"
	"github.com/megakuul/bob/pkg/engine/ir"
	"github.com/megakuul/bob/pkg/engine/lock"
	"github.com/megakuul/bob/pkg/npm/internal/npmspec"
	"github.com/megakuul/bob/pkg/npm/resolve"
)

// buildTop is the build directory inside Nix's sandbox.
const buildTop = "/build"

// Plan turns the npm nodes into derivations (SPEC §03.4.3): one derivation per package
// instance (dependency cycles share one), one node_modules per importer (or one hoisted
// tree), and the tsc/Vite/esbuild/Bun build or test of each node.
func (Ecosystem) Plan(ctx context.Context, req engine.PlanRequest) (res engine.PlanResult, err error) {
	defer func() { // a planner bug must not crash bob
		if v := recover(); v != nil {
			err = fmt.Errorf("npm planner: internal error: %v", v)
		}
	}()
	if req.Graph == nil {
		return engine.PlanResult{}, fmt.Errorf("npm planner: no graph")
	}
	pl, err := newPlanner(ctx, req)
	if err != nil {
		return engine.PlanResult{}, err
	}
	return pl.plan()
}

// planner plans the npm nodes of one PlanRequest; planning is a pure function of the request.
type planner struct {
	ctx  context.Context
	req  engine.PlanRequest
	sec  *lock.NpmSection
	ims  []*importer
	byID map[string]*importer

	nodes   map[string]*ir.Node
	done    map[string]engine.Resolved // node id → planned
	own     map[string]*drv.Derivation
	modulo  map[string][32]byte
	inst    map[string]instRef // platform|allow|key → package instance
	nm      map[string]nmRef   // node_modules per importer, platform and layout
	fetched map[string]bool
	plats   map[string]*platInfo
	out     engine.PlanResult
}

// instRef is a planned package instance: the derivation that contains it and the package's
// directory (<out>/node_modules/<name>, <out>/<i>/node_modules/<name> in a cycle group).
type instRef struct {
	drv string
	d   *drv.Derivation
	dir string
}

// nmRef is a planned node_modules directory.
type nmRef struct {
	drv string
	d   *drv.Derivation
	out string
}

func newPlanner(ctx context.Context, req engine.PlanRequest) (*planner, error) {
	pl := &planner{ctx: ctx, req: req, nodes: map[string]*ir.Node{}, done: map[string]engine.Resolved{},
		own: map[string]*drv.Derivation{}, modulo: map[string][32]byte{}, inst: map[string]instRef{},
		nm: map[string]nmRef{}, fetched: map[string]bool{}, plats: map[string]*platInfo{},
		out: engine.PlanResult{Nodes: map[string]engine.Resolved{}}}
	for _, n := range req.Graph.Nodes {
		pl.nodes[n.ID] = n
	}
	var err error
	if pl.ims, pl.byID, err = importers(req.Graph); err != nil {
		return nil, err
	}
	if pl.sec, err = decodeSection(req.Lock); err != nil {
		return nil, diag.New(1012, "what", "the npm section of bob.lock cannot be read: "+err.Error())
	}
	if pl.sec == nil {
		return nil, diag.New(1012, "what", "npm is not locked")
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
		if _, err := pl.planNode(n); err != nil {
			errs = append(errs, err)
		}
	}
	if len(errs) > 0 {
		return pl.out, joinErrs(errs)
	}
	return pl.out, nil
}

func (pl *planner) planNode(n *ir.Node) (engine.Resolved, error) {
	if rv, ok := pl.done[n.ID]; ok {
		return rv, nil
	}
	var rv engine.Resolved
	var err error
	switch n.Kind {
	case npmspec.KindLib, npmspec.KindApp, npmspec.KindSite:
		im, ok := pl.byID[n.ID]
		if !ok {
			return rv, internal(n, "importer of a dependency repository: not supported")
		}
		switch n.Kind {
		case npmspec.KindLib:
			rv, err = pl.planLib(n, im)
		case npmspec.KindApp:
			rv, err = pl.planApp(n, im)
		default:
			rv, err = pl.planSite(n, im)
		}
	case npmspec.KindTest:
		rv, err = pl.planTest(n)
	default:
		err = internal(n, "unknown npm kind %s", n.Kind)
	}
	if err != nil {
		return rv, err
	}
	pl.done[n.ID] = rv
	pl.out.Nodes[n.ID] = rv
	return rv, nil
}

// ---- errors ----

func internal(n *ir.Node, format string, a ...any) error {
	return fmt.Errorf("%s: %s", n.Label, fmt.Sprintf(format, a...))
}

// invalid is BOB-E-1020 for node n.
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

// ---- platforms ----

// platInfo is a target platform as npm names it, and its Nix system.
type platInfo struct {
	os, cpu, libc, system string
	sccs                  map[string]*scc // instance key → its cycle group (members > 1 or self-cycle)
}

func (pl *planner) platformOf(n *ir.Node) (*platInfo, error) {
	raw, ok := n.Proj["platform"]
	if !ok {
		return nil, internal(n, "no platform")
	}
	var p string
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, internal(n, "platform: %v", err)
	}
	if pi, ok := pl.plats[p]; ok {
		return pi, nil
	}
	goos, goarch, _ := strings.Cut(p, "-")
	cpu := map[string]string{"amd64": "x64", "arm64": "arm64"}[goarch]
	arch := map[string]string{"amd64": "x86_64", "arm64": "aarch64"}[goarch]
	if cpu == "" || goos != "linux" {
		return nil, invalid(n, "platform", fmt.Sprintf("npm packages cannot be built for %s", p))
	}
	pi := &platInfo{os: goos, cpu: cpu, libc: "glibc", system: arch + "-" + goos}
	pi.sccs = pl.cycles(pi)
	pl.plats[p] = pi
	return pi, nil
}

// matches applies npm's os/cpu/libc lists ("!x" excludes) to the platform.
func matches(list []string, v string) bool {
	if len(list) == 0 {
		return true
	}
	allowed, anyPositive := false, false
	for _, w := range list {
		if neg, ok := strings.CutPrefix(w, "!"); ok {
			if neg == v {
				return false
			}
			continue
		}
		anyPositive = true
		allowed = allowed || w == v
	}
	return allowed || !anyPositive
}

func (pi *platInfo) installable(p lock.NpmPackage) bool {
	return matches(p.OS, pi.os) && matches(p.CPU, pi.cpu) && matches(p.Libc, pi.libc)
}

// edges returns the dependency edges of key on the platform (alias → instance key), without
// optional packages that do not run there; a required one that does not is an error.
func (pl *planner) edges(pi *platInfo, key string) (map[string]string, error) {
	p, ok := pl.sec.Packages[key]
	if !ok {
		return nil, fmt.Errorf("bob.lock has no npm package %s", key)
	}
	out := map[string]string{}
	for alias, k := range p.Deps {
		dp, ok := pl.sec.Packages[k]
		if !ok {
			return nil, fmt.Errorf("bob.lock: %s depends on %s, which it does not list", key, k)
		}
		if !pi.installable(dp) {
			if dp.Optional {
				continue
			}
			return nil, fmt.Errorf("%s@%s does not support %s-%s (os %v, cpu %v, libc %v)", dp.Name, dp.Version, pi.os, pi.cpu, dp.OS, dp.CPU, dp.Libc)
		}
		out[alias] = k
	}
	return out, nil
}

// scc is a dependency cycle of package instances: one derivation, member i at <out>/<i>.
type scc struct {
	members []string // sorted instance keys
	index   map[string]int
}

// cycles finds the strongly connected components of the instance graph on the platform
// (Tarjan); only components with a cycle are returned.
func (pl *planner) cycles(pi *platInfo) map[string]*scc {
	keys := make([]string, 0, len(pl.sec.Packages))
	for k := range pl.sec.Packages {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	index, low := map[string]int{}, map[string]int{}
	onStack := map[string]bool{}
	var stack []string
	next := 0
	out := map[string]*scc{}
	var visit func(string)
	visit = func(v string) {
		index[v], low[v] = next, next
		next++
		stack = append(stack, v)
		onStack[v] = true
		es, _ := pl.edges(pi, v)
		targets := make([]string, 0, len(es))
		for _, k := range es {
			targets = append(targets, k)
		}
		sort.Strings(targets)
		self := false
		for _, w := range targets {
			if w == v {
				self = true
			}
			if _, seen := index[w]; !seen {
				visit(w)
				low[v] = min(low[v], low[w])
			} else if onStack[w] {
				low[v] = min(low[v], index[w])
			}
		}
		if low[v] != index[v] {
			return
		}
		var comp []string
		for {
			w := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			onStack[w] = false
			comp = append(comp, w)
			if w == v {
				break
			}
		}
		if len(comp) == 1 && !self {
			return
		}
		sort.Strings(comp)
		c := &scc{members: comp, index: map[string]int{}}
		for i, m := range comp {
			c.index[m] = i
			out[m] = c
		}
	}
	for _, k := range keys {
		if _, seen := index[k]; !seen {
			visit(k)
		}
	}
	return out
}

// ---- inputs and jobs ----

// inputs collects a job's input derivations and sources.
type inputs struct {
	drvs map[string]map[string]bool
	srcs map[string]bool
}

func (pl *planner) newInputs() *inputs {
	return &inputs{drvs: map[string]map[string]bool{}, srcs: map[string]bool{pl.req.Builder: true}}
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

func (in *inputs) addDrv(p string) {
	if in.drvs[p] == nil {
		in.drvs[p] = map[string]bool{}
	}
	in.drvs[p]["out"] = true
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

// noFixup: npm kinds skip the binary fixup; the contract still names it.
func noFixup() map[string]any {
	return map[string]any{"strip": false, "debugOut": false, "shrinkRpath": false, "forbidden": []any{},
		"tools": map[string]any{"strip": "", "objcopy": "", "patchelf": ""}}
}

// job finalises one bob-builder job and records it; it returns its path and derivation.
func (pl *planner) job(name, system, kind, label string, args any, in *inputs) (string, *drv.Derivation, error) {
	spec := map[string]any{"v": 1, "kind": kind, "label": label, "outputs": []any{"out"}, "args": args, "fixup": noFixup()}
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

// tool returns output o (or the first of prefer that exists) of the node's dep i.
func (pl *planner) tool(n *ir.Node, i int, in *inputs, prefer ...string) (string, error) {
	if i < 0 || i >= len(n.Deps) {
		return "", internal(n, "tool index %d out of range", i)
	}
	rv, ok := pl.req.Inputs[n.Deps[i]]
	if !ok {
		return "", internal(n, "tool %s is not planned", n.Deps[i])
	}
	for _, o := range append(prefer, "out") {
		if _, ok := rv.Outputs[o]; ok {
			return in.add(rv, o), nil
		}
	}
	return "", internal(n, "tool %s has no output %v", n.Deps[i], prefer)
}

// ---- tarballs and package instances ----

// tarball plans the download of key's tarball and returns its store path.
func (pl *planner) tarball(p lock.NpmPackage) (string, error) {
	url := p.Tarball
	if url == "" {
		reg := pl.sec.Registry
		if reg == "" {
			reg = resolve.DefaultRegistry
		}
		base := p.Name
		if i := strings.LastIndex(base, "/"); i >= 0 {
			base = base[i+1:]
		}
		url = strings.TrimSuffix(reg, "/") + "/" + p.Name + "/-/" + base + "-" + p.Version + ".tgz"
	}
	algo, b64, _ := strings.Cut(p.Integrity, "-")
	if algo != "sha512" && algo != "sha256" {
		return "", fmt.Errorf("%s@%s has only a %s integrity, which bob cannot verify downloads with", p.Name, p.Version, algo)
	}
	sum, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return "", fmt.Errorf("%s@%s: integrity %q: %v", p.Name, p.Version, p.Integrity, err)
	}
	name := drv.Name("npm", p.Name, p.Version) + ".tgz"
	sp, err := drv.FixedOutputPath(algo, hex.EncodeToString(sum), false, name)
	if err != nil {
		return "", err
	}
	if !pl.fetched[sp] {
		pl.fetched[sp] = true
		pl.out.Fetches = append(pl.out.Fetches, fetch.Request{URLs: []string{url}, Kind: fetch.File, Hash: p.Integrity,
			Name: name, Label: p.Name + "@" + p.Version})
	}
	return sp, nil
}

// scripts is how one importer runs allowed lifecycle scripts.
type scripts struct {
	allow map[string]bool // package names (AllowScripts)
	key   string          // the part of the allowlist that matters for this lock (memo key)
	node  string          // nodejs root
	bash  string          // bash binary
	path  []string        // tool bin dirs
	in    *inputs         // the tools' inputs, added to each script derivation
}

// scriptsOf collects the importer's allowlist and script tools.
func (pl *planner) scriptsOf(n *ir.Node, im *importer) (*scripts, error) {
	s := &scripts{allow: map[string]bool{}, in: pl.newInputs()}
	var relevant []string
	for _, a := range im.spec.Options.AllowScripts {
		s.allow[a] = true
	}
	for _, p := range pl.sec.Packages {
		if p.HasInstallScript && s.allow[p.Name] && !quirkSkips(p.Name, p.Version) && !slices.Contains(relevant, p.Name) {
			relevant = append(relevant, p.Name)
		}
	}
	sort.Strings(relevant)
	s.key = strings.Join(relevant, ",")
	if len(relevant) == 0 {
		return s, nil
	}
	var err error
	if s.node, err = pl.tool(n, im.spec.Tools.Node, s.in); err != nil {
		return nil, err
	}
	bash, err := pl.tool(n, im.spec.Tools.Bash, s.in)
	if err != nil {
		return nil, err
	}
	s.bash = bash + "/bin/bash"
	for _, i := range im.spec.Tools.Scripts {
		d, err := pl.tool(n, i, s.in, "bin")
		if err != nil {
			return nil, err
		}
		s.path = append(s.path, d+"/bin")
	}
	return s, nil
}

// runs reports whether the instance key runs its install scripts for this importer.
func (s *scripts) runs(p lock.NpmPackage) bool {
	return p.HasInstallScript && s.allow[p.Name] && !quirkSkips(p.Name, p.Version)
}

// instance plans the derivation holding package instance key on the platform.
func (pl *planner) instance(pi *platInfo, s *scripts, key string) (instRef, error) {
	memo := pi.system + "|" + s.key + "|" + key
	if r, ok := pl.inst[memo]; ok {
		return r, nil
	}
	p, ok := pl.sec.Packages[key]
	if !ok {
		return instRef{}, fmt.Errorf("bob.lock has no npm package %s", key)
	}
	members := []string{key}
	group := pi.sccs[key]
	if group != nil {
		members = group.members
	}
	in := pl.newInputs()
	self := drv.Placeholder("out")
	var ms []member
	for i, m := range members {
		mp := pl.sec.Packages[m]
		tb, err := pl.tarball(mp)
		if err != nil {
			return instRef{}, err
		}
		in.srcs[tb] = true
		dir := self
		if group != nil {
			dir = fmt.Sprintf("%s/%d", self, i)
		}
		es, err := pl.edges(pi, m)
		if err != nil {
			return instRef{}, err
		}
		mem := member{Dir: dir, Name: mp.Name, Tarball: tb, Links: []link{}}
		var bins []bin
		for _, alias := range sortedKeys(es) {
			target := es[alias]
			var tdir string
			if j, ok := group.indexOf(target); ok {
				tdir = fmt.Sprintf("%s/%d/node_modules/%s", self, j, pl.sec.Packages[target].Name)
			} else if target == m {
				tdir = dir + "/node_modules/" + mp.Name // depends on itself
			} else {
				dr, err := pl.instance(pi, s, target)
				if err != nil {
					return instRef{}, err
				}
				in.addDrv(dr.drv)
				tdir = dr.dir
			}
			mem.Links = append(mem.Links, link{Alias: alias, Target: tdir})
			for _, cmd := range sortedKeys(pl.sec.Packages[target].Bin) {
				bins = append(bins, bin{Name: cmd, Script: tdir + "/" + pl.sec.Packages[target].Bin[cmd]})
			}
		}
		if s.runs(mp) {
			sc := &script{Node: s.node, Bash: s.bash, Path: s.path, Bins: bins, Env: map[string]string{}}
			if sc.Bins == nil {
				sc.Bins = []bin{}
			}
			if q, ok := quirkFor(mp.Name, mp.Version); ok {
				for k, v := range q.Env {
					sc.Env[k] = v
				}
			}
			mem.Script = sc
			for d, outs := range s.in.drvs {
				for o := range outs {
					if in.drvs[d] == nil {
						in.drvs[d] = map[string]bool{}
					}
					in.drvs[d][o] = true
				}
			}
			for src := range s.in.srcs {
				in.srcs[src] = true
			}
		}
		ms = append(ms, mem)
	}
	name := drv.Name("npm", key)
	if group != nil {
		h := sha256.Sum256([]byte(strings.Join(members, "\n")))
		name = "npm-scc-" + hex.EncodeToString(h[:4])
	}
	if s.key != "" && group == nil && s.runs(p) {
		name += "-script"
	}
	dp, d, err := pl.job(name, pi.system, "npm-unpack", p.Name+"@"+p.Version, unpackArgs{Members: ms}, in)
	if err != nil {
		return instRef{}, err
	}
	out := outPath(d)
	var r instRef
	for i, m := range members {
		ref := instRef{drv: dp, d: d, dir: out + "/node_modules/" + pl.sec.Packages[m].Name}
		if group != nil {
			ref.dir = fmt.Sprintf("%s/%d/node_modules/%s", out, i, pl.sec.Packages[m].Name)
		}
		pl.inst[pi.system+"|"+s.key+"|"+m] = ref
		if m == key {
			r = ref
		}
	}
	return r, nil
}

func (c *scc) indexOf(key string) (int, bool) {
	if c == nil {
		return 0, false
	}
	i, ok := c.index[key]
	return i, ok
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// ---- importers ----

// direct returns the importer's locked direct dependencies on the platform: alias → instance
// key or "link:<dir>", and which are Dev().
func (pl *planner) direct(n *ir.Node, im *importer, pi *platInfo) (map[string]string, map[string]bool, error) {
	rec, ok := pl.sec.Importers[im.dir]
	if !ok {
		return nil, nil, diag.New(1012, "what", fmt.Sprintf("npm importer %s is not locked", im.dir))
	}
	out := map[string]string{}
	for alias, k := range rec.Deps {
		if strings.HasPrefix(k, "link:") {
			out[alias] = k
			continue
		}
		p, ok := pl.sec.Packages[k]
		if !ok {
			return nil, nil, internal(n, "bob.lock: importer %s depends on %s, which it does not list", im.dir, k)
		}
		if !pi.installable(p) {
			if p.Optional {
				continue
			}
			return nil, nil, invalid(n, "dependency "+alias, fmt.Sprintf("%s@%s does not support %s-%s", p.Name, p.Version, pi.os, pi.cpu))
		}
		out[alias] = k
	}
	dev := map[string]bool{}
	for _, a := range rec.Dev {
		dev[a] = true
	}
	return out, dev, nil
}

// libByDir returns the library importer in dir.
func (pl *planner) libByDir(dir string) (*importer, error) {
	for _, im := range pl.ims {
		if im.dir == dir && im.node.Kind == npmspec.KindLib {
			return im, nil
		}
	}
	return nil, fmt.Errorf("no npm library in %s", dir)
}

// libOutput plans the library in dir for the platform of n and returns the directory of the
// built package (<out>/node_modules/<name>).
func (pl *planner) libOutput(n *ir.Node, dir string, in *inputs) (string, error) {
	lib, err := pl.libByDir(dir)
	if err != nil {
		return "", internal(n, "%v", err)
	}
	ln := lib.node
	// The library's node of the same configuration (platform), when the graph has it.
	for _, id := range n.Deps {
		if x, ok := pl.byID[id]; ok && x == lib {
			ln = pl.nodes[id]
		}
	}
	rv, err := pl.planNode(ln)
	if err != nil {
		return "", err
	}
	in.add(rv, "out")
	return rv.Outputs["out"] + "/node_modules/" + lib.spec.Name, nil
}

// depDir returns the directory a direct dependency links to.
func (pl *planner) depDir(n *ir.Node, pi *platInfo, s *scripts, target string, in *inputs) (string, error) {
	if dir, ok := strings.CutPrefix(target, "link:"); ok {
		return pl.libOutput(n, dir, in)
	}
	r, err := pl.instance(pi, s, target)
	if err != nil {
		return "", err
	}
	in.addDrv(r.drv)
	return r.dir, nil
}

// nodeModules plans the importer's node_modules for builds: every direct dependency (Dev
// included), the workspace libraries and .bin wrappers; isolated by default, one npm-style
// tree with npm.Hoisted().
func (pl *planner) nodeModules(n *ir.Node, im *importer, pi *platInfo) (nmRef, error) {
	memo := pi.system + "|" + im.dir
	if r, ok := pl.nm[memo]; ok {
		return r, nil
	}
	s, err := pl.scriptsOf(n, im)
	if err != nil {
		return nmRef{}, err
	}
	deps, _, err := pl.direct(n, im, pi)
	if err != nil {
		return nmRef{}, err
	}
	in := pl.newInputs()
	node, err := pl.tool(n, im.spec.Tools.Node, in)
	if err != nil {
		return nmRef{}, err
	}
	bash, err := pl.tool(n, im.spec.Tools.Bash, in)
	if err != nil {
		return nmRef{}, err
	}
	out := drv.Placeholder("out")
	args := nodeModulesArgs{Out: out, Links: []link{}, Hoisted: []hoistEntry{}, Bins: []bin{},
		Node: node + "/bin/node", Bash: bash + "/bin/bash"}
	name := drv.Name("node_modules", strings.ReplaceAll(dirName(im.dir), "/", "-"))
	if im.spec.Options.Hoisted {
		name += "-hoisted"
		if err := pl.hoist(n, pi, s, deps, &args, in); err != nil {
			return nmRef{}, err
		}
	} else {
		for _, alias := range sortedKeys(deps) {
			dir, err := pl.depDir(n, pi, s, deps[alias], in)
			if err != nil {
				return nmRef{}, err
			}
			args.Links = append(args.Links, link{Alias: alias, Target: dir})
			args.Bins = append(args.Bins, pl.binsOf(deps[alias], dir)...)
		}
	}
	dp, d, err := pl.job(name, pi.system, "npm-node-modules", n.Label+" node_modules", args, in)
	if err != nil {
		return nmRef{}, internal(n, "%v", err)
	}
	r := nmRef{drv: dp, d: d, out: outPath(d)}
	pl.nm[memo] = r
	return r, nil
}

func dirName(dir string) string {
	if dir == "." {
		return "root"
	}
	return dir
}

// binsOf are the commands of the package target installed at dir.
func (pl *planner) binsOf(target, dir string) []bin {
	p, ok := pl.sec.Packages[target]
	if !ok {
		return nil
	}
	var out []bin
	for _, cmd := range sortedKeys(p.Bin) {
		out = append(out, bin{Name: cmd, Script: dir + "/" + p.Bin[cmd]})
	}
	return out
}

// hoist lays the importer's packages out as an npm-style tree (D30 fallback): breadth-first
// from the direct dependencies, each package at the top level when its name is free there (or
// the same instance is), else nested in the node_modules of the package that needs it.
// Placing level by level keeps every earlier resolution valid.
func (pl *planner) hoist(n *ir.Node, pi *platInfo, s *scripts, deps map[string]string, args *nodeModulesArgs, in *inputs) error {
	type loc struct {
		path string // relative to node_modules: "a", "a/node_modules/b"
		key  string
	}
	at := map[string]string{} // path → instance key
	var queue []loc
	place := func(p, key string) error {
		at[p] = key
		if dir, ok := strings.CutPrefix(key, "link:"); ok {
			lib, err := pl.libOutput(n, dir, in)
			if err != nil {
				return err
			}
			args.Hoisted = append(args.Hoisted, hoistEntry{Path: p, Link: lib})
			return nil
		}
		pk := pl.sec.Packages[key]
		tb, err := pl.tarball(pk)
		if err != nil {
			return err
		}
		in.srcs[tb] = true
		args.Hoisted = append(args.Hoisted, hoistEntry{Path: p, Tarball: tb})
		queue = append(queue, loc{p, key})
		return nil
	}
	for _, alias := range sortedKeys(deps) {
		if err := place(alias, deps[alias]); err != nil {
			return err
		}
		args.Bins = append(args.Bins, pl.binsOf(deps[alias], drv.Placeholder("out")+"/"+alias)...)
	}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		es, err := pl.edges(pi, cur.key)
		if err != nil {
			return err
		}
		for _, alias := range sortedKeys(es) {
			want := es[alias]
			switch k, ok := resolveAt(at, cur.path, alias); {
			case ok && k == want:
			case !ok: // nothing named alias between cur and the top: hoist it
				if err := place(alias, want); err != nil {
					return err
				}
			default:
				if err := place(cur.path+"/node_modules/"+alias, want); err != nil {
					return err
				}
			}
		}
	}
	// Scripts run after the tree is complete, deepest packages first.
	for i := len(args.Hoisted) - 1; i >= 0; i-- {
		e := &args.Hoisted[i]
		key := at[e.Path]
		if e.Tarball == "" || !s.runs(pl.sec.Packages[key]) {
			continue
		}
		pk := pl.sec.Packages[key]
		sc := &script{Node: s.node, Bash: s.bash, Path: s.path, Bins: []bin{}, Env: map[string]string{}}
		if q, ok := quirkFor(pk.Name, pk.Version); ok {
			for k, v := range q.Env {
				sc.Env[k] = v
			}
		}
		e.Script = sc
		for d := range s.in.drvs {
			in.addDrv(d)
		}
		for src := range s.in.srcs {
			in.srcs[src] = true
		}
	}
	return nil
}

// resolveAt is what Node finds for alias from the package at p: the nearest node_modules
// between p and the top that holds it.
func resolveAt(at map[string]string, p, alias string) (string, bool) {
	for {
		if k, ok := at[p+"/node_modules/"+alias]; ok {
			return k, true
		}
		i := strings.LastIndex(p, "/node_modules/")
		if i < 0 {
			break
		}
		p = p[:i]
	}
	k, ok := at[alias]
	return k, ok
}
