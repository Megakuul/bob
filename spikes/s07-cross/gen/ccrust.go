package gen

import (
	"fmt"
	"sort"
	"strings"

	"bobspike/s07/drv"
	"bobspike/s07/ir"
)

// host returns the generator for host units (proc-macros, build scripts and their deps). It is a
// separate generator even for native builds, so a host unit has one derivation whatever the
// target is (native and cross builds share it).
func (g *Gen) host() *Gen {
	if g.role == "host" {
		return g
	}
	if g.hostG == nil {
		g.hostG = &Gen{G: g.G, Ix: g.Ix, Srcs: g.Srcs, Built: map[string]*Built{}, Labels: g.Labels,
			Host: g.Host, Tgt: g.Host, V: Variant{Profile: "release", Features: g.V.Features}, parent: g, role: "host"}
	}
	return g.hostG
}

// ensure generates label (and its deps) in this generator. Platform-independent packages
// (downloads, codegen) are shared with the root generator.
func (g *Gen) ensure(label string) (*Built, error) {
	if b := g.Built[label]; b != nil {
		return b, nil
	}
	p := g.G.Get(label)
	if p.Kind == ir.KindFetch || p.Kind == ir.KindProtoGo {
		root := g
		for root.parent != nil {
			root = root.parent
		}
		if b := root.Built[label]; b != nil {
			g.Built[label] = b
			return b, nil
		}
	}
	deps := append([]string{}, p.Deps...)
	if p.Src != nil && p.Src.FromLabel != "" {
		deps = append(deps, p.Src.FromLabel)
	}
	if p.Rust != nil {
		for _, l := range p.Rust.Externs {
			deps = append(deps, l)
		}
	}
	for _, d := range deps {
		if _, err := g.ensure(d); err != nil {
			return nil, err
		}
	}
	if err := g.one(p); err != nil {
		return nil, err
	}
	return g.Built[label], nil
}

func (g *Gen) cc(p *ir.Package) (*drv.Derivation, error) {
	e := newEnv()
	nx, err := g.attrs(e, p, g.Toolchain(p))
	if err != nil {
		return nil, err
	}
	src, err := g.srcDir(e, p)
	if err != nil {
		return nil, err
	}
	pr := g.ccProj()
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
	pre := g.CCPrefix()
	compiler := pre + "gcc"
	if p.CC.Lang == "c++" {
		compiler = pre + "g++"
	}
	var s strings.Builder
	fmt.Fprintf(&s, "mkdir -p $out/lib $out/include obj\ncp -r %s/%s/. $out/include/\n", src, p.CC.Hdrs)
	pcflags, pclibs := "", ""
	if len(pc) > 0 {
		fmt.Fprintf(&s, "export PKG_CONFIG_PATH=%s\nmods=$(for d in ${PKG_CONFIG_PATH//:/ }; do if [ -d $d ]; then ls $d; fi; done | grep '\\.pc$' | sed 's/\\.pc$//' | sort -u)\n", strings.Join(pc, ":"))
		pcflags, pclibs = "$(pkg-config --cflags $mods)", "$(pkg-config --libs $mods)"
	}
	flags := fmt.Sprintf("-std=%s -fPIC %s -I%s/%s %s %s", p.CC.Std, pr.ccFlags(), src, p.CC.Hdrs, strings.Join(incs, " "), pcflags)
	for i, f := range p.CC.Srcs {
		fmt.Fprintf(&s, "%s %s -c %s/%s -o obj/%d.o\n", compiler, flags, src, f, i)
	}
	if p.CC.Shared {
		fmt.Fprintf(&s, "%s -shared %s -Wl,-soname,lib%s.so -o $out/lib/lib%s.so obj/*.o %s %s\n", compiler, pr.sanLink(), p.CC.Lib, p.CC.Lib, strings.Join(libs, " "), pclibs)
	} else {
		fmt.Fprintf(&s, "%sar rcs $out/lib/lib%s.a obj/*.o\n", pre, p.CC.Lib)
	}
	return g.finish(sanitize(p.Label)+g.suffix(pr), e, s.String(), nil)
}

// RustStdDrv copies the target's std rlibs out of the target system's nixpkgs rustc (same
// source, same version string as the host rustc, so the metadata is compatible).
func (g *Gen) RustStdDrv() (*drv.Derivation, error) {
	if g.rustStdD != nil {
		return g.rustStdD, nil
	}
	e := newEnv()
	for _, a := range common {
		if _, err := e.useAttr(g.Ix, a); err != nil {
			return nil, err
		}
	}
	o, err := e.useAttr(g.Ix, "@"+g.Tgt.Nix+"/rustc.unwrapped")
	if err != nil {
		return nil, err
	}
	t := g.Tgt.Rust
	s := fmt.Sprintf("d=$out/lib/rustlib/%s/lib; mkdir -p $d\nsed -n 's/^file://p' %s/lib/rustlib/manifest-rust-std-%s | while read -r f; do cp \"$f\" $d/; done\n", t, o["out"], t)
	d, err := g.finish("rust-std-"+t, e, s, nil)
	g.rustStdD = d
	return d, err
}

func rustArch(triple string) string { return strings.SplitN(triple, "-", 2)[0] }

func (g *Gen) rustOpt(pr Proj) []string {
	switch {
	case g.role == "host":
		return []string{"-C", "opt-level=0"}
	case pr.Opt == "dbg":
		return []string{"-C", "opt-level=0", "-C", "debuginfo=2", "-C", "debug-assertions=on"}
	}
	return []string{"-C", "opt-level=2"}
}

// targetArgs: what a target unit adds when cross-compiling.
func (g *Gen) targetArgs(e *env) ([]string, error) {
	if !g.Cross() || g.role == "host" {
		return nil, nil
	}
	sd, err := g.RustStdDrv()
	if err != nil {
		return nil, err
	}
	return []string{"--target", g.Tgt.Rust, "--sysroot", e.useDrv(sd), "-C", "linker=" + g.CCPrefix() + "cc"}, nil
}

func (g *Gen) rust(p *ir.Package) (*drv.Derivation, error) {
	r := p.Rust
	if r.CrateType == "proc-macro" && g.role != "host" {
		hb, err := g.host().ensure(p.Label)
		if err != nil {
			return nil, err
		}
		return hb.Drv, nil
	}
	e := newEnv()
	if _, err := g.attrs(e, p, g.Toolchain(p)); err != nil {
		return nil, err
	}
	src, err := g.srcDir(e, p)
	if err != nil {
		return nil, err
	}
	pr := g.rustProj(p.Label)
	if g.role == "host" {
		pr = Proj{Features: pr.Features}
	}
	md := r.CrateName + "-" + r.Version
	if t := pr.Tag(); t != "" {
		md += "-" + t
	}
	args := []string{"rustc", "--crate-name", r.CrateName, "--edition", r.Edition, "--crate-type", r.CrateType}
	args = append(args, g.rustOpt(pr)...)
	args = append(args, "-C", "metadata="+md)
	ta, err := g.targetArgs(e)
	if err != nil {
		return nil, err
	}
	args = append(args, ta...)
	if p.Src.FromLabel != "" {
		args = append(args, "--cap-lints", "allow")
	}
	if r.CrateType == "proc-macro" {
		args = append(args, "--extern", "proc_macro")
	}
	feats := append(append([]string{}, r.Features...), pr.Features...)
	for _, f := range feats {
		args = append(args, "--cfg", fmt.Sprintf("'feature=\"%s\"'", f))
	}
	pre := ""
	if r.BuildScript != "" {
		run, err := g.buildRun(p, pr, feats)
		if err != nil {
			return nil, err
		}
		ro := e.useDrv(run)
		pre = fmt.Sprintf("export OUT_DIR=%s/out-dir\ncfgs=$(sed -n 's/^cargo::\\{0,1\\}rustc-cfg=\\([A-Za-z_][A-Za-z0-9_]*\\)$/--cfg \\1/p' %s/output | tr '\\n' ' ')\n", ro, ro)
		args = append(args, "$cfgs")
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
		b := g.Built[l]
		if b.Pkg.Rust != nil && b.Pkg.Rust.CrateType == "proc-macro" {
			return // its deps are host units; not needed at target link time
		}
		for _, d := range b.Pkg.Deps {
			walk(d)
		}
	}
	for _, n := range names {
		b := g.Built[r.Externs[n]]
		out := e.useDrv(b.Drv)
		file := "/lib/lib" + b.Pkg.Rust.CrateName + ".rlib"
		if b.Pkg.Rust.CrateType == "proc-macro" {
			file = "/lib/lib" + b.Pkg.Rust.CrateName + ".so"
		}
		args = append(args, "--extern", n+"="+out+file)
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
	outDir := "$out/lib"
	if r.CrateType == "bin" {
		outDir = "$out/bin"
	}
	args = append(args, src+"/"+r.Root, "--out-dir", outDir)
	s := pre + "mkdir -p " + outDir + "\n" + strings.Join(args, " ") + "\n"
	if r.Hdrs != "" {
		s += fmt.Sprintf("mkdir -p $out/include && cp -r %s/%s/. $out/include/\n", src, r.Hdrs)
	}
	name := sanitize(p.Label)
	if g.role == "host" {
		name += "-host"
	}
	return g.finish(name+g.suffix(pr), e, s, nil)
}

// buildRun: the build script is compiled once for the host (shared by all targets) and run per
// target with the target's description in its environment.
func (g *Gen) buildRun(p *ir.Package, pr Proj, feats []string) (*drv.Derivation, error) {
	hg := g.host()
	key := p.Label + "#build-script"
	bin, ok := hg.Built[key]
	if !ok {
		e := newEnv()
		if _, err := hg.attrs(e, &ir.Package{Kind: ir.KindRustCrate}, hg.Toolchain(&ir.Package{Kind: ir.KindRustCrate})); err != nil {
			return nil, err
		}
		src, err := hg.srcDir(e, p)
		if err != nil {
			return nil, err
		}
		s := fmt.Sprintf("mkdir -p $out/bin\nrustc --crate-name build_script_build --edition %s --crate-type bin -C opt-level=0 %s/%s -o $out/bin/build-script\n",
			p.Rust.Edition, src, p.Rust.BuildScript)
		d, err := hg.finish(sanitize(p.Label)+"-build-script", e, s, nil)
		if err != nil {
			return nil, err
		}
		bin = &Built{Pkg: p, Drv: d}
		hg.Built[key] = bin
		g.Labels[d.DrvPath] = p.Label
	}
	e := newEnv()
	if _, err := g.attrs(e, &ir.Package{Kind: ir.KindRustCrate}, g.Toolchain(&ir.Package{Kind: ir.KindRustCrate})); err != nil {
		return nil, err
	}
	src, err := g.srcDir(e, p)
	if err != nil {
		return nil, err
	}
	profile, optl, dbg := "release", "2", "false"
	if pr.Opt == "dbg" {
		profile, optl, dbg = "debug", "0", "true"
	}
	if g.role == "host" {
		optl = "0"
	}
	env := map[string]string{"TARGET": g.Tgt.Rust, "HOST": g.Host.Rust, "CARGO_CFG_TARGET_ARCH": rustArch(g.Tgt.Rust),
		"CARGO_CFG_TARGET_OS": "linux", "CARGO_CFG_TARGET_ENV": "gnu", "CARGO_CFG_TARGET_POINTER_WIDTH": "64",
		"CARGO_CFG_TARGET_ENDIAN": "little", "PROFILE": profile, "OPT_LEVEL": optl, "DEBUG": dbg,
		"CARGO_PKG_NAME": p.Rust.CrateName, "CARGO_PKG_VERSION": p.Rust.Version, "CC": g.CCPrefix() + "cc"}
	for _, f := range feats {
		env["CARGO_FEATURE_"+strings.ToUpper(strings.ReplaceAll(f, "-", "_"))] = "1"
	}
	s := fmt.Sprintf("mkdir -p $out/out-dir\ncd %s\nOUT_DIR=$out/out-dir CARGO_MANIFEST_DIR=%s %s/bin/build-script > $out/output\n", src, src, e.useDrv(bin.Drv))
	return g.finish(sanitize(p.Label)+"-build-run"+g.suffix(pr), e, s, env)
}
