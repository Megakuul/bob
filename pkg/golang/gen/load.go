package gen

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"go/build"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/megakuul/bob/pkg/bob/diag"
	"github.com/megakuul/bob/pkg/engine/drv"
	"github.com/megakuul/bob/pkg/engine/fetch"
	"github.com/megakuul/bob/pkg/engine/ir"
	"github.com/megakuul/bob/pkg/engine/lock"
	"github.com/megakuul/bob/pkg/engine/srcs"
	"github.com/megakuul/bob/pkg/golang/internal/gospec"
	"github.com/megakuul/bob/pkg/golang/resolve"
)

// Bounds on what planning reads (hostile repositories, §5.4).
const (
	maxPackages  = 100_000 // packages of one closure
	maxModScan   = 20_000  // directories walked to find workspace go.mod files
	maxGoModRead = 1 << 20 // bytes of one go.mod read for its module line
)

// wsModule is one workspace module: a go.module node of the graph or, when the graph has
// none for it, a go.mod of the working tree found by an import (dir and path only).
type wsModule struct {
	node *ir.Node // nil for a module found on disk only
	spec gospec.Module
	dir  string         // repository-relative module root ("" = repository root)
	gens map[string]int // generated package import path → index into spec.Generated
}

// thirdParty is one module of bob.lock that provides packages.
type thirdParty struct {
	lock.GoModule
}

// labelDir returns the directory of a label ("//api/x:y" → "api/x", foreign labels keep their
// module-relative directory).
func labelDir(label string) string {
	_, rest, ok := strings.Cut(label, "//")
	if !ok {
		return ""
	}
	dir, _, _ := strings.Cut(rest, ":")
	return dir
}

// workspace collects the modules of the graph.
func (pl *planner) workspace() error {
	pl.mods = map[string]*wsModule{}
	for _, n := range pl.req.Graph.Nodes {
		if n.Kind != gospec.KindModule {
			continue
		}
		var s gospec.Module
		if err := json.Unmarshal(n.Spec, &s); err != nil {
			return internal(n, "module spec: %v", err)
		}
		if old, ok := pl.mods[s.Path]; ok && old.dir != labelDir(n.Label) {
			e := diag.New(1020, "label", n.Label, "option", "Path", "reason",
				fmt.Sprintf("Go module %s is also declared by %s", s.Path, old.node.Label))
			e.DefinedAt = diag.Pos{File: n.DefinedAt.File, Line: n.DefinedAt.Line}
			return e
		}
		m := &wsModule{node: n, spec: s, dir: labelDir(n.Label), gens: map[string]int{}}
		for i, g := range s.Generated {
			m.gens[s.Path+"/"+g.Dir] = i
		}
		pl.mods[s.Path] = m
	}
	return nil
}

// diskModule finds a workspace module that the graph does not declare (a partial build: the
// imported module's bob.go was not evaluated) by its go.mod in the working tree.
func (pl *planner) diskModule(importPath string) *wsModule {
	if pl.req.Root == "" {
		return nil
	}
	if pl.disk == nil {
		pl.disk = map[string]string{}
		walked := 0
		_ = filepath.WalkDir(pl.req.Root, func(p string, d fs.DirEntry, err error) error {
			if err != nil || !d.IsDir() {
				return nil
			}
			walked++
			if walked > maxModScan {
				return fs.SkipAll
			}
			name := d.Name()
			if p != pl.req.Root && (strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_") || name == "testdata" || name == "vendor" || name == "node_modules") {
				return fs.SkipDir
			}
			if mp := modulePathOf(filepath.Join(p, "go.mod")); mp != "" {
				rel, _ := filepath.Rel(pl.req.Root, p)
				if rel == "." {
					rel = ""
				}
				pl.disk[mp] = filepath.ToSlash(rel)
			}
			return nil
		})
	}
	best := ""
	for mp := range pl.disk {
		if (importPath == mp || strings.HasPrefix(importPath, mp+"/")) && len(mp) > len(best) {
			best = mp
		}
	}
	if best == "" || best == pl.rootModule() {
		return nil
	}
	return &wsModule{spec: gospec.Module{Path: best}, dir: pl.disk[best], gens: map[string]int{}}
}

// rootModule is the module of the repository's root go.mod (the bob.go module); its
// packages are not built by the Go ecosystem.
func (pl *planner) rootModule() string { return pl.req.Graph.Module }

var moduleLine = regexp.MustCompile(`(?m)^module\s+"?([^\s"]+)"?\s*$`)

func modulePathOf(p string) string {
	f, err := os.Open(p)
	if err != nil {
		return ""
	}
	defer f.Close()
	b := make([]byte, maxGoModRead)
	n, _ := f.Read(b)
	if m := moduleLine.FindSubmatch(b[:n]); m != nil {
		return string(m[1])
	}
	return ""
}

// lockModules reads the third-party modules of bob.lock that provide packages, and the
// build list of each workspace module: a closure uses the versions its main module selects.
func (pl *planner) lockModules() error {
	pl.third = map[string]*thirdParty{}
	pl.selected = map[string]map[string]string{}
	if len(pl.req.Lock) == 0 {
		return nil
	}
	v, err := lock.DecodeSection(lock.SectionGo, pl.req.Lock)
	if err != nil {
		return diag.New(1019, "reason", "go section: "+err.Error(), "line", "0")
	}
	sec := v.(*lock.GoSection)
	pl.lock = sec
	for _, m := range sec.Modules {
		if m.Packages {
			pl.third[m.Path+"@"+m.Version] = &thirdParty{GoModule: m}
		}
	}
	for ws, bl := range sec.Workspaces {
		pl.selected[ws] = selected(bl)
	}
	return nil
}

// pkgRef is where an import path's package comes from.
type pkgRef struct {
	kind   refKind
	ws     *wsModule
	third  *thirdParty
	modDir string // module root on the host (where bob reads it)
	rel    string // directory inside the module ("" = module root)
	gen    int    // index into ws.spec.Generated
}

type refKind int

const (
	refStd refKind = iota
	refWorkspace
	refThird
	refGen
)

// resolve maps an import path to its module (S2-1: bob resolves imports itself); third-party
// modules come from the build list of main, the closure's main module.
func (pl *planner) resolve(main, importPath string) (pkgRef, error) {
	var best *wsModule
	for mp, m := range pl.mods {
		if importPath == mp || strings.HasPrefix(importPath, mp+"/") {
			if gi, ok := m.gens[importPath]; ok {
				return pkgRef{kind: refGen, ws: m, gen: gi}, nil
			}
			if best == nil || len(mp) > len(best.spec.Path) {
				best = m
			}
		}
	}
	var bestThird *thirdParty
	for mp, v := range pl.selected[main] {
		if t := pl.third[mp+"@"+v]; t != nil && (importPath == mp || strings.HasPrefix(importPath, mp+"/")) && (bestThird == nil || len(mp) > len(bestThird.Path)) {
			bestThird = t
		}
	}
	if bestThird != nil && (best == nil || len(bestThird.Path) > len(best.spec.Path)) {
		root, err := pl.moduleTree(bestThird)
		if err != nil {
			return pkgRef{}, err
		}
		return pkgRef{kind: refThird, third: bestThird, modDir: root, rel: strings.TrimPrefix(strings.TrimPrefix(importPath, bestThird.Path), "/")}, nil
	}
	if best == nil {
		best = pl.diskModule(importPath)
		if best != nil {
			pl.mods[best.spec.Path] = best
		}
	}
	if best != nil {
		if pl.req.Root == "" {
			return pkgRef{}, fmt.Errorf("planning Go packages needs the repository root (engine.PlanRequest.Root)")
		}
		return pkgRef{kind: refWorkspace, ws: best, modDir: filepath.Join(pl.req.Root, filepath.FromSlash(best.dir)),
			rel: strings.TrimPrefix(strings.TrimPrefix(importPath, best.spec.Path), "/")}, nil
	}
	first, _, _ := strings.Cut(importPath, "/")
	if !strings.Contains(first, ".") {
		return pkgRef{kind: refStd}, nil
	}
	return pkgRef{}, errNoModule
}

var errNoModule = fmt.Errorf("no module provides it")

// moduleTree realises a third-party module's unpacked tree (bob's fetcher, the lock's tree
// hash, S2-2) and returns where bob reads it. When the fetcher cannot get it (a file:// proxy,
// or offline), the zip in the local proxy directory is unpacked and imported instead; the
// store path proves it is the locked tree.
func (pl *planner) moduleTree(t *thirdParty) (string, error) {
	key := t.Path + "@" + t.Version
	if p, ok := pl.trees[key]; ok {
		return p, nil
	}
	if pl.req.Store == nil {
		return "", fmt.Errorf("planning Go module %s needs the store (engine.PlanRequest.Store)", t.Path)
	}
	proxy := pl.proxy()
	req, sp, err := moduleRequest(proxy, t.GoModule)
	if err != nil {
		return "", err
	}
	got, err := pl.req.Store.Ensure(pl.ctx, req)
	if err != nil {
		if got, err = pl.importLocalZip(proxy, t.GoModule, req.Name, err); err != nil {
			return "", err
		}
	}
	if got != sp {
		return "", fmt.Errorf("module %s@%s: fetched path %s, want %s", t.Path, t.Version, got, sp)
	}
	host := pl.req.Store.HostPath(sp)
	pl.trees[key] = host
	pl.storeOf[host] = sp
	return host, nil
}

// importLocalZip imports a module tree from a local zip: the file:// proxy's, else bob's
// proxy directory's. ensureErr is returned when there is none.
func (pl *planner) importLocalZip(proxy string, m lock.GoModule, name string, ensureErr error) (string, error) {
	mv := resolve.Module{Path: m.Path, Version: m.Version}
	var file string
	for _, dir := range []string{strings.TrimPrefix(proxy, "file://"), ProxyDir()} {
		if !filepath.IsAbs(dir) {
			continue
		}
		f := filepath.Join(dir, filepath.FromSlash(resolve.EscapePath(m.Path)), "@v", resolve.EscapePath(m.Version)+".zip")
		if _, err := os.Stat(f); err == nil {
			file = f
			break
		}
	}
	if file == "" {
		return "", ensureErr
	}
	z, err := resolve.OpenModuleZip(file, mv)
	if err != nil {
		return "", err
	}
	defer z.Close()
	tmp, err := os.MkdirTemp("", "bob-gotree-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(tmp)
	root := filepath.Join(tmp, "tree")
	if err := z.Extract(root); err != nil {
		return "", fmt.Errorf("%s: %w", mv, err)
	}
	ents, err := os.ReadDir(root)
	if err != nil {
		return "", err
	}
	files := []string{}
	for _, e := range ents {
		files = append(files, e.Name())
	}
	paths, err := pl.req.Store.ImportSources(pl.ctx, []srcs.Source{{Name: name, Tree: srcs.Tree{Root: root, Files: files}}})
	if err != nil {
		return "", err
	}
	return paths[0], nil
}

// proxy is where module zips are fetched from (BOB_GOPROXY, else bob.lock's proxy).
func (pl *planner) proxy() string {
	locked := ""
	if pl.lock != nil {
		locked = pl.lock.Proxy
	}
	return readProxy(locked)
}

// moduleRequest is the fetch of a module's unpacked tree: the proxy's zip, unpacked without
// the "<path>@<version>/" prefix, pinned by the lock's tree hash (R-FW-063).
func moduleRequest(proxy string, m lock.GoModule) (fetch.Request, string, error) {
	name := drv.Name("gomod", m.Path, m.Version)
	req := fetch.Request{URLs: []string{strings.TrimSuffix(proxy, "/") + "/" + resolve.EscapePath(m.Path) + "/@v/" + resolve.EscapePath(m.Version) + ".zip"},
		Kind: fetch.Archive, NarHash: m.Tree, Name: name, Strip: strings.Count(m.Path, "/") + 1,
		Label: m.Path + "@" + m.Version}
	h, err := sriHex(m.Tree)
	if err != nil {
		return req, "", fmt.Errorf("module %s@%s: tree %w", m.Path, m.Version, err)
	}
	sp, err := drv.FixedOutputPath("sha256", h, true, name)
	return req, sp, err
}

// buildCtx is one configuration packages are loaded and compiled in.
type buildCtx struct {
	main         string // module path of the closure's main module (its build list applies)
	goos, goarch string
	tags         []string // module tags of the main module + binary/test tags, sorted
	cgo          bool
	tc           *toolchain // the closure's toolchain (the main module's)
}

// goContext is the go/build context of c: the target, the build tags, the toolchain's
// release tags (go1.1 … go1.<minor>), cgo; bob.go files are excluded by not setting "bob".
func (c buildCtx) goContext() build.Context {
	bc := build.Default
	bc.GOOS, bc.GOARCH = c.goos, c.goarch
	bc.CgoEnabled = c.cgo
	bc.BuildTags = slices.Clone(c.tags)
	bc.ToolTags = nil
	bc.ReleaseTags = nil
	for i := 1; i <= c.tc.minor; i++ {
		bc.ReleaseTags = append(bc.ReleaseTags, fmt.Sprintf("go1.%d", i))
	}
	bc.GOROOT, bc.GOPATH = "", ""
	bc.Dir = ""
	bc.Compiler = "gc"
	return bc
}

// pkg is one loaded non-std package of a closure.
type pkg struct {
	ip      string
	ref     pkgRef
	dir     string // where bob read it (host path)
	bp      *build.Package
	imports []string // direct imports (non-std, non-"C"), sorted
	std     []string // direct std imports
	embeds  *embedSet
	mod     *wsModule // workspace module (nil for third-party)
}

// cgo reports whether p uses cgo.
func (p *pkg) cgo() bool { return len(p.bp.CgoFiles) > 0 }

// closure is the loaded import graph of a set of root packages in one configuration.
type closure struct {
	ctx  buildCtx
	pkgs map[string]*pkg
}

// load reads roots and everything they import, transitively, in configuration c.
func (pl *planner) load(c buildCtx, roots []string, tests map[string]bool) (*closure, error) {
	cl := &closure{ctx: c, pkgs: map[string]*pkg{}}
	bc := c.goContext()
	var visit func(ip, from string) error
	visit = func(ip, from string) error {
		if _, ok := cl.pkgs[ip]; ok {
			return nil
		}
		if len(cl.pkgs) >= maxPackages {
			return fmt.Errorf("more than %d packages in one closure", maxPackages)
		}
		ref, err := pl.resolve(c.main, ip)
		if err != nil {
			if err == errNoModule {
				return pl.importError(ip, from)
			}
			return err
		}
		if ref.kind == refStd {
			return nil
		}
		dir, err := pl.packageDir(ref)
		if err != nil {
			return err
		}
		bp, err := pl.importDir(c, &bc, dir)
		if err != nil {
			return pl.loadError(ip, from, err)
		}
		if len(bp.CgoPkgConfig) > 0 {
			return pl.defError(from, fmt.Sprintf("%s uses #cgo pkg-config, which bob does not support; give the package its C dependencies with Module.Cgo(dir, golang.CgoOpts{Deps: …nixpkgs.Lib(…)})", ip))
		}
		if len(bp.CXXFiles)+len(bp.MFiles)+len(bp.FFiles)+len(bp.SwigFiles)+len(bp.SwigCXXFiles) > 0 {
			return pl.defError(from, fmt.Sprintf("%s has C++, Objective-C, Fortran or SWIG files, which bob does not compile in Go packages; put that code in a cc.Library and give it to Module.Cgo", ip))
		}
		p := &pkg{ip: ip, ref: ref, dir: dir, bp: bp}
		if ref.kind == refWorkspace {
			p.mod = ref.ws
		}
		imports := bp.Imports
		if tests[ip] {
			imports = append(append(slices.Clone(imports), bp.TestImports...), bp.XTestImports...)
		}
		cl.pkgs[ip] = p
		for _, imp := range sortedUnique(imports) {
			switch {
			case imp == "C":
				continue
			case imp == ip:
				continue // the external test package imports the package under test
			}
			r, err := pl.resolve(c.main, imp)
			if err != nil && err != errNoModule {
				return err
			}
			if err == nil && r.kind == refStd {
				p.std = append(p.std, imp)
				continue
			}
			if !tests[ip] || slices.Contains(bp.Imports, imp) {
				p.imports = append(p.imports, imp)
			}
			if err := visit(imp, ip); err != nil {
				return err
			}
		}
		return nil
	}
	for _, r := range roots {
		if err := visit(r, ""); err != nil {
			return nil, err
		}
	}
	return cl, nil
}

// importDir reads a package directory in configuration c, once per planner.
func (pl *planner) importDir(c buildCtx, bc *build.Context, dir string) (*build.Package, error) {
	key := strings.Join([]string{c.goos, c.goarch, strings.Join(c.tags, ","), fmt.Sprint(c.cgo, c.tc.minor), dir}, "|")
	if bp, ok := pl.bp[key]; ok {
		return bp, nil
	}
	bp, err := bc.ImportDir(dir, build.ImportComment)
	if err != nil {
		return nil, err
	}
	pl.bp[key] = bp
	return bp, nil
}

// packageDir is where bob reads the package of ref.
func (pl *planner) packageDir(ref pkgRef) (string, error) {
	switch ref.kind {
	case refGen:
		g := ref.ws.spec.Generated[ref.gen]
		dep := ref.ws.node.Deps[g.Dep]
		rv, ok := pl.req.Inputs[dep]
		out := rv.Outputs["out"]
		if !ok || out == "" {
			return "", fmt.Errorf("generated package %s has no planned output", ref.ws.spec.Path+"/"+g.Dir)
		}
		if pl.req.Store == nil {
			return "", fmt.Errorf("planning generated Go code needs the store (engine.PlanRequest.Store)")
		}
		host := pl.req.Store.HostPath(out)
		if _, err := os.Stat(host); err != nil {
			return "", fmt.Errorf("generated package %s must be built before the Go code that imports it (codegen early cutoff, §03.6.3)", ref.ws.spec.Path+"/"+g.Dir)
		}
		pl.storeOf[host] = out
		return host, nil
	default:
		return filepath.Join(ref.modDir, filepath.FromSlash(ref.rel)), nil
	}
}

// importError is the error of an import no module provides.
func (pl *planner) importError(ip, from string) error {
	if from == "" {
		return pl.defError("", fmt.Sprintf("package %s is not in any module of this repository", ip))
	}
	m := pl.moduleOf(from)
	where := "the module's bob.go"
	if m != nil && m.node != nil {
		where = m.node.DefinedAt.File
	}
	return pl.defError(from, fmt.Sprintf("%s imports %s, which no module provides; declare it in %s (golang.Require(%q, \"<version>\")) and run bob lock", from, ip, where, modulePrefix(ip)))
}

// modulePrefix guesses the module path of an import path for hints (the first three
// elements for well-known hosts, else the first two).
func modulePrefix(ip string) string {
	parts := strings.Split(ip, "/")
	n := 3
	if len(parts) < n {
		n = len(parts)
	}
	return strings.Join(parts[:n], "/")
}

func (pl *planner) loadError(ip, from string, err error) error {
	msg := err.Error()
	if pl.req.Root != "" {
		msg = strings.ReplaceAll(msg, pl.req.Root+string(filepath.Separator), "")
	}
	if from != "" {
		return pl.defError(from, fmt.Sprintf("%s (imported by %s): %s", ip, from, msg))
	}
	return pl.defError(ip, fmt.Sprintf("%s: %s", ip, msg))
}

// moduleOf returns the workspace module of an import path.
func (pl *planner) moduleOf(ip string) *wsModule {
	var best *wsModule
	for mp, m := range pl.mods {
		if (ip == mp || strings.HasPrefix(ip, mp+"/")) && (best == nil || len(mp) > len(best.spec.Path)) {
			best = m
		}
	}
	return best
}

// order returns the closure's packages with dependencies first (ties by import path).
func (cl *closure) order() []*pkg {
	var out []*pkg
	seen := map[string]bool{}
	var visit func(ip string)
	visit = func(ip string) {
		if seen[ip] {
			return
		}
		seen[ip] = true
		p := cl.pkgs[ip]
		for _, d := range p.imports {
			if _, ok := cl.pkgs[d]; ok {
				visit(d)
			}
		}
		out = append(out, p)
	}
	ips := make([]string, 0, len(cl.pkgs))
	for ip := range cl.pkgs {
		ips = append(ips, ip)
	}
	sort.Strings(ips)
	for _, ip := range ips {
		visit(ip)
	}
	return out
}

// transitive returns the packages ip depends on, ip included, sorted.
func (cl *closure) transitive(roots ...string) []string {
	seen := map[string]bool{}
	var visit func(ip string)
	visit = func(ip string) {
		if seen[ip] {
			return
		}
		if _, ok := cl.pkgs[ip]; !ok {
			return
		}
		seen[ip] = true
		for _, d := range cl.pkgs[ip].imports {
			visit(d)
		}
	}
	for _, r := range roots {
		visit(r)
	}
	out := make([]string, 0, len(seen))
	for ip := range seen {
		out = append(out, ip)
	}
	sort.Strings(out)
	return out
}

func sortedUnique(s []string) []string {
	out := slices.Clone(s)
	sort.Strings(out)
	return slices.Compact(out)
}

// sriHex turns "sha256-<base64>" into lowercase hex.
func sriHex(sri string) (string, error) {
	b64, ok := strings.CutPrefix(sri, "sha256-")
	if !ok {
		return "", fmt.Errorf("%q is not a sha256 SRI hash", sri)
	}
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil || len(raw) != sha256.Size {
		return "", fmt.Errorf("%q is not a sha256 SRI hash", sri)
	}
	return hex.EncodeToString(raw), nil
}
