// Package gen turns an ir.Graph into Nix derivations computed in Go (backend B).
// bob owns every builder script here; nixpkgs only supplies toolchains and libraries.
package gen

import (
	"encoding/base64"
	"fmt"
	"path"
	"sort"
	"strings"

	"bobspike/s02/drv"
	"bobspike/s02/ir"
	"bobspike/s02/nixidx"
)

// Built is the result for one bob package.
type Built struct {
	Pkg   *ir.Package
	Drv   *drv.Derivation // the package itself (for fetch: the fixed-output download)
	Unpk  *drv.Derivation // fetch only: unpacked tree, created when used as a source
	Extra []string        // nixpkgs attrs used (for diagnostics)
}

func (b *Built) Out() string { return b.Drv.OutPath }

type Gen struct {
	G      *ir.Graph
	Ix     *nixidx.Index
	Srcs   map[string]string // label -> store path of imported local source
	Built  map[string]*Built
	Order  []*drv.Derivation // creation order (deps first)
	Labels map[string]string // drv path -> label
}

// prelude makes every failing command visible in the log (bob's error translation relies on it).
const prelude = "trap 'echo \"bob-builder: command failed (exit $?): $BASH_COMMAND\" >&2' ERR\n"

var common = []string{"bash", "coreutils", "gnused", "gnugrep", "findutils"}

// Toolchain lists the nixpkgs attributes a package kind needs besides the package's own nixpkgs deps.
func Toolchain(p *ir.Package) []string {
	t := append([]string{}, common...)
	switch p.Kind {
	case ir.KindFetch:
		t = append(t, "gnutar", "gzip", "unzip")
	case ir.KindCCLibrary:
		t = append(t, "stdenv.cc")
		if len(p.Nixpkgs) > 0 {
			t = append(t, "pkg-config")
		}
	case ir.KindRustCrate:
		t = append(t, "rustc", "stdenv.cc")
	case ir.KindProtoGo:
		t = append(t, "protobuf", "protoc-gen-go")
	case ir.KindGoBinary:
		t = append(t, "go", "unzip")
		if p.Go.CGo {
			t = append(t, "stdenv.cc")
		}
	}
	return t
}

func New(g *ir.Graph, ix *nixidx.Index, srcs map[string]string) *Gen {
	return &Gen{G: g, Ix: ix, Srcs: srcs, Built: map[string]*Built{}, Labels: map[string]string{}}
}

// Closure returns the packages needed for target in dependency order.
func Closure(g *ir.Graph, target string) []*ir.Package {
	need := map[string]bool{}
	var visit func(l string)
	visit = func(l string) {
		if need[l] {
			return
		}
		need[l] = true
		if p := g.Get(l); p != nil {
			for _, d := range p.Deps {
				visit(d)
			}
			if p.Src != nil && p.Src.FromLabel != "" {
				visit(p.Src.FromLabel)
			}
		}
	}
	if target == "" {
		return g.Packages
	}
	visit(target)
	var out []*ir.Package
	for _, p := range g.Packages {
		if need[p.Label] {
			out = append(out, p)
		}
	}
	return out
}

type env struct {
	inputs map[string][]string // drv -> outputs
	srcs   map[string]bool
	path   []string
}

func newEnv() *env { return &env{inputs: map[string][]string{}, srcs: map[string]bool{}} }

func (e *env) useDrv(d *drv.Derivation) string {
	e.inputs[d.DrvPath] = []string{"out"}
	return d.OutPath
}

func (e *env) useAttr(ix *nixidx.Index, attr string, outs ...string) (map[string]string, error) {
	a := ix.Attrs[attr]
	if a == nil || a.DrvPath == "" {
		return nil, fmt.Errorf("nixpkgs attribute %s not resolved", attr)
	}
	if len(outs) == 0 {
		outs = []string{"out"}
	}
	have := map[string]bool{}
	for _, o := range e.inputs[a.DrvPath] {
		have[o] = true
	}
	res := map[string]string{}
	for _, o := range outs {
		p, ok := a.Outputs[o]
		if !ok {
			continue
		}
		res[o] = p
		if !have[o] {
			e.inputs[a.DrvPath] = append(e.inputs[a.DrvPath], o)
			have[o] = true
		}
		e.path = append(e.path, p+"/bin")
	}
	return res, nil
}

func (g *Gen) finish(name string, e *env, script string, extraEnv map[string]string) (*drv.Derivation, error) {
	bash := g.Ix.Attrs["bash"].Outputs["out"] + "/bin/bash"
	envm := map[string]string{"name": name, "system": g.G.System, "builder": bash,
		"PATH": strings.Join(uniq(e.path), ":")}
	for k, v := range extraEnv {
		envm[k] = v
	}
	args := []string{"-euo", "pipefail", "-c", prelude + script}
	if len(script) > 64<<10 { // one argv string is limited to 128 KiB on Linux
		envm["bobScript"] = prelude + script
		envm["passAsFile"] = "bobScript"
		args = []string{"-euo", "pipefail", "-c", `. "$bobScriptPath"`}
	}
	d := &drv.Derivation{Name: name, System: g.G.System, Builder: bash,
		Args: args, Env: envm, InputDrvs: e.inputs}
	for s := range e.srcs {
		d.InputSrcs = append(d.InputSrcs, s)
	}
	sort.Strings(d.InputSrcs)
	if err := d.Finalize(g.Ix.Cache); err != nil {
		return nil, err
	}
	g.Ix.Cache.Set(d.DrvPath, d.Modulo())
	g.Order = append(g.Order, d)
	return d, nil
}

// Generate creates derivations for pkgs (must be in dependency order).
func (g *Gen) Generate(pkgs []*ir.Package) error {
	for _, p := range pkgs {
		if err := g.one(p); err != nil {
			return fmt.Errorf("%s (%s): %w", p.Label, p.DefinedAt, err)
		}
	}
	return nil
}

func sanitize(label string) string {
	s := strings.TrimPrefix(label, "//")
	s = strings.NewReplacer("/", "-", ":", "-", "@", "-").Replace(s)
	return s
}

func (g *Gen) srcDir(e *env, p *ir.Package) (string, error) {
	if p.Src == nil {
		return "", fmt.Errorf("no source")
	}
	if p.Src.Path != "" {
		sp, ok := g.Srcs[p.Label]
		if !ok {
			return "", fmt.Errorf("source %s not imported", p.Src.Path)
		}
		e.srcs[sp] = true
		return sp, nil
	}
	f := g.Built[p.Src.FromLabel]
	if f.Unpk == nil {
		if err := g.unpack(f); err != nil {
			return "", err
		}
	}
	return path.Join(e.useDrv(f.Unpk), p.Src.Subdir), nil
}

func (g *Gen) one(p *ir.Package) error {
	b := &Built{Pkg: p}
	g.Built[p.Label] = b
	var err error
	switch p.Kind {
	case ir.KindFetch:
		b.Drv, err = g.fetch(p)
	case ir.KindCCLibrary:
		b.Drv, err = g.cc(p)
	case ir.KindRustCrate:
		b.Drv, err = g.rust(p)
	case ir.KindProtoGo:
		b.Drv, err = g.protoGo(p)
	case ir.KindGoBinary:
		b.Drv, err = g.goBinary(p)
	default:
		err = fmt.Errorf("unknown kind %q", p.Kind)
	}
	if err == nil {
		g.Labels[b.Drv.DrvPath] = p.Label
	}
	return err
}

// fetch: Nix's builtin:fetchurl as a fixed-output derivation, flat sha256 of the file (as in bob.lock).
func (g *Gen) fetch(p *ir.Package) (*drv.Derivation, error) {
	h := strings.TrimPrefix(p.Fetch.Hash, "sha256-")
	raw, err := base64.StdEncoding.DecodeString(h)
	if err != nil || len(raw) != 32 {
		return nil, fmt.Errorf("bad hash %q", p.Fetch.Hash)
	}
	d := &drv.Derivation{Name: p.Fetch.Name, System: "builtin", Builder: "builtin:fetchurl",
		Env: map[string]string{"name": p.Fetch.Name, "system": "builtin", "builder": "builtin:fetchurl",
			"url": p.Fetch.URL, "urls": p.Fetch.URL, "outputHash": p.Fetch.Hash, "outputHashAlgo": "",
			"outputHashMode": "flat", "preferLocalBuild": "1", "executable": "", "unpack": ""},
		FixedAlgo: "sha256", FixedMode: "flat", FixedHash: raw}
	if err := d.Finalize(g.Ix.Cache); err != nil {
		return nil, err
	}
	g.Ix.Cache.Set(d.DrvPath, d.Modulo())
	g.Order = append(g.Order, d)
	return d, nil
}

func (g *Gen) unpack(f *Built) error {
	e := newEnv()
	if _, err := g.attrs(e, f.Pkg, Toolchain(f.Pkg)); err != nil {
		return err
	}
	in := e.useDrv(f.Drv)
	var s string
	switch f.Pkg.Fetch.Unpack {
	case "tar.gz":
		s = fmt.Sprintf("mkdir -p $out && tar -xzf %s -C $out", in)
	case "zip":
		s = fmt.Sprintf("mkdir -p $out && unzip -q %s -d $out", in)
	default:
		s = fmt.Sprintf("mkdir -p $out && cp %s $out/", in)
	}
	d, err := g.finish(strings.TrimSuffix(strings.TrimSuffix(f.Pkg.Fetch.Name, ".crate"), ".zip")+"-src", e, s, nil)
	if err != nil {
		return err
	}
	f.Unpk = d
	g.Labels[d.DrvPath] = f.Pkg.Label
	return nil
}

func (g *Gen) attrs(e *env, p *ir.Package, tool []string) (map[string]map[string]string, error) {
	got := map[string]map[string]string{}
	for _, a := range tool {
		o, err := e.useAttr(g.Ix, a)
		if err != nil {
			return nil, err
		}
		got[a] = o
	}
	for _, a := range p.Nixpkgs {
		o, err := e.useAttr(g.Ix, a, "out", "dev", "lib", "bin")
		if err != nil {
			return nil, err
		}
		got[a] = o
	}
	return got, nil
}

// link flags for direct + transitive bob library deps.
func (g *Gen) libDeps(p *ir.Package, e *env) (incs, libs []string) {
	seen := map[string]bool{}
	var walk func(l string, direct bool)
	walk = func(l string, direct bool) {
		if seen[l] {
			return
		}
		seen[l] = true
		b := g.Built[l]
		switch b.Pkg.Kind {
		case ir.KindCCLibrary:
			out := e.useDrv(b.Drv)
			if direct {
				incs = append(incs, "-I"+out+"/include")
			}
			libs = append(libs, "-L"+out+"/lib", "-l"+b.Pkg.CC.Lib)
			if !b.Pkg.CC.Shared {
				for _, d := range b.Pkg.Deps {
					walk(d, false)
				}
			}
		case ir.KindRustCrate:
			if b.Pkg.Rust.CrateType == "staticlib" {
				out := e.useDrv(b.Drv)
				if direct {
					incs = append(incs, "-I"+out+"/include")
				}
				libs = append(libs, "-L"+out+"/lib", "-l"+b.Pkg.Rust.CrateName)
			}
		}
	}
	for _, d := range p.Deps {
		walk(d, true)
	}
	return
}

func (g *Gen) directIncs(p *ir.Package, e *env) []string {
	var incs []string
	for _, l := range p.Deps {
		if b := g.Built[l]; b.Pkg.Kind == ir.KindCCLibrary || b.Pkg.Kind == ir.KindRustCrate && b.Pkg.Rust.CrateType == "staticlib" {
			incs = append(incs, "-I"+e.useDrv(b.Drv)+"/include")
		}
	}
	return incs
}

func (g *Gen) cc(p *ir.Package) (*drv.Derivation, error) {
	e := newEnv()
	nx, err := g.attrs(e, p, Toolchain(p))
	if err != nil {
		return nil, err
	}
	src, err := g.srcDir(e, p)
	if err != nil {
		return nil, err
	}
	var incs, libs []string
	if p.CC.Shared {
		incs, libs = g.libDeps(p, e)
	} else { // a static library needs only its direct deps' headers; linking happens at the consumer
		incs = g.directIncs(p, e)
	}
	var pc []string
	for _, a := range p.Nixpkgs {
		for _, o := range []string{"dev", "out", "lib"} {
			if v, ok := nx[a][o]; ok {
				pc = append(pc, v+"/lib/pkgconfig")
			}
		}
	}
	compiler := "gcc"
	if p.CC.Lang == "c++" {
		compiler = "g++"
	}
	var s strings.Builder
	fmt.Fprintf(&s, "mkdir -p $out/lib $out/include obj\ncp -r %s/%s/. $out/include/\n", src, p.CC.Hdrs)
	pcflags, pclibs := "", ""
	if len(pc) > 0 {
		fmt.Fprintf(&s, "export PKG_CONFIG_PATH=%s\nmods=$(for d in ${PKG_CONFIG_PATH//:/ }; do if [ -d $d ]; then ls $d; fi; done | grep '\\.pc$' | sed 's/\\.pc$//' | sort -u)\n", strings.Join(pc, ":"))
		pcflags, pclibs = "$(pkg-config --cflags $mods)", "$(pkg-config --libs $mods)"
	}
	flags := fmt.Sprintf("-std=%s -fPIC -O2 -I%s/%s %s %s", p.CC.Std, src, p.CC.Hdrs, strings.Join(incs, " "), pcflags)
	for i, f := range p.CC.Srcs {
		fmt.Fprintf(&s, "%s %s -c %s/%s -o obj/%d.o\n", compiler, flags, src, f, i)
	}
	if p.CC.Shared {
		fmt.Fprintf(&s, "%s -shared -Wl,-soname,lib%s.so -o $out/lib/lib%s.so obj/*.o %s %s\n", compiler, p.CC.Lib, p.CC.Lib, strings.Join(libs, " "), pclibs)
	} else {
		fmt.Fprintf(&s, "ar rcs $out/lib/lib%s.a obj/*.o\n", p.CC.Lib)
	}
	return g.finish(sanitize(p.Label), e, s.String(), nil)
}

func (g *Gen) rust(p *ir.Package) (*drv.Derivation, error) {
	e := newEnv()
	if _, err := g.attrs(e, p, Toolchain(p)); err != nil {
		return nil, err
	}
	src, err := g.srcDir(e, p)
	if err != nil {
		return nil, err
	}
	r := p.Rust
	args := []string{"rustc", "--crate-name", r.CrateName, "--edition", r.Edition, "--crate-type", r.CrateType,
		"-C", "opt-level=2", "-C", "metadata=" + r.CrateName + "-" + r.Version}
	if p.Src.FromLabel != "" {
		args = append(args, "--cap-lints", "allow")
	}
	for _, f := range r.Features {
		args = append(args, "--cfg", fmt.Sprintf("'feature=\"%s\"'", f))
	}
	names := make([]string, 0, len(r.Externs))
	for n := range r.Externs {
		names = append(names, n)
	}
	sort.Strings(names)
	trans := map[string]bool{}
	var walk func(l string)
	walk = func(l string) {
		if trans[l] {
			return
		}
		trans[l] = true
		for _, d := range g.Built[l].Pkg.Deps {
			walk(d)
		}
	}
	for _, n := range names {
		b := g.Built[r.Externs[n]]
		out := e.useDrv(b.Drv)
		args = append(args, "--extern", n+"="+out+"/lib/lib"+b.Pkg.Rust.CrateName+".rlib")
		walk(r.Externs[n])
	}
	tl := make([]string, 0, len(trans))
	for l := range trans {
		tl = append(tl, l)
	}
	sort.Strings(tl)
	for _, l := range tl {
		args = append(args, "-L", "dependency="+e.useDrv(g.Built[l].Drv)+"/lib")
	}
	args = append(args, src+"/"+r.Root, "--out-dir", "$out/lib")
	s := "mkdir -p $out/lib\n" + strings.Join(args, " ") + "\n"
	if r.Hdrs != "" {
		s += fmt.Sprintf("mkdir -p $out/include && cp -r %s/%s/. $out/include/\n", src, r.Hdrs)
	}
	return g.finish(sanitize(p.Label), e, s, nil)
}

func (g *Gen) protoGo(p *ir.Package) (*drv.Derivation, error) {
	e := newEnv()
	if _, err := g.attrs(e, p, Toolchain(p)); err != nil {
		return nil, err
	}
	src, err := g.srcDir(e, p)
	if err != nil {
		return nil, err
	}
	s := fmt.Sprintf("mkdir -p $out\ncd %s\nprotoc --go_out=$out --go_opt=paths=source_relative %s\n", src, strings.Join(p.Proto.Files, " "))
	return g.finish(sanitize(p.Label), e, s, nil)
}

// GoMod renders the go.mod bob generates from the graph (source of truth is bob.go, R-FW-006).
func GoMod(p *ir.Package) string {
	var s strings.Builder
	fmt.Fprintf(&s, "module %s\n\ngo %s\n\nrequire (\n", p.Go.ModulePath, p.Go.GoVersion)
	for _, r := range p.Go.Requires {
		fmt.Fprintf(&s, "\t%s %s\n", r.Path, r.Version)
	}
	s.WriteString(")\n")
	return s.String()
}

func (g *Gen) goBinary(p *ir.Package) (*drv.Derivation, error) {
	e := newEnv()
	if _, err := g.attrs(e, p, Toolchain(p)); err != nil {
		return nil, err
	}
	src, err := g.srcDir(e, p)
	if err != nil {
		return nil, err
	}
	var s strings.Builder
	fmt.Fprintf(&s, "mkdir -p $out/bin $TMPDIR/mod\ncp -r %s/. $TMPDIR/mod/\nchmod -R u+w $TMPDIR/mod\ncd $TMPDIR/mod\n", src)
	for _, gg := range p.Go.Generated {
		fmt.Fprintf(&s, "mkdir -p %s && cp %s/*.go %s/\n", gg.Subdir, e.useDrv(g.Built[gg.Label].Drv), gg.Subdir)
	}
	s.WriteString("printf '%s' \"$gomod\" > go.mod\n")
	for _, r := range p.Go.Requires {
		zip := e.useDrv(g.Built[r.Label].Drv)
		fmt.Fprintf(&s, "d=$TMPDIR/proxy/%s/@v; mkdir -p $d; cp %s $d/%s.zip\n", r.Path, zip, r.Version)
		fmt.Fprintf(&s, "(unzip -p %s '%s@%s/go.mod' || printf 'module %s\\n') > $d/%s.mod\n", zip, r.Path, r.Version, r.Path, r.Version)
		fmt.Fprintf(&s, "printf '{\"Version\":\"%s\"}' > $d/%s.info; echo %s >> $d/list\n", r.Version, r.Version, r.Version)
	}
	incs, libs := g.libDeps(p, e)
	cgo := "0"
	if p.Go.CGo {
		cgo = "1"
	}
	fmt.Fprintf(&s, "export GOPROXY=file://$TMPDIR/proxy GOSUMDB=off GOFLAGS=-mod=mod GOTOOLCHAIN=local GOCACHE=$TMPDIR/gocache GOPATH=$TMPDIR/gopath GOMODCACHE=$TMPDIR/gomodcache HOME=$TMPDIR CGO_ENABLED=%s CC=gcc\n", cgo)
	fmt.Fprintf(&s, "export CGO_CFLAGS=%q CGO_LDFLAGS=%q\n", strings.Join(incs, " "), strings.Join(libs, " "))
	fmt.Fprintf(&s, "go build -trimpath -o $out/bin/%s %s\n", p.Go.Binary, p.Go.Main)
	return g.finish(sanitize(p.Label), e, s.String(), map[string]string{"gomod": GoMod(p)})
}

func uniq(xs []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, x := range xs {
		if !seen[x] {
			seen[x] = true
			out = append(out, x)
		}
	}
	return out
}
