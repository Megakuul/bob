package cc

import (
	"fmt"
	"maps"
	"path"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"sync"

	"github.com/megakuul/bob/pkg/bob"
	"github.com/megakuul/bob/pkg/bob/ext"
	"github.com/megakuul/bob/pkg/cc/internal/ccspec"
	"github.com/megakuul/bob/pkg/nixpkgs"
	"github.com/megakuul/bob/pkg/nixpkgs/nixext"
)

// Kinds of the C/C++ ecosystem (SPEC §03.1). Objects depend on the platform, the profile
// (-O2 / -O0 -g) and the sanitisers; an External on the platform and the profile. A library,
// binary or External is the package's one C/C++ unit (R-CC-006, BOB-E-1029); tests are not
// units.
func init() {
	axes := []ext.Axis{ext.AxisPlatform, ext.AxisProfile, ext.AxisSanitizers}
	ext.Register(ext.Kind{Name: ccspec.KindLibrary, Axes: axes, Unit: "cc"})
	ext.Register(ext.Kind{Name: ccspec.KindBinary, Axes: axes, Unit: "cc"})
	ext.Register(ext.Kind{Name: ccspec.KindTest, Axes: axes})
	ext.Register(ext.Kind{Name: ccspec.KindExternal, Axes: []ext.Axis{ext.AxisPlatform, ext.AxisProfile}, Unit: "cc"})
}

// Linkable is anything a C/C++/cgo/Rust/OCaml consumer can link: bob C/C++ libraries,
// Rust StaticLib/CDylib, cc.External, nixpkgs.Lib. Alias of the core type.
type Linkable = bob.Linkable

// Std is the language standard (-std=…).
type Std string

// Language standards.
const (
	C11   Std = "c11"
	C17   Std = "c17"
	C23   Std = "c23"
	CXX17 Std = "c++17"
	CXX20 Std = "c++20"
	CXX23 Std = "c++23"
)

// LibraryOpts configures StaticLibrary and SharedLibrary.
type LibraryOpts struct {
	Srcs      bob.Sources       // .c .cc .cpp .cxx .S; required
	Hdrs      bob.Include       // public include tree: bob.IncludeDir("include"); zero = none
	Private   bob.Sources       // private headers (not exported to consumers)
	Std       Std               // default C17 for .c, CXX20 for C++ sources
	Defines   map[string]string // -DKEY=VALUE ("" value = -DKEY)
	Flags     []string          // extra compile flags (validated: no -o, no include path flags of any path, no -fplugin, no @file)
	LinkOpts  []string          // extra link flags (shared libs and binaries only; no -L/-l/@file: use Deps)
	Deps      []Linkable        // public deps: their headers propagate to consumers
	PrivDeps  []Linkable        // private deps: linked, headers not propagated
	Platforms []bob.Platform    // restrict; default all
	// Libs selects where nixpkgs libraries in Deps and PrivDeps come from for a cross target
	// (R-FW-036): NativeLibs (default, the target system's own cached set) or CrossLibs.
	Libs LibSource
}

// LibSource is where the nixpkgs libraries (nixpkgs.Lib) of a cross-compiled package come
// from (SPEC R-FW-036). On the host's own platform both are the same package.
type LibSource int

const (
	// NativeLibs takes nixpkgs.Lib packages from the target system's own package set
	// (legacyPackages.<target-system>): usually in the binary cache, but the closure then holds
	// the target's glibc next to the cross toolchain's.
	NativeLibs LibSource = iota
	// CrossLibs takes them from the cross package set (pkgsCross.<x>): one glibc in the closure,
	// usually built from source (several minutes per library).
	CrossLibs
)

// BinaryOpts configures NewBinary and NewTest.
type BinaryOpts struct {
	Srcs      bob.Sources
	Std       Std
	Defines   map[string]string
	Flags     []string
	LinkOpts  []string
	Deps      []Linkable
	Platforms []bob.Platform
	Libs      LibSource // see LibraryOpts.Libs
}

// Library is a C/C++ library built by bob; it implements Linkable. A shared library's output
// also holds the .so for bundling.
type Library struct{ bob.Node }

// LinkInfo is filled by bob's generator from the planned library; the evaluator only records
// the library's identity.
func (l *Library) LinkInfo() bob.LinkInfo { return bob.LinkInfo{} }

// Binary is a C/C++ program (bin/<name>).
type Binary struct {
	bob.Node
	name string
}

// MainProgram is the program's path in the package output.
func (b *Binary) MainProgram() string { return "bin/" + b.name }

// Test is a C/C++ test program and its run; building it runs the test.
type Test struct{ bob.Node }

// StaticLibrary builds lib<name>.a. Label //<dir>:<name>.
//
//	var Foo = bob.Define(func(p *bob.P) *cc.Library {
//		return cc.StaticLibrary(p, "foo", cc.LibraryOpts{Srcs: bob.Glob("src/*.cpp"), Hdrs: bob.IncludeDir("include")})
//	})
func StaticLibrary(p *bob.P, name string, o LibraryOpts) *Library {
	return library(p, name, o, ccspec.TypeStatic)
}

// SharedLibrary builds lib<name>.so (soname lib<name>.so).
//
//	var Foo = bob.Define(func(p *bob.P) *cc.Library {
//		return cc.SharedLibrary(p, "foo", cc.LibraryOpts{
//			Srcs: bob.Glob("src/*.cpp"), Hdrs: bob.IncludeDir("include"), Std: cc.CXX20,
//			Deps: []cc.Linkable{nixpkgs.Openssl.Lib(p)},
//		})
//	})
func SharedLibrary(p *bob.P, name string, o LibraryOpts) *Library {
	return library(p, name, o, ccspec.TypeShared)
}

func library(p *bob.P, name string, o LibraryOpts, typ string) *Library {
	var problems []string
	// The public include tree is the package's own (R-CC-006): its files are sources of the
	// library (a tree outside the package region fails with BOB-E-1028 there) and its root goes
	// only into the library's link info for consumers. "." is the package directory itself.
	hdrs := o.Hdrs.Dir()
	srcs := []bob.Sources{o.Srcs, o.Private}
	if hdrs != "" {
		srcs = append(srcs, o.Hdrs.Sources())
		if hdrs != "." && !localDir(hdrs) {
			hdrs = "" // reported by the region rule of the sources
		}
	}
	t := ccspec.Target{Name: name, Type: typ, Std: string(o.Std), Defines: defines(o.Defines),
		Flags: list(o.Flags), LinkOpts: list(o.LinkOpts), Hdrs: hdrs, Private: !isEmpty(o.Private)}
	deps := tools(p, &t.Tools)
	t.Deps, deps = linkables(libsFrom(p, o.Libs, o.Deps), deps)
	t.PrivDeps, deps = linkables(libsFrom(p, o.Libs, o.PrivDeps), deps)
	if typ == ccspec.TypeStatic && len(o.LinkOpts) > 0 {
		problems = append(problems, "LinkOpts is set, but a static library is not linked; set it on the shared library or binary that links this one")
	}
	problems = append(problems, check(o.Srcs, o.Std, o.Flags, o.LinkOpts, o.Defines)...)
	l := &Library{Node: ext.Restrict(ext.New(p, ccspec.KindLibrary, name, deps, srcs, t), o.Platforms)}
	report(&l.Node, problems)
	return l
}

// NewBinary builds bin/<name>. Label //<dir>:<name>.
//
//	var Tool = bob.Define(func(p *bob.P) *cc.Binary {
//		return cc.NewBinary(p, "tool", cc.BinaryOpts{Srcs: bob.Glob("*.c"), Deps: []cc.Linkable{foo.Foo(p)}})
//	})
func NewBinary(p *bob.P, name string, o BinaryOpts) *Binary {
	n, problems := program(p, name, o, ccspec.KindBinary, ccspec.TypeBinary)
	b := &Binary{Node: n, name: name}
	report(&b.Node, problems)
	return b
}

// NewTest builds a program and runs it in a test-run derivation; exit 0 = pass. Output is
// parsed as TAP if it starts with "TAP version", else it is one case named <name>.
// Label //<dir>:<name>.
//
//	var FooTest = bob.Define(func(p *bob.P) *cc.Test {
//		return cc.NewTest(p, "foo_test", cc.BinaryOpts{Srcs: bob.Files("foo_test.cpp"), Deps: []cc.Linkable{Foo(p)}})
//	})
func NewTest(p *bob.P, name string, o BinaryOpts) *Test {
	n, problems := program(p, name, o, ccspec.KindTest, ccspec.TypeTest)
	t := &Test{Node: n}
	report(&t.Node, problems)
	return t
}

func program(p *bob.P, name string, o BinaryOpts, kind, typ string) (bob.Node, []string) {
	t := ccspec.Target{Name: name, Type: typ, Std: string(o.Std), Defines: defines(o.Defines),
		Flags: list(o.Flags), LinkOpts: list(o.LinkOpts), PrivDeps: []int{}}
	deps := tools(p, &t.Tools)
	if tc := nixext.Toolchain(p); typ == ccspec.TypeTest && tc.Emulator.Name() != "" {
		t.Tools.Emulator = len(deps)
		deps = append(deps, tc.Emulator.Tool(p))
	}
	t.Deps, deps = linkables(libsFrom(p, o.Libs, o.Deps), deps)
	problems := check(o.Srcs, o.Std, o.Flags, o.LinkOpts, o.Defines)
	return ext.Restrict(ext.New(p, kind, name, deps, []bob.Sources{o.Srcs}, t), o.Platforms), problems
}

// tools adds the host tools of every C/C++ derivation and records their dep indices: the
// compiler and binutils of the package's toolchain (cross ones for another platform, R-FW-033)
// and patchelf. A sanitised cross build also gets the cross compiler's lib output, whose
// directory goes into the RUNPATH (R-FW-037).
func tools(p *bob.P, t *ccspec.Tools) []bob.Package {
	tc := nixext.Toolchain(p)
	deps := []bob.Package{tc.CC.Tool(p), tc.Bintools.Tool(p), nixpkgs.Patchelf.Tool(p)}
	*t = ccspec.Tools{CC: 0, Binutils: 1, Patchelf: 2}
	if tc.CCLib.Name() != "" && len(p.Variant().Sanitize) > 0 {
		t.CCLib = len(deps)
		deps = append(deps, tc.CCLib.Tool(p, nixext.Outputs(nixext.OutLib)))
	}
	return deps
}

// libsFrom returns ls with every nixpkgs library taken from the cross package set when src is
// CrossLibs and p is a cross configuration (R-FW-036); other Linkables are kept.
func libsFrom(p *bob.P, src LibSource, ls []Linkable) []Linkable {
	if src != CrossLibs || !p.Toolchain().Cross() {
		return ls
	}
	out := make([]Linkable, len(ls))
	for i, l := range ls {
		out[i] = l
		if nl, ok := l.(*nixext.Package); ok && nl != nil {
			out[i] = nl.CrossSet(p)
		}
	}
	return out
}

// linkables appends ls to deps and returns their indices.
func linkables(ls []Linkable, deps []bob.Package) ([]int, []bob.Package) {
	idx := []int{}
	for _, l := range ls {
		idx = append(idx, len(deps))
		deps = append(deps, l)
	}
	return idx, deps
}

func defines(m map[string]string) map[string]string {
	out := maps.Clone(m)
	if out == nil {
		out = map[string]string{}
	}
	return out
}

func list(s []string) []string {
	if s == nil {
		return []string{}
	}
	return slices.Clone(s)
}

// isEmpty reports whether s selects nothing (the zero Sources).
func isEmpty(s bob.Sources) bool { return reflect.ValueOf(s).IsZero() }

var (
	defineName = sync.OnceValue(func() *regexp.Regexp { return regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*(\([A-Za-z0-9_, ]*\))?$`) })
	stds       = []Std{C11, C17, C23, CXX17, CXX20, CXX23}
)

// check validates the typed options (R-CC-002a): flags that would make the build depend on
// something outside its inputs, or redirect its outputs, are refused at evaluation.
func check(srcs bob.Sources, std Std, flags, linkOpts []string, defs map[string]string) []string {
	var out []string
	if isEmpty(srcs) {
		out = append(out, "Srcs is empty; list the .c/.cpp files to compile")
	}
	if std != "" && !slices.Contains(stds, std) {
		out = append(out, fmt.Sprintf("Std %q is not one of cc.C11, cc.C17, cc.C23, cc.CXX17, cc.CXX20, cc.CXX23", std))
	}
	for _, k := range slices.Sorted(maps.Keys(defs)) {
		if !defineName().MatchString(k) {
			out = append(out, fmt.Sprintf("Defines key %q is not a macro name", k))
		}
		if strings.ContainsAny(defs[k], "\n\x00") {
			out = append(out, fmt.Sprintf("Defines value of %s contains a newline or NUL", k))
		}
	}
	for i, f := range flags {
		if r := flagProblem(f, prev(flags, i)); r != "" {
			out = append(out, r)
		}
	}
	for i, f := range linkOpts {
		if r := flagProblem(f, prev(linkOpts, i)); r != "" {
			out = append(out, r)
		}
	}
	return out
}

func prev(s []string, i int) string {
	if i == 0 {
		return ""
	}
	return s[i-1]
}

// Include and library path flags (R-CC-006): a package gets include paths and libraries only
// from its own Hdrs/Private and the link info of its Deps, never from a path in a flag.
var (
	includeFlags = []string{"-I", "-isystem", "-idirafter", "-iquote", "-include", "-imacros", "--sysroot",
		"-isysroot", "-iprefix", "-iwithprefix", "-iwithprefixbefore"}
	libraryFlags = []string{"-L", "-l"}
	toolFlags    = []string{"-B", "-specs"}
)

// The reasons of refused include and library path flags (R-CC-006); report adds a hint to them.
const (
	useDepsIncludes  = "use Deps to add include paths"
	useDepsLibraries = "use Deps to add libraries"
)

// flagProblem explains why flag f (after flag before) is not allowed, or returns "".
func flagProblem(f, before string) string {
	q := fmt.Sprintf("flag %q is not allowed; ", f)
	switch {
	case strings.ContainsAny(f, "\n\x00"):
		return fmt.Sprintf("flag %q contains a newline or NUL", f)
	case strings.HasPrefix(f, "-o"):
		return q + "bob names the outputs"
	case strings.HasPrefix(f, "-fplugin") || strings.HasPrefix(f, "-Xclang"):
		return q + "compiler plugins are not hermetic"
	case strings.HasPrefix(f, "@"):
		return q + "response files are read from outside the sources"
	case pathFlag(f, before, includeFlags):
		return q + useDepsIncludes
	case pathFlag(f, before, libraryFlags) || linkerPathFlag(f):
		return q + useDepsLibraries
	case pathFlag(f, before, toolFlags) || strings.Contains(f, "/nix/store"):
		return q + "it names a path outside the sources; use Deps"
	}
	return ""
}

// pathFlag reports whether f is one of the flags names, with its argument joined ("-Iinclude",
// "--sysroot=/x") or f is the separate argument of one ("-isystem", "include"). Every path is
// refused, relative or absolute (R-CC-006).
func pathFlag(f, before string, names []string) bool {
	for _, n := range names {
		if strings.HasPrefix(f, n) || before == n {
			return true
		}
	}
	return false
}

// linkerPathFlag reports whether f passes a library path or name to the linker directly
// ("-Wl,-L,../x", "-Wl,-lfoo", "-Wl,@file").
func linkerPathFlag(f string) bool {
	rest, ok := strings.CutPrefix(f, "-Wl,")
	if !ok {
		return false
	}
	for _, a := range strings.Split(rest, ",") {
		if strings.HasPrefix(a, "-L") || strings.HasPrefix(a, "-l") || strings.HasPrefix(a, "@") ||
			strings.HasPrefix(a, "--library") {
			return true
		}
	}
	return false
}

// localDir reports whether d is a clean relative directory below the bob.go's directory.
func localDir(d string) bool {
	return d != "" && d != "." && !path.IsAbs(d) && d != ".." && !strings.HasPrefix(d, "../")
}

// report records each problem as a definition error at n's position.
func report(n *bob.Node, problems []string) {
	for _, pr := range problems {
		switch {
		case strings.HasSuffix(pr, useDepsIncludes):
			pr += "\nhint: depend on the library that provides the headers through Deps; its Hdrs include tree joins the include path"
		case strings.HasSuffix(pr, useDepsLibraries):
			pr += "\nhint: depend on the library through Deps (a package of this repository, or a typed nixpkgs package such as nixpkgs.Zlib.Lib(p))"
		}
		ext.Errorf("%s: %s: %s", n.DefinedAt(), n.Label(), pr)
	}
}
