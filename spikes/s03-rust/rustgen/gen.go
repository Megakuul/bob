// Package rustgen turns a crates.Plan into Nix derivations computed in Go (backend B style):
// per crate one source fetch (fixed-output, unpacked), one compile derivation, and for crates
// with build.rs one build-script compile + one build-script run derivation.
package rustgen

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"bobspike/s03/bspec"
	"bobspike/s03/crates"
	"bobspike/s03/drv"
	"bobspike/s03/nixidx"
)

// Toolchain attrs every Rust derivation uses.
var Toolchain = []string{"rustc", "stdenv.cc", "stdenv.cc.bintools", "coreutils"}

type Gen struct {
	Ix      *nixidx.Index
	System  string
	Builder string            // store path of the bob-builder binary (an input source)
	Srcs    map[string]string // local crate / cc / go name -> imported store path
	WS      *crates.Workspace
	RustcV  string

	Order  []*drv.Derivation
	Labels map[string]string // drv path -> label for error translation
	src    map[string]*drv.Derivation
	units  map[string]*Out
	cc     map[string]*drv.Derivation
	Stats  map[string]int
}

// Out are the derivations of one unit.
type Out struct {
	U       *crates.Unit
	Compile *drv.Derivation
	BSBuild *drv.Derivation
	BSRun   *drv.Derivation
	SrcPath string
	LibFile string
}

func New(ix *nixidx.Index, system, builder string, srcs map[string]string, ws *crates.Workspace, rustcV string) *Gen {
	return &Gen{Ix: ix, System: system, Builder: builder, Srcs: srcs, WS: ws, RustcV: rustcV,
		Labels: map[string]string{}, src: map[string]*drv.Derivation{}, units: map[string]*Out{},
		cc: map[string]*drv.Derivation{}, Stats: map[string]int{}}
}

type env struct {
	inputs map[string][]string
	srcs   map[string]bool
	path   []string
}

func newEnv() *env { return &env{inputs: map[string][]string{}, srcs: map[string]bool{}} }

func (e *env) use(d *drv.Derivation) string {
	e.inputs[d.DrvPath] = []string{"out"}
	return d.OutPath
}

func (g *Gen) toolchain(e *env, extra ...string) error {
	for _, a := range append(append([]string{}, Toolchain...), extra...) {
		at := g.Ix.Attrs[a]
		if at == nil || at.DrvPath == "" {
			return fmt.Errorf("nixpkgs attribute %s not resolved", a)
		}
		e.inputs[at.DrvPath] = []string{"out"}
		e.path = append(e.path, at.Outputs["out"]+"/bin")
	}
	return nil
}

func (g *Gen) finish(kind, name, label string, e *env, job string, spec any) (*drv.Derivation, error) {
	js, err := json.Marshal(spec)
	if err != nil {
		return nil, err
	}
	e.srcs[g.Builder] = true
	envm := map[string]string{"name": name, "system": g.System, "builder": g.Builder,
		"PATH": strings.Join(e.path, ":"), "bobSpec": string(js)}
	d := &drv.Derivation{Name: name, System: g.System, Builder: g.Builder, Args: []string{job}, Env: envm, InputDrvs: e.inputs}
	for s := range e.srcs {
		d.InputSrcs = append(d.InputSrcs, s)
	}
	sort.Strings(d.InputSrcs)
	if err := d.Finalize(g.Ix.Cache); err != nil {
		return nil, err
	}
	g.Ix.Cache.Set(d.DrvPath, d.Modulo())
	g.Order = append(g.Order, d)
	g.Labels[d.DrvPath] = label
	g.Stats[kind]++
	return d, nil
}

// source returns the unpacked source of a registry crate: builtin:fetchurl with unpack,
// a recursive fixed-output derivation verified by the NAR hash recorded in bob.lock.
func (g *Gen) source(c *crates.LockedCrate) (*drv.Derivation, error) {
	key := c.Name + "@" + c.Version
	if d := g.src[key]; d != nil {
		return d, nil
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(c.NarHash, "sha256-"))
	if err != nil || len(raw) != 32 {
		return nil, fmt.Errorf("%s: bob.lock has no valid source hash (run bob lock)", key)
	}
	name := sanitizeName(c.Name + "-" + c.Version)
	ca := g.Ix.Attrs["cacert"]
	if ca == nil || ca.DrvPath == "" {
		return nil, fmt.Errorf("nixpkgs attribute cacert not resolved")
	}
	spec, _ := json.Marshal(bspec.FetchCrate{URL: crates.CrateURL(c.Name, c.Version), Checksum: c.Checksum,
		CACert: ca.Outputs["out"] + "/etc/ssl/certs/ca-bundle.crt"})
	d := &drv.Derivation{Name: name, System: g.System, Builder: g.Builder, Args: []string{"fetch-crate"},
		Env: map[string]string{"name": name, "system": g.System, "builder": g.Builder, "bobSpec": string(spec),
			"outputHash": c.NarHash, "outputHashAlgo": "", "outputHashMode": "recursive", "preferLocalBuild": "1"},
		InputDrvs: map[string][]string{ca.DrvPath: {"out"}}, InputSrcs: []string{g.Builder},
		FixedAlgo: "sha256", FixedMode: "recursive", FixedHash: raw}
	if err := d.Finalize(g.Ix.Cache); err != nil {
		return nil, err
	}
	g.Ix.Cache.Set(d.DrvPath, d.Modulo())
	g.Order = append(g.Order, d)
	g.Labels[d.DrvPath] = "crates.io:" + key
	g.src[key] = d
	g.Stats["fetch"]++
	return d, nil
}

func sanitizeName(s string) string {
	return strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("+-._?=", r) {
			return r
		}
		return '-'
	}, s)
}

func (g *Gen) label(u *crates.Unit) string {
	if u.Ws != nil {
		return "//" + u.Ws.Src + ":" + u.Name
	}
	return "crates.io:" + u.Name + "@" + u.Version
}

func (g *Gen) srcOf(e *env, u *crates.Unit) (string, error) {
	if u.Ws != nil {
		p, ok := g.Srcs[u.Name]
		if !ok {
			return "", fmt.Errorf("source of %s not imported", u.Name)
		}
		e.srcs[p] = true
		return p, nil
	}
	d, err := g.source(u.Locked)
	if err != nil {
		return "", err
	}
	return e.use(d), nil
}

func optLevel(u *crates.Unit) string {
	if u.Host {
		return "0" // cargo's release build-override profile: build scripts, proc-macros and their deps
	}
	return "3"
}

func pkgEnv(u *crates.Unit) map[string]string {
	v, _ := crates.ParseVersion(u.Version)
	m := map[string]string{"CARGO_PKG_NAME": u.Name, "CARGO_PKG_VERSION": u.Version, "CARGO_CRATE_NAME": u.LibName(),
		"CARGO_PKG_VERSION_MAJOR": fmt.Sprint(v.Major), "CARGO_PKG_VERSION_MINOR": fmt.Sprint(v.Minor),
		"CARGO_PKG_VERSION_PATCH": fmt.Sprint(v.Patch), "CARGO_PKG_VERSION_PRE": strings.Join(v.Pre, "."),
		"CARGO_PKG_AUTHORS": "", "CARGO_PKG_DESCRIPTION": "", "CARGO_PKG_HOMEPAGE": "", "CARGO_PKG_REPOSITORY": "",
		"CARGO_PKG_LICENSE": "", "CARGO_PKG_LICENSE_FILE": "", "CARGO_PKG_RUST_VERSION": "", "CARGO_PKG_README": ""}
	if u.Ws != nil {
		m["CARGO_PRIMARY_PACKAGE"] = "1"
	}
	return m
}

// libFile is the file a dependent passes to --extern.
func libFile(u *crates.Unit) string {
	if u.ProcMacro() {
		return "lib" + u.LibName() + "-" + u.Metadata + ".so"
	}
	return "lib" + u.LibName() + "-" + u.Metadata + ".rlib"
}

func crateType(u *crates.Unit) string {
	if u.Ws != nil {
		return u.Ws.Type
	}
	if u.ProcMacro() {
		return "proc-macro"
	}
	return "lib"
}

func rootFile(u *crates.Unit) string {
	if u.Ws != nil {
		return u.Ws.Root
	}
	if u.Locked.Meta.LibPath != "" {
		return u.Locked.Meta.LibPath
	}
	return "src/lib.rs"
}

func checkCfg(u *crates.Unit) []string {
	if u.Ws == nil {
		return nil
	}
	var fs []string
	for f := range u.Ws.Features {
		fs = append(fs, fmt.Sprintf("%q", f))
	}
	sort.Strings(fs)
	return []string{"cfg(docsrs,test)", "cfg(feature, values(" + strings.Join(fs, ",") + "))"}
}

// Unit generates (memoised) derivations for u and its dependencies.
func (g *Gen) Unit(u *crates.Unit, p *crates.Plan) (*Out, error) {
	if o := g.units[u.Key]; o != nil {
		return o, nil
	}
	o := &Out{U: u}
	g.units[u.Key] = o
	var deps []*Out
	for _, d := range u.Deps {
		do, err := g.Unit(d.Unit, p)
		if err != nil {
			return nil, err
		}
		deps = append(deps, do)
	}
	var bdeps []*Out
	for _, d := range u.BuildDeps {
		do, err := g.Unit(d.Unit, p)
		if err != nil {
			return nil, err
		}
		bdeps = append(bdeps, do)
	}
	name := sanitizeName(u.Name + "-" + u.Version)
	if u.Host {
		name += "-host"
	}
	if bs := u.BuildScript(); bs != "" {
		if err := g.buildScript(o, u, bs, bdeps, deps, name, p); err != nil {
			return nil, err
		}
	}
	e := newEnv()
	if err := g.toolchain(e); err != nil {
		return nil, err
	}
	src, err := g.srcOf(e, u)
	if err != nil {
		return nil, err
	}
	o.SrcPath = src
	spec := bspec.Rustc{Src: bspec.Source{Dir: src}, CrateName: u.LibName(), CrateType: crateType(u), Edition: u.Edition(),
		Root: rootFile(u), Features: u.Features, Metadata: u.Metadata, OptLevel: optLevel(u), CapLints: u.Ws == nil,
		Env: pkgEnv(u), CheckCfg: checkCfg(u)}
	if u.Ws != nil && crateType(u) == "bin" {
		spec.Env["CARGO_BIN_NAME"] = u.Name
	}
	if u.Ws != nil {
		spec.Headers = u.Ws.Hdrs
		for _, n := range u.Ws.CLibs {
			cd, kind, err := g.CC(n)
			if err != nil {
				return nil, err
			}
			spec.NativeLib = append(spec.NativeLib, bspec.NativeLib{Dir: e.use(cd), Name: n, Kind: kind})
		}
	}
	for i, d := range u.Deps {
		spec.Externs = append(spec.Externs, bspec.Extern{Name: d.Extern, Path: e.use(deps[i].Compile), Lib: libFile(d.Unit)})
	}
	spec.DepDirs = g.depDirs(e, deps)
	if o.BSRun != nil {
		spec.BuildRun = e.use(o.BSRun)
	}
	if t := crateType(u); t == "bin" || t == "cdylib" || t == "staticlib" || t == "proc-macro" {
		for _, br := range g.transitiveBuildRuns(deps) {
			spec.LinkFrom = append(spec.LinkFrom, e.use(br))
		}
	}
	o.Compile, err = g.finish("compile", name, g.label(u), e, "rustc", spec)
	o.LibFile = libFile(u)
	return o, err
}

func (g *Gen) depDirs(e *env, deps []*Out) []string {
	seen := map[string]bool{}
	var out []string
	var walk func(o *Out)
	walk = func(o *Out) {
		if seen[o.U.Key] {
			return
		}
		seen[o.U.Key] = true
		out = append(out, e.use(o.Compile))
		for _, d := range o.U.Deps {
			walk(g.units[d.Unit.Key])
		}
	}
	for _, d := range deps {
		walk(d)
	}
	sort.Strings(out)
	return out
}

func (g *Gen) transitiveBuildRuns(deps []*Out) []*drv.Derivation {
	seen := map[string]bool{}
	var out []*drv.Derivation
	var walk func(o *Out)
	walk = func(o *Out) {
		if seen[o.U.Key] {
			return
		}
		seen[o.U.Key] = true
		if o.BSRun != nil {
			out = append(out, o.BSRun)
		}
		for _, d := range o.U.Deps {
			walk(g.units[d.Unit.Key])
		}
	}
	for _, d := range deps {
		walk(d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].DrvPath < out[j].DrvPath })
	return out
}

func envName(s string) string { return strings.ToUpper(strings.ReplaceAll(s, "-", "_")) }

func (g *Gen) buildScript(o *Out, u *crates.Unit, script string, bdeps, deps []*Out, name string, p *crates.Plan) error {
	// 1. compile build.rs for the host
	e := newEnv()
	if err := g.toolchain(e); err != nil {
		return err
	}
	src, err := g.srcOf(e, u)
	if err != nil {
		return err
	}
	spec := bspec.Rustc{Src: bspec.Source{Dir: src}, CrateName: "build_script_build", CrateType: "bin", Edition: u.Edition(),
		Root: script, Features: u.Features, Metadata: u.Metadata + "bs", OptLevel: "0", CapLints: u.Ws == nil, Env: pkgEnv(u)}
	for i, d := range u.BuildDeps {
		spec.Externs = append(spec.Externs, bspec.Extern{Name: d.Extern, Path: e.use(bdeps[i].Compile), Lib: libFile(d.Unit)})
	}
	spec.DepDirs = g.depDirs(e, bdeps)
	for _, br := range g.transitiveBuildRuns(bdeps) {
		spec.LinkFrom = append(spec.LinkFrom, e.use(br))
	}
	if o.BSBuild, err = g.finish("bs-compile", name+"-build-script", g.label(u)+" (build script)", e, "rustc", spec); err != nil {
		return err
	}
	// 2. run it for the unit's platform
	e = newEnv()
	if err := g.toolchain(e); err != nil {
		return err
	}
	if src, err = g.srcOf(e, u); err != nil {
		return err
	}
	pl := p.Target
	if u.Host {
		pl = p.HostP
	}
	env := pkgEnv(u)
	for k, v := range map[string]string{"TARGET": pl.Triple, "HOST": p.HostP.Triple, "PROFILE": "release",
		"OPT_LEVEL": optLevel(u), "DEBUG": "false", "CARGO_CFG_PANIC": "unwind", "CARGO_MANIFEST_LINKS": u.Links(),
		"RUSTC_LINKER": "", "CARGO_PKG_NAME": u.Name} {
		env[k] = v
	}
	if env["RUSTC_LINKER"] == "" {
		delete(env, "RUSTC_LINKER")
	}
	if env["CARGO_MANIFEST_LINKS"] == "" {
		delete(env, "CARGO_MANIFEST_LINKS")
	}
	for _, f := range u.Features {
		env["CARGO_FEATURE_"+envName(f)] = "1"
	}
	for k, vs := range pl.Cfg {
		if k == "debug_assertions" {
			continue // release profile
		}
		env["CARGO_CFG_"+envName(k)] = strings.Join(vs, ",")
	}
	var pcs []string
	for _, a := range g.WS.Native[u.Name] {
		at := g.Ix.Attrs[a]
		if at == nil || at.DrvPath == "" {
			return fmt.Errorf("%s: nixpkgs package %q not resolved", u.Name, a)
		}
		var outs []string
		for _, on := range []string{"out", "dev", "lib", "bin"} {
			if op, ok := at.Outputs[on]; ok {
				outs = append(outs, on)
				e.path = append(e.path, op+"/bin")
				pcs = append(pcs, op+"/lib/pkgconfig", op+"/share/pkgconfig")
			}
		}
		e.inputs[at.DrvPath] = outs
	}
	if len(pcs) > 0 {
		env["PKG_CONFIG_PATH"] = strings.Join(pcs, ":")
	}
	run := bspec.BuildRun{Src: bspec.Source{Dir: src}, Script: e.use(o.BSBuild), Env: env}
	for i, d := range u.Deps {
		if l := d.Unit.Links(); l != "" && deps[i].BSRun != nil {
			run.DepLinks = append(run.DepLinks, bspec.DepLinks{Links: l, Out: e.use(deps[i].BSRun)})
		}
	}
	o.BSRun, err = g.finish("bs-run", name+"-build-run", g.label(u)+" (build script run)", e, "build-run", run)
	return err
}

// CC generates a bob C library / program derivation.
func (g *Gen) CC(name string) (*drv.Derivation, string, error) {
	var c *crates.WsCC
	for _, x := range g.WS.CC {
		if x.Name == name {
			c = x
		}
	}
	if c == nil {
		return nil, "", fmt.Errorf("unknown C library %s", name)
	}
	kind := "static"
	if c.Kind == "shared" {
		kind = "dylib"
	}
	if d := g.cc[name]; d != nil {
		return d, kind, nil
	}
	e := newEnv()
	if err := g.toolchain(e); err != nil {
		return nil, "", err
	}
	src := g.Srcs["cc:"+name]
	e.srcs[src] = true
	spec := bspec.CC{Src: src, Srcs: c.Srcs, Hdrs: c.Hdrs, Name: c.Name, Kind: c.Kind, LinkArgs: c.LinkArgs}
	for _, r := range c.RustDeps {
		ro := g.units[r+"@"+g.WS.Get(r).Version+"/target"]
		if ro == nil {
			return nil, "", fmt.Errorf("C target %s: Rust crate %s not generated", name, r)
		}
		spec.Deps = append(spec.Deps, bspec.NativeLib{Dir: e.use(ro.Compile), Name: ro.U.LibName()})
	}
	d, err := g.finish("cc", sanitizeName(name), "//"+c.Src+":"+name, e, "cc", spec)
	g.cc[name] = d
	return d, kind, err
}

// GoBin generates a cgo binary linking Rust staticlibs.
func (g *Gen) GoBin(w *crates.WsGo) (*drv.Derivation, error) {
	e := newEnv()
	if err := g.toolchain(e, "go"); err != nil {
		return nil, err
	}
	src := g.Srcs["go:"+w.Name]
	e.srcs[src] = true
	spec := bspec.Go{Src: src, Name: w.Name, Main: w.Main, LdExtra: w.LdExtra}
	for _, r := range w.RustDeps {
		ro := g.units[r+"@"+g.WS.Get(r).Version+"/target"]
		if ro == nil {
			return nil, fmt.Errorf("Go target %s: Rust crate %s not generated", w.Name, r)
		}
		spec.Deps = append(spec.Deps, bspec.NativeLib{Dir: e.use(ro.Compile), Name: ro.U.LibName()})
	}
	return g.finish("go", sanitizeName(w.Name), "//"+w.Src+":"+w.Name, e, "go", spec)
}
