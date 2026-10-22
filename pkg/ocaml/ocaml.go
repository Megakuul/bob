package ocaml

import (
	"fmt"
	"os"
	"path"
	"reflect"
	"regexp"
	"runtime"
	"runtime/debug"
	"slices"
	"strings"

	"github.com/megakuul/bob/pkg/bob"
	"github.com/megakuul/bob/pkg/bob/ext"
	"github.com/megakuul/bob/pkg/cc"
	"github.com/megakuul/bob/pkg/nixpkgs"
	"github.com/megakuul/bob/pkg/ocaml/internal/mlspec"
)

// Kinds of the OCaml ecosystem (SPEC §03.5). Native code depends on the target platform.
func init() {
	axes := []ext.Axis{ext.AxisPlatform}
	ext.Register(ext.Kind{Name: mlspec.KindLibrary, Axes: axes})
	ext.Register(ext.Kind{Name: mlspec.KindExecutable, Axes: axes})
	ext.Register(ext.Kind{Name: mlspec.KindTest, Axes: axes})
}

// Dep is an opam package, a findlib library of one, or a local *Lib.
type Dep interface{ mlDep() }

// OpamPkg is an opam package resolved into bob.lock by bob lock. As a Dep it means the findlib
// library of the same name.
type OpamPkg struct {
	name, req string
}

func (*OpamPkg) mlDep() {}

// Opam is an opam package; req is an opam version formula ("", ">= 5.0", "= 6.1.2").
// As a Dep it means the findlib library of the same name.
//
//	ocaml.Opam("lwt", ">= 5.7")
func Opam(name, req string) *OpamPkg { return &OpamPkg{name: name, req: req} }

// findlibDep is one findlib library of an opam package.
type findlibDep struct {
	pkg *OpamPkg
	lib string
}

func (findlibDep) mlDep() {}

// Lib selects one findlib library of the package: ocaml.Opam("lwt", ">= 5.7").Lib("lwt.unix").
func (o *OpamPkg) Lib(findlib string) Dep { return findlibDep{pkg: o, lib: findlib} }

// Option is a graph-wide setting attached to a local library or executable.
type Option interface{ mlOption() }

type confOption struct {
	opam string
	libs []cc.Linkable
}

func (confOption) mlOption() {}

// Conf maps an opam conf-* package to nixpkgs packages for this project (added to the shipped
// table, §03.5.5).
//
//	ocaml.Conf("conf-libcurl", nixpkgs.Lib(p, "curl"))
func Conf(opamName string, libs ...cc.Linkable) Option { return confOption{opam: opamName, libs: libs} }

// LibOpts configures Library.
type LibOpts struct {
	Name    string        // default directory name; module namespace = capitalised name
	Srcs    bob.Sources   // default bob.Glob("*.ml", "*.mli")
	Deps    []Dep         // ocaml.Opam(…), (*OpamPkg).Lib(…), *Lib
	Stubs   bob.Sources   // C stub files
	Native  []cc.Linkable // linked into the stubs
	Wrapped *bool         // default true (dune semantics: Lib__Mod)
	Flags   []string      // extra ocamlopt flags (validated)
	Dune    bool          // fallback mode: generated dune file built inside the derivation (ppx, menhir)
	Options []Option
}

// ExeOpts configures Executable and Test.
type ExeOpts struct {
	Name    string // default directory name
	Main    string // module, default "main"
	Srcs    bob.Sources
	Deps    []Dep
	Native  []cc.Linkable
	Dune    bool
	Options []Option
}

// Lib is a local OCaml library (label //<dir>:<Name>), usable as Dep.
type Lib struct{ bob.Node }

func (*Lib) mlDep() {}

// Exe is a native OCaml executable (label //<dir>:<Name>); its output holds bin/<Name>.
type Exe struct {
	bob.Node
	name string
}

// MainProgram is the program's path in the package output.
func (e *Exe) MainProgram() string { return "bin/" + e.name }

// Test is an OCaml test executable and its run; building it runs the test.
type Test struct{ bob.Node }

// Library is a native OCaml library, usable as Dep. Label //<dir>:<Name>.
//
//	var Geom = bob.Define(func(p *bob.P) *ocaml.Lib {
//		return ocaml.Library(p, ocaml.LibOpts{Deps: []ocaml.Dep{ocaml.Opam("fmt", "")}})
//	})
func Library(p *bob.P, o LibOpts) *Lib {
	wrapped := true
	if o.Wrapped != nil {
		wrapped = *o.Wrapped
	}
	t := mlspec.Target{Type: mlspec.TypeLibrary, Wrapped: wrapped, Flags: list(o.Flags), Dune: o.Dune}
	srcs := defaultSrcs(o.Srcs)
	b := newBuilder(p, &t, o.Name)
	b.deps(o.Deps)
	b.native(o.Native)
	b.options(o.Options)
	b.flags(o.Flags)
	n := ext.New(p, mlspec.KindLibrary, t.Name, b.pkgs, []bob.Sources{srcs, o.Stubs}, t)
	l := &Lib{Node: n}
	b.report(&l.Node)
	return l
}

// Executable builds a native executable bin/<Name> (build references scrubbed).
//
//	var Demo = bob.Define(func(p *bob.P) *ocaml.Exe {
//		return ocaml.Executable(p, ocaml.ExeOpts{Deps: []ocaml.Dep{
//			ocaml.Opam("cmdliner", ""), ocaml.Opam("lwt", ">= 5.7").Lib("lwt.unix"), Geom(p)}})
//	})
func Executable(p *bob.P, o ExeOpts) *Exe {
	n, name := program(p, o, mlspec.KindExecutable, mlspec.TypeExecutable)
	return &Exe{Node: n, name: name}
}

// NewTest builds an executable and runs it; exit 0 = pass; TAP output parsed if present.
// (SPEC §03.5.1 names it Test, which Go does not allow next to the type Test; see D-M11a-1.)
//
//	var GeomTest = bob.Define(func(p *bob.P) *ocaml.Test {
//		return ocaml.NewTest(p, ocaml.ExeOpts{Name: "geom_test", Main: "test", Deps: []ocaml.Dep{Geom(p)}})
//	})
func NewTest(p *bob.P, o ExeOpts) *Test {
	n, _ := program(p, o, mlspec.KindTest, mlspec.TypeTest)
	return &Test{Node: n}
}

func program(p *bob.P, o ExeOpts, kind, typ string) (bob.Node, string) {
	main := o.Main
	if main == "" {
		main = "main"
	}
	t := mlspec.Target{Type: typ, Main: main, Flags: []string{}, Dune: o.Dune}
	srcs := defaultSrcs(o.Srcs)
	b := newBuilder(p, &t, o.Name)
	if !moduleName.MatchString(main) {
		b.problems = append(b.problems, fmt.Sprintf("Main %q is not a module name (a .ml file name without the extension)", main))
	}
	b.deps(o.Deps)
	b.native(o.Native)
	b.options(o.Options)
	n := ext.New(p, kind, t.Name, b.pkgs, []bob.Sources{srcs}, t)
	b.report(&n)
	return n, t.Name
}

var (
	// libName is a findlib/OCaml library name (also the module namespace when capitalised).
	libName    = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)
	moduleName = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_']{0,127}$`)
	opamName   = regexp.MustCompile(`^[A-Za-z0-9_+][A-Za-z0-9_+-]{0,127}$`)
	findlib    = regexp.MustCompile(`^[A-Za-z0-9_+-]+(\.[A-Za-z0-9_+-]+)*$`)
)

// builder collects a node's deps and spec while checking the options.
type builder struct {
	t        *mlspec.Target
	pkgs     []bob.Package
	problems []string
	confs    map[string]int // opam name → index in t.Conf
}

func newBuilder(p *bob.P, t *mlspec.Target, name string) *builder {
	b := &builder{t: t, confs: map[string]int{}}
	if name == "" {
		dir, _ := ext.CallerDir()
		name = path.Base(dir)
		if dir == "" {
			name = "main"
		}
	}
	if !libName.MatchString(name) {
		b.problems = append(b.problems, fmt.Sprintf("Name %q is not a lower-case OCaml library name ([a-z][a-z0-9_]*)", name))
	}
	t.Name = name
	t.Opam, t.Local, t.Native, t.Conf = []mlspec.Opam{}, []int{}, []int{}, []mlspec.Conf{}
	t.Tools = map[string]int{}
	for _, a := range mlspec.ToolAttrs {
		t.Tools[a] = len(b.pkgs)
		b.pkgs = append(b.pkgs, nixpkgs.Tool(p, a))
	}
	for _, e := range mlspec.ConfTable {
		c := mlspec.Conf{Opam: e.Opam, Libs: []int{}}
		for _, a := range e.Attrs {
			c.Libs = append(c.Libs, len(b.pkgs))
			b.pkgs = append(b.pkgs, nixpkgs.Lib(p, a))
		}
		b.confs[e.Opam] = len(t.Conf)
		t.Conf = append(t.Conf, c)
	}
	return b
}

func (b *builder) deps(ds []Dep) {
	for i, d := range ds {
		switch d := d.(type) {
		case *OpamPkg:
			if d == nil {
				b.problems = append(b.problems, fmt.Sprintf("Deps[%d] is nil", i))
				continue
			}
			b.opam(d, d.name)
		case findlibDep:
			if d.pkg == nil {
				b.problems = append(b.problems, fmt.Sprintf("Deps[%d] is nil", i))
				continue
			}
			if !findlib.MatchString(d.lib) {
				b.problems = append(b.problems, fmt.Sprintf("Deps[%d]: %q is not a findlib library name", i, d.lib))
				continue
			}
			b.opam(d.pkg, d.lib)
		case *Lib:
			if d == nil || reflect.ValueOf(d.Node).IsZero() {
				b.problems = append(b.problems, fmt.Sprintf("Deps[%d] is nil", i))
				continue
			}
			b.t.Local = append(b.t.Local, len(b.pkgs))
			b.pkgs = append(b.pkgs, d)
		default:
			b.problems = append(b.problems, fmt.Sprintf("Deps[%d] is nil", i))
		}
	}
}

func (b *builder) opam(o *OpamPkg, lib string) {
	switch {
	case !opamName.MatchString(o.name):
		b.problems = append(b.problems, fmt.Sprintf("%q is not an opam package name", o.name))
		return
	case strings.ContainsAny(o.req, "\n\x00") || len(o.req) > 256:
		b.problems = append(b.problems, fmt.Sprintf("opam %s: the version formula %q is invalid", o.name, o.req))
		return
	}
	d := mlspec.Opam{Name: o.name, Req: strings.TrimSpace(o.req), Lib: lib}
	if !slices.Contains(b.t.Opam, d) {
		b.t.Opam = append(b.t.Opam, d)
	}
}

func (b *builder) native(ls []cc.Linkable) {
	for i, l := range ls {
		if isNil(l) {
			b.problems = append(b.problems, fmt.Sprintf("Native[%d] is nil", i))
			continue
		}
		b.t.Native = append(b.t.Native, len(b.pkgs))
		b.pkgs = append(b.pkgs, l)
	}
}

func (b *builder) options(opts []Option) {
	for _, o := range opts {
		c, ok := o.(confOption)
		if !ok {
			continue
		}
		if !strings.HasPrefix(c.opam, "conf-") || !opamName.MatchString(c.opam) {
			b.problems = append(b.problems, fmt.Sprintf("Conf %q is not an opam conf-* package name", c.opam))
			continue
		}
		e := mlspec.Conf{Opam: c.opam, Libs: []int{}}
		for i, l := range c.libs {
			if isNil(l) {
				b.problems = append(b.problems, fmt.Sprintf("Conf(%q) library %d is nil", c.opam, i))
				continue
			}
			e.Libs = append(e.Libs, len(b.pkgs))
			b.pkgs = append(b.pkgs, l)
		}
		if i, ok := b.confs[c.opam]; ok { // the project's mapping replaces the shipped one
			b.t.Conf[i] = e
			continue
		}
		b.confs[c.opam] = len(b.t.Conf)
		b.t.Conf = append(b.t.Conf, e)
	}
}

// flags validates extra ocamlopt flags: nothing that names the outputs or paths outside the
// sources.
func (b *builder) flags(fs []string) {
	for _, f := range fs {
		switch {
		case strings.ContainsAny(f, "\n\x00"):
			b.problems = append(b.problems, fmt.Sprintf("flag %q contains a newline or NUL", f))
		case f == "-o" || f == "-a" || f == "-c" || f == "-pack" || f == "-shared":
			b.problems = append(b.problems, fmt.Sprintf("flag %q is not allowed; bob compiles and names the outputs", f))
		case f == "-I" || f == "-ppx" || f == "-pp" || f == "-plugin" || strings.HasPrefix(f, "/") || strings.Contains(f, "/nix/store"):
			b.problems = append(b.problems, fmt.Sprintf("flag %q is not allowed; use Deps (ppx and preprocessors need Dune: true)", f))
		}
	}
}

// report records each problem as a definition error at n's position.
func (b *builder) report(n *bob.Node) {
	for _, pr := range b.problems {
		ext.Errorf("%s: %s: %s", n.DefinedAt(), n.Label(), pr)
	}
}

func list(s []string) []string {
	if s == nil {
		return []string{}
	}
	return slices.Clone(s)
}

// isEmpty reports whether s selects nothing (the zero Sources).
func isEmpty(s bob.Sources) bool { return reflect.ValueOf(s).IsZero() }

// isNil reports whether v is nil or a typed nil pointer.
func isNil(v any) bool {
	if v == nil {
		return true
	}
	rv := reflect.ValueOf(v)
	return rv.Kind() == reflect.Pointer && rv.IsNil()
}

// defaultSrcs is s, or the .ml files of the bob.go's directory and its .mli files when it has
// any (a pattern that matches nothing is a definition error).
func defaultSrcs(s bob.Sources) bob.Sources {
	if !isEmpty(s) {
		return s
	}
	if dir, ok := siteDir(); ok {
		if dir == "" {
			dir = "."
		}
		ents, _ := os.ReadDir(dir)
		for _, e := range ents {
			if strings.HasSuffix(e.Name(), ".mli") {
				return bob.Glob("*.ml", "*.mli")
			}
		}
	}
	return bob.Glob("*.ml")
}

// siteDir is the repository-relative directory of the calling bob.go when it belongs to the
// repository being evaluated (the evaluator runs in the repository root); ok is false for
// dependency repositories, whose files bob cannot list here.
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
