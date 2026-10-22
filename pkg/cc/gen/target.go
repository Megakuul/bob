package gen

import (
	"encoding/json"
	"fmt"
	"path"
	"sort"
	"strconv"
	"strings"

	"github.com/megakuul/bob/pkg/bob"
	"github.com/megakuul/bob/pkg/cc/internal/ccspec"
	"github.com/megakuul/bob/pkg/engine"
	"github.com/megakuul/bob/pkg/engine/drv"
	"github.com/megakuul/bob/pkg/engine/ir"
)

// Source extensions bob compiles; every other file of a source set is a header.
var (
	cExts   = map[string]bool{".c": true}
	cxxExts = map[string]bool{".cc": true, ".cpp": true, ".cxx": true, ".c++": true, ".C": true}
	asmExts = map[string]bool{".S": true, ".s": true}
)

func isSource(f string) bool {
	e := path.Ext(f)
	return cExts[e] || cxxExts[e] || asmExts[e]
}

// ---- dependency closures ----

// Library kinds a C/C++ target can link.
const (
	libStatic   = "static"   // a bob static library: its own deps are linked by its consumers
	libShared   = "shared"   // a bob shared library
	libNixpkgs  = "nixpkgs"  // nixpkgs.Lib: flags from its pkg-config files
	libExternal = "external" // cc.External
	libOther    = "other"    // another ecosystem's Linkable (Rust, Go C libraries): its LinkInfo
)

// lib is one library in a target's closure.
type lib struct {
	n    *ir.Node
	kind string
	t    ccspec.Target   // libStatic, libShared
	x    ccspec.External // libExternal
}

func (pl *planner) libOf(id string) (*lib, error) {
	n := pl.byID[id]
	if n == nil {
		return nil, fmt.Errorf("dependency %s is not in the graph", id)
	}
	l := &lib{n: n, kind: libOther}
	switch {
	case n.Kind == ccspec.KindLibrary:
		if err := json.Unmarshal(n.Spec, &l.t); err != nil {
			return nil, internal(n, "spec: %v", err)
		}
		l.kind = libStatic
		if l.t.Type == ccspec.TypeShared {
			l.kind = libShared
		}
	case n.Kind == ccspec.KindExternal:
		if err := json.Unmarshal(n.Spec, &l.x); err != nil {
			return nil, internal(n, "spec: %v", err)
		}
		l.kind = libExternal
	case strings.HasPrefix(n.Kind, "nixpkgs."):
		l.kind = libNixpkgs
	}
	return l, nil
}

// depIDs returns the node ids of n's dep indices.
func depIDs(n *ir.Node, idx []int) ([]string, error) {
	out := make([]string, 0, len(idx))
	for _, i := range idx {
		if i < 0 || i >= len(n.Deps) {
			return nil, internal(n, "dep index %d out of range", i)
		}
		out = append(out, n.Deps[i])
	}
	return out, nil
}

// headerDeps is the order of -I of a target's deps (§03.1.3): its Deps and PrivDeps in order,
// then the libraries their public Deps propagate, breadth first.
func (pl *planner) headerDeps(n *ir.Node, t ccspec.Target) ([]*lib, error) {
	direct, err := depIDs(n, append(append([]int{}, t.Deps...), t.PrivDeps...))
	if err != nil {
		return nil, err
	}
	var out []*lib
	seen := map[string]bool{}
	queue := direct
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		if seen[id] {
			continue
		}
		seen[id] = true
		l, err := pl.libOf(id)
		if err != nil {
			return nil, err
		}
		out = append(out, l)
		if l.kind == libStatic || l.kind == libShared {
			pub, err := depIDs(l.n, l.t.Deps)
			if err != nil {
				return nil, err
			}
			queue = append(queue, pub...)
		}
	}
	return out, nil
}

// linkDeps is the link order of a program's or shared library's deps: each dep before the
// libraries it needs (a static library's deps, a shared library's public deps), each library
// once at its last position.
func (pl *planner) linkDeps(n *ir.Node, t ccspec.Target) ([]*lib, error) {
	direct, err := depIDs(n, append(append([]int{}, t.Deps...), t.PrivDeps...))
	if err != nil {
		return nil, err
	}
	var seq []*lib
	var visit func(id string, depth int) error
	visit = func(id string, depth int) error {
		if depth > 256 {
			return internal(n, "library dependencies nest deeper than 256")
		}
		l, err := pl.libOf(id)
		if err != nil {
			return err
		}
		seq = append(seq, l)
		var next []int
		switch l.kind {
		case libStatic:
			next = append(append(next, l.t.Deps...), l.t.PrivDeps...)
		case libShared:
			next = l.t.Deps
		}
		ids, err := depIDs(l.n, next)
		if err != nil {
			return err
		}
		for _, c := range ids {
			if err := visit(c, depth+1); err != nil {
				return err
			}
		}
		return nil
	}
	for _, id := range direct {
		if err := visit(id, 0); err != nil {
			return nil, err
		}
	}
	last := map[string]int{}
	for i, l := range seq {
		last[l.n.ID] = i
	}
	var out []*lib
	for i, l := range seq {
		if last[l.n.ID] == i {
			out = append(out, l)
		}
	}
	return out, nil
}

// ---- the build view of libraries ----

// pkgConfig is one PkgConfig entry of a cc-compile-link job (bop.PkgConfig).
type pkgConfig struct {
	Roots   []string `json:"roots"`
	Modules []string `json:"modules"`
	Include []string `json:"include"`
	LibDir  string   `json:"libDir"`
}

// libFlags renders libraries for one job: their -I and link arguments, pkg-config entries
// and inputs.
type libFlags struct {
	pl  *planner
	in  *inputs
	pcs []pkgConfig
	pcN map[string]int // node id → pkg-config entry
}

func (pl *planner) newLibFlags(in *inputs) *libFlags {
	return &libFlags{pl: pl, in: in, pcs: []pkgConfig{}, pcN: map[string]int{}}
}

// outputs adds l's outputs to the job's inputs and returns them.
func (f *libFlags) outputs(l *lib) (map[string]string, *bob.LinkInfo, error) {
	rv, ok := f.pl.req.Inputs[l.n.ID]
	if !ok {
		return nil, nil, internal(l.n, "is not planned")
	}
	outs := map[string]string{}
	names := []string{"out"}
	switch l.kind {
	case libNixpkgs:
		names = []string{"dev", "out", "lib"}
	case libOther:
		names = names[:0]
		for o := range rv.Outputs {
			names = append(names, o)
		}
		sort.Strings(names)
	}
	for _, o := range names {
		if _, ok := rv.Outputs[o]; ok {
			outs[o] = f.in.add(rv, o)
		}
	}
	return outs, rv.LinkInfo, nil
}

// pc returns the pkg-config entry of l (created on first use).
func (f *libFlags) pc(l *lib, outs map[string]string) int {
	if i, ok := f.pcN[l.n.ID]; ok {
		return i
	}
	e := pkgConfig{Roots: []string{}, Modules: []string{}, Include: []string{}}
	switch l.kind {
	case libNixpkgs:
		for _, o := range []string{"dev", "out", "lib"} {
			if p, ok := outs[o]; ok {
				e.Roots = append(e.Roots, p)
			}
		}
		if l.n.Nix != nil {
			e.Modules = append(e.Modules, l.n.Nix.Modules...)
		}
		e.Include = append(e.Include, outs[pickOutput(outs, "dev", "out")]+"/include")
		e.LibDir = outs[pickOutput(outs, "lib", "out")] + "/lib"
	case libExternal:
		e.Roots = append(e.Roots, outs["out"])
		e.Include = append(e.Include, outs["out"]+"/"+l.x.Include)
		e.LibDir = outs["out"] + "/lib"
	}
	f.pcN[l.n.ID] = len(f.pcs)
	f.pcs = append(f.pcs, e)
	return len(f.pcs) - 1
}

// include returns the compile arguments that make l's headers visible.
func (f *libFlags) include(l *lib) ([]string, error) {
	outs, li, err := f.outputs(l)
	if err != nil {
		return nil, err
	}
	switch l.kind {
	case libStatic, libShared:
		if l.t.Hdrs == "" {
			return nil, nil
		}
		return []string{"-I" + outs["out"] + "/include"}, nil
	case libExternal:
		if len(l.x.Libs) > 0 {
			return []string{"-I" + outs["out"] + "/" + l.x.Include}, nil
		}
		return []string{"@cflags:" + strconv.Itoa(f.pc(l, outs))}, nil
	case libNixpkgs:
		return []string{"@cflags:" + strconv.Itoa(f.pc(l, outs))}, nil
	}
	var args []string
	if li != nil {
		for _, d := range li.IncludeDirs {
			args = append(args, "-I"+d)
		}
	}
	return args, nil
}

// link returns the link arguments of l.
func (f *libFlags) link(l *lib) ([]string, error) {
	outs, li, err := f.outputs(l)
	if err != nil {
		return nil, err
	}
	switch l.kind {
	case libStatic:
		return []string{outs["out"] + "/lib/lib" + l.t.Name + ".a"}, nil
	case libShared:
		d := outs["out"] + "/lib"
		return []string{"-L" + d, "-l" + l.t.Name, "-Wl,-rpath," + d}, nil
	case libExternal:
		if len(l.x.Libs) == 0 {
			return []string{"@libs:" + strconv.Itoa(f.pc(l, outs))}, nil
		}
		d := outs["out"] + "/lib"
		args := []string{"-L" + d}
		for _, n := range l.x.Libs {
			args = append(args, "-l"+n)
		}
		return append(args, "-Wl,-rpath,"+d), nil
	case libNixpkgs:
		return []string{"@libs:" + strconv.Itoa(f.pc(l, outs))}, nil
	}
	return linkInfoArgs(li), nil
}

// linkInfoArgs are the link arguments of another ecosystem's LinkInfo.
func linkInfoArgs(li *bob.LinkInfo) []string {
	if li == nil {
		return nil
	}
	var args []string
	for _, d := range li.LinkDirs {
		args = append(args, "-L"+d)
	}
	args = append(args, li.Archives...)
	for _, n := range li.Libs {
		args = append(args, "-l"+n)
	}
	for _, d := range li.RuntimeDirs {
		args = append(args, "-Wl,-rpath,"+d)
	}
	return append(args, li.LinkArgs...)
}

// ---- compile flags ----

// language of a source file: "c", "c++" or "asm".
func language(f string) string {
	e := path.Ext(f)
	switch {
	case cxxExts[e]:
		return "c++"
	case asmExts[e]:
		return "asm"
	}
	return "c"
}

// stdFor is the -std of a file: the target's Std when it is of the file's language, else the
// language default (c17, c++20).
func stdFor(std, lang string) string {
	isCXX := strings.HasPrefix(std, "c++")
	switch {
	case lang == "asm":
		return ""
	case lang == "c++" && isCXX, lang == "c" && std != "" && !isCXX:
		return std
	case lang == "c++":
		return "c++20"
	}
	return "c17"
}

func compiler(lang string) string {
	if lang == "c++" {
		return "g++"
	}
	return "gcc"
}

// ownIncludes are the target's own include dirs, relative to the repository root (the working
// directory of every compile, and compile_commands.json's directory).
func ownIncludes(n *ir.Node, t ccspec.Target) []string {
	dir := labelDir(n.Label)
	var out []string
	if t.Hdrs != "" {
		out = append(out, "-I"+joinDir(dir, t.Hdrs))
	}
	if t.Private {
		out = append(out, "-I"+joinDir(dir, "."))
	}
	return out
}

// compileArgs are the flags of one source file in the fixed order of §03.1.3, without the
// compiler, -c, the file and -o: -std, profile and sanitiser flags, -fPIC (libraries), the
// prefix map, the defines (sorted by key), the own includes, the deps' includes, then Flags.
func compileArgs(t ccspec.Target, c config, file, srcRoot string, own, deps []string) []string {
	var args []string
	lang := language(file)
	if s := stdFor(t.Std, lang); s != "" {
		args = append(args, "-std="+s)
	}
	args = append(args, c.profileFlags()...)
	if t.Type == ccspec.TypeStatic || t.Type == ccspec.TypeShared {
		args = append(args, "-fPIC")
	}
	if srcRoot != "" {
		args = append(args, "-ffile-prefix-map="+srcRoot+"=.")
	}
	keys := make([]string, 0, len(t.Defines))
	for k := range t.Defines {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if v := t.Defines[k]; v != "" {
			args = append(args, "-D"+k+"="+v)
		} else {
			args = append(args, "-D"+k)
		}
	}
	args = append(args, own...)
	args = append(args, deps...)
	return append(args, t.Flags...)
}

// ---- planning ----

// planTarget plans a library, a program or a test (§03.1.3): one cc-compile-link derivation,
// for a test followed by its test-run.
func (pl *planner) planTarget(n *ir.Node) error {
	var t ccspec.Target
	if err := json.Unmarshal(n.Spec, &t); err != nil {
		return internal(n, "spec: %v", err)
	}
	c, err := configOf(n)
	if err != nil {
		return internal(n, "%v", err)
	}
	if c.system != hostSystem() {
		return crossNotImplemented(n, c)
	}
	src, ok := pl.req.Sources[n.ID]
	if !ok {
		return internal(n, "its sources were not imported")
	}
	files, err := pl.files(n)
	if err != nil {
		return err
	}
	var compiled []string
	for _, f := range files {
		if isSource(f) {
			compiled = append(compiled, f)
		}
	}
	if len(compiled) == 0 {
		return invalid(n, "Srcs", "no .c, .cc, .cpp, .cxx or .S file matches")
	}
	in := newInputs(pl.req.Builder)
	in.srcs[src] = true
	ts, err := pl.tools(n, t.Tools, in)
	if err != nil {
		return err
	}
	lf := pl.newLibFlags(in)
	hdeps, err := pl.headerDeps(n, t)
	if err != nil {
		return err
	}
	var depInc []string
	for _, l := range hdeps {
		a, err := lf.include(l)
		if err != nil {
			return err
		}
		depInc = append(depInc, a...)
	}
	own := ownIncludes(n, t)
	out := drv.Placeholder("out")
	type command struct {
		Argv []string `json:"argv"`
		Out  string   `json:"out"`
	}
	compile := []command{}
	var objs []string
	cxx := false
	for _, f := range compiled {
		lang := language(f)
		cxx = cxx || lang == "c++"
		obj := buildTop + "/obj/" + f + ".o"
		argv := append([]string{compiler(lang)}, compileArgs(t, c, f, src, own, depInc)...)
		argv = append(argv, "-c", f, "-o", obj)
		compile = append(compile, command{Argv: argv, Out: obj})
		objs = append(objs, obj)
	}
	var link command
	headers := []map[string]string{}
	name := t.Name
	var li *bob.LinkInfo
	switch t.Type {
	case ccspec.TypeStatic:
		lib := out + "/lib/lib" + name + ".a"
		link = command{Argv: append([]string{"ar", "rcsD", lib}, objs...), Out: lib}
	default:
		ldeps, err := pl.linkDeps(n, t)
		if err != nil {
			return err
		}
		var largs []string
		for _, l := range ldeps {
			a, err := lf.link(l)
			if err != nil {
				return err
			}
			largs = append(largs, a...)
			cxx = cxx || l.kind == libStatic && pl.hasCXX(l.n)
		}
		linker := "gcc"
		if cxx {
			linker = "g++"
		}
		target := out + "/bin/" + name
		argv := []string{linker}
		if t.Type == ccspec.TypeShared {
			target = out + "/lib/lib" + name + ".so"
			argv = append(argv, "-shared", "-Wl,-soname,lib"+name+".so")
		}
		argv = append(argv, "-o", target)
		argv = append(argv, objs...)
		argv = append(argv, largs...)
		argv = append(argv, c.sanitizeFlags()...)
		argv = append(argv, t.LinkOpts...)
		link = command{Argv: argv, Out: target}
	}
	if t.Hdrs != "" && (t.Type == ccspec.TypeStatic || t.Type == ccspec.TypeShared) {
		headers = append(headers, map[string]string{"from": src + "/" + joinDir(labelDir(n.Label), t.Hdrs), "to": out + "/include"})
	}
	args := map[string]any{"src": src, "path": ts.path, "pkgConfig": lf.pcs, "headers": headers, "compile": compile, "link": link}
	fx := &fixup{Strip: c.profile != string(bob.Debug), ShrinkRPath: true,
		Forbidden: append(append([]string{}, ts.forbids...), src, pl.req.Builder), Tools: ts.fixup}
	if t.Type == ccspec.TypeStatic {
		fx.ShrinkRPath = false
	}
	p, d, err := pl.job(drv.Name("cc", name), c.system, "cc-compile-link", n.Label, args, fx, in)
	if err != nil {
		return internal(n, "%v", err)
	}
	rv := engine.Resolved{Drv: p, Outputs: map[string]string{"out": outPath(d)}}
	switch t.Type {
	case ccspec.TypeStatic, ccspec.TypeShared:
		li, err = pl.linkInfo(n, t, outPath(d))
		if err != nil {
			return err
		}
		rv.LinkInfo = li
	case ccspec.TypeBinary:
		rv.MainProgram = "bin/" + name
	case ccspec.TypeTest:
		rv, err = pl.testRun(n, name, d, p, c.system)
		if err != nil {
			return err
		}
	}
	pl.out.Nodes[n.ID] = rv
	return nil
}

// hasCXX reports whether a bob library has C++ sources (its consumers then link with g++).
func (pl *planner) hasCXX(n *ir.Node) bool {
	files, err := pl.files(n)
	if err != nil {
		return false
	}
	for _, f := range files {
		if language(f) == "c++" {
			return true
		}
	}
	return false
}

// crossNotImplemented: the C/C++ toolchain is the host's; cross toolchains are task M7.4.
func crossNotImplemented(n *ir.Node, c config) error {
	return diagNotImplemented(fmt.Sprintf("C/C++ for %s from %s (%s, task M7.4)", c.system, hostSystem(), n.Label))
}

// linkInfo is what other ecosystems (cgo, Rust, OCaml) link for a bob library: its headers,
// the archive or shared library and, for a static library, what it needs itself. nixpkgs
// libraries contribute their include and library directories (their -l names come from their
// pkg-config files, which C/C++ consumers read at build time).
func (pl *planner) linkInfo(n *ir.Node, t ccspec.Target, out string) (*bob.LinkInfo, error) {
	li := &bob.LinkInfo{}
	if t.Hdrs != "" {
		li.IncludeDirs = append(li.IncludeDirs, out+"/include")
	}
	if t.Type == ccspec.TypeStatic {
		li.Archives = append(li.Archives, out+"/lib/lib"+t.Name+".a")
	} else {
		li.LinkDirs = append(li.LinkDirs, out+"/lib")
		li.Libs = append(li.Libs, t.Name)
		li.RuntimeDirs = append(li.RuntimeDirs, out+"/lib")
	}
	pub := ccspec.Target{Deps: t.Deps}
	hdeps, err := pl.headerDeps(n, pub)
	if err != nil {
		return nil, err
	}
	for _, l := range hdeps {
		rv := pl.req.Inputs[l.n.ID]
		li.IncludeDirs = append(li.IncludeDirs, staticIncludes(l, rv)...)
	}
	deps := t
	if t.Type == ccspec.TypeShared {
		deps = pub
	}
	ldeps, err := pl.linkDeps(n, deps)
	if err != nil {
		return nil, err
	}
	for _, l := range ldeps {
		rv := pl.req.Inputs[l.n.ID]
		o := rv.Outputs["out"]
		switch l.kind {
		case libStatic:
			li.Archives = append(li.Archives, o+"/lib/lib"+l.t.Name+".a")
		case libShared:
			li.LinkDirs = append(li.LinkDirs, o+"/lib")
			li.Libs = append(li.Libs, l.t.Name)
			li.RuntimeDirs = append(li.RuntimeDirs, o+"/lib")
		case libExternal:
			li.LinkDirs = append(li.LinkDirs, o+"/lib")
			li.Libs = append(li.Libs, l.x.Libs...)
			li.RuntimeDirs = append(li.RuntimeDirs, o+"/lib")
		case libNixpkgs:
			d := rv.Outputs[pickOutput(rv.Outputs, "lib", "out")] + "/lib"
			li.LinkDirs = append(li.LinkDirs, d)
			li.RuntimeDirs = append(li.RuntimeDirs, d)
			li.PkgConfig = append(li.PkgConfig, rv.Outputs[pickOutput(rv.Outputs, "dev", "out")]+"/lib/pkgconfig")
		default:
			if rv.LinkInfo != nil {
				li.LinkDirs = append(li.LinkDirs, rv.LinkInfo.LinkDirs...)
				li.Archives = append(li.Archives, rv.LinkInfo.Archives...)
				li.Libs = append(li.Libs, rv.LinkInfo.Libs...)
				li.RuntimeDirs = append(li.RuntimeDirs, rv.LinkInfo.RuntimeDirs...)
				li.LinkArgs = append(li.LinkArgs, rv.LinkInfo.LinkArgs...)
			}
		}
	}
	return li, nil
}

// staticIncludes are the include dirs of l known without building it.
func staticIncludes(l *lib, rv engine.Resolved) []string {
	switch l.kind {
	case libStatic, libShared:
		if l.t.Hdrs != "" {
			return []string{rv.Outputs["out"] + "/include"}
		}
	case libExternal:
		return []string{rv.Outputs["out"] + "/" + l.x.Include}
	case libNixpkgs:
		return []string{rv.Outputs[pickOutput(rv.Outputs, "dev", "out")] + "/include"}
	default:
		if rv.LinkInfo != nil {
			return rv.LinkInfo.IncludeDirs
		}
	}
	return nil
}

// testRun plans the test-run job of a test: run the program once; TAP output gives the cases.
func (pl *planner) testRun(n *ir.Node, name string, binD *drv.Derivation, binDrv, system string) (engine.Resolved, error) {
	in := newInputs(pl.req.Builder)
	in.add(engine.Resolved{Drv: binDrv, Outputs: map[string]string{"out": outPath(binD)}}, "out")
	argv := append([]string{outPath(binD) + "/bin/" + name}, pl.req.TestArgs...)
	args := map[string]any{"argv": argv, "format": "tap", "timeoutSeconds": defaultTestTimeoutS}
	p, d, err := pl.job(drv.Name("ccrun", name), system, "test-run", n.Label, args, nil, in)
	if err != nil {
		return engine.Resolved{}, internal(n, "%v", err)
	}
	return engine.Resolved{Drv: p, Outputs: map[string]string{"out": outPath(d)}}, nil
}

// defaultTestTimeoutS is the timeout of a C/C++ test unit: 10 minutes (A92).
const defaultTestTimeoutS = 600
