package nixpkgs

import (
	"slices"

	"github.com/megakuul/bob/pkg/bob"
	"github.com/megakuul/bob/pkg/bob/ext"
)

// Node kinds. A Tool is built for the host (it runs at build time); a Lib or Pkg for the
// target platform (R-FW-032). The label of every nixpkgs package is "nixpkgs:<attr>"; its node
// id also names the kind and the options, so one attribute may be used as Tool and Lib, or
// with different Modules, side by side.
func init() {
	ext.Register(ext.Kind{Name: "nixpkgs.tool", Axes: []ext.Axis{ext.AxisPlatform}, Host: true})
	ext.Register(ext.Kind{Name: "nixpkgs.lib", Axes: []ext.Axis{ext.AxisPlatform}})
	ext.Register(ext.Kind{Name: "nixpkgs.pkg", Axes: []ext.Axis{ext.AxisPlatform}})
}

// Tool is a nixpkgs package used at build time (always from the host package set).
//
//	nixpkgs.Tool(p, "protobuf")
func Tool(p *bob.P, attr string, opts ...Option) *ToolPackage {
	o := collect(opts)
	t := &ToolPackage{outputs: o.outputs}
	t.Node = ext.New(p, "nixpkgs.tool", attr, nil, nil, o.spec(attr, "host"))
	return t
}

// Lib is a nixpkgs library linked into target code (target package set; see Cross).
//
//	nixpkgs.Lib(p, "openssl", nixpkgs.Modules("libcrypto"))
func Lib(p *bob.P, attr string, opts ...Option) *Package {
	return newPackage(p, "nixpkgs.lib", attr, opts)
}

// Pkg is any other nixpkgs package needed at run time (data, services, runtime tools), from the
// target package set.
//
//	nixpkgs.Pkg(p, "tzdata")
func Pkg(p *bob.P, attr string, opts ...Option) *Package {
	return newPackage(p, "nixpkgs.pkg", attr, opts)
}

func newPackage(p *bob.P, kind, attr string, opts []Option) *Package {
	o := collect(opts)
	set := "target"
	if o.cross {
		set = "cross"
	}
	pk := &Package{outputs: o.outputs, kind: kind, attr: attr, opts: o}
	pk.Node = ext.New(p, kind, attr, nil, nil, o.spec(attr, set))
	return pk
}

// CrossSet returns the same package (attribute, kind and options) taken from the cross package
// set, as if declared with Cross(). C/C++ packages with Libs: cc.CrossLibs use it for their
// nixpkgs libraries (R-FW-036).
func (pk *Package) CrossSet(p *bob.P) *Package {
	if pk == nil || pk.attr == "" || pk.opts.cross {
		return pk
	}
	o := pk.opts
	o.cross = true
	o.outputs, o.modules, o.insecure = slices.Clone(o.outputs), slices.Clone(o.modules), slices.Clone(o.insecure)
	c := &Package{outputs: o.outputs, kind: pk.kind, attr: pk.attr, opts: o}
	c.Node = ext.New(p, pk.kind, pk.attr, nil, nil, o.spec(pk.attr, "cross"))
	return c
}

// Option changes how a nixpkgs package is used.
type Option func(*options)

type options struct {
	outputs  []string
	modules  []string
	cross    bool
	insecure []string
	unfree   bool
}

func collect(opts []Option) options {
	var o options
	for _, f := range opts {
		if f != nil {
			f(&o)
		}
	}
	return o
}

// spec is the IR "nix" object of the node (SPEC R-FW-047).
func (o options) spec(attr, set string) nixSpec {
	return nixSpec{Attr: attr, Set: set, Outputs: sorted(o.outputs), Modules: sorted(o.modules),
		Allow: nixAllow{Insecure: sorted(o.insecure), Unfree: o.unfree}}
}

type nixSpec struct {
	Attr    string   `json:"attr"`
	Set     string   `json:"set"` // host | target | cross
	Outputs []string `json:"outputs"`
	Modules []string `json:"modules,omitempty"`
	Allow   nixAllow `json:"allow"`
}

type nixAllow struct {
	Insecure []string `json:"insecure"`
	Unfree   bool     `json:"unfree"`
}

// sorted returns a sorted copy without duplicates (never nil, so the IR has [] not null).
func sorted(s []string) []string {
	out := append([]string{}, s...)
	slices.Sort(out)
	return slices.Compact(out)
}

// Outputs selects the package's outputs; default: the package's default outputs.
func Outputs(names ...string) Option {
	return func(o *options) { o.outputs = append(o.outputs, names...) }
}

// Modules lists the pkg-config modules a Lib links; default: all modules of its dev output.
func Modules(names ...string) Option {
	return func(o *options) { o.modules = append(o.modules, names...) }
}

// Cross takes a target library from the cross package set (A19); default: the target system's
// own set. A Tool always comes from the host set.
func Cross() Option { return func(o *options) { o.cross = true } }

// AllowInsecure accepts these knownVulnerabilities entries of the package.
func AllowInsecure(ids ...string) Option {
	return func(o *options) { o.insecure = append(o.insecure, ids...) }
}

// AllowUnfree accepts an unfree licence.
func AllowUnfree() Option { return func(o *options) { o.unfree = true } }

// Package is a nixpkgs package of the target set (Lib, Pkg). It is a bob.Tool, a bob.Tree and,
// for linking, a bob.Linkable (cc.Linkable).
type Package struct {
	bob.Node
	outputs []string
	kind    string  // nixpkgs.lib or nixpkgs.pkg
	attr    string  // nixpkgs attribute
	opts    options // as declared (CrossSet)
}

// Outputs lists the selected outputs ("out" when none were selected).
func (p *Package) Outputs() []bob.Output { return outputsOf(p, p.outputs) }

// LinkInfo is filled by bob's generators from the package's pkg-config data; the evaluator
// only records the package and its Modules.
func (p *Package) LinkInfo() bob.LinkInfo { return bob.LinkInfo{} }

// ToolPackage is a nixpkgs package of the host set used at build time (Tool). It is a bob.Tool
// and a bob.Tree, but not a library: it cannot be linked into target code (R-FW-032).
type ToolPackage struct {
	bob.Node
	outputs []string
}

// Outputs lists the selected outputs ("out" when none were selected).
func (t *ToolPackage) Outputs() []bob.Output { return outputsOf(t, t.outputs) }

func outputsOf(pkg bob.Package, names []string) []bob.Output {
	if len(names) == 0 {
		return []bob.Output{{Package: pkg, Name: "out"}}
	}
	out := make([]bob.Output, 0, len(names))
	for _, n := range sorted(names) {
		out = append(out, bob.Output{Package: pkg, Name: n})
	}
	return out
}

// Program is a nixpkgs package of the target set used as an Executable (Exe).
type Program struct {
	*Package
	main string
}

// MainProgram is the program's path relative to the package's bin (else out) output.
func (x *Program) MainProgram() string { return x.main }

// Exe is a nixpkgs package needed at run time (like Pkg) whose program mainProgram is run, e.g.
// as an extra entry of an AppImage. mainProgram is relative to the package's "bin" output when
// it has one (nixpkgs puts the programs of multi-output packages there), else to "out":
//
//	nixpkgs.Exe(p, "curl", "bin/curl")
func Exe(p *bob.P, attr, mainProgram string, opts ...Option) *Program {
	return &Program{Package: Pkg(p, attr, opts...), main: mainProgram}
}
