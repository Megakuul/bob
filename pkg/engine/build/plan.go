package build

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/megakuul/bob/pkg/bob/diag"
	"github.com/megakuul/bob/pkg/engine"
	"github.com/megakuul/bob/pkg/engine/drv"
	"github.com/megakuul/bob/pkg/engine/fetch"
	"github.com/megakuul/bob/pkg/engine/ir"
	"github.com/megakuul/bob/pkg/engine/lock"
	"github.com/megakuul/bob/pkg/engine/nixidx"
	"github.com/megakuul/bob/pkg/engine/srcs"
	"github.com/megakuul/bob/pkg/engine/store"
)

// planner computes the derivations of a graph: bob's core kinds (bob.run, bob.file,
// bob.fetch, nixpkgs.*) itself, every other kind through its registered ecosystem.
type planner struct {
	g        *ir.Graph
	st       *store.Store
	builder  string                  // bob-builder store path of the host system
	system   string                  // host Nix system
	srcs     map[string]string       // node id → imported source path
	entries  map[string]nixidx.Entry // nixpkgs attr → index entry
	lockf    *lock.File
	idx      *nixidx.Index
	res      *resolver
	testArgs []string      // bob test -- args, passed to the ecosystems
	root     string        // repository root (PlanRequest.Root)
	fetcher  fetch.Fetcher // the run's fetcher (PlanRequest.Store.Ensure)
	offline  bool          // no substituters for builds while planning (Options.Offline)
	maxJobs  int           // --max-jobs of builds while planning (0 = Nix's default)
	// planBuilt counts the bob derivations built while planning (PlanRealiser): the
	// translator only sees the build stage, run.finish counts.built includes these.
	planBuilt int

	nodes   map[string]engine.Resolved // node id → planned outputs
	drvs    []*drv.Derivation          // every bob derivation, in planning order
	byPath  map[string]*drv.Derivation // drv path → derivation
	drvNode map[string]string          // drv path → node id of the package it builds
	fetches []fetch.Request
	// materialise are the ecosystems' working-tree outputs (engine.PlanResult.Materialise).
	materialise []engine.Materialisation
}

func newPlanner(g *ir.Graph, st *store.Store, builder string, srcs map[string]string,
	entries map[string]nixidx.Entry, lf *lock.File, idx *nixidx.Index) *planner {
	p := &planner{g: g, st: st, builder: builder, system: store.HostSystem(), srcs: srcs,
		entries: entries, lockf: lf, idx: idx, nodes: map[string]engine.Resolved{},
		byPath: map[string]*drv.Derivation{}, drvNode: map[string]string{}}
	p.res = &resolver{home: st.Home(), bob: p.byPath, memo: map[string][32]byte{}, idx: idx, learned: map[string]string{}}
	return p
}

// plan plans every node of the graph in dependency waves: a node is planned once all its
// dependencies are, so ecosystems always find their inputs in PlanRequest.Inputs.
func (p *planner) plan(ctx context.Context) error {
	pending := append([]*ir.Node(nil), p.g.Nodes...)
	for len(pending) > 0 {
		var ready, rest []*ir.Node
		for _, n := range pending {
			if p.depsPlanned(n) {
				ready = append(ready, n)
			} else {
				rest = append(rest, n)
			}
		}
		if len(ready) == 0 {
			return internalf(pending[0].Label, "its dependencies cannot be planned (cycle)")
		}
		byEco := map[string][]*ir.Node{}
		var errs []error
		for _, n := range ready {
			if isCoreKind(n.Kind) {
				if err := p.planCore(n); err != nil {
					errs = append(errs, err)
				}
				continue
			}
			name, _, _ := strings.Cut(n.Kind, ".")
			byEco[name] = append(byEco[name], n)
		}
		names := make([]string, 0, len(byEco))
		for n := range byEco {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, name := range names {
			if err := p.planEcosystem(ctx, name, byEco[name]); err != nil {
				errs = append(errs, err)
			}
		}
		if len(errs) > 0 {
			return errors.Join(errs...)
		}
		pending = rest
	}
	if p.idx != nil && len(p.res.learned) > 0 {
		// Best effort: a failed cache write only costs the parse next time.
		_ = p.idx.RecordModulo(p.res.learned)
	}
	return nil
}

func (p *planner) depsPlanned(n *ir.Node) bool {
	for _, d := range n.Deps {
		if _, ok := p.nodes[d]; !ok {
			return false
		}
	}
	return true
}

func isCoreKind(kind string) bool {
	return strings.HasPrefix(kind, "bob.") || strings.HasPrefix(kind, "nixpkgs.")
}

// planEcosystem asks the ecosystem that owns kind prefix name for nodes' derivations.
func (p *planner) planEcosystem(ctx context.Context, name string, nodes []*ir.Node) (err error) {
	eco, ok := engine.Lookup(name)
	if !ok {
		return internalf(nodes[0].Label, "no ecosystem plans kind %s", nodes[0].Kind)
	}
	defer func() { // an ecosystem bug must not crash bob
		if v := recover(); v != nil {
			err = internalf(nodes[0].Label, "ecosystem %s panicked: %v", name, v)
		}
	}()
	res, err := eco.Plan(ctx, engine.PlanRequest{Graph: p.g, Nodes: nodes, Lock: p.lockf.Sections[name],
		Inputs: p.nodes, Sources: p.srcs, Nixpkgs: p.idx, Builder: p.builder,
		Resolver: p.res, TestArgs: p.testArgs, Root: p.root, Store: planStore{st: p.st, f: p.fetcher, p: p}})
	if err != nil {
		if _, ok := diagErrors(err); ok {
			return err
		}
		return internalf(nodes[0].Label, "%v", err)
	}
	for _, d := range res.Drvs {
		if err := p.addDrv(d); err != nil {
			return internalf(nodes[0].Label, "%v", err)
		}
	}
	for _, n := range nodes {
		rv, ok := res.Nodes[n.ID]
		if !ok {
			return internalf(n.Label, "ecosystem %s did not plan it", name)
		}
		if rv.Drv != "" && p.byPath[rv.Drv] == nil {
			return internalf(n.Label, "ecosystem %s named a derivation it did not return", name)
		}
		p.setNode(n, rv)
	}
	p.fetches = append(p.fetches, res.Fetches...)
	p.materialise = append(p.materialise, res.Materialise...)
	return nil
}

func (p *planner) addDrv(d *drv.Derivation) error {
	dp, err := d.Path()
	if err != nil {
		return err
	}
	if p.byPath[dp] == nil {
		p.byPath[dp] = d
		p.drvs = append(p.drvs, d)
	}
	return nil
}

func (p *planner) setNode(n *ir.Node, rv engine.Resolved) {
	p.nodes[n.ID] = rv
	if _, taken := p.drvNode[rv.Drv]; rv.Drv != "" && !taken && n.Kind != "bob.file" {
		p.drvNode[rv.Drv] = n.ID
	}
}

// planCore plans one node of a core kind.
func (p *planner) planCore(n *ir.Node) error {
	switch {
	case strings.HasPrefix(n.Kind, "nixpkgs."):
		return p.planNixpkgs(n)
	case n.Kind == "bob.run":
		return p.planRun(n)
	case n.Kind == "bob.file":
		return p.planFile(n)
	case n.Kind == "bob.fetch":
		return p.planFetch(n)
	case n.Kind == "bob.devshell":
		p.setNode(n, engine.Resolved{}) // a dev shell builds nothing itself (bob dev, M6)
		return nil
	}
	return internalf(n.Label, "kind %s has no planner", n.Kind)
}

func (p *planner) planNixpkgs(n *ir.Node) error {
	if n.Nix == nil {
		return internalf(n.Label, "nixpkgs node without a nix object")
	}
	sys, attr, err := engine.NixpkgsSource(n)
	if err != nil {
		return internalf(n.Label, "%v", err)
	}
	e, ok := p.entries[entryKey(sys, attr)]
	if !ok || e.DrvPath == "" {
		return internalf(n.Label, "nixpkgs attribute %s (%s) was not resolved", attr, sys)
	}
	outs := map[string]string{}
	want := n.Nix.Outputs
	if len(want) == 0 {
		for o := range e.Outputs {
			want = append(want, o)
		}
	}
	for _, o := range want {
		sp, ok := e.Outputs[o]
		if !ok {
			return internalf(n.Label, "nixpkgs package %s has no output %q", n.Nix.Attr, o)
		}
		outs[o] = sp
	}
	p.setNode(n, engine.Resolved{Drv: e.DrvPath, Outputs: outs})
	return nil
}

// fetchSpec is the IR spec of kind bob.fetch (pkg/bob).
type fetchSpec struct {
	URL     string   `json:"url"`
	Mirrors []string `json:"mirrors"`
	Archive bool     `json:"archive"`
	Strip   int      `json:"strip"`
	Name    string   `json:"name"`
}

func (p *planner) planFetch(n *ir.Node) error {
	var s fetchSpec
	if err := json.Unmarshal(n.Spec, &s); err != nil {
		return internalf(n.Label, "fetch spec: %v", err)
	}
	e, ok := p.lockf.Sources[n.Label]
	if !ok {
		return diag.New(1009, "label", n.Label)
	}
	req, sp, err := fetchRequest(n.Label, s, e)
	if err != nil {
		return internalf(n.Label, "%v", err)
	}
	p.fetches = append(p.fetches, req)
	p.setNode(n, engine.Resolved{Outputs: map[string]string{"out": sp}})
	return nil
}

// fetchRequest is the request and the store path of a pinned bob.fetch input.
func fetchRequest(label string, s fetchSpec, e lock.Source) (fetch.Request, string, error) {
	req := fetch.Request{URLs: append([]string{e.URL}, s.Mirrors...), Kind: fetch.Kind(e.Kind), Hash: e.Hash,
		NarHash: e.NarHash, Name: e.Name, Strip: e.Strip, Label: label}
	sri, recursive := e.Hash, false
	if e.Kind == string(fetch.Archive) {
		sri, recursive = e.NarHash, true
	}
	h, err := sriHex(sri)
	if err != nil {
		return req, "", err
	}
	sp, err := drv.FixedOutputPath("sha256", h, recursive, e.Name)
	return req, sp, err
}

// sriHex turns "sha256-<base64>" into lowercase hex.
func sriHex(sri string) (string, error) {
	b64, ok := strings.CutPrefix(sri, "sha256-")
	if !ok {
		return "", fmt.Errorf("hash %q is not a sha256 SRI hash", sri)
	}
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil || len(raw) != sha256.Size {
		return "", fmt.Errorf("hash %q is not a sha256 SRI hash", sri)
	}
	return hex.EncodeToString(raw), nil
}

// fileSpec is the IR spec of kind bob.file.
type fileSpec struct {
	Path string `json:"path"`
}

func (p *planner) planFile(n *ir.Node) error {
	var s fileSpec
	if err := json.Unmarshal(n.Spec, &s); err != nil || len(n.Deps) != 1 {
		return internalf(n.Label, "file spec: %v", err)
	}
	step := p.nodes[n.Deps[0]]
	out, ok := step.Outputs["out"]
	if !ok {
		return internalf(n.Label, "its step has no output")
	}
	p.setNode(n, engine.Resolved{Drv: step.Drv, Outputs: map[string]string{"out": path.Join(out, path.Clean("/"+s.Path))}})
	return nil
}

// runSpec is the IR spec of kind bob.run (pkg/bob): deps are the tools (first Tools entries)
// followed by the inputs; Inputs maps an environment name to its dep index.
type runSpec struct {
	Cmd      []string          `json:"cmd"`
	Env      map[string]string `json:"env"`
	Tools    int               `json:"tools"`
	Inputs   map[string]int    `json:"inputs"`
	Outputs  []string          `json:"outputs"`
	TimeoutS int64             `json:"timeoutSeconds"`
}

// Paths inside the build sandbox (SPEC R-FW-023).
const (
	buildSrc = "/build/src"
)

var varRe = sync.OnceValue(func() *regexp.Regexp { return regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}`) })

// planRun turns a bob.Run step into one bob-builder "run" job: create $out, copy the sources
// writable to /build/src, exec the command there with the declared tools' bin/ on PATH.
func (p *planner) planRun(n *ir.Node) error {
	var s runSpec
	if err := json.Unmarshal(n.Spec, &s); err != nil {
		return internalf(n.Label, "run spec: %v", err)
	}
	if s.Tools < 0 || s.Tools > len(n.Deps) || len(s.Cmd) == 0 {
		return internalf(n.Label, "run spec does not match its dependencies")
	}
	system, err := p.systemOf(n)
	if err != nil {
		return err
	}
	in := newInputs(p.builder)
	var bins []string
	for _, id := range n.Deps[:s.Tools] {
		rv := p.nodes[id]
		out := pickOutput(rv.Outputs, "bin", "out")
		if out == "" {
			return internalf(n.Label, "tool %s has no output", id)
		}
		bins = append(bins, in.add(rv, out)+"/bin")
	}
	out := drv.Placeholder("out")
	env := map[string]string{}
	for k, v := range s.Env {
		env[k] = v
	}
	env["BOB_SRC"], env["BOB_OUT"] = buildSrc, out
	for name, i := range s.Inputs {
		if i < s.Tools || i >= len(n.Deps) {
			return internalf(n.Label, "input %s does not name a dependency", name)
		}
		rv := p.nodes[n.Deps[i]]
		o := pickOutput(rv.Outputs, "out")
		if o == "" {
			return internalf(n.Label, "input %s has no output", name)
		}
		env["BOB_IN_"+name] = in.add(rv, o)
	}
	// ${VAR} expands from the step's environment only (plus the fixed builder variables).
	fixed := map[string]string{"HOME": "/homeless-shelter", "TMPDIR": "/build/tmp", "SOURCE_DATE_EPOCH": "1", "TZ": "UTC", "LC_ALL": "C"}
	argv := make([]string, len(s.Cmd))
	for i, a := range s.Cmd {
		argv[i] = varRe().ReplaceAllStringFunc(a, func(m string) string {
			k := m[2 : len(m)-1]
			if v, ok := env[k]; ok {
				return v
			}
			return fixed[k]
		})
	}
	steps := []any{step("mkdir", map[string]any{"path": out})}
	if src, ok := p.srcs[n.ID]; ok {
		in.srcs = append(in.srcs, src)
		steps = append(steps, step("copy", map[string]any{"from": src, "to": buildSrc}))
	} else {
		steps = append(steps, step("mkdir", map[string]any{"path": buildSrc}))
	}
	exec := map[string]any{"argv": argv, "env": env, "cwd": buildSrc}
	if len(bins) > 0 {
		exec["path"] = bins
	}
	if s.TimeoutS > 0 {
		exec["timeoutSeconds"] = s.TimeoutS
	}
	steps = append(steps, step("exec", exec))
	if len(s.Outputs) > 0 { // R-FW-023: the declared outputs must exist
		paths := make([]any, len(s.Outputs))
		for i, o := range s.Outputs {
			paths[i] = o
		}
		steps = append(steps, step("check-files", map[string]any{"root": out, "paths": paths}))
	}
	spec := map[string]any{"v": 1, "kind": "run", "label": n.Label, "outputs": []any{"out"},
		"args": map[string]any{"steps": steps}, "fixup": emptyFixup()}
	return p.addJob(n, system, spec, in)
}

func step(op string, args map[string]any) map[string]any {
	return map[string]any{"op": op, "args": args}
}

// emptyFixup is the fixup contract of a job whose outputs are not ELF trees bob controls:
// no stripping, no RUNPATH change, only the reference check against nothing.
func emptyFixup() map[string]any {
	return map[string]any{"strip": false, "debugOut": false, "shrinkRpath": false, "forbidden": []any{},
		"tools": map[string]any{"strip": "", "objcopy": "", "patchelf": ""}}
}

// addJob finalises the bob-builder job of node n and records it.
func (p *planner) addJob(n *ir.Node, system string, spec map[string]any, in *inputs) error {
	j := drv.Job{Name: storeName(n), System: system, Builder: p.builder, Spec: spec,
		InputDrvs: in.drvList(), InputSrcs: in.srcList(),
		// A71: bob checks its own caches in one batch before Nix runs; Nix must not ask a
		// substituter about every bob output (≈ 0.12 s per derivation).
		ExtraAttribute: map[string]any{"allowSubstitutes": false}}
	d, err := j.Derivation()
	if err != nil {
		return internalf(n.Label, "%v", err)
	}
	if err := d.Finalize(p.res); err != nil {
		return internalf(n.Label, "%v", err)
	}
	dp, err := d.Path()
	if err != nil {
		return internalf(n.Label, "%v", err)
	}
	if err := p.addDrv(d); err != nil {
		return internalf(n.Label, "%v", err)
	}
	outs := map[string]string{}
	for _, o := range d.Outputs {
		outs[o.Name] = o.Path
	}
	p.setNode(n, engine.Resolved{Drv: dp, Outputs: outs})
	return nil
}

// systemOf is the Nix system of the derivation of node n: always the host's (R-PLAT-004). The
// node's platform only selects toolchains, flags and target libraries; it must be one bob
// knows.
func (p *planner) systemOf(n *ir.Node) (string, error) {
	if _, err := engine.NodePlatform(n); err != nil {
		return "", internalf(n.Label, "%v", err)
	}
	return p.system, nil
}

// storeName is the fallback store name of a package (SPEC R-NIX-006 "Store names"):
// drv.Name(dir, name, projection tag).
func storeName(n *ir.Node) string {
	dir, name, _ := strings.Cut(strings.TrimPrefix(n.Label, "//"), ":")
	parts := []string{}
	if dir != "" {
		parts = append(parts, dir)
	}
	parts = append(parts, name)
	if _, tag, ok := strings.Cut(n.ID, "@"); ok && tag != "" {
		parts = append(parts, tag)
	}
	return drv.Name(parts...)
}

// pickOutput returns the first of prefer that outs has, else its first output by name.
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

// inputs collects the input derivations and sources of one job.
type inputs struct {
	drvs map[string]map[string]bool
	srcs []string
}

func newInputs(builder string) *inputs {
	return &inputs{drvs: map[string]map[string]bool{}, srcs: []string{builder}}
}

// add records the dependency rv's output o and returns its path.
func (in *inputs) add(rv engine.Resolved, o string) string {
	sp := rv.Outputs[o]
	if rv.Drv == "" {
		in.srcs = append(in.srcs, sp)
		return sp
	}
	if in.drvs[rv.Drv] == nil {
		in.drvs[rv.Drv] = map[string]bool{}
	}
	// bob.file outputs are paths inside their step's "out".
	in.drvs[rv.Drv][o] = true
	return sp
}

func (in *inputs) drvList() []drv.InputDrv {
	var out []drv.InputDrv
	for p, outs := range in.drvs {
		d := drv.InputDrv{Path: p}
		for o := range outs {
			d.Outputs = append(d.Outputs, o)
		}
		sort.Strings(d.Outputs)
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

func (in *inputs) srcList() []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in.srcs {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	sort.Strings(out)
	return out
}

// resolver returns hashDerivationModulo of input derivations: bob's own from memory, nixpkgs'
// from the index cache or by parsing the .drv files of the store (recursively, memoised).
type resolver struct {
	mu      sync.Mutex
	home    string
	bob     map[string]*drv.Derivation
	memo    map[string][32]byte
	idx     *nixidx.Index
	learned map[string]string // newly parsed nixpkgs modulo hashes (hex) for the index cache
}

func (r *resolver) Modulo(p string) ([32]byte, error) {
	r.mu.Lock()
	h, ok := r.memo[p]
	d := r.bob[p]
	r.mu.Unlock()
	if ok {
		return h, nil
	}
	if d == nil && r.idx != nil {
		if hx, ok := r.idx.Modulo(p); ok {
			if b, err := hex.DecodeString(hx); err == nil && len(b) == 32 {
				copy(h[:], b)
				r.remember(p, h, false)
				return h, nil
			}
		}
	}
	nix := d == nil
	if nix {
		if !strings.HasPrefix(p, "/nix/store/") || strings.Contains(p[len("/nix/store/"):], "/") {
			return h, fmt.Errorf("input derivation %q is not a store path", p)
		}
		b, err := os.ReadFile(filepath.Join(r.home, p))
		if err != nil {
			return h, fmt.Errorf("input derivation %s: %w", p, err)
		}
		if d, err = drv.Parse(b); err != nil {
			return h, fmt.Errorf("input derivation %s: %w", p, err)
		}
	}
	h, err := d.Modulo(r)
	if err != nil {
		return h, err
	}
	r.remember(p, h, nix)
	return h, nil
}

func (r *resolver) remember(p string, h [32]byte, learned bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.memo[p] = h
	if learned {
		r.learned[p] = hex.EncodeToString(h[:])
	}
}

// planStore is engine.PlanStore over the run's store and fetcher.
type planStore struct {
	st *store.Store
	f  fetch.Fetcher
	p  *planner // the derivations of earlier waves and the run's build options
}

func (s planStore) HostPath(p string) string { return hostPath(s.st, p) }

func (s planStore) ImportSources(ctx context.Context, trees []srcs.Source) ([]string, error) {
	return srcs.Import(ctx, s.st, trees)
}

func (s planStore) Ensure(ctx context.Context, r fetch.Request) (string, error) {
	if s.f == nil {
		return "", fmt.Errorf("fetch %s: no fetcher while planning", r.Label)
	}
	return s.f.Ensure(ctx, r)
}

// maxPlanLog bounds the Nix output kept from a build while planning (only its tail is used).
const maxPlanLog = 64 << 10

// RealiseNow implements engine.PlanRealiser: drvs and the bob derivations they need (planned
// in earlier waves) are written to the store, those with invalid outputs are built with one
// `nix build`, and the outputs are checked structurally (R-NIX-018) before they are returned.
func (s planStore) RealiseNow(ctx context.Context, drvs map[string]*drv.Derivation) (map[string]map[string]string, error) {
	all := map[string]*drv.Derivation{}
	var add func(path string, d *drv.Derivation)
	add = func(path string, d *drv.Derivation) {
		if _, ok := all[path]; ok {
			return
		}
		all[path] = d
		for _, in := range d.InputDrvs {
			if dd, ok := drvs[in.Path]; ok {
				add(in.Path, dd)
			} else if s.p != nil && s.p.byPath[in.Path] != nil {
				add(in.Path, s.p.byPath[in.Path])
			}
		}
	}
	paths := make([]string, 0, len(drvs))
	for p, d := range drvs {
		if d == nil {
			return nil, fmt.Errorf("realise while planning: no derivation for %s", p)
		}
		paths = append(paths, p)
		add(p, d)
	}
	sort.Strings(paths)
	list := make([]*drv.Derivation, 0, len(all))
	for _, p := range sortedKeys(all) {
		list = append(list, all[p])
	}
	if _, err := s.st.ImportDrvs(ctx, list); err != nil {
		return nil, err
	}
	var outs []string
	outOf := map[string]string{} // output path → drv path
	for p, d := range all {
		for _, o := range d.Outputs {
			outs = append(outs, o.Path)
			outOf[o.Path] = p
		}
	}
	sort.Strings(outs)
	before, err := s.st.Invalid(ctx, outs)
	if err != nil {
		return nil, err
	}
	if len(before) > 0 {
		args := []string{"build", "--no-link", "--keep-going"}
		if s.p != nil && s.p.maxJobs > 0 {
			args = append(args, "--max-jobs", strconv.Itoa(s.p.maxJobs))
		}
		if s.p != nil && s.p.offline {
			args = append(args, "--option", "substituters", "")
		}
		for _, p := range paths {
			var names []string
			for _, o := range drvs[p].Outputs {
				names = append(names, o.Name)
			}
			sort.Strings(names)
			args = append(args, p+"^"+strings.Join(names, ","))
		}
		var log tailBuffer
		c := s.st.Command(ctx, "nix", args...)
		c.Stderr = &log
		runErr := c.Run()
		after, err := s.st.Invalid(ctx, outs)
		if err != nil {
			return nil, err
		}
		still := map[string]bool{}
		for _, o := range after {
			still[outOf[o]] = true
		}
		built := map[string]bool{}
		for _, o := range before {
			if dp := outOf[o]; !still[dp] {
				built[dp] = true
			}
		}
		if s.p != nil {
			s.p.planBuilt += len(built)
		}
		for _, p := range paths {
			if still[p] {
				return nil, fmt.Errorf("building %s while planning failed (%v): %s", p, runErr, log.tail())
			}
		}
	}
	res := make(map[string]map[string]string, len(paths))
	for _, p := range paths {
		m := map[string]string{}
		for _, o := range drvs[p].Outputs {
			m[o.Name] = s.HostPath(o.Path)
		}
		res[p] = m
	}
	return res, nil
}

// tailBuffer keeps the last maxPlanLog bytes written to it.
type tailBuffer struct{ b []byte }

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.b = append(t.b, p...)
	if len(t.b) > maxPlanLog {
		t.b = append([]byte(nil), t.b[len(t.b)-maxPlanLog:]...)
	}
	return len(p), nil
}

// tail is the last lines of the output (at most 12).
func (t *tailBuffer) tail() string {
	lines := strings.Split(strings.TrimSpace(string(t.b)), "\n")
	if len(lines) > 12 {
		lines = lines[len(lines)-12:]
	}
	return strings.Join(lines, "\n")
}
