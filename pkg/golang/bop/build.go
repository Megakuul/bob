package bop

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/megakuul/bob/pkg/builder"
)

// buildKind is kinds go-compile, go-cgo, go-link and go-testmain: compile the packages in
// order (each one's imports are earlier packages or inputs), packages of one level in
// parallel, then link. The steps are cmd/go's (S2 §5.2): compile with an importcfg, cgo with
// cmd/go's sequence, assembly with symabis, pack for objects.
func buildKind(c *builder.Ctx, raw json.RawMessage) error {
	var a BuildArgs
	if err := decode(raw, &a); err != nil {
		return err
	}
	if err := a.check(); err != nil {
		return err
	}
	if a.Std == "" || (len(a.Packages) == 0 && a.Link == nil) {
		return builder.Invalidf("go build job: std importcfg and packages or a link are required")
	}
	cgo := a.CC != ""
	r, err := newRunner(c, a.Toolchain, cgo)
	if err != nil {
		return err
	}
	std, err := os.ReadFile(a.Std)
	if err != nil {
		return err
	}
	b := &build{r: r, a: a, std: string(std)}
	for _, level := range levels(a.Packages) {
		if err := b.compileLevel(level); err != nil {
			return err
		}
	}
	if a.Link != nil {
		c.Phase("link")
		return b.link(*a.Link)
	}
	return nil
}

type build struct {
	r   *runner
	a   BuildArgs
	std string
}

// levels groups packages by dependency level inside the job (imports of other packages of the
// job come first), keeping the given order inside a level.
func levels(pkgs []Package) [][]Package {
	byOut := map[string]int{}
	for i, p := range pkgs {
		byOut[p.Out] = i
		if p.Export != "" {
			byOut[p.Export] = i
		}
	}
	level := make([]int, len(pkgs))
	maxL := 0
	for i, p := range pkgs {
		for _, archive := range p.Imports {
			if j, ok := byOut[archive]; ok && j < i && level[j]+1 > level[i] {
				level[i] = level[j] + 1
			}
		}
		maxL = max(maxL, level[i])
	}
	out := make([][]Package, maxL+1)
	for i, p := range pkgs {
		out[level[i]] = append(out[level[i]], p)
	}
	return out
}

func (b *build) compileLevel(pkgs []Package) error {
	if len(pkgs) == 1 {
		return b.compile(pkgs[0])
	}
	errs := make([]error, len(pkgs))
	sem := make(chan struct{}, cores())
	var wg sync.WaitGroup
	for i, p := range pkgs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() {
				if v := recover(); v != nil {
					errs[i] = fmt.Errorf("%s: internal error: %v", p.ImportPath, v)
				}
			}()
			sem <- struct{}{}
			defer func() { <-sem }()
			errs[i] = b.compile(p)
		}()
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			return err
		}
	}
	return nil
}

func (b *build) compile(p Package) error {
	r := b.r
	pflag := flagValue(p.Flags, "-p")
	if p.ImportPath == "" || p.Out == "" || pflag == "" {
		return builder.Invalidf("go package without import path, -p or output")
	}
	obj := workDir(r.tmp, p.ImportPath)
	if err := os.MkdirAll(obj, 0o755); err != nil {
		return err
	}
	dir := p.Dir
	if dir == "" {
		dir = obj
	}
	names := make([]string, 0, len(p.Write))
	for n := range p.Write {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		if n != filepath.Base(n) || strings.HasPrefix(n, ".") {
			return builder.Invalidf("%s: generated file name %q", p.ImportPath, n)
		}
		if err := os.WriteFile(filepath.Join(obj, n), []byte(p.Write[n]), 0o644); err != nil {
			return err
		}
	}
	out, err := outPath(r.c, p.Out)
	if err != nil {
		return err
	}
	if p.Export != "" {
		if _, err := outPath(r.c, p.Export); err != nil {
			return err
		}
	}
	// importcfg: std, then the direct imports.
	var cfg strings.Builder
	cfg.WriteString(b.std)
	imps := make([]string, 0, len(p.Imports))
	for ip := range p.Imports {
		imps = append(imps, ip)
	}
	sort.Strings(imps)
	for _, ip := range imps {
		fmt.Fprintf(&cfg, "packagefile %s=%s\n", ip, p.Imports[ip])
	}
	if err := os.WriteFile(filepath.Join(obj, "importcfg"), []byte(cfg.String()), 0o644); err != nil {
		return err
	}
	trim := dir + "=>" + p.Trim + ";" + obj + "=>"
	args := []string{r.t.tool("compile")}
	if p.Export != "" {
		args = append(args, "-o", p.Export, "-linkobj", out)
	} else {
		args = append(args, "-o", out)
	}
	args = append(args, "-trimpath", trim)
	args = append(args, p.Flags...)
	args = append(args, r.t.GCFlags...)
	args = append(args, "-c="+strconv.Itoa(cores()), "-nolocalimports", "-importcfg", filepath.Join(obj, "importcfg"), "-pack")
	if len(p.CgoFiles)+len(p.CFiles)+len(p.SFiles)+len(p.SysoFiles) == 0 {
		args = append(args, "-complete")
	}
	if p.Embed != nil {
		cfgFile, err := writeEmbedCfg(obj, dir, *p.Embed)
		if err != nil {
			return err
		}
		args = append(args, "-embedcfg", cfgFile)
	}
	var objs []string
	if len(p.SFiles) > 0 {
		if err := os.WriteFile(filepath.Join(obj, "go_asm.h"), nil, 0o644); err != nil {
			return err
		}
		sym := filepath.Join(obj, "symabis")
		asm := append(b.asmArgs(pflag, p.Trim, dir, obj), "-gensymabis", "-o", sym)
		if _, err := r.run(dir, false, append(asm, rel(p.SFiles)...)...); err != nil {
			return fmt.Errorf("%s: %w", p.ImportPath, err)
		}
		args = append(args, "-symabis", sym, "-asmhdr", filepath.Join(obj, "go_asm.h"))
	}
	files := make([]string, 0, len(p.GoFiles))
	for _, f := range p.GoFiles {
		files = append(files, filepath.Join(dir, f))
	}
	if len(p.CgoFiles) > 0 {
		gen, cobjs, err := b.cgo(p, dir, obj)
		if err != nil {
			return fmt.Errorf("%s: %w", p.ImportPath, err)
		}
		files = append(files, gen...)
		objs = append(objs, cobjs...)
	}
	if _, err := r.run(dir, false, append(args, files...)...); err != nil {
		return fmt.Errorf("%s: %w", p.ImportPath, err)
	}
	for _, s := range p.SFiles {
		o := filepath.Join(obj, strings.TrimSuffix(s, filepath.Ext(s))+".o")
		asm := append(b.asmArgs(pflag, p.Trim, dir, obj), "-o", o, "./"+s)
		if _, err := r.run(dir, false, asm...); err != nil {
			return fmt.Errorf("%s: %w", p.ImportPath, err)
		}
		objs = append(objs, o)
	}
	for _, s := range p.SysoFiles {
		objs = append(objs, filepath.Join(dir, s))
	}
	if len(objs) > 0 {
		if b.a.Pack == "" {
			return builder.Invalidf("%s: objects to pack but no pack program", p.ImportPath)
		}
		if _, err := r.run(obj, false, append([]string{b.a.Pack, "r", out}, objs...)...); err != nil {
			return fmt.Errorf("%s: %w", p.ImportPath, err)
		}
	}
	return nil
}

// flagValue returns the value after flag in flags ("" if absent).
func flagValue(flags []string, flag string) string {
	for i, f := range flags {
		if f == flag && i+1 < len(flags) {
			return flags[i+1]
		}
	}
	return ""
}

func (b *build) asmArgs(pflag, trimTo, dir, obj string) []string {
	t := b.r.t
	args := []string{t.tool("asm"), "-p", pflag, "-trimpath", dir + "=>" + trimTo + ";" + obj + "=>",
		"-I", obj + "/", "-I", filepath.Join(t.GoRoot, "pkg", "include"),
		"-D", "GOOS_" + t.GOOS, "-D", "GOARCH_" + t.GOARCH}
	if t.GOARCH == "amd64" {
		args = append(args, "-D", "GOAMD64_v1")
	}
	return args
}

func rel(files []string) []string {
	out := make([]string, len(files))
	for i, f := range files {
		out[i] = "./" + f
	}
	return out
}

// writeEmbedCfg writes the -embedcfg file: patterns → files, files → absolute paths.
func writeEmbedCfg(obj, dir string, e Embed) (string, error) {
	cfg := struct {
		Patterns map[string][]string
		Files    map[string]string
	}{e.Patterns, map[string]string{}}
	for _, f := range e.Files {
		cfg.Files[f] = filepath.Join(dir, f)
	}
	data, err := json.Marshal(cfg)
	if err != nil {
		return "", err
	}
	p := filepath.Join(obj, "embedcfg")
	return p, os.WriteFile(p, data, 0o644)
}

// ccFlags are the C compiler flags of cgo objects: cmd/go's, plus path maps so neither the
// source store path nor the work directory reaches the debug info (and the binary's closure).
func ccFlags(dir, obj, trim string) []string {
	return []string{"-fPIC", "-pthread", "-fmessage-length=0", "-gno-record-gcc-switches",
		"-ffile-prefix-map=" + dir + "=" + trim, "-ffile-prefix-map=" + obj + "=/tmp/go-build"}
}

// cgo runs cmd/go's cgo sequence for p: cgo, the C objects, _cgo_.o for -dynimport. It returns
// the Go files to compile and the objects to pack.
func (b *build) cgo(p Package, dir, obj string) ([]string, []string, error) {
	r, t := b.r, b.r.t
	if t.CC == "" {
		return nil, nil, fmt.Errorf("the package imports \"C\" but cgo is off for this build")
	}
	if p.Name == "" {
		return nil, nil, builder.Invalidf("cgo package without a package name")
	}
	ld := make([]string, len(p.LDFlags))
	for i, f := range p.LDFlags {
		ld[i] = strconv.Quote(f)
	}
	cflags := append(append([]string{"-O2", "-g"}, t.CFlags...), p.CFlags...)
	args := []string{t.tool("cgo"), "-objdir", obj + "/", "-importpath", p.ImportPath,
		"-trimpath", dir + "=>" + p.Trim + ";" + obj + "=>"}
	if p.Header != "" {
		args = append(args, "-exportheader", filepath.Join(obj, "_cgo_install.h"))
	}
	args = append(args, "-ldflags="+strings.Join(ld, " "), "--", "-I", obj+"/")
	args = append(args, cflags...)
	if _, err := r.run(dir, false, append(args, rel(p.CgoFiles)...)...); err != nil {
		return nil, nil, err
	}
	gcc := append([]string{t.CC, "-I", dir}, ccFlags(dir, obj, p.Trim)...)
	var objs []string
	n := 0
	compileC := func(cwd, src string, extra ...string) error {
		n++
		o := filepath.Join(obj, fmt.Sprintf("_x%03d.o", n))
		argv := append(append(append([]string{}, gcc...), extra...), "-I", obj+"/")
		argv = append(append(argv, cflags...), "-o", o, "-c", src)
		if _, err := r.run(cwd, false, argv...); err != nil {
			return err
		}
		objs = append(objs, o)
		return nil
	}
	if err := compileC(obj, "_cgo_export.c"); err != nil {
		return nil, nil, err
	}
	var gofiles []string
	for _, f := range p.CgoFiles {
		base := strings.TrimSuffix(f, ".go")
		if err := compileC(obj, base+".cgo2.c"); err != nil {
			return nil, nil, err
		}
		gofiles = append(gofiles, filepath.Join(obj, base+".cgo1.go"))
	}
	for _, f := range p.CFiles {
		if err := compileC(dir, f); err != nil {
			return nil, nil, err
		}
	}
	mainObj := filepath.Join(obj, "_cgo_main.o")
	argv := append(append(append([]string{}, gcc...), "-I", obj+"/"), cflags...)
	if _, err := r.run(obj, false, append(argv, "-o", mainObj, "-c", "_cgo_main.c")...); err != nil {
		return nil, nil, err
	}
	dyn := filepath.Join(obj, "_cgo_.o")
	argv = append(append([]string{}, gcc...), "-o", dyn, mainObj)
	argv = append(append(append(argv, objs...), "-O2", "-g"), t.CFlags...)
	argv = append(argv, p.LDFlags...)
	if _, err := r.run(obj, false, argv...); err != nil {
		return nil, nil, err
	}
	imp := filepath.Join(obj, "_cgo_import.go")
	if _, err := r.run(obj, false, t.tool("cgo"), "-dynpackage", p.Name, "-dynimport", dyn, "-dynout", imp); err != nil {
		return nil, nil, err
	}
	if p.Header != "" {
		h, err := outPath(r.c, p.Header)
		if err != nil {
			return nil, nil, err
		}
		if err := copyFile(filepath.Join(obj, "_cgo_install.h"), h); err != nil {
			return nil, nil, err
		}
	}
	files := append([]string{filepath.Join(obj, "_cgo_gotypes.go")}, gofiles...)
	return append(files, imp), objs, nil
}

// link links the program or C library: importcfg of std plus every package of the closure,
// the module info cmd/go embeds, no build id.
func (b *build) link(l Link) error {
	r, t := b.r, b.r.t
	if l.Main == "" || l.Out == "" {
		return builder.Invalidf("go link without main archive or output")
	}
	out, err := outPath(r.c, l.Out)
	if err != nil {
		return err
	}
	obj := filepath.Join(r.tmp, "link")
	if err := os.MkdirAll(obj, 0o755); err != nil {
		return err
	}
	var cfg strings.Builder
	cfg.WriteString(b.std)
	ips := make([]string, 0, len(l.Packages))
	for ip := range l.Packages {
		ips = append(ips, ip)
	}
	sort.Strings(ips)
	for _, ip := range ips {
		fmt.Fprintf(&cfg, "packagefile %s=%s\n", ip, l.Packages[ip])
	}
	if l.ModInfo != "" {
		fmt.Fprintf(&cfg, "modinfo %s\n", strconv.Quote(l.ModInfo))
	}
	ic := filepath.Join(obj, "importcfg.link")
	if err := os.WriteFile(ic, []byte(cfg.String()), 0o644); err != nil {
		return err
	}
	mode := l.Mode
	if mode == "" {
		mode = "exe"
	}
	args := []string{t.tool("link"), "-o", out, "-importcfg", ic, "-buildmode=" + mode, "-buildid="}
	args = append(args, t.LDFlags...)
	if l.External || mode != "exe" || has(t.LDFlags, "-asan") {
		if t.CC == "" {
			return fmt.Errorf("linking needs a C compiler but cgo is off for this build")
		}
		if !slices.ContainsFunc(t.LDFlags, func(f string) bool { return strings.HasPrefix(f, "-extld=") }) {
			args = append(args, "-extld="+t.CC)
		}
		args = append(args, "-linkmode=external")
		if len(t.CFlags) > 0 {
			args = append(args, "-extldflags="+strings.Join(t.CFlags, " "))
		}
	}
	for _, x := range l.X {
		args = append(args, "-X", x)
	}
	_, err = r.run(obj, false, append(args, l.Main)...)
	return err
}
