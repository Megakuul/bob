package golang

import (
	"fmt"
	"maps"
	"path"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/megakuul/bob/pkg/bob"
	"github.com/megakuul/bob/pkg/bob/ext"
	"github.com/megakuul/bob/pkg/golang/internal/gospec"
	"github.com/megakuul/bob/pkg/nixpkgs"
)

// Kinds of the Go ecosystem (SPEC §03.2). A module is the same for every configuration; the
// packages built from it depend on the target platform.
func init() {
	ext.Register(ext.Kind{Name: gospec.KindModule})
	// Binaries and test units see the variant (R-FW-038): -N -l for debug, -race, and the
	// sanitisers (Go -asan for every package, all of them for cgo code and the link).
	variant := []ext.Axis{ext.AxisPlatform, ext.AxisProfile, ext.AxisSanitizers, ext.AxisGoRace}
	ext.Register(ext.Kind{Name: gospec.KindBinary, Axes: variant})
	ext.Register(ext.Kind{Name: gospec.KindPackage, Axes: []ext.Axis{ext.AxisPlatform}})
	ext.Register(ext.Kind{Name: gospec.KindTests, Axes: []ext.Axis{ext.AxisPlatform}})
	ext.Register(ext.Kind{Name: gospec.KindTest, Axes: variant})
}

// ModuleOpts describes one Go module; its go.mod/go.sum are generated from it (R-GO-003).
type ModuleOpts struct {
	Path string // module path, required ("example.com/api")
	Go   string // "1.26": go directive (written "1.26.0") and toolchain minor, required (R-GO-004)
	// Toolchain is the Go toolchain; default nixpkgs.Tool(p, "go_1_26") for Go "1.26". It must
	// report version <Go>.x.
	Toolchain bob.Tool
	Require   []Requirement // direct third-party requirements, exact versions (R-GO-002)
	Generated []Generated   // codegen outputs materialised into this module (§03.6)
	Tags      []string      // build tags for every package of the module
}

// Requirement is one direct module requirement.
type Requirement struct{ Path, Version string }

// Require returns a requirement; version must be a canonical semver ("v1.6.0") or a
// pseudo-version; ranges and "latest" are rejected at evaluation.
func Require(path, version string) Requirement { return Requirement{Path: path, Version: version} }

// Generated is a codegen recipe for a Go module (connect.Go, graphql.Gqlgen, openapi.Go): the
// module realises it as the package //<module dir>:gen-<dir> whose output is the Go package in
// Dir. Codegen packages fill it; users only pass it on.
type Generated struct {
	Kind    string        // node kind of the generator, registered by the codegen package
	Dir     string        // owned directory relative to the module ("gen/proto/greet/v1")
	Deps    []bob.Package // the generator's inputs (proto libraries, schemas)
	Spec    any           // the generator's IR payload
	Require []Requirement // runtime libraries the generated code imports (§03.6.4)
}

// Module is one Go module (label //<dir>:module).
type Module struct {
	bob.Node
	p    *bob.P
	spec gospec.Module
	dir  string // repository-relative module root (the label's directory)
	cc   bob.Tool
	cgo  []*Package
}

var (
	goVersion = sync.OnceValue(func() *regexp.Regexp { return regexp.MustCompile(`^1\.(0|[1-9][0-9]*)$`) })
	semver    = sync.OnceValue(func() *regexp.Regexp {
		return regexp.MustCompile(`^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-[0-9A-Za-z.-]+)?(\+incompatible)?$`)
	})
	modElem    = sync.OnceValue(func() *regexp.Regexp { return regexp.MustCompile(`^[A-Za-z0-9._~+-]+$`) })
	buildTag   = sync.OnceValue(func() *regexp.Regexp { return regexp.MustCompile(`^[A-Za-z0-9_.]+$`) })
	labelName  = sync.OnceValue(func() *regexp.Regexp { return regexp.MustCompile(`^[a-z0-9][a-z0-9_.-]{0,127}$`) })
	xSymbol    = sync.OnceValue(func() *regexp.Regexp { return regexp.MustCompile(`^[^\s=]+\.[A-Za-z_][A-Za-z0-9_]*$`) })
	genDirElem = sync.OnceValue(func() *regexp.Regexp { return regexp.MustCompile(`^[A-Za-z0-9_.-]+$`) })
)

// NewModule declares the module whose root is the directory of the calling bob.go.
// Library packages need no declaration: bob follows the imports of binaries and tests.
// Label //<dir>:module.
//
//	var Module = bob.Define(func(p *bob.P) *golang.Module {
//		return golang.NewModule(p, golang.ModuleOpts{
//			Path: "example.com/api", Go: "1.26",
//			Require: []golang.Requirement{golang.Require("github.com/google/uuid", "v1.6.0")},
//		})
//	})
func NewModule(p *bob.P, o ModuleOpts) *Module {
	m := &Module{p: p}
	tc := o.Toolchain
	if tc == nil && goVersion().MatchString(o.Go) {
		tc = nixpkgs.Tool(p, "go_"+strings.ReplaceAll(o.Go, ".", "_"))
	}
	m.spec = gospec.Module{Path: o.Path, Go: o.Go, Tags: sortedSet(o.Tags), Require: []gospec.Requirement{},
		Generated: []gospec.Generated{}}
	deps := []bob.Package{}
	if tc != nil {
		deps = append(deps, tc)
	}
	for _, r := range o.Require {
		m.spec.Require = append(m.spec.Require, gospec.Requirement{Path: r.Path, Version: r.Version})
	}
	slices.SortFunc(m.spec.Require, func(a, b gospec.Requirement) int { return strings.Compare(a.Path, b.Path) })
	// The codegen packages are labelled in the module's directory: create them first, the
	// module node depends on them.
	var problems []string
	for _, g := range o.Generated {
		dir := path.Clean(g.Dir)
		if g.Kind == "" || !validRel(dir) || dir == "." {
			problems = append(problems, fmt.Sprintf("generated package %q needs a generator kind and a directory inside the module", g.Dir))
			continue
		}
		gen := &genPackage{Node: ext.New(p, g.Kind, "gen-"+strings.ReplaceAll(dir, "/", "-"), g.Deps, nil, g.Spec)}
		req := []gospec.Requirement{}
		for _, r := range g.Require {
			req = append(req, gospec.Requirement{Path: r.Path, Version: r.Version})
		}
		m.spec.Generated = append(m.spec.Generated, gospec.Generated{Dir: dir, Dep: len(deps), Require: req})
		deps = append(deps, gen)
	}
	m.Node = ext.New(p, gospec.KindModule, "module", deps, nil, m.spec)
	m.dir = m.Label().Dir
	pos := m.DefinedAt().String()
	problems = append(problems, checkModule(o, tc == nil)...)
	for _, pr := range problems {
		ext.Errorf("%s: %s", pos, pr)
	}
	return m
}

// genPackage is a realised codegen recipe.
type genPackage struct{ bob.Node }

func checkModule(o ModuleOpts, noToolchain bool) []string {
	var out []string
	if err := checkModulePath(o.Path); err != nil {
		out = append(out, fmt.Sprintf("golang.ModuleOpts.Path: %v", err))
	}
	if !goVersion().MatchString(o.Go) {
		out = append(out, fmt.Sprintf(`golang.ModuleOpts.Go is %q; write the Go version as "1.26"`, o.Go))
	} else if noToolchain {
		out = append(out, "golang.ModuleOpts: no Go toolchain")
	}
	seen := map[string]bool{}
	for _, r := range o.Require {
		if err := checkModulePath(r.Path); err != nil {
			out = append(out, fmt.Sprintf("golang.Require(%q, %q): %v", r.Path, r.Version, err))
		}
		if !semver().MatchString(r.Version) {
			out = append(out, fmt.Sprintf(`golang.Require(%q, %q): the version must be exact, e.g. "v1.6.0" (ranges and "latest" are not allowed)`, r.Path, r.Version))
		}
		if seen[r.Path] {
			out = append(out, fmt.Sprintf("golang.Require(%q): the module is required twice", r.Path))
		}
		seen[r.Path] = true
	}
	for _, t := range o.Tags {
		if !buildTag().MatchString(t) {
			out = append(out, fmt.Sprintf("golang.ModuleOpts.Tags: %q is not a build tag", t))
		}
	}
	return out
}

func checkModulePath(p string) error {
	if p == "" {
		return fmt.Errorf("the module path is empty")
	}
	if len(p) > 512 {
		return fmt.Errorf("the module path is longer than 512 bytes")
	}
	for _, e := range strings.Split(p, "/") {
		if !modElem().MatchString(e) || e == "." || e == ".." || strings.HasPrefix(e, ".") || strings.HasSuffix(e, ".") {
			return fmt.Errorf("%q is not a valid module path", p)
		}
	}
	return nil
}

// validRel reports whether d is a clean, relative, slash-separated path inside its root.
func validRel(d string) bool {
	if d == "." {
		return true
	}
	if d == "" || strings.HasPrefix(d, "/") || path.Clean(d) != d || d == ".." || strings.HasPrefix(d, "../") {
		return false
	}
	for _, e := range strings.Split(d, "/") {
		if !genDirElem().MatchString(e) {
			return false
		}
	}
	return true
}

// relDir turns a module-relative directory argument ("./cmd/api", "cmd/api", ".") into its
// clean form ("cmd/api", "."); ok is false for paths leaving the module.
func relDir(dir string) (string, bool) {
	d := path.Clean(strings.TrimPrefix(dir, "./"))
	if dir == "" {
		return "", false
	}
	return d, validRel(d)
}

// labelDir is the repository-relative directory of the module-relative dir d.
func (m *Module) labelDir(d string) string {
	if d == "." {
		return m.dir
	}
	if m.dir == "" {
		return d
	}
	return m.dir + "/" + d
}

// ccTool is the C compiler of the module's cgo packages (one per configuration).
func (m *Module) ccTool() bob.Tool {
	if m.cc == nil {
		m.cc = nixpkgs.Tool(m.p, m.p.Toolchain().CC) // the target's C compiler (R-FW-033)
	}
	return m.cc
}

// build returns the shared part of a binary's or test's spec and its deps: the module, the C
// compiler (unless cgo is off) and the module's cgo packages declared so far.
func (m *Module) build(tags []string, cgo CGo) (gospec.Build, []bob.Package) {
	b := gospec.Build{Module: 0, CC: -1, Cgo: []int{}, Tags: sortedSet(tags), CGo: gospec.CgoAuto}
	deps := []bob.Package{m}
	if cgo == CGoOff {
		b.CGo = gospec.CgoOff
		return b, deps
	}
	b.CC = len(deps)
	deps = append(deps, m.ccTool())
	for _, c := range m.cgo {
		b.Cgo = append(b.Cgo, len(deps))
		deps = append(deps, c)
	}
	return b, deps
}

// BinaryOpts configures a main package.
type BinaryOpts struct {
	Name string            // default: last element of dir
	Tags []string          // added to the module's tags
	X    map[string]string // -ldflags -X importpath.name=value (sorted)
	CGo  CGo               // CGoAuto (default: on iff a package in the closure imports "C"), CGoOff
}

// CGo selects cgo for a binary's closure.
type CGo int

const (
	CGoAuto CGo = iota // on iff a package in the closure imports "C"
	CGoOff             // pure Go; importing "C" is an evaluation error
)

// Binary is a Go program (label //<module dir>/<dir>:<name>).
type Binary struct {
	bob.Node
	name string
}

// MainProgram is the program's path in the package output.
func (b *Binary) MainProgram() string { return "bin/" + b.name }

// Binary declares the main package in dir (module-relative, "./cmd/api").
// Label //<module dir>/<dir>:<Name>.
//
//	var API = bob.Define(func(p *bob.P) *golang.Binary {
//		return Module(p).Binary("./cmd/api", golang.BinaryOpts{})
//	})
func (m *Module) Binary(dir string, o BinaryOpts) *Binary {
	n, spec, deps, problems := m.mainPackage(dir, o, gospec.ModeExe)
	b := &Binary{name: spec.Name}
	b.Node = ext.NewIn(m.p, gospec.KindBinary, m.labelDir(n), spec.Name, deps, nil, spec)
	report(&b.Node, problems)
	return b
}

// CLib is a Go main package built as a C library (-buildmode=c-archive or c-shared); it is a
// bob.Linkable (cc.Linkable) for C, C++ and Rust consumers.
type CLib struct{ bob.Node }

// LinkInfo is filled by bob's generator; the evaluator only records the library's identity.
func (c *CLib) LinkInfo() bob.LinkInfo { return bob.LinkInfo{} }

// CArchive builds -buildmode=c-archive of the main package in dir; the result implements
// cc.Linkable. Label //<module dir>/<dir>:<Name>.
func (m *Module) CArchive(dir string, o BinaryOpts) *CLib {
	return m.clib(dir, o, gospec.ModeCArchive)
}

// CShared builds -buildmode=c-shared of the main package in dir; the result implements
// cc.Linkable. Label //<module dir>/<dir>:<Name>.
func (m *Module) CShared(dir string, o BinaryOpts) *CLib {
	return m.clib(dir, o, gospec.ModeCShared)
}

func (m *Module) clib(dir string, o BinaryOpts, mode string) *CLib {
	n, spec, deps, problems := m.mainPackage(dir, o, mode)
	if o.CGo == CGoOff {
		problems = append(problems, "a C library needs cgo; remove CGo: golang.CGoOff")
	}
	c := &CLib{}
	c.Node = ext.NewIn(m.p, gospec.KindBinary, m.labelDir(n), spec.Name, deps, nil, spec)
	report(&c.Node, problems)
	return c
}

func (m *Module) mainPackage(dir string, o BinaryOpts, mode string) (string, gospec.Binary, []bob.Package, []string) {
	var problems []string
	d, ok := relDir(dir)
	if !ok {
		problems = append(problems, fmt.Sprintf("%q is not a directory inside the module", dir))
		d = "."
	}
	name := o.Name
	if name == "" {
		name = path.Base(m.labelDir(d))
		if name == "." || name == "/" || name == "" {
			name = path.Base(m.spec.Path)
		}
	}
	if !labelName().MatchString(name) {
		problems = append(problems, fmt.Sprintf("binary name %q is not a valid package name (lower case letters, digits, '_', '-', '.'); set BinaryOpts.Name", name))
		name = "invalid"
	}
	for k := range o.X {
		if !xSymbol().MatchString(k) {
			problems = append(problems, fmt.Sprintf("BinaryOpts.X: %q is not importpath.name", k))
		}
	}
	for _, t := range o.Tags {
		if !buildTag().MatchString(t) {
			problems = append(problems, fmt.Sprintf("BinaryOpts.Tags: %q is not a build tag", t))
		}
	}
	b, deps := m.build(o.Tags, o.CGo)
	x := maps.Clone(o.X)
	if x == nil {
		x = map[string]string{}
	}
	return d, gospec.Binary{Build: b, Dir: d, Name: name, X: x, Mode: mode}, deps, problems
}

// report records each problem as a definition error at n's position.
func report(n *bob.Node, problems []string) {
	for _, pr := range problems {
		ext.Errorf("%s: %s: %s", n.DefinedAt(), n.Label(), pr)
	}
}

// CgoOpts gives a cgo package its typed C/C++/Rust dependencies (R-GO-001, R-RS-001).
type CgoOpts struct {
	// Deps are -I → CGO_CFLAGS of this package; link info → final external link. cc.Linkable
	// is an alias of bob.Linkable.
	Deps []bob.Linkable
}

// Package is a Go package with a declaration of its own (a cgo package and its C
// dependencies). Label //<module dir>/<dir>:go.
type Package struct{ bob.Node }

// Cgo attaches C dependencies to the cgo package in dir. Packages that import "C" without
// a Cgo declaration may use only libc and their own C files. Declare it before the binaries
// and tests of the module in bob.go order (same file above them, or a file that sorts earlier):
// they pick up the cgo packages declared so far.
//
//	var _ = bob.Define(func(p *bob.P) *golang.Package {
//		return Module(p).Cgo("./internal/ffi", golang.CgoOpts{Deps: []cc.Linkable{foo.Lib(p), rcore.Crate(p)}})
//	})
func (m *Module) Cgo(dir string, o CgoOpts) *Package {
	var problems []string
	d, ok := relDir(dir)
	if !ok {
		problems = append(problems, fmt.Sprintf("%q is not a directory inside the module", dir))
		d = "."
	}
	spec := gospec.Package{Module: 0, Dir: d, Libs: []int{}}
	deps := []bob.Package{m}
	for _, l := range o.Deps {
		spec.Libs = append(spec.Libs, len(deps))
		deps = append(deps, l)
	}
	pk := &Package{Node: ext.NewIn(m.p, gospec.KindPackage, m.labelDir(d), "go", deps, nil, spec)}
	report(&pk.Node, problems)
	for _, c := range m.cgo {
		if c.Label() == pk.Label() {
			return pk
		}
	}
	m.cgo = append(m.cgo, pk)
	return pk
}

// TestOpts configures a test set.
type TestOpts struct {
	Name     string        // label name; default "tests" (or "go_test" for one package)
	Tags     []string      // added to the module's tags
	Services []bob.Service // R-FW-025 (§01)
	Args     []string      // passed to the test binary (-test.run=…)
	Timeout  time.Duration // per test unit; default 10 minutes (A92)
	CGo      CGo
}

// TestSet is a set of test units, one per package.
type TestSet struct{ bob.Node }

// Tests declares one test unit per package matching pattern ("./..." or "./pkg/x") that
// has _test.go files. A "..." pattern gives the set //<bob.go dir>:<name> ("tests" by default)
// and the units //<pkg dir>:go_test; a one-package pattern gives one unit, //<pkg dir>:<name>
// ("go_test" by default).
//
//	var Tests = bob.Define(func(p *bob.P) *golang.TestSet { return Module(p).Tests("./...", golang.TestOpts{}) })
func (m *Module) Tests(pattern string, o TestOpts) *TestSet {
	var problems []string
	base, wild := strings.CutSuffix(pattern, "/...")
	if pattern == "..." || pattern == "./..." {
		base, wild = ".", true
	}
	d, ok := relDir(base)
	if !ok {
		problems = append(problems, fmt.Sprintf("test pattern %q is not \"./...\", \"./dir/...\" or \"./dir\"", pattern))
		d = "."
	}
	if o.Name != "" && !labelName().MatchString(o.Name) {
		problems = append(problems, fmt.Sprintf("TestOpts.Name %q is not a valid package name", o.Name))
		o.Name = ""
	}
	if o.Timeout < 0 {
		problems = append(problems, "TestOpts.Timeout is negative")
		o.Timeout = 0
	}
	if !wild {
		name := o.Name
		if name == "" {
			name = "go_test"
		}
		u := m.unit(d, name, o)
		report(&u.Node, problems)
		return u
	}
	name := o.Name
	if name == "" {
		name = "tests"
	}
	var units []bob.Package
	for _, pd := range m.testDirs(d) {
		units = append(units, m.unit(pd, "go_test", o))
	}
	s := &TestSet{Node: ext.New(m.p, gospec.KindTests, name, units, nil, gospec.Tests{Pattern: pattern})}
	report(&s.Node, problems)
	return s
}

// unit creates the test unit of the package in d (module-relative).
func (m *Module) unit(d, name string, o TestOpts) *TestSet {
	b, deps := m.build(o.Tags, o.CGo)
	svcs, deps := ext.Services(o.Services, deps)
	spec := gospec.Test{Build: b, Dir: d, Args: slices.Clone(o.Args), Services: svcs,
		TimeoutS: int64(o.Timeout / time.Second)}
	if spec.Args == nil {
		spec.Args = []string{}
	}
	if tc := m.p.Toolchain(); tc.Emulator != "" {
		// A test for another platform runs under the emulator (R-FW-039).
		spec.Emulator = len(deps)
		deps = append(deps, nixpkgs.Tool(m.p, tc.Emulator))
	}
	return &TestSet{Node: ext.NewIn(m.p, gospec.KindTest, m.labelDir(d), name, deps, nil, spec)}
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
