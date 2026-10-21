package gen

// Fine-grained Go derivations (spike S2): std as one derivation per toolchain/variant, then one
// derivation per group of Go packages. A group is a package ("package"), a module ("module"),
// or third-party-per-module + workspace-per-package ("hybrid"). Each derivation calls
// `go tool compile` / `cgo` / `link` directly with an importcfg; outputs are package archives.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path"
	"sort"
	"strings"

	"bobspike/s07/drv"
	"bobspike/s07/gopkg"
	"bobspike/s07/ir"
)

type GoBuild struct {
	Gran         string // package | module | hybrid
	Plan         *gopkg.Plan
	SrcStore     map[string]string // workspace package import path -> imported source store path
	GenDrv       map[string]*drv.Derivation
	ModDrv       map[string]*drv.Derivation // module path -> unpack drv (third-party)
	CGo          bool
	Lib          *ir.Package // the go_binary package (for cc deps)
	CacheProgSrc string      // store path of cacheprog/ sources (V3 only)

	Cutoff  bool              // compile with -linkobj; dependents import export data by content
	Exports map[string]string // import path -> content-addressed store path of its export data
	Std     *drv.Derivation
	GOARCH  string
	Tools   *drv.Derivation // cmd/pack (not shipped prebuilt since Go 1.24)
	Groups  []*Group
	byPkg   map[string]*Group
}

type Group struct {
	Level int
	Key   string
	Pkgs  []*gopkg.Pkg // dependency order
	Deps  []*Group     // direct group deps
	Drv   *drv.Derivation
}

func (gb *GoBuild) key(p *gopkg.Pkg) string {
	switch gb.Gran {
	case "module":
		return "mod:" + p.Mod.Path
	case "hybrid":
		if !p.Mod.Main {
			return "mod:" + p.Mod.Path
		}
	}
	return "pkg:" + p.ImportPath
}

func (g *Gen) goToolchain(e *env, cgo bool) (goroot string, err error) {
	tools := []string{"bash", "coreutils", "gnused", "gnugrep", "findutils", "go"}
	if cgo {
		tools = append(tools, g.CCAttr())
	}
	for _, a := range tools {
		if _, err := e.useAttr(g.Ix, a); err != nil {
			return "", err
		}
	}
	return g.Ix.Attrs["go"].Outputs["out"] + "/share/go", nil
}

// StdDrv compiles the whole standard library once per toolchain, target, cgo setting and the
// Go-relevant part of the variant.
func (g *Gen) StdDrv(cgo bool) (*drv.Derivation, error) {
	e := newEnv()
	if _, err := g.goToolchain(e, cgo); err != nil {
		return nil, err
	}
	pr := g.goProj()
	c := "0"
	name := "go-std-nocgo"
	if cgo {
		c, name = "1", "go-std-cgo"
	}
	s := fmt.Sprintf(`export HOME=$TMPDIR GOCACHE=$TMPDIR/cache GOPATH=$TMPDIR/gopath GOTOOLCHAIN=local GOFLAGS= GOENV=off CGO_ENABLED=%s CC=%sgcc GOOS=linux GOARCH=%s CGO_CFLAGS=%s
cd $TMPDIR
%s
go list -trimpath -export -deps %s -f '{{if .Export}}{{.ImportPath}} {{.Export}}{{end}}' std > list
mkdir -p $out
while read -r ip f; do
  mkdir -p "$out/$(dirname "$ip")"; cp "$f" "$out/$ip.a"
  echo "packagefile $ip=$out/$ip.a" >> $out/importcfg
done < list
`, c, g.CCPrefix(), g.Tgt.GOARCH, shq(goCgoCflags(pr)), g.ccShim(), goListFlags(pr))
	return g.finish(name+g.suffix(pr), e, s, nil)
}

// ccShim: cmd/go's -asan/-race checks require `$CC --version` to start with "gcc"; a prefixed
// cross gcc prints "x86_64-unknown-linux-gnu-gcc (GCC) …". Only the std build runs cmd/go.
func (g *Gen) ccShim() string {
	if !g.Cross() {
		return ""
	}
	p := g.CCPrefix()
	return fmt.Sprintf(`mkdir -p $TMPDIR/ccbin
printf '#!%%s\nif [ "$1" = --version ]; then echo "gcc (GCC) $(%sgcc -dumpversion)"; exit 0; fi\nexec %sgcc "$@"\n' "$BASH" > $TMPDIR/ccbin/gcc
chmod +x $TMPDIR/ccbin/gcc; export CC=$TMPDIR/ccbin/gcc`, p, p)
}

// goCgoCflags mirrors cmd/go's default CGO_CFLAGS plus what -asan adds.
func goCgoCflags(pr Proj) string {
	f := "-O2 -g"
	if pr.Opt == "dbg" {
		f = "-O0 -g"
	}
	if len(pr.Sanitize) > 0 {
		f += " -fsanitize=address"
	}
	return f
}

func goListFlags(pr Proj) string {
	var f []string
	if pr.Opt == "dbg" {
		f = append(f, `-gcflags=all="-N -l"`)
	}
	if pr.Race {
		f = append(f, "-race")
	}
	if len(pr.Sanitize) > 0 {
		f = append(f, "-asan")
	}
	return strings.Join(f, " ")
}

// goCompileFlags: what the variant adds to every `compile` call.
func goCompileFlags(pr Proj) []string {
	var f []string
	if pr.Opt == "dbg" {
		f = append(f, "-N", "-l")
	}
	if pr.Race {
		f = append(f, "-race")
	}
	if len(pr.Sanitize) > 0 {
		f = append(f, "-asan")
	}
	return f
}

// GoToolTags are the build tags cmd/go sets for the variant.
func (g *Gen) GoToolTags() []string {
	pr := g.goProj()
	var t []string
	if pr.Race {
		t = append(t, "race")
	}
	if len(pr.Sanitize) > 0 {
		t = append(t, "asan")
	}
	return t
}

// ToolsDrv builds the Go tools cmd/go runs internally but nixpkgs Go does not ship (pack).
func (g *Gen) ToolsDrv() (*drv.Derivation, error) {
	e := newEnv()
	if _, err := g.goToolchain(e, false); err != nil {
		return nil, err
	}
	s := "export HOME=$TMPDIR GOCACHE=$TMPDIR/cache GOPATH=$TMPDIR/gopath GOTOOLCHAIN=local GOFLAGS= GOENV=off CGO_ENABLED=0\ncd $TMPDIR\nmkdir -p $out/bin\ngo build -trimpath -o $out/bin/pack cmd/pack\n"
	return g.finish("go-tools", e, s, nil)
}

// GoPlanInputs returns the derivations that must exist before planning (module sources, codegen).
func (g *Gen) GoPlanInputs(p *ir.Package) (mods map[string]*drv.Derivation, gens map[string]*drv.Derivation, err error) {
	mods, gens = map[string]*drv.Derivation{}, map[string]*drv.Derivation{}
	for _, r := range p.Go.Requires {
		f := g.Built[r.Label]
		if f.Unpk == nil {
			if err := g.unpack(f); err != nil {
				return nil, nil, err
			}
		}
		mods[r.Path] = f.Unpk
	}
	for _, gg := range p.Go.Generated {
		gens[p.Go.ModulePath+"/"+gg.Subdir] = g.Built[gg.Label].Drv
	}
	return mods, gens, nil
}

// GoFine creates std + group derivations; returns the derivation producing the binary.
func (g *Gen) GoFine(gb *GoBuild) (*drv.Derivation, error) {
	order, err := g.GoPrepare(gb)
	if err != nil {
		return nil, err
	}
	var final *drv.Derivation
	for _, gr := range order {
		if err := g.GroupDrv(gb, gr); err != nil {
			return nil, err
		}
		for _, p := range gr.Pkgs {
			if p == gb.Plan.Main {
				final = gr.Drv
			}
		}
	}
	return final, nil
}

// GoPrepare creates std/tools derivations and the group graph; returns groups in dependency order
// with Level set (longest path from a leaf).
func (g *Gen) GoPrepare(gb *GoBuild) ([]*Group, error) {
	var err error
	if gb.Std, err = g.StdDrv(gb.CGo); err != nil {
		return nil, err
	}
	if gb.Tools, err = g.ToolsDrv(); err != nil {
		return nil, err
	}
	gb.byPkg = map[string]*Group{}
	byKey := map[string]*Group{}
	for _, p := range gb.Plan.Order {
		k := gb.key(p)
		gr := byKey[k]
		if gr == nil {
			gr = &Group{Key: k}
			byKey[k] = gr
			gb.Groups = append(gb.Groups, gr)
		}
		gr.Pkgs = append(gr.Pkgs, p)
		gb.byPkg[p.ImportPath] = gr
	}
	// group edges + cycle check (modules can import each other in a cycle; packages cannot)
	for _, gr := range gb.Groups {
		seen := map[*Group]bool{}
		for _, p := range gr.Pkgs {
			for _, i := range p.Imports {
				d := gb.byPkg[i]
				if d != gr && !seen[d] {
					seen[d] = true
					gr.Deps = append(gr.Deps, d)
				}
			}
		}
		sort.Slice(gr.Deps, func(a, b int) bool { return gr.Deps[a].Key < gr.Deps[b].Key })
	}
	order, err := topoGroups(gb.Groups)
	if err != nil {
		return nil, err
	}
	for _, gr := range order {
		for _, d := range gr.Deps {
			gr.Level = max(gr.Level, d.Level+1)
		}
	}
	return order, nil
}

// GroupDrv computes the derivation of one group (its deps must already have theirs; in cutoff
// mode their export data must already be imported into gb.Exports).
func (g *Gen) GroupDrv(gb *GoBuild, gr *Group) error {
	var err error
	if gr.Drv, err = g.groupDrv(gb, gr); err != nil {
		return err
	}
	g.Labels[gr.Drv.DrvPath] = gb.Lib.Label
	return nil
}

// LinkDrv (cutoff mode) links the binary from the link objects of every group.
func (g *Gen) LinkDrv(gb *GoBuild) (*drv.Derivation, error) {
	e := newEnv()
	goroot, err := g.goToolchain(e, true)
	if err != nil {
		return nil, err
	}
	std := e.useDrv(gb.Std)
	g.libDeps(gb.Lib, e) // C/C++/Rust libraries the cgo packages link against
	var s strings.Builder
	fmt.Fprintf(&s, "T=%s/pkg/tool/linux_arm64\nmkdir -p $out/bin\ncat %s/importcfg > $TMPDIR/importcfg.link\ncat >> $TMPDIR/importcfg.link <<EOF\n", goroot, std)
	for _, p := range gb.Plan.Order {
		fmt.Fprintf(&s, "packagefile %s=%s/%s\n", p.ImportPath, e.useDrv(gb.byPkg[p.ImportPath].Drv), archive(p.ImportPath))
	}
	s.WriteString("EOF\ncat >> $TMPDIR/importcfg.link <<'EOF'\n")
	fmt.Fprintf(&s, "modinfo %q\n", modinfo(gb))
	fmt.Fprintf(&s, "EOF\n$T/link -o $out/bin/%s -importcfg $TMPDIR/importcfg.link -buildmode=exe -buildid= -extld=gcc %s/%s\n",
		gb.Lib.Go.Binary, e.useDrv(gb.byPkg[gb.Plan.Main.ImportPath].Drv), archive(gb.Plan.Main.ImportPath))
	d, err := g.finish("go-link-"+gb.Lib.Go.Binary, e, s.String(), nil)
	if err == nil {
		g.Labels[d.DrvPath] = gb.Lib.Label
	}
	return d, err
}

func topoGroups(gs []*Group) ([]*Group, error) {
	st := map[*Group]int{}
	var out []*Group
	var visit func(*Group, []string) error
	visit = func(gr *Group, path []string) error {
		switch st[gr] {
		case 1:
			return fmt.Errorf("Go import cycle between groups: %s -> %s (group by package instead)", strings.Join(path, " -> "), gr.Key)
		case 2:
			return nil
		}
		st[gr] = 1
		for _, d := range gr.Deps {
			if err := visit(d, append(path, gr.Key)); err != nil {
				return err
			}
		}
		st[gr] = 2
		out = append(out, gr)
		return nil
	}
	for _, gr := range gs {
		if err := visit(gr, nil); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func archive(ip string) string { return ip + ".a" }

func shq(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

func (g *Gen) groupDrv(gb *GoBuild, gr *Group) (*drv.Derivation, error) {
	e := newEnv()
	isMain := false
	for _, p := range gr.Pkgs {
		if p == gb.Plan.Main {
			isMain = true
		}
	}
	cgo := false
	for _, p := range gr.Pkgs {
		if len(p.CgoFiles) > 0 {
			cgo = true
		}
	}
	goroot, err := g.goToolchain(e, cgo || (isMain && gb.CGo))
	if err != nil {
		return nil, err
	}
	tool := goroot + "/pkg/tool/linux_" + g.Host.GOARCH // compiler and linker are host tools
	pr := g.goProj()
	std := e.useDrv(gb.Std)
	packTool := ""
	if cgo {
		packTool = e.useDrv(gb.Tools) + "/bin/pack"
	}
	// where each package's archive lives
	loc := func(ip string) string {
		d := gb.byPkg[ip]
		if gb.Cutoff {
			if d == gr {
				return "$out/" + ip + ".x"
			}
			x := gb.Exports[ip]
			e.srcs[x] = true
			return x
		}
		if d == gr {
			return "$out/" + archive(ip)
		}
		return e.useDrv(d.Drv) + "/" + archive(ip)
	}
	var s strings.Builder
	fmt.Fprintf(&s, "export HOME=$TMPDIR GOOS=linux GOARCH=%s CC=%sgcc\n", g.Tgt.GOARCH, g.CCPrefix())
	fmt.Fprintf(&s, "T=%s\nc=${NIX_BUILD_CORES:-1}; [ \"$c\" -gt 4 ] && c=4; [ \"$c\" -lt 1 ] && c=1\nmkdir -p $out\n", tool)
	var incs, libs []string
	if cgo || isMain {
		incs, libs = g.libDeps(gb.Lib, e)
		if sl := g.ccProj().sanLink(); sl != "" { // C deps built with sanitisers need their runtimes
			libs = append(libs, sl)
		}
	}
	// packages of one group compile in parallel by dependency level (like cmd/go does)
	level := map[string]int{}
	maxL := 0
	for _, p := range gr.Pkgs {
		l := 0
		for _, i := range p.Imports {
			if gb.byPkg[i] == gr && level[i]+1 > l {
				l = level[i] + 1
			}
		}
		level[p.ImportPath] = l
		maxL = max(maxL, l)
	}
	par := len(gr.Pkgs) > 1
	for l := 0; l <= maxL; l++ {
		if par {
			s.WriteString("pids=\n")
		}
		for n, p := range gr.Pkgs {
			if level[p.ImportPath] != l {
				continue
			}
			src, err := g.goSrc(gb, e, p)
			if err != nil {
				return nil, err
			}
			if par {
				s.WriteString("(\n")
			}
			writePkg(&s, n, p, src, std, packTool, loc, incs, libs, gb.Cutoff, g.CCPrefix(), pr)
			if par {
				s.WriteString(") &\npids=\"$pids $!\"\n")
			}
		}
		if par {
			s.WriteString("for p in $pids; do wait $p; done\n")
		}
	}
	if isMain && !gb.Cutoff {
		// link: needs every transitive package archive
		trans := map[string]bool{}
		var walk func(ip string)
		walk = func(ip string) {
			if trans[ip] {
				return
			}
			trans[ip] = true
			for _, i := range gb.Plan.Pkgs[ip].Imports {
				walk(i)
			}
		}
		walk(gb.Plan.Main.ImportPath)
		ips := make([]string, 0, len(trans))
		for ip := range trans {
			ips = append(ips, ip)
		}
		sort.Strings(ips)
		s.WriteString("cat " + std + "/importcfg > $TMPDIR/importcfg.link\ncat >> $TMPDIR/importcfg.link <<EOF\n")
		for _, ip := range ips {
			fmt.Fprintf(&s, "packagefile %s=%s\n", ip, loc(ip))
		}
		s.WriteString("EOF\ncat >> $TMPDIR/importcfg.link <<'EOF'\n")
		fmt.Fprintf(&s, "modinfo %q\n", modinfo(gb))
		s.WriteString("EOF\n")
		fmt.Fprintf(&s, "mkdir -p $out/bin\n$T/link -o $out/bin/%s -importcfg $TMPDIR/importcfg.link -buildmode=exe -buildid= -extld=%sgcc %s $out/%s\n",
			gb.Lib.Go.Binary, g.CCPrefix(), g.goLinkFlags(pr), archive(gb.Plan.Main.ImportPath))
	}
	name := "go-" + sanitize(strings.SplitN(gr.Key, ":", 2)[1])
	sp := pr
	if isMain {
		sp.Sanitize = g.V.Sanitize // the link pulls in every sanitiser runtime the C objects need
	}
	name += g.suffix(sp)
	if len(name) > 200 {
		name = name[:200]
	}
	return g.finish(name, e, s.String(), nil)
}

// modinfo mirrors the build info cmd/go embeds (so `go version -m` works on bob binaries).
func modinfo(gb *GoBuild) string {
	var b strings.Builder
	fmt.Fprintf(&b, "path\t%s\nmod\t%s\t(devel)\t\n", gb.Plan.Main.ImportPath, gb.Lib.Go.ModulePath)
	for _, r := range gb.Lib.Go.Requires {
		fmt.Fprintf(&b, "dep\t%s\t%s\t\n", r.Path, r.Version)
	}
	fmt.Fprintf(&b, "build\t-compiler=gc\nbuild\t-trimpath=true\nbuild\tCGO_ENABLED=%d\nbuild\tGOARCH=%s\nbuild\tGOOS=linux\n", b2i(gb.CGo), gb.GOARCH)
	// cmd/go frames the string with 16 magic bytes on each side
	return "0w\xaf\f\x92t\b\x02A\xe1\xc1\a\xe6\xd6\x18\xe6" + b.String() + "\xf92C1\x86\x18 r\x00\x82B\x10A\x16\xd8\xf2"
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

// goSrc returns the in-sandbox directory with p's files.
func (g *Gen) goSrc(gb *GoBuild, e *env, p *gopkg.Pkg) (string, error) {
	if p.Generated {
		return e.useDrv(gb.GenDrv[p.ImportPath]), nil
	}
	if p.Mod.Main {
		sp, ok := gb.SrcStore[p.ImportPath]
		if !ok {
			return "", fmt.Errorf("source of %s not imported", p.ImportPath)
		}
		e.srcs[sp] = true
		return sp, nil
	}
	d := gb.ModDrv[p.Mod.Path]
	return path.Join(e.useDrv(d), p.Mod.Path+"@"+p.Mod.Version, p.Rel), nil
}

func seed(ip string) string {
	h := sha256.Sum256([]byte(ip))
	return hex.EncodeToString(h[:10])
}

func writePkg(s *strings.Builder, n int, p *gopkg.Pkg, src, std, packTool string, loc func(string) string, incs, libs []string, cutoff bool, ccPre string, pr Proj) {
	d := "$TMPDIR/b" + seed(p.ImportPath) // same path in every grouping: cgo objdir paths reach DWARF
	_ = n
	out := "$out/" + archive(p.ImportPath)
	fmt.Fprintf(s, "# --- %s\nd=%s; mkdir -p $d \"$(dirname %s)\"\ncat %s/importcfg > $d/importcfg\n", p.ImportPath, d, out, std)
	if len(p.Imports) > 0 {
		s.WriteString("cat >> $d/importcfg <<EOF\n")
		for _, i := range p.Imports {
			fmt.Fprintf(s, "packagefile %s=%s\n", i, loc(i))
		}
		s.WriteString("EOF\n")
	}
	trim := p.Mod.Trim()
	if p.Rel != "" {
		trim += "/" + p.Rel
	}
	pflag := p.ImportPath
	if p.Name == "main" {
		pflag = "main"
	}
	oflags := []string{"-o", out}
	if cutoff { // export data alone in .x (what dependents compile against), objects in .a
		oflags = []string{"-o", "$out/" + p.ImportPath + ".x", "-linkobj", out}
	}
	args := []string{"$T/compile", oflags[0], oflags[1]}
	if cutoff {
		args = append(args, oflags[2], oflags[3])
	}
	args = append(args, "-p", pflag, "-lang=go"+p.Mod.Lang, "-nolocalimports", "-importcfg", "$d/importcfg", "-pack", "-c=$c")
	args = append(args, goCompileFlags(pr)...)
	var files []string
	for _, f := range p.GoFiles {
		files = append(files, src+"/"+f)
	}
	if len(p.Embed) > 0 {
		ec := struct {
			Patterns map[string][]string
			Files    map[string]string
		}{p.Embed, map[string]string{}}
		for _, fs := range p.Embed {
			for _, f := range fs {
				ec.Files[f] = src + "/" + f
			}
		}
		j, _ := json.Marshal(ec)
		fmt.Fprintf(s, "cat > $d/embedcfg <<'EOF'\n%s\nEOF\n", j)
		args = append(args, "-embedcfg", "$d/embedcfg")
	}
	if len(p.SFiles) > 0 {
		s.WriteString("echo 'bob-builder: assembly files are not supported by the spike' >&2; exit 1\n")
	}
	if len(p.CgoFiles) == 0 {
		if len(p.CFiles) == 0 {
			args = append(args, "-complete")
		}
		args = append(args, "-trimpath", shq(src+"=>"+trim))
		fmt.Fprintf(s, "%s %s\n", strings.Join(args, " "), strings.Join(files, " "))
		return
	}
	// cgo: same sequence as cmd/go (cgo -> gcc -> dynimport -> compile -> pack)
	o := d + "/obj"
	cflags := append(append(strings.Fields(goCgoCflags(pr)), incs...), p.CgoCFLAGS...)
	ldflags := append(append([]string{}, libs...), p.CgoLDFLAGS...)
	var ldq []string
	for _, l := range ldflags {
		ldq = append(ldq, fmt.Sprintf("%q", l))
	}
	gcc := fmt.Sprintf(ccPre+"gcc -I %s -fPIC -pthread -fmessage-length=0 -ffile-prefix-map=%s=/tmp/go-build -gno-record-gcc-switches -I %s/ %s -ffile-prefix-map=%s=/_/%s -frandom-seed=%s",
		src, o, o, strings.Join(cflags, " "), src, trim, seed(p.ImportPath))
	fmt.Fprintf(s, "mkdir -p %s\n(cd %s && CGO_LDFLAGS= $T/cgo -objdir %s/ -importpath %s %s -- -I %s/ %s %s)\n",
		o, src, o, p.ImportPath, shq("-ldflags="+strings.Join(ldq, " ")), o, strings.Join(cflags, " "), strings.Join(p.CgoFiles, " "))
	fmt.Fprintf(s, "cd %s\nobjs=\nfor f in _cgo_export.c *.cgo2.c; do %s -o ${f%%.c}.o -c $f; objs=\"$objs ${f%%.c}.o\"; done\n", o, gcc)
	for i, f := range p.CFiles {
		fmt.Fprintf(s, "%s -o cfile%d.o -c %s/%s; objs=\"$objs cfile%d.o\"\n", gcc, i, src, f, i)
	}
	fmt.Fprintf(s, "%s -o _cgo_main.o -c _cgo_main.c\n%s -o _cgo_.o _cgo_main.o $objs %s\n", gcc, gcc, strings.Join(ldflags, " "))
	fmt.Fprintf(s, "$T/cgo -dynpackage %s -dynimport _cgo_.o -dynout _cgo_import.go\ncd - >/dev/null\n", p.Name)
	args = append(args, "-trimpath", shq(src+"=>"+trim+";"+o+"=>"))
	fmt.Fprintf(s, "%s %s %s/_cgo_gotypes.go %s/*.cgo1.go %s/_cgo_import.go\n", strings.Join(args, " "), strings.Join(files, " "), o, o, o)
	fmt.Fprintf(s, "(cd %s && %s r %s $objs)\n", o, packTool, out)
}
