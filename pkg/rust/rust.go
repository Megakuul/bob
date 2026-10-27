package rust

import (
	"fmt"
	"os"
	"path"
	"reflect"
	"regexp"
	"runtime"
	"runtime/debug"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/megakuul/bob/pkg/bob"
	"github.com/megakuul/bob/pkg/bob/ext"
	"github.com/megakuul/bob/pkg/nixpkgs"
	"github.com/megakuul/bob/pkg/nixpkgs/nixext"
	"github.com/megakuul/bob/pkg/rust/internal/rsspec"
)

// Kinds of the Rust ecosystem (SPEC §03.3). Every crate unit depends on the target platform;
// target crates and build-script runs also on the profile (R-FW-038; host units never, the
// planner keeps them profile-free, R-FW-035); a variant's Features of a crate are added on top
// of its Enable list (§04).
func init() {
	ext.Register(ext.Kind{Name: rsspec.KindCrate, Axes: []ext.Axis{ext.AxisPlatform, ext.AxisProfile, ext.AxisFeatures}, Unit: "rust"})
	ext.Register(ext.Kind{Name: rsspec.KindTest, Axes: []ext.Axis{ext.AxisPlatform, ext.AxisProfile, ext.AxisFeatures}})
	ext.Register(ext.Kind{Name: rsspec.KindTests, Axes: []ext.Axis{ext.AxisPlatform}})
}

// rustcUnwrapped is the target system's own rustc: its standard library for cross builds
// (R-FW-034).
var rustcUnwrapped = nixext.New("rustc.unwrapped")

// Edition is a Rust edition.
type Edition string

// Editions bob accepts.
const (
	Edition2018 Edition = "2018"
	Edition2021 Edition = "2021"
	Edition2024 Edition = "2024"
)

// Dep is a dependency of a crate: a crates.io crate (Crate) or a local crate (*Lib, (*Lib).With).
type Dep interface{ rustDep() }

// DepOpt modifies a crates.io dependency.
type DepOpt func(*depOpts)

type depOpts struct {
	features  []string
	noDefault bool
	rename    string
}

// CrateDep is a declared crates.io crate (Crate, Indirect): the one place its name is written
// (R-FW-072). A Crate handle is a Dep; both are the argument of Uses. An Indirect handle is not
// a Dep of the crate that names it (it only carries options).
type CrateDep struct {
	name, req string
	o         depOpts
	indirect  string // the bob.go position of rust.Indirect; "" for rust.Crate
}

func (*CrateDep) rustDep() {}

// Crate declares a crates.io dependency (the one place its name is written); req is a Cargo
// version requirement ("1", "^0.13", "=1.38.0"). Resolved into bob.lock by `bob lock`
// (R-RS-002). The handle is a Dep and the argument of Uses.
//
//	rust.Crate("serde", "1", rust.Features("derive"))
func Crate(name, req string, opts ...DepOpt) *CrateDep {
	d := &CrateDep{name: name, req: req}
	for _, o := range opts {
		if o != nil {
			o(&d.o)
		}
	}
	return d
}

// Indirect declares a crates.io crate that is not a direct dependency (it is in the resolved
// graph through another crate) so that an option can refer to it. bob lock fails with
// BOB-E-1015 at this position when the graph does not contain it.
//
//	rust.Uses(rust.Indirect("openssl-sys"), nixpkgs.Openssl.Lib(p))
func Indirect(name string) *CrateDep {
	pos := callerPos()
	if pos == "" {
		pos = "rust.Indirect"
	}
	return &CrateDep{name: name, indirect: pos}
}

// Features enables features of the dependency.
func Features(fs ...string) DepOpt {
	return func(o *depOpts) { o.features = append(o.features, fs...) }
}

// NoDefaultFeatures turns the dependency's default features off (default-features = false).
func NoDefaultFeatures() DepOpt { return func(o *depOpts) { o.noDefault = true } }

// Rename sets the name the code uses for the dependency (`use codeName::…`).
func Rename(codeName string) DepOpt { return func(o *depOpts) { o.rename = codeName } }

// Option is a graph-wide setting attached to any local crate's Options.
type Option interface{ rustOption() }

type usesOption struct {
	crate *CrateDep
	libs  []bob.Linkable
}

func (usesOption) rustOption() {}

// Uses gives a crates.io crate's build script native packages (PATH, PKG_CONFIG_PATH,
// include/lib dirs). Applies to every unit of that crate in the graph. The crate is a handle of
// Crate or Indirect.
//
//	rust.Uses(rust.Indirect("openssl-sys"), nixpkgs.Openssl.Lib(p))
func Uses(crate *CrateDep, libs ...bob.Linkable) Option {
	return usesOption{crate: crate, libs: libs}
}

// LibOpts configures Library, ProcMacro, StaticLib and CDylib.
type LibOpts struct {
	Name        string              // crate name; default: the directory name ('-' kept; code name '-'→'_')
	Edition     Edition             // default Edition2021
	Root        string              // default "src/lib.rs"
	Srcs        bob.Sources         // default src/**, build.rs, Headers/**, tests/** (tests only)
	Deps        []Dep               // [dependencies]
	BuildDeps   []Dep               // build-dependencies (host units)
	DevDeps     []Dep               // tests only
	Features    map[string][]string // this crate's feature table (Cargo syntax: "dep:x", "x/f", "x?/f")
	Default     []string            // default features
	BuildScript string              // "build.rs" or "" (default: "build.rs" if the file exists)
	Native      []bob.Linkable      // bob C/C++ libraries, cc.External or nixpkgs.Lib linked into this crate
	Headers     bob.Include         // StaticLib/CDylib only: bob.IncludeDir("include") exported to C consumers
	Toolchain   bob.Tool            // default nixpkgs.Rustc.Tool(p) (R-RS-004)
	Options     []Option
}

// BinOpts configures Binary.
type BinOpts struct {
	Name        string // default: the directory name
	Root        string // default "src/main.rs"
	Edition     Edition
	Srcs        bob.Sources
	Deps        []Dep
	BuildDeps   []Dep
	DevDeps     []Dep
	Features    map[string][]string
	Default     []string
	Enable      []string // features enabled when built as a root target
	BuildScript string
	Native      []bob.Linkable
	Toolchain   bob.Tool
	Options     []Option
}

// Lib is a local Rust library or proc-macro crate (label //<dir>:<Name>), usable as Dep.
type Lib struct {
	bob.Node
	c *crate
}

func (*Lib) rustDep()   {}
func (*Lib) rustCrate() {}

// With enables features of a local crate for this edge.
func (l *Lib) With(features ...string) Dep { return libDep{lib: l, features: features} }

type libDep struct {
	lib      *Lib
	features []string
}

func (libDep) rustDep() {}

// Bin is a Rust program (label //<dir>:<Name>); its output holds bin/<Name>.
type Bin struct {
	bob.Node
	c *crate
}

func (*Bin) rustCrate() {}

// MainProgram is the program's path in the package output.
func (b *Bin) MainProgram() string { return "bin/" + b.c.spec.Name }

// CLib is a Rust staticlib or cdylib (label //<dir>:<Name>): lib<name>.a or lib<name>.so plus
// the Headers as include/. It implements cc.Linkable for C, C++ and cgo consumers.
type CLib struct {
	bob.Node
	c *crate
}

func (*CLib) rustCrate() {}

// LinkInfo is filled by bob's generator; the evaluator only records the library's identity.
func (*CLib) LinkInfo() bob.LinkInfo { return bob.LinkInfo{} }

// crate is what a crate handle keeps for its tests: its options and spec.
type crate struct {
	p     *bob.P
	spec  rsspec.Crate
	deps  []bob.Package
	srcs  bob.Sources
	given bool   // Srcs were set by the user
	dir   string // repository-relative crate directory
	local bool   // the crate is in this repository (its directory can be read)
	node  *bob.Node
}

var (
	crateName = sync.OnceValue(func() *regexp.Regexp { return regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_-]{0,63}$`) })
	feature   = sync.OnceValue(func() *regexp.Regexp { return regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_+.-]{0,127}$`) })
	featRef   = sync.OnceValue(func() *regexp.Regexp {
		return regexp.MustCompile(`^(dep:[A-Za-z0-9_-]+|[A-Za-z0-9_][A-Za-z0-9_+.-]*|[A-Za-z0-9_-]+\??/[A-Za-z0-9_][A-Za-z0-9_+.-]*)$`)
	})
)

// Library is an rlib, usable as Dep. Label //<dir>:<Name>.
//
//	var Core = bob.Define(func(p *bob.P) *rust.Lib {
//		return rust.Library(p, rust.LibOpts{Deps: []rust.Dep{rust.Crate("serde", "1", rust.Features("derive"))}})
//	})
func Library(p *bob.P, o LibOpts) *Lib {
	c := newCrate(p, rsspec.TypeLib, libCommon(o), o.Headers.Dir())
	return &Lib{Node: *c.node, c: c}
}

// ProcMacro is a host-compiled proc-macro crate, usable as Dep.
//
//	var Macros = bob.Define(func(p *bob.P) *rust.Lib {
//		return rust.ProcMacro(p, rust.LibOpts{Deps: []rust.Dep{rust.Crate("syn", "2", rust.Features("full")), rust.Crate("quote", "1")}})
//	})
func ProcMacro(p *bob.P, o LibOpts) *Lib {
	c := newCrate(p, rsspec.TypeProcMacro, libCommon(o), o.Headers.Dir())
	return &Lib{Node: *c.node, c: c}
}

// StaticLib builds lib<name>.a (+ Headers); implements cc.Linkable for C and cgo consumers.
//
//	var RCore = bob.Define(func(p *bob.P) *rust.CLib {
//		return rust.StaticLib(p, rust.LibOpts{Name: "rcore", Headers: bob.IncludeDir("include")})
//	})
func StaticLib(p *bob.P, o LibOpts) *CLib {
	c := newCrate(p, rsspec.TypeStaticLib, libCommon(o), o.Headers.Dir())
	return &CLib{Node: *c.node, c: c}
}

// CDylib builds lib<name>.so (+ Headers); implements cc.Linkable.
//
//	var Plugin = bob.Define(func(p *bob.P) *rust.CLib { return rust.CDylib(p, rust.LibOpts{Name: "plugin"}) })
func CDylib(p *bob.P, o LibOpts) *CLib {
	c := newCrate(p, rsspec.TypeCDylib, libCommon(o), o.Headers.Dir())
	return &CLib{Node: *c.node, c: c}
}

// Binary builds bin/<Name>.
//
//	var Demo = bob.Define(func(p *bob.P) *rust.Bin {
//		return rust.Binary(p, rust.BinOpts{Deps: []rust.Dep{rust.Crate("tokio", "1", rust.Features("rt", "macros")), Core(p)}})
//	})
func Binary(p *bob.P, o BinOpts) *Bin {
	c := newCrate(p, rsspec.TypeBin, o, "")
	return &Bin{Node: *c.node, c: c}
}

// libCommon is LibOpts in BinOpts form (the options both share; Enable is a binary's only).
func libCommon(o LibOpts) BinOpts {
	return BinOpts{Name: o.Name, Edition: o.Edition, Root: o.Root, Srcs: o.Srcs, Deps: o.Deps, BuildDeps: o.BuildDeps,
		DevDeps: o.DevDeps, Features: o.Features, Default: o.Default, BuildScript: o.BuildScript, Native: o.Native,
		Toolchain: o.Toolchain, Options: o.Options}
}

func newCrate(p *bob.P, typ string, o BinOpts, headers string) *crate {
	c := &crate{p: p, given: !isZeroSources(o.Srcs), srcs: o.Srcs}
	var problems []string
	ed := o.Edition
	if ed == "" {
		ed = Edition2021
	}
	switch ed {
	case Edition2018, Edition2021, Edition2024:
	default:
		problems = append(problems, fmt.Sprintf("edition %q is not 2018, 2021 or 2024", ed))
		ed = Edition2021
	}
	root := o.Root
	if root == "" {
		root = "src/lib.rs"
		if typ == rsspec.TypeBin {
			root = "src/main.rs"
		}
	}
	if !validRel(root) {
		problems = append(problems, fmt.Sprintf("Root %q is not a file inside the crate directory", root))
		root = "src/lib.rs"
	}
	tc := o.Toolchain
	if tc == nil {
		tc = nixpkgs.Rustc.Tool(p)
	}
	// The host's C toolchain links host units (build scripts, proc-macros) and native builds.
	host := nixext.Toolchain(p.Host())
	c.deps = []bob.Package{tc, host.CC.Tool(p), host.Bintools.Tool(p)}
	s := rsspec.Crate{Name: o.Name, Type: typ, Edition: string(ed), Root: root, Toolchain: 0, CC: 1, Binutils: 2,
		PkgConfig: -1, Features: map[string][]string{}, Default: sortedSet(o.Default), Enable: sortedSet(o.Enable),
		Native: []int{}, Uses: []rsspec.Use{}, Headers: path.Clean(headers)}
	if xt := p.Toolchain(); xt.Cross() {
		// R-FW-033/034: host rustc --target, the target's cross C compiler as linker, and the
		// standard library of the target system's own rustc (same source and version).
		s.Cross = &rsspec.Cross{Triple: xt.Triple, Prefix: xt.Prefix, CC: 3, Binutils: 4, Std: 5}
		xc := nixext.Toolchain(p)
		c.deps = append(c.deps, xc.CC.Tool(p), xc.Bintools.Tool(p), rustcUnwrapped.Pkg(p, nixext.Outputs(nixext.Out)))
	}
	if headers == "" {
		s.Headers = ""
	} else if !validRel(s.Headers) || s.Headers == "." {
		problems = append(problems, fmt.Sprintf("Headers %q is not a directory inside the crate directory", headers))
		s.Headers = ""
	}
	if headers != "" && typ != rsspec.TypeStaticLib && typ != rsspec.TypeCDylib {
		problems = append(problems, "Headers is only for StaticLib and CDylib")
	}
	for f, vs := range o.Features {
		if !feature().MatchString(f) {
			problems = append(problems, fmt.Sprintf("feature name %q is invalid", f))
			continue
		}
		for _, v := range vs {
			if !featRef().MatchString(v) {
				problems = append(problems, fmt.Sprintf("feature %q enables %q, which is not a feature, \"dep:x\", \"x/f\" or \"x?/f\"", f, v))
			}
		}
		s.Features[f] = sortedSet(vs)
	}
	for _, f := range append(slices.Clone(s.Default), s.Enable...) {
		if _, ok := s.Features[f]; !ok && !strings.Contains(f, "/") && !strings.HasPrefix(f, "dep:") {
			problems = append(problems, fmt.Sprintf("feature %q is not in the crate's Features", f))
		}
	}
	var dp []string
	s.Deps, dp = c.convertDeps(o.Deps, "Deps")
	problems = append(problems, dp...)
	s.BuildDeps, dp = c.convertDeps(o.BuildDeps, "BuildDeps")
	problems = append(problems, dp...)
	s.DevDeps, dp = c.convertDeps(o.DevDeps, "DevDeps")
	problems = append(problems, dp...)
	for i, l := range o.Native {
		if l == nil {
			problems = append(problems, fmt.Sprintf("Native[%d] is nil", i))
			continue
		}
		s.Native = append(s.Native, len(c.deps))
		c.deps = append(c.deps, l)
	}
	for _, opt := range o.Options {
		u, ok := opt.(usesOption)
		if !ok {
			continue
		}
		if u.crate == nil {
			problems = append(problems, "rust.Uses: the crate is nil; pass a handle of rust.Crate or rust.Indirect")
			continue
		}
		if !crateName().MatchString(u.crate.name) {
			problems = append(problems, fmt.Sprintf("rust.Uses(%q): not a crate name", u.crate.name))
			continue
		}
		use := rsspec.Use{Crate: u.crate.name, Libs: []int{}, Indirect: u.crate.indirect}
		for _, l := range u.libs {
			if l == nil {
				continue
			}
			use.Libs = append(use.Libs, len(c.deps))
			c.deps = append(c.deps, l)
		}
		s.Uses = append(s.Uses, use)
	}
	if len(s.Uses) > 0 {
		s.PkgConfig = len(c.deps)
		c.deps = append(c.deps, nixpkgs.PkgConfig.Tool(p))
	}
	sort.SliceStable(s.Uses, func(i, j int) bool { return s.Uses[i].Crate < s.Uses[j].Crate })
	s.BuildScript = o.BuildScript
	if s.BuildScript != "" && !validRel(s.BuildScript) {
		problems = append(problems, fmt.Sprintf("BuildScript %q is not a file inside the crate directory", s.BuildScript))
		s.BuildScript = ""
	}
	c.spec = s
	c.finish(problems)
	return c
}

// finish creates the node: it reads the crate directory for the defaults (name, build.rs,
// sources) and records the problems as definition errors.
func (c *crate) finish(problems []string) {
	dir, local := siteDir()
	c.dir, c.local = dir, local
	if c.spec.Name == "" {
		base := path.Base(dir)
		if dir == "" || base == "." || base == "/" {
			base = "crate"
		}
		c.spec.Name = base
	}
	if !crateName().MatchString(c.spec.Name) {
		problems = append(problems, fmt.Sprintf("crate name %q is invalid (letters, digits, '_', '-'); set Name", c.spec.Name))
		c.spec.Name = "invalid"
	}
	if c.spec.BuildScript == "" && c.fileExists("build.rs") {
		c.spec.BuildScript = "build.rs"
	}
	srcs := c.srcs
	if !c.given {
		patterns := []string{"src/**"}
		if c.spec.BuildScript != "" && c.fileExists(c.spec.BuildScript) {
			patterns = append(patterns, escape(c.spec.BuildScript))
		}
		if c.spec.Headers != "" && c.dirExists(c.spec.Headers) {
			patterns = append(patterns, escape(c.spec.Headers)+"/**")
		}
		srcs = bob.Glob(patterns...)
	}
	label := strings.ToLower(c.spec.Name)
	n := ext.New(c.p, rsspec.KindCrate, label, c.deps, []bob.Sources{srcs}, c.spec)
	c.node = &n
	c.dir = n.Label().Dir
	for _, pr := range problems {
		ext.Errorf("%s: %s: %s", n.DefinedAt(), n.Label(), pr)
	}
}

// convertDeps turns Dep values into the spec form and appends local crates to c.deps.
func (c *crate) convertDeps(ds []Dep, field string) ([]rsspec.Dep, []string) {
	out := []rsspec.Dep{}
	var problems []string
	for i, d := range ds {
		switch d := d.(type) {
		case *CrateDep:
			if d == nil {
				problems = append(problems, fmt.Sprintf("%s[%d] is nil", field, i))
				continue
			}
			if d.indirect != "" {
				problems = append(problems, fmt.Sprintf("%s: rust.Indirect(%q) declared at %s is not a dependency; declare it with rust.Crate(name, req)", field, d.name, d.indirect))
				continue
			}
			name := d.name
			if d.o.rename != "" {
				name = d.o.rename
			}
			if !crateName().MatchString(d.name) || !crateName().MatchString(name) {
				problems = append(problems, fmt.Sprintf("%s: rust.Crate(%q): not a valid crate name", field, d.name))
				continue
			}
			if strings.TrimSpace(d.req) == "" {
				problems = append(problems, fmt.Sprintf("%s: rust.Crate(%q, %q): the version requirement is empty", field, d.name, d.req))
				continue
			}
			out = append(out, rsspec.Dep{Name: name, Crate: d.name, Req: d.req, Local: -1, Features: sortedSet(d.o.features),
				NoDefault: d.o.noDefault})
		case *Lib:
			if d == nil || d.c == nil {
				problems = append(problems, fmt.Sprintf("%s[%d] is a nil crate", field, i))
				continue
			}
			out = append(out, rsspec.Dep{Name: d.c.spec.Name, Local: len(c.deps), Features: []string{}})
			c.deps = append(c.deps, d)
		case libDep:
			if d.lib == nil || d.lib.c == nil {
				problems = append(problems, fmt.Sprintf("%s[%d] is a nil crate", field, i))
				continue
			}
			out = append(out, rsspec.Dep{Name: d.lib.c.spec.Name, Local: len(c.deps), Features: sortedSet(d.features)})
			c.deps = append(c.deps, d.lib)
		case nil:
			problems = append(problems, fmt.Sprintf("%s[%d] is nil", field, i))
		default:
			problems = append(problems, fmt.Sprintf("%s[%d]: unsupported dependency %T", field, i, d))
		}
	}
	return out, problems
}

// TestOpts selects what to test: the unit tests of Crate (--test) and every tests/*.rs
// integration test (one unit per file).
type TestOpts struct {
	Crate    interface{ rustCrate() } // *Lib, *Bin or *CLib
	Args     []string
	Services []bob.Service
	Timeout  time.Duration // per test unit; default 10 minutes (A92)
}

// TestSet is a set of Rust test units, or one unit when the crate has no integration tests.
// (SPEC §03.3.1 names both the type and the constructor Test, which Go does not allow; the
// type is TestSet, as in pkg/golang.)
type TestSet struct{ bob.Node }

// Test declares the tests of a local crate. Units //<dir>:<crate>_test and
// //<dir>:<crate>_test_<file stem>; with integration tests the set itself is
// //<dir>:<crate>_tests.
//
//	var CoreTest = bob.Define(func(p *bob.P) *rust.TestSet { return rust.Test(p, rust.TestOpts{Crate: Core(p)}) })
func Test(p *bob.P, o TestOpts) *TestSet {
	var c *crate
	switch k := o.Crate.(type) {
	case *Lib:
		if k != nil {
			c = k.c
		}
	case *Bin:
		if k != nil {
			c = k.c
		}
	case *CLib:
		if k != nil {
			c = k.c
		}
	}
	if c == nil || c.node == nil {
		ext.Errorf("rust.Test: TestOpts.Crate must be a local crate (*rust.Lib, *rust.Bin or *rust.CLib)")
		return &TestSet{}
	}
	if o.Timeout < 0 {
		ext.Errorf("%s: rust.Test: TestOpts.Timeout is negative", c.node.DefinedAt())
		o.Timeout = 0
	}
	base := strings.ToLower(strings.ReplaceAll(c.spec.Name, "-", "_")) + "_test"
	var files []string
	if c.local {
		files = testFiles(c.dir)
	}
	units := []bob.Package{c.unit(p, base, "", o)}
	for _, f := range files {
		stem := strings.TrimSuffix(path.Base(f), ".rs")
		units = append(units, c.unit(p, base+"_"+strings.ToLower(stem), f, o))
	}
	if len(units) == 1 {
		return units[0].(*TestSet)
	}
	return &TestSet{Node: ext.NewIn(p, rsspec.KindTests, c.dir, base+"s", units, nil, rsspec.Tests{Crate: c.spec.Name})}
}

// unit creates one test unit of the crate: its unit tests (file "") or one integration test.
func (c *crate) unit(p *bob.P, name, file string, o TestOpts) *TestSet {
	deps := []bob.Package{c.node}
	svcs, deps := ext.Services(o.Services, deps)
	spec := rsspec.Test{Crate: 0, File: file, Args: slices.Clone(o.Args), Services: svcs, TimeoutS: int64(o.Timeout / time.Second)}
	if em := nixext.Toolchain(p).Emulator; em.Name() != "" {
		spec.Emulator, spec.EmulatorCmd = len(deps), p.Toolchain().EmulatorCmd
		deps = append(deps, em.Tool(p))
	}
	if spec.Args == nil {
		spec.Args = []string{}
	}
	// The unit's sources: the crate's own plus the integration tests (tests/**).
	srcs := c.srcs
	if !c.given {
		patterns := []string{"src/**"}
		if c.spec.BuildScript != "" && c.fileExists(c.spec.BuildScript) {
			patterns = append(patterns, escape(c.spec.BuildScript))
		}
		if c.spec.Headers != "" && c.dirExists(c.spec.Headers) {
			patterns = append(patterns, escape(c.spec.Headers)+"/**")
		}
		if c.dirExists("tests") {
			patterns = append(patterns, "tests/**")
		}
		srcs = bob.Glob(patterns...)
	}
	return &TestSet{Node: ext.NewIn(p, rsspec.KindTest, c.dir, name, deps, []bob.Sources{srcs}, spec)}
}

// testFiles lists the integration tests (tests/*.rs) of the crate directory, sorted.
func testFiles(dir string) []string {
	ents, err := os.ReadDir(path.Join(rootOf(dir), "tests"))
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range ents {
		n := e.Name()
		if e.Type().IsRegular() && strings.HasSuffix(n, ".rs") && !strings.HasPrefix(n, ".") {
			out = append(out, "tests/"+n)
		}
	}
	sort.Strings(out)
	return out
}

// siteDir is the repository-relative directory of the bob.go whose definition is running (the
// label directory ext.New gives the node; "" = the repository root). ok is false for a bob.go of
// a dependency repository: its files are not in the working directory, so the defaults that
// read the crate directory (build.rs, tests/*.rs) are not applied there.
func siteDir() (dir string, ok bool) {
	bi, found := debug.ReadBuildInfo()
	if !found || bi.Main.Path == "" {
		return "", false
	}
	var pcs [64]uintptr
	n := runtime.Callers(2, pcs[:])
	frames := runtime.CallersFrames(pcs[:n])
	for {
		f, more := frames.Next()
		if path.Base(f.File) == "bob.go" {
			rel, inMain := strings.CutPrefix(f.File, bi.Main.Path+"/")
			if !inMain {
				return "", false
			}
			if d := path.Dir(rel); d != "." {
				return d, true
			}
			return "", true
		}
		if !more {
			return "", false
		}
	}
}

// callerPos is the "<dir>/bob.go:<line>" position of the running bob.go call ("" outside a
// bob.go of this build), for messages that point at a handle's declaration.
func callerPos() string {
	bi, found := debug.ReadBuildInfo()
	if !found || bi.Main.Path == "" {
		return ""
	}
	var pcs [64]uintptr
	n := runtime.Callers(2, pcs[:])
	frames := runtime.CallersFrames(pcs[:n])
	for {
		f, more := frames.Next()
		if path.Base(f.File) == "bob.go" {
			if rel, ok := strings.CutPrefix(f.File, bi.Main.Path+"/"); ok {
				return fmt.Sprintf("%s:%d", rel, f.Line)
			}
			return fmt.Sprintf("%s:%d", f.File, f.Line)
		}
		if !more {
			return ""
		}
	}
}

func rootOf(dir string) string {
	if dir == "" {
		return "."
	}
	return dir
}

func (c *crate) fileExists(rel string) bool { return c.local && fileExists(c.dir, rel) }

func (c *crate) dirExists(rel string) bool { return c.local && dirExists(c.dir, rel) }

func fileExists(dir, rel string) bool {
	fi, err := os.Stat(path.Join(rootOf(dir), rel))
	return err == nil && fi.Mode().IsRegular()
}

func dirExists(dir, rel string) bool {
	fi, err := os.Stat(path.Join(rootOf(dir), rel))
	return err == nil && fi.IsDir()
}

// escape quotes glob metacharacters of a file name.
func escape(p string) string {
	var b strings.Builder
	for i, ch := range p {
		if strings.ContainsRune(`\*?[]{}`, ch) || i == 0 && ch == '!' {
			b.WriteByte('\\')
		}
		b.WriteRune(ch)
	}
	return b.String()
}

// isZeroSources reports whether s was left unset (bob.Sources has no exported fields).
func isZeroSources(s bob.Sources) bool { return reflect.DeepEqual(s, bob.Sources{}) }

// validRel reports whether d is a clean, relative, slash-separated path inside its root.
func validRel(d string) bool {
	return d != "" && !strings.HasPrefix(d, "/") && path.Clean(d) == d && d != ".." && !strings.HasPrefix(d, "../")
}

func sortedSet(s []string) []string {
	out := slices.Clone(s)
	slices.Sort(out)
	out = slices.Compact(out)
	if out == nil {
		out = []string{}
	}
	return out
}
