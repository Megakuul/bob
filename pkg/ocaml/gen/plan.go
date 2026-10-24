package gen

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path"
	"runtime"
	"slices"
	"sort"
	"strings"

	"github.com/megakuul/bob/pkg/bob"
	"github.com/megakuul/bob/pkg/bob/diag"
	"github.com/megakuul/bob/pkg/engine"
	"github.com/megakuul/bob/pkg/engine/drv"
	"github.com/megakuul/bob/pkg/engine/fetch"
	"github.com/megakuul/bob/pkg/engine/ir"
	"github.com/megakuul/bob/pkg/engine/lock"
	"github.com/megakuul/bob/pkg/engine/srcs"
	"github.com/megakuul/bob/pkg/ocaml/internal/mlspec"
	"github.com/megakuul/bob/pkg/ocaml/resolve"
)

// scrubPatterns are the store path names of build-only references removed from executables
// (SPEC §03.5.3): the compiler (its standard library path is baked into every native program),
// the C compiler wrapper and compiler, glibc's headers and binutils.
var scrubPatterns = []string{`^ocaml-[0-9][0-9.]*$`, `^gcc-wrapper-`, `^gcc-[0-9][0-9.]*$`, `^glibc-.*-dev$`, `^binutils-`}

// planner plans the OCaml nodes of one PlanRequest. Planning is a pure function of the request,
// bob.lock's opam section, the pinned repository's opam files and the source files it lists.
type planner struct {
	ctx     context.Context
	req     engine.PlanRequest
	byID    map[string]*ir.Node
	own     map[string]*drv.Derivation
	out     engine.PlanResult
	sec     *lock.OpamSection
	repo    string              // readable directory of the repository snapshot ("" until needed)
	opam    map[string]*opamOut // planned opam packages by name
	walking map[string]bool
	fetched map[string]bool
	files   func(n *ir.Node) ([]string, error)
}

// opamOut is a planned opam package: its derivation and the closure of its dependencies.
type opamOut struct {
	name, version string
	drv           string
	out           string
	closure       []string // dependency names (transitive, sorted), without the package
}

func newPlanner(ctx context.Context, req engine.PlanRequest) (*planner, error) {
	pl := &planner{ctx: ctx, req: req, byID: map[string]*ir.Node{}, own: map[string]*drv.Derivation{},
		out: engine.PlanResult{Nodes: map[string]engine.Resolved{}}, opam: map[string]*opamOut{},
		walking: map[string]bool{}, fetched: map[string]bool{}}
	for _, n := range req.Graph.Nodes {
		pl.byID[n.ID] = n
	}
	pl.files = pl.sourceFiles
	if len(req.Lock) > 0 {
		v, err := lock.DecodeSection(lock.SectionOpam, req.Lock)
		if err != nil {
			return nil, err
		}
		pl.sec = v.(*lock.OpamSection)
	}
	return pl, nil
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
		t, err := targetOf(n)
		if err == nil {
			err = pl.planTarget(t)
		}
		if err != nil {
			errs = append(errs, err)
		}
	}
	return pl.out, engine.JoinErrors(errs)
}

// ---- inputs and jobs ----

// job finalises one bob-builder job and records it; it returns its path and output path.
func (pl *planner) job(name, system, kind, label string, args any, fx *engine.Fixup, in *engine.Inputs) (string, string, error) {
	p, d, err := engine.BuilderJob{Name: name, System: system, Builder: pl.req.Builder, Kind: kind, Label: label,
		Args: args, Fixup: fx, Inputs: in}.Finalize(pl)
	if err != nil {
		return "", "", err
	}
	if _, ok := pl.own[p]; !ok {
		pl.own[p] = d
		pl.out.Drvs = append(pl.out.Drvs, d)
	}
	return p, engine.OutPath(d), nil
}

// ---- toolchain ----

// toolchain is the OCaml, C and shell toolchain of one node.
type toolchain struct {
	path     []string // bin dirs in ToolAttrs order
	roots    map[string]string
	in       []engine.Resolved
	siteLib  string // findlib's site-lib (bytes, topfind)
	ocamlLib string
}

func (pl *planner) toolchainOf(t *target) (*toolchain, error) {
	tc := &toolchain{roots: map[string]string{}}
	for _, a := range mlspec.ToolAttrs {
		i, ok := t.spec.Tools[a]
		if !ok {
			return nil, fmt.Errorf("%s: no tool %s", t.n.Label, a)
		}
		rv, ok := pl.req.Inputs[t.n.Deps[i]]
		if !ok {
			return nil, fmt.Errorf("%s: tool %s is not planned", t.n.Label, a)
		}
		o := engine.PickOutput(rv.Outputs, "bin", "out")
		tc.path = append(tc.path, rv.Outputs[o]+"/bin")
		tc.roots[a] = rv.Outputs[o]
		tc.in = append(tc.in, engine.Resolved{Drv: rv.Drv, Outputs: map[string]string{o: rv.Outputs[o]}})
		if a == mlspec.AttrOCaml && !strings.HasSuffix(rv.Outputs[o], "-ocaml-"+OCamlVersion) {
			return nil, engine.Invalid(t.n, "toolchain", fmt.Sprintf("the pinned nixpkgs has %s, but bob's opam resolution fixes OCaml %s (other OCaml versions are not supported in v1)",
				path.Base(rv.Outputs[o]), OCamlVersion))
		}
	}
	tc.ocamlLib = tc.roots[mlspec.AttrOCaml] + "/lib/ocaml"
	tc.siteLib = tc.roots[mlspec.AttrFindlib] + "/lib/ocaml/" + OCamlVersion + "/site-lib"
	return tc, nil
}

func (tc *toolchain) addTo(in *engine.Inputs) {
	for _, rv := range tc.in {
		in.AddAll(rv)
	}
}

// hostSystem is the Nix system bob runs on; OCaml builds only natively in v1.
func hostSystem() string {
	return map[string]string{"amd64": "x86_64-linux", "arm64": "aarch64-linux"}[runtime.GOARCH]
}

func systemOf(n *ir.Node) (string, error) {
	var plat string
	if raw, ok := n.Proj["platform"]; !ok || json.Unmarshal(raw, &plat) != nil {
		return "", fmt.Errorf("%s has no platform", n.Label)
	}
	goos, goarch, _ := strings.Cut(plat, "-")
	arch := map[string]string{"amd64": "x86_64", "arm64": "aarch64"}[goarch]
	if arch == "" || goos != "linux" {
		return "", fmt.Errorf("%s: unsupported platform %s", n.Label, plat)
	}
	return arch + "-" + goos, nil
}

// ---- conf-* mappings ----

// confEnv is what packages with conf-* packages in their closure get: the mapped libraries'
// outputs (inputs, PATH, pkg-config, C include and library paths).
type confEnv struct {
	rvs      []engine.Resolved
	bin, pc  []string
	inc, lib []string
}

func (pl *planner) confOf(t *target, name string) (*confEnv, bool) {
	for _, c := range t.spec.Conf {
		if c.Opam != name {
			continue
		}
		ce := &confEnv{}
		for _, i := range c.Libs {
			rv, ok := pl.req.Inputs[t.n.Deps[i]]
			if !ok {
				continue
			}
			ce.rvs = append(ce.rvs, rv)
			for _, o := range sortedKeys(rv.Outputs) {
				p := rv.Outputs[o]
				ce.bin = append(ce.bin, p+"/bin")
				ce.pc = append(ce.pc, p+"/lib/pkgconfig", p+"/share/pkgconfig")
				ce.inc = append(ce.inc, p+"/include")
				ce.lib = append(ce.lib, p+"/lib")
			}
		}
		return ce, true
	}
	return nil, false
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// ---- opam packages ----

// repoDir makes the pinned snapshot valid and returns where bob can read it.
func (pl *planner) repoDir() (string, error) {
	if pl.repo != "" {
		return pl.repo, nil
	}
	if pl.req.Store == nil {
		return "", fmt.Errorf("opam: the planner has no store to read the opam repository")
	}
	s := snapshotOf(pl.sec.Repository)
	p, err := pl.req.Store.Ensure(pl.ctx, s.request(pl.sec.Repository.NarHash))
	if err != nil {
		return "", err
	}
	pl.repo = pl.req.Store.HostPath(p)
	return pl.repo, nil
}

// notLocked is BOB-E-1012 for a package bob.lock does not hold.
func notLocked(n *ir.Node, name string) error {
	e := diag.New(1012, "what", fmt.Sprintf("opam package %s (used by %s) is not in bob.lock; run bob lock", name, n.Label))
	e.DefinedAt = diag.Pos{File: n.DefinedAt.File, Line: n.DefinedAt.Line}
	return e
}

// opamPackage plans opam package name (and its dependencies) with t's toolchain.
func (pl *planner) opamPackage(t *target, tc *toolchain, system, name string) (*opamOut, error) {
	if o := pl.opam[name]; o != nil {
		return o, nil
	}
	if pl.sec == nil {
		return nil, notLocked(t.n, name)
	}
	p, ok := pl.sec.Packages[name]
	if !ok {
		return nil, notLocked(t.n, name)
	}
	if pl.walking[name] {
		return nil, fmt.Errorf("opam:%s.%s: dependency cycle in bob.lock", name, p.Version)
	}
	pl.walking[name] = true
	defer delete(pl.walking, name)
	label := "opam:" + name + "." + p.Version
	in := engine.NewInputs(pl.req.Builder)
	tc.addTo(in)
	closure := map[string]bool{}
	deps := map[string]string{}
	for _, d := range p.Deps {
		do, err := pl.opamPackage(t, tc, system, d)
		if err != nil {
			return nil, err
		}
		closure[d] = true
		for _, c := range do.closure {
			closure[c] = true
		}
	}
	names := sortedKeys(closure)
	var libs, stubs, bins []string
	for _, c := range names {
		do := pl.opam[c]
		in.Add(engine.Resolved{Drv: do.drv, Outputs: map[string]string{"out": do.out}}, "out")
		deps[c] = do.out
		libs = append(libs, do.out+"/lib")
		stubs = append(stubs, do.out+"/lib/stublibs")
		bins = append(bins, do.out+"/bin")
	}
	ce := &confEnv{}
	for _, c := range append(names, name) {
		if !strings.HasPrefix(c, "conf-") {
			continue
		}
		m, ok := pl.confOf(t, c)
		if !ok {
			return nil, diag.New(1023, "package", "opam:"+c+"."+pl.sec.Packages[c].Version,
				"reason", fmt.Sprintf("no nixpkgs mapping for this system package (add ocaml.Conf(%q, nixpkgs.Lib(p, \"<attr>\")) or extend bob's table)", c))
		}
		for _, rv := range m.rvs {
			in.AddAll(rv)
		}
		ce.bin, ce.pc = append(ce.bin, m.bin...), append(ce.pc, m.pc...)
		ce.inc, ce.lib = append(ce.inc, m.inc...), append(ce.lib, m.lib...)
	}
	out := drv.Placeholder("out")
	args := map[string]any{"name": name, "src": "", "path": append(append(slices.Clone(tc.path), bins...), ce.bin...),
		"env": map[string]string{}, "commands": [][]string{}, "installer": tc.roots[mlspec.AttrInstaller] + "/bin/opam-installer", "out": out}
	if !strings.HasPrefix(name, "conf-") { // conf-* packages run no checks: bob provides the library
		raw, err := pl.opamFile(name, p)
		if err != nil {
			return nil, err
		}
		f, err := resolve.Parse(raw)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", label, err)
		}
		if len(resolve.Items(f.Fields["patches"])) > 0 || hasExtraSource(f) {
			return nil, diag.NotImplemented("opam packages with patches or extra-source (" + label + ")")
		}
		env := &varEnv{name: name, version: p.Version, out: out, deps: deps, versions: map[string]string{},
			provided: map[string]string{}, ocamlLib: tc.ocamlLib}
		for n, lp := range pl.sec.Packages {
			env.versions[n] = lp.Version
		}
		for n, v := range resolve.Toolchain(OCamlVersion, DuneVersion, FindlibVersion) {
			env.versions[n] = v
			env.provided[n] = ""
		}
		env.provided["ocaml"] = tc.roots[mlspec.AttrOCaml]
		env.provided["ocamlfind"] = tc.roots[mlspec.AttrFindlib]
		env.provided["dune"] = tc.roots[mlspec.AttrDune]
		var cmds [][]string
		for _, field := range []string{"build", "install"} {
			c, err := commands(f.Fields[field], env)
			if err != nil {
				return nil, fmt.Errorf("%s: %s: %w", label, field, err)
			}
			cmds = append(cmds, c...)
		}
		args["commands"] = cmds
		if p.URL != "" {
			src, err := pl.source(name, p)
			if err != nil {
				return nil, err
			}
			in.AddSrc(src)
			args["src"] = src
		}
		e := map[string]string{
			"OCAMLPATH":             strings.Join(append(libs, tc.siteLib), ":"),
			"OCAMLFIND_DESTDIR":     out + "/lib",
			"OCAMLTOP_INCLUDE_PATH": tc.siteLib,
			"CAML_LD_LIBRARY_PATH":  strings.Join(append(append(stubs, out+"/lib/stublibs"), tc.ocamlLib+"/stublibs"), ":"),
		}
		if len(ce.pc) > 0 {
			e["PKG_CONFIG_PATH"] = strings.Join(ce.pc, ":")
			e["C_INCLUDE_PATH"] = strings.Join(ce.inc, ":")
			e["LIBRARY_PATH"] = strings.Join(ce.lib, ":")
		}
		args["env"] = e
	}
	dp, op, err := pl.job(drv.Name("opam", name, p.Version), system, "opam-build", label, args, nil, in)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", label, err)
	}
	o := &opamOut{name: name, version: p.Version, drv: dp, out: op, closure: names}
	pl.opam[name] = o
	return o, nil
}

func hasExtraSource(f *resolve.File) bool {
	for s := range f.Sections {
		if strings.HasPrefix(s, "extra-source") {
			return true
		}
	}
	return len(resolve.Items(f.Fields["extra-files"])) > 0
}

// opamFile reads a locked package's opam file from the pinned snapshot (digest checked).
func (pl *planner) opamFile(name string, p lock.OpamPackage) ([]byte, error) {
	dir, err := pl.repoDir()
	if err != nil {
		return nil, err
	}
	return readOpam(dir, name, p.Version, p.Opam)
}

// source plans the fetch of a package's archive (a flat file pinned by the opam checksum) and
// returns its store path.
func (pl *planner) source(name string, p lock.OpamPackage) (string, error) {
	algo, b64, _ := strings.Cut(p.Hash, "-")
	sum, err := base64.StdEncoding.DecodeString(b64)
	if err != nil || (algo != "sha512" && algo != "sha256") {
		return "", fmt.Errorf("opam:%s.%s: hash %q is not an SRI sha512/sha256 hash", name, p.Version, p.Hash)
	}
	hexSum := hex.EncodeToString(sum)
	sname := drv.Name("opam-src", name, p.Version)
	sp, err := drv.FixedOutputPath(algo, hexSum, false, sname)
	if err != nil {
		return "", err
	}
	if !pl.fetched[sp] {
		pl.fetched[sp] = true
		var urls []string
		if c := strings.TrimSuffix(strings.TrimSpace(envOr(cacheEnv)), "/"); c != "" {
			urls = append(urls, c+"/"+algo+"/"+hexSum[:2]+"/"+hexSum)
		}
		urls = append(urls, p.URL, opamCache+"/"+algo+"/"+hexSum[:2]+"/"+hexSum)
		pl.out.Fetches = append(pl.out.Fetches, fetch.Request{URLs: urls, Kind: fetch.File, Hash: p.Hash,
			Name: sname, Label: "opam:" + name + "." + p.Version})
	}
	return sp, nil
}

// ---- local libraries, executables and tests ----

// local is what a target needs from its dependencies: the local libraries (transitively),
// the opam packages they and the target name, and the findlib packages it uses directly.
type local struct {
	libs     []*target // local libraries, deps before dependents
	opamRoot []string  // opam package names (direct and through local libraries)
	packages []string  // findlib packages of the target itself (-package)
}

func (pl *planner) localOf(t *target) (*local, error) {
	l := &local{}
	seen := map[string]bool{}
	roots := map[string]bool{}
	var visit func(t *target, depth int) error
	visit = func(t *target, depth int) error {
		if depth > 256 {
			return fmt.Errorf("%s: local libraries nest deeper than 256", t.n.Label)
		}
		for _, o := range t.spec.Opam {
			roots[o.Name] = true
		}
		for _, i := range t.spec.Local {
			d := pl.byID[t.n.Deps[i]]
			if d == nil {
				return fmt.Errorf("%s: dependency %s is not in the graph", t.n.Label, t.n.Deps[i])
			}
			if seen[d.ID] {
				continue
			}
			seen[d.ID] = true
			dt, err := targetOf(d)
			if err != nil {
				return err
			}
			if err := visit(dt, depth+1); err != nil {
				return err
			}
			l.libs = append(l.libs, dt)
		}
		return nil
	}
	if err := visit(t, 0); err != nil {
		return nil, err
	}
	l.opamRoot = sortedKeys(roots)
	for _, o := range t.spec.Opam {
		if !slices.Contains(l.packages, o.Lib) {
			l.packages = append(l.packages, o.Lib)
		}
	}
	for _, i := range t.spec.Local {
		if d := pl.byID[t.n.Deps[i]]; d != nil {
			if dt, err := targetOf(d); err == nil && !slices.Contains(l.packages, dt.spec.Name) {
				l.packages = append(l.packages, dt.spec.Name)
			}
		}
	}
	return l, nil
}

// sourceFiles lists the files of n's source set exactly as bob imported them (R-FW-021).
func (pl *planner) sourceFiles(n *ir.Node) ([]string, error) {
	if pl.req.Root == "" {
		return nil, fmt.Errorf("%s: the planner has no repository root (PlanRequest.Root)", n.Label)
	}
	if !strings.HasPrefix(n.Label, "//") {
		return nil, diag.NotImplemented("OCaml packages of dependency repositories (" + n.Label + ")")
	}
	return srcs.ResolveAll(pl.req.Root, n.Srcs, n.DefinedAt.String(), nil)
}

// split sorts a target's files into modules and C stubs, relative to its directory.
func split(dir string, files []string) (modules, stubs []string) {
	for _, f := range files {
		rel := f
		if dir != "" {
			r, ok := strings.CutPrefix(f, dir+"/")
			if !ok {
				continue
			}
			rel = r
		}
		switch path.Ext(rel) {
		case ".ml", ".mli":
			modules = append(modules, rel)
		case ".c":
			stubs = append(stubs, rel)
		}
	}
	return modules, stubs
}

// nativeArgs are the C flags and link arguments of the Native libraries (bob.LinkInfo; nixpkgs
// libraries by their include and library directories).
func (pl *planner) nativeArgs(t *target, in *engine.Inputs) (cflags, clibs []string, err error) {
	for _, i := range t.spec.Native {
		id := t.n.Deps[i]
		rv, ok := pl.req.Inputs[id]
		if !ok {
			return nil, nil, fmt.Errorf("%s: Native library %s is not planned", t.n.Label, id)
		}
		in.AddAll(rv)
		if li := rv.LinkInfo; li != nil {
			for _, d := range li.IncludeDirs {
				cflags = append(cflags, "-I"+d)
			}
			clibs = append(clibs, linkArgs(li)...)
			continue
		}
		inc := rv.Outputs[engine.PickOutput(rv.Outputs, "dev", "out")] + "/include"
		lib := rv.Outputs[engine.PickOutput(rv.Outputs, "lib", "out")] + "/lib"
		cflags = append(cflags, "-I"+inc)
		clibs = append(clibs, "-L"+lib, "-Wl,-rpath,"+lib)
	}
	return cflags, clibs, nil
}

func linkArgs(li *bob.LinkInfo) []string {
	var args []string
	args = append(args, li.Archives...)
	for _, d := range li.LinkDirs {
		args = append(args, "-L"+d)
	}
	for _, l := range li.Libs {
		args = append(args, "-l"+l)
	}
	for _, d := range li.RuntimeDirs {
		args = append(args, "-Wl,-rpath,"+d)
	}
	return append(args, li.LinkArgs...)
}

// planTarget plans a library (ml-compile), an executable (ml-link) or a test (ml-link and its
// test-run).
func (pl *planner) planTarget(t *target) error {
	n := t.n
	system, err := systemOf(n)
	if err != nil {
		return err
	}
	if system != hostSystem() {
		return engine.Invalid(n, "platform", "OCaml cross-compilation is not supported in v1")
	}
	tc, err := pl.toolchainOf(t)
	if err != nil {
		return err
	}
	src, ok := pl.req.Sources[n.ID]
	if !ok {
		return fmt.Errorf("%s: its sources were not imported", n.Label)
	}
	files, err := pl.files(n)
	if err != nil {
		return err
	}
	dir := labelDir(n.Label)
	modules, stubs := split(dir, files)
	if len(modules) == 0 {
		return engine.Invalid(n, "Srcs", "no .ml or .mli file matches")
	}
	if t.spec.Type != mlspec.TypeLibrary && len(stubs) > 0 {
		return engine.Invalid(n, "Srcs", "C files belong to a library's Stubs")
	}
	lc, err := pl.localOf(t)
	if err != nil {
		return err
	}
	in := engine.NewInputs(pl.req.Builder)
	in.AddSrc(src)
	tc.addTo(in)
	var ocamlpath []string
	opamClosure := map[string]bool{}
	for _, r := range lc.opamRoot {
		o, err := pl.opamPackage(t, tc, system, r)
		if err != nil {
			return err
		}
		opamClosure[r] = true
		for _, c := range o.closure {
			opamClosure[c] = true
		}
	}
	var confLibs []string
	for _, name := range sortedKeys(opamClosure) {
		o := pl.opam[name]
		in.Add(engine.Resolved{Drv: o.drv, Outputs: map[string]string{"out": o.out}}, "out")
		ocamlpath = append(ocamlpath, o.out+"/lib")
		if strings.HasPrefix(name, "conf-") {
			if m, ok := pl.confOf(t, name); ok {
				for _, rv := range m.rvs {
					in.AddAll(rv)
				}
				confLibs = append(confLibs, m.lib...)
			}
		}
	}
	for _, l := range lc.libs {
		rv, ok := pl.req.Inputs[l.n.ID]
		if !ok {
			return fmt.Errorf("%s: local library %s is not planned", n.Label, l.n.Label)
		}
		ocamlpath = append(ocamlpath, in.Add(rv, "out")+"/lib")
	}
	cflags, clibs, err := pl.nativeArgs(t, in)
	if err != nil {
		return err
	}
	out := drv.Placeholder("out")
	env := map[string]string{
		"OCAMLPATH":            strings.Join(append(ocamlpath, tc.siteLib), ":"),
		"OCAMLFIND_DESTDIR":    out + "/lib",
		"CAML_LD_LIBRARY_PATH": tc.ocamlLib + "/stublibs",
	}
	if len(confLibs) > 0 {
		env["LIBRARY_PATH"] = strings.Join(confLibs, ":")
	}
	common := map[string]any{"name": t.spec.Name, "src": src, "dir": dir, "modules": modules, "packages": nonNil(lc.packages),
		"flags": nonNil(t.spec.Flags), "clibs": nonNil(clibs), "path": tc.path, "env": env, "dune": t.spec.Dune, "out": out}
	rv := engine.Resolved{}
	if t.spec.Type == mlspec.TypeLibrary {
		common["stubs"] = nonNil(stubs)
		common["wrapped"] = t.spec.Wrapped
		common["cflags"] = nonNil(cflags)
		dp, op, err := pl.job(drv.Name("ml", t.spec.Name), system, "ml-compile", n.Label, common, nil, in)
		if err != nil {
			return fmt.Errorf("%s: %w", n.Label, err)
		}
		pl.out.Nodes[n.ID] = engine.Resolved{Drv: dp, Outputs: map[string]string{"out": op}}
		return nil
	}
	for _, d := range confLibs { // conf libraries found at link time also at run time
		clibs = append(clibs, "-L"+d, "-Wl,-rpath,"+d)
	}
	common["clibs"] = nonNil(clibs)
	common["main"] = t.spec.Main
	common["scrub"] = scrubPatterns
	fx := &engine.Fixup{ShrinkRPath: true, Tools: engine.FixupTools{Patchelf: tc.roots[mlspec.AttrPatchelf] + "/bin/patchelf"},
		Forbidden: []string{tc.roots[mlspec.AttrOCaml], tc.roots[mlspec.AttrCC], tc.roots[mlspec.AttrBinutils], src, pl.req.Builder}}
	dp, op, err := pl.job(drv.Name("ml", t.spec.Name), system, "ml-link", n.Label, common, fx, in)
	if err != nil {
		return fmt.Errorf("%s: %w", n.Label, err)
	}
	rv = engine.Resolved{Drv: dp, Outputs: map[string]string{"out": op}, MainProgram: "bin/" + t.spec.Name}
	if t.spec.Type == mlspec.TypeTest {
		rin := engine.NewInputs(pl.req.Builder)
		rin.Add(rv, "out")
		args := map[string]any{"argv": append([]string{op + "/bin/" + t.spec.Name}, pl.req.TestArgs...), "format": "tap",
			"timeoutSeconds": defaultTestTimeoutS}
		tp, top, err := pl.job(drv.Name("mlrun", t.spec.Name), system, "test-run", n.Label, args, nil, rin)
		if err != nil {
			return fmt.Errorf("%s: %w", n.Label, err)
		}
		rv = engine.Resolved{Drv: tp, Outputs: map[string]string{"out": top}}
	}
	pl.out.Nodes[n.ID] = rv
	return nil
}

// defaultTestTimeoutS is the timeout of an OCaml test unit: 10 minutes (A92).
const defaultTestTimeoutS = 600

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
