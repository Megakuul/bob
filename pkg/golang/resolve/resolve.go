package resolve

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

// Workspace is one workspace module to resolve.
type Workspace struct {
	Path    string   // module path
	Go      string   // language version ("1.26")
	Dir     string   // host directory of the module root; "" = no sources to scan
	BobGo   string   // repo-relative bob.go that declares it (messages)
	Require []Module // declared direct requirements, generated-code runtimes included
	// GenRequire are the runtime modules of generated packages: their sources do not exist at
	// lock time, so every package of these modules counts as used.
	GenRequire []Module
	Generated  []string // import paths of generated packages
	Prev       []Module // previous build list (pins); nil = none
}

// Request is one resolution of all workspace modules of a repository.
type Request struct {
	Proxy      *Proxy
	Workspaces []Workspace
	Update     []string // module paths to move to their latest version
	Fresh      bool     // ignore the previous pins (resolve from the declarations alone)
	TmpDir     string   // where module zips are kept while resolving
}

// ModuleInfo is what bob.lock records of one module version.
type ModuleInfo struct {
	Module
	ModH1    string
	Go       string
	Packages bool       // provides packages to some workspace build (zip below)
	Zip      *ModuleZip // the checked zip (Packages only), open until Result.Close
}

// Result is the resolution: a build list per workspace module and every module version
// whose go.mod is part of some graph.
type Result struct {
	BuildLists map[string][]Module // workspace path → selected versions, sorted by path
	Modules    []*ModuleInfo       // sorted by path, then version
	zips       *zipCache
}

// Close removes the downloaded zips.
func (r *Result) Close() error {
	if r == nil || r.zips == nil {
		return nil
	}
	return r.zips.close()
}

// Error kinds the caller maps to bob's error codes.
type (
	// NotFoundError: a required module version does not exist on the proxy, or no module of the
	// build list provides an imported package (E-RESOLVE).
	NotFoundError struct {
		Workspace string
		Reason    string
	}
	// ToolchainError: a module of the build list needs a newer Go than the workspace module
	// declares.
	ToolchainError struct {
		Workspace, WorkspaceGo, BobGo string
		Module                        Module
		NeedsGo                       string
	}
)

func (e *NotFoundError) Error() string {
	return fmt.Sprintf("cannot resolve Go modules for %s: %s", e.Workspace, e.Reason)
}

func (e *ToolchainError) Error() string {
	return fmt.Sprintf("%s needs go %s, but %s uses go %s (set Go in %s)", e.Module, e.NeedsGo, e.Workspace, e.WorkspaceGo, e.BobGo)
}

// maxRounds bounds the root fixpoint (it converges in a few rounds; hostile graphs stop here).
const maxRounds = 64

// Resolve computes the build lists. The go.mod bob writes for a workspace module W lists as
// requirements exactly Roots(W) (see Roots), so `go list -m all` there lists the build list
// returned here.
func Resolve(ctx context.Context, req Request) (*Result, error) {
	r := &resolver{req: req, mods: newMods(req.Proxy), zips: newZipCache(req.Proxy, req.TmpDir)}
	res, err := r.run(ctx)
	if err != nil {
		r.zips.close()
		return nil, err
	}
	return res, nil
}

type resolver struct {
	req  Request
	mods *mods
	zips *zipCache
}

// state is the per-workspace state of one round.
type state struct {
	ws      *Workspace
	roots   map[string]string // path → version
	graph   *modGraph
	used    map[string]bool // module paths that provide packages to this workspace
	wsDeps  map[string]bool // other workspace modules whose packages it imports
	missing []string        // imports no module provides
}

func (r *resolver) run(ctx context.Context) (*Result, error) {
	wsByPath := map[string]*Workspace{}
	declared := map[string]bool{} // every path declared by some workspace module
	for i := range r.req.Workspaces {
		w := &r.req.Workspaces[i]
		wsByPath[w.Path] = w
		for _, m := range append(append([]Module(nil), w.Require...), w.GenRequire...) {
			declared[m.Path] = true
		}
	}
	for _, w := range r.req.Workspaces {
		for _, m := range append(append([]Module(nil), w.Require...), w.GenRequire...) {
			if wsByPath[m.Path] != nil {
				return nil, &NotFoundError{w.Path, fmt.Sprintf("%s is a module of this repository; it cannot be required with a version", m.Path)}
			}
		}
	}
	updates := map[string]string{}
	for _, p := range r.req.Update {
		if CheckPath(p) != nil || wsByPath[p] != nil {
			continue
		}
		v, err := r.req.Proxy.Latest(ctx, p)
		if errors.Is(err, ErrNotFound) {
			continue // not a Go module (bob lock --update names any locked entry)
		}
		if err != nil {
			return nil, fmt.Errorf("latest version of %s: %w", p, err)
		}
		updates[p] = v
	}

	states := make([]*state, len(r.req.Workspaces))
	for i := range r.req.Workspaces {
		states[i] = &state{ws: &r.req.Workspaces[i], wsDeps: map[string]bool{}}
	}
	used := map[string]bool{} // paths providing packages to some workspace
	for round := 0; ; round++ {
		if round == maxRounds {
			return nil, fmt.Errorf("module resolution does not settle after %d rounds", maxRounds)
		}
		changed := false
		for _, s := range states {
			roots := r.roots(s, wsByPath, declared, used, updates)
			if s.graph == nil || !sameRoots(roots, s.roots) {
				changed = true
				s.roots = roots
				g, errs := buildGraph(ctx, r.mods, mapModules(roots), GoCompare(s.ws.Go, "1.17") >= 0)
				s.graph = g
				if err := r.checkToolchain(ctx, s); err != nil {
					return nil, err
				}
				if len(errs) > 0 {
					return nil, r.fetchError(s.ws.Path, errs)
				}
			}
		}
		if round > 0 && !changed {
			break
		}
		// Packages: what each workspace imports, with the current selections.
		next := map[string]bool{}
		for _, s := range states {
			if err := r.loadPackages(ctx, s, wsByPath); err != nil {
				return nil, err
			}
			for p := range s.used {
				next[p] = true
			}
		}
		used = next
	}
	for _, s := range states {
		if len(s.missing) > 0 {
			sort.Strings(s.missing)
			return nil, &NotFoundError{s.ws.Path, s.missing[0]}
		}
	}
	return r.result(ctx, states)
}

// roots are the requirements of a workspace's go.mod: its declarations, those of the
// workspace modules it imports, and every module of its graph whose path is declared by
// some workspace module or provides packages to some workspace (Roots rule), each at the
// highest of its selected version, the declared version and the previous pin (pins of
// updated modules give way to their latest version).
func (r *resolver) roots(s *state, wsByPath map[string]*Workspace, declared, used map[string]bool, updates map[string]string) map[string]string {
	out := map[string]string{}
	raise := func(p, v string) {
		if Compare(v, out[p]) > 0 || out[p] == "" {
			out[p] = v
		}
	}
	add := func(w *Workspace) {
		for _, m := range append(append([]Module(nil), w.Require...), w.GenRequire...) {
			raise(m.Path, m.Version)
		}
	}
	add(s.ws)
	for p := range s.wsDeps {
		if w := wsByPath[p]; w != nil {
			add(w)
		}
	}
	if s.graph != nil {
		for p, v := range s.graph.selected {
			if declared[p] || used[p] {
				raise(p, v)
			}
		}
	}
	// bob lock --update moves roots only: the go.mod bob writes lists roots alone (Roots).
	for p, v := range updates {
		if _, ok := out[p]; ok {
			raise(p, v)
		}
	}
	if !r.req.Fresh {
		for _, m := range s.ws.Prev {
			if _, upd := updates[m.Path]; upd {
				continue
			}
			if _, ok := out[m.Path]; ok {
				raise(m.Path, m.Version)
			}
		}
	}
	return out
}

func sameRoots(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

func mapModules(m map[string]string) []Module {
	out := make([]Module, 0, len(m))
	for p, v := range m {
		out = append(out, Module{p, v})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

// checkToolchain fails when a selected module needs a newer Go language version than the
// workspace module declares (R-GO-004a).
func (r *resolver) checkToolchain(ctx context.Context, s *state) error {
	var bad []*ToolchainError
	for _, m := range s.graph.list() {
		mi := r.mods.get(ctx, m)
		if mi.err != nil || mi.file.Go == "" {
			continue
		}
		if GoCompare(GoLang(mi.file.Go), s.ws.Go) > 0 {
			bad = append(bad, &ToolchainError{Workspace: s.ws.Path, WorkspaceGo: s.ws.Go, BobGo: s.ws.BobGo, Module: m, NeedsGo: mi.file.Go})
		}
	}
	if len(bad) == 0 {
		return nil
	}
	return bad[0]
}

func (r *resolver) fetchError(ws string, errs []fetchErr) error {
	sort.Slice(errs, func(i, j int) bool { return errs[i].mod.String() < errs[j].mod.String() })
	for _, e := range errs {
		if errors.Is(e.err, context.Canceled) || errors.Is(e.err, context.DeadlineExceeded) {
			return e.err
		}
	}
	e := errs[0]
	if errors.Is(e.err, ErrNotFound) {
		return &NotFoundError{ws, fmt.Sprintf("%s: not found on %s", e.mod, r.req.Proxy.Host())}
	}
	return &NotFoundError{ws, e.err.Error()}
}

// pkgKey is one package to load: its import path, whether it comes from the generated-code
// rule (missing imports are then ignored), and the package that imported it.
type pkgKey struct {
	ip, from string
	lenient  bool
}

// loadPackages computes the modules that provide packages to workspace s (cmd/go's "all":
// the workspace's packages and their tests, and the non-test imports of everything they
// import, transitively).
func (r *resolver) loadPackages(ctx context.Context, s *state, wsByPath map[string]*Workspace) error {
	s.used, s.wsDeps, s.missing = map[string]bool{}, map[string]bool{}, nil
	seen := map[string]bool{}
	var queue []pkgKey
	push := func(ips []string, from string, lenient bool) {
		for _, ip := range ips {
			if !seen[ip] && !isStd(ip) {
				seen[ip] = true
				queue = append(queue, pkgKey{ip, from, lenient})
			}
		}
	}
	if s.ws.Dir != "" {
		dirs, err := workspaceDirs(s.ws.Dir)
		if err != nil {
			return err
		}
		for _, d := range dirs {
			pi, err := readImports(diskDir(filepath.Join(s.ws.Dir, filepath.FromSlash(d))))
			if err != nil {
				return err
			}
			ip := joinPath(s.ws.Path, d)
			seen[ip] = true
			push(pi.imports, ip, false)
			push(pi.tests, ip, false)
		}
	}
	genMods := map[string]bool{}
	for _, m := range s.ws.GenRequire {
		genMods[m.Path] = true
	}
	for len(queue) > 0 {
		if err := ctx.Err(); err != nil {
			return err
		}
		k := queue[0]
		queue = queue[1:]
		if w := longestWorkspace(wsByPath, k.ip); w != nil {
			if w.Path != s.ws.Path {
				s.wsDeps[w.Path] = true
			}
			if generated(w.Generated, k.ip) {
				for _, m := range w.GenRequire {
					genMods[m.Path] = true
				}
				continue
			}
			if w.Dir == "" {
				continue
			}
			pi, err := readImports(diskDir(filepath.Join(w.Dir, filepath.FromSlash(strings.TrimPrefix(strings.TrimPrefix(k.ip, w.Path), "/")))))
			if err != nil {
				return err
			}
			push(pi.imports, k.ip, k.lenient)
			continue
		}
		mod, dir, err := r.provider(ctx, s, k.ip)
		if err != nil {
			if k.lenient {
				continue
			}
			var nf *NotFoundError
			if errors.As(err, &nf) {
				s.missing = append(s.missing, nf.Reason+" (imported by "+k.from+")")
				continue
			}
			return err
		}
		s.used[mod.Path] = true
		z, err := r.zips.get(ctx, mod)
		if err != nil {
			return err
		}
		pi, err := readImports(z.GoFiles(dir))
		if err != nil {
			return fmt.Errorf("%s: %w", mod, err)
		}
		push(pi.imports, k.ip, k.lenient)
	}
	// Generated code: every package of its runtime modules counts as used.
	for _, p := range sortedSet(genMods) {
		v := s.graph.selected[p]
		if v == "" {
			continue
		}
		mod := Module{p, v}
		s.used[p] = true
		z, err := r.zips.get(ctx, mod)
		if err != nil {
			return err
		}
		for _, d := range z.Dirs() {
			if !z.HasGoFiles(d) {
				continue
			}
			pi, err := readImports(z.GoFiles(d))
			if err != nil {
				return fmt.Errorf("%s: %w", mod, err)
			}
			seen[joinPath(p, d)] = true
			push(pi.imports, joinPath(p, d), true)
		}
		for len(queue) > 0 {
			k := queue[0]
			queue = queue[1:]
			if longestWorkspace(wsByPath, k.ip) != nil {
				continue
			}
			mod, dir, err := r.provider(ctx, s, k.ip)
			if err != nil {
				var nf *NotFoundError
				if errors.As(err, &nf) {
					continue
				}
				return err
			}
			s.used[mod.Path] = true
			z, err := r.zips.get(ctx, mod)
			if err != nil {
				return err
			}
			pi, err := readImports(z.GoFiles(dir))
			if err != nil {
				return fmt.Errorf("%s: %w", mod, err)
			}
			push(pi.imports, k.ip, true)
		}
	}
	return nil
}

// provider finds the module of the build list that provides package ip: the modules whose
// path is a prefix of ip and whose zip has .go files in the package directory. More than one
// is an ambiguous import (cmd/go refuses it too).
func (r *resolver) provider(ctx context.Context, s *state, ip string) (Module, string, error) {
	var found []Module
	var dir string
	for p := ip; ; {
		if v := s.graph.selected[p]; v != "" {
			m := Module{p, v}
			z, err := r.zips.get(ctx, m)
			if err != nil {
				return Module{}, "", err
			}
			d := strings.TrimPrefix(strings.TrimPrefix(ip, p), "/")
			if z.HasGoFiles(d) {
				found = append(found, m)
				dir = d
			}
		}
		i := strings.LastIndexByte(p, '/')
		if i < 0 {
			break
		}
		p = p[:i]
	}
	switch len(found) {
	case 0:
		return Module{}, "", &NotFoundError{s.ws.Path, fmt.Sprintf("no module of the build list provides package %s", ip)}
	case 1:
		return found[0], dir, nil
	}
	return Module{}, "", &NotFoundError{s.ws.Path, fmt.Sprintf("ambiguous import: package %s is provided by %s and %s", ip, found[0], found[1])}
}

func longestWorkspace(ws map[string]*Workspace, ip string) *Workspace {
	var best *Workspace
	for p, w := range ws {
		if (ip == p || strings.HasPrefix(ip, p+"/")) && (best == nil || len(p) > len(best.Path)) {
			best = w
		}
	}
	return best
}

func joinPath(mod, dir string) string {
	if dir == "" {
		return mod
	}
	return mod + "/" + dir
}

// result collects the build lists and the module entries: every loaded go.mod and every
// selected module (go.sum needs the go.mod hash of both), with zips for the modules that
// provide packages at their selected version.
func (r *resolver) result(ctx context.Context, states []*state) (*Result, error) {
	res := &Result{BuildLists: map[string][]Module{}, zips: r.zips}
	infos := map[Module]*ModuleInfo{}
	add := func(m Module) error {
		if infos[m] != nil {
			return nil
		}
		mi := r.mods.get(ctx, m)
		if mi.err != nil {
			return r.fetchError("", []fetchErr{{m, mi.err}})
		}
		infos[m] = &ModuleInfo{Module: m, ModH1: ModH1(mi.raw), Go: mi.file.Go}
		return nil
	}
	for _, s := range states {
		bl := s.graph.list()
		res.BuildLists[s.ws.Path] = bl
		for m := range s.graph.loaded {
			if err := add(m); err != nil {
				return nil, err
			}
		}
		for _, m := range bl {
			if err := add(m); err != nil {
				return nil, err
			}
			if s.used[m.Path] {
				info := infos[m]
				if info.Packages {
					continue
				}
				z, err := r.zips.get(ctx, m)
				if err != nil {
					return nil, err
				}
				info.Packages, info.Zip = true, z.ModuleZip
			}
		}
	}
	for _, info := range infos {
		res.Modules = append(res.Modules, info)
	}
	sort.Slice(res.Modules, func(i, j int) bool {
		a, b := res.Modules[i], res.Modules[j]
		if a.Path != b.Path {
			return a.Path < b.Path
		}
		return Compare(a.Version, b.Version) < 0
	})
	return res, nil
}

// Roots is the requirement list of the go.mod of a workspace module (§03.2.5): every module
// of its build list that it declares, that some workspace module declares, or that provides
// packages to some build (packages[path]). Resolve guarantees that these roots select exactly
// the build list.
func Roots(buildList []Module, declared, packages map[string]bool) []Module {
	var out []Module
	for _, m := range buildList {
		if declared[m.Path] || packages[m.Path] {
			out = append(out, m)
		}
	}
	return out
}

// zipCache opens each module zip once per resolution.
type zipCache struct {
	proxy *Proxy
	tmp   string
	mu    sync.Mutex
	zips  map[Module]*cachedZip
}

type cachedZip struct {
	*ModuleZip
	file string
	temp bool
}

func newZipCache(p *Proxy, tmp string) *zipCache {
	return &zipCache{proxy: p, tmp: tmp, zips: map[Module]*cachedZip{}}
}

func (c *zipCache) get(ctx context.Context, m Module) (*cachedZip, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if z, ok := c.zips[m]; ok {
		return z, nil
	}
	file, temp, err := c.proxy.ZipFile(ctx, m, c.tmp)
	if errors.Is(err, ErrNotFound) {
		return nil, &NotFoundError{"", fmt.Sprintf("%s: zip not found on %s", m, c.proxy.Host())}
	}
	if err != nil {
		return nil, fmt.Errorf("download %s: %w", m, err)
	}
	mz, err := OpenModuleZip(file, m)
	if err != nil {
		if temp {
			os.Remove(file)
		}
		return nil, err
	}
	z := &cachedZip{ModuleZip: mz, file: file, temp: temp}
	c.zips[m] = z
	return z, nil
}

func (c *zipCache) close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	var first error
	for _, z := range c.zips {
		z.Close()
		if z.temp {
			if err := os.Remove(z.file); err != nil && first == nil {
				first = err
			}
		}
	}
	c.zips = map[Module]*cachedZip{}
	return first
}
