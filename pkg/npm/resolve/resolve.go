// Package resolve is bob's npm resolver (SPEC §03.4.2, ADR 0006, R-NPM-001a): it turns the
// npm dependencies declared by importers into the records of bob.lock's "npm" section.
//
// Resolution reproduces npm 11's own algorithm (an Arborist port, see arborist.go) against
// the registry's abbreviated metadata, so the installed name@version set and every
// dependency edge equal `npm install --package-lock-only`. On top of npm's tree bob computes
// peer-correct instances (instances.go): a package whose dependents see different peers is
// recorded once per peer set. The package is pure: it reads only the registry (with an ETag
// cache) and writes nothing but that cache.
package resolve

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"sort"
	"strings"
	"sync"

	"github.com/megakuul/bob/pkg/bob/diag"
)

// Dep is one declared dependency of an importer (npm.Dep, npm.Alias, a local *npm.Lib).
type Dep struct {
	Alias    string // the name under node_modules
	Name     string // registry package; "" means Alias (they differ for npm.Alias)
	Req      string // node-semver range, exact version or dist-tag; "" means "*"
	Dev      bool
	Optional bool
	Link     string // importer directory of a local library; Name and Req are ignored
}

func (d Dep) name() string {
	if d.Name == "" {
		return d.Alias
	}
	return d.Name
}

// spec is the dependency spec npm would read from the generated package.json.
func (d Dep) spec() string {
	req := d.Req
	if req == "" {
		req = "*"
	}
	if d.Link != "" {
		return "*"
	}
	if d.name() != d.Alias {
		return "npm:" + d.name() + "@" + req
	}
	return req
}

// Importer is one directory with npm dependencies (an npm.App, npm.Vite or npm.Library).
type Importer struct {
	Dir   string // repo-relative, "." for the repository root
	Name  string // package name; libraries are linked into consumers by it
	Deps  []Dep
	Peers []Dep // libraries: provided by the consumer, installed for the library's own build
}

// Extension is npm.Extend: dependencies a registry package uses without declaring them.
type Extension struct {
	Package string
	Deps    []Dep    // added as dependencies of every instance of Package
	Peers   []string // npm.Peer: linked from the dependent's context when present
}

// Input is the npm part of a repository's declarations.
type Input struct {
	Importers []Importer
	Extend    []Extension
}

// Options configures Resolve.
type Options struct {
	Registry *Registry
	// NodeVersion is the Node.js version npm would run on ("24.21.0"); versions whose
	// engines.node excludes it are picked last, as npm does. "" disables the check.
	NodeVersion string
	// Root names the repository or package in error messages (E-RESOLVE "{root}").
	Root string
	// Prefer keeps previous pins (bob lock without --update after a declaration changed):
	// package name → versions locked before. A range dependency whose range admits one of
	// them (and whose engines.node admits the Node version) gets the highest such version
	// instead of npm's pick, so a changed declaration moves only what it must. Exact
	// versions and dist-tags are picked as npm does. nil resolves from scratch.
	Prefer map[string][]string
}

// Section is bob.lock's "npm" section (SPEC §03.4.2; the same JSON as lock.NpmSection).
type Section struct {
	Inputs    string                   `json:"inputs"`
	Registry  string                   `json:"registry"`
	Importers map[string]ImporterEntry `json:"importers"`
	Packages  map[string]Package       `json:"packages"`
}

// ImporterEntry is one importer's record (lock.NpmImporter).
type ImporterEntry struct {
	Deps map[string]string `json:"deps"`          // alias → instance key or "link:<dir>"
	Dev  []string          `json:"dev,omitempty"` // aliases that are Dev()
}

// Package is one package instance (lock.NpmPackage).
type Package struct {
	Name             string            `json:"name"`
	Version          string            `json:"version"`
	Integrity        string            `json:"integrity"`
	Tarball          string            `json:"tarball,omitempty"`
	Deps             map[string]string `json:"deps,omitempty"`
	Peers            []string          `json:"peers,omitempty"`
	Optional         bool              `json:"optional,omitempty"`
	OS               []string          `json:"os,omitempty"`
	CPU              []string          `json:"cpu,omitempty"`
	Libc             []string          `json:"libc,omitempty"`
	Bundled          []string          `json:"bundled,omitempty"`
	HasInstallScript bool              `json:"hasInstallScript,omitempty"`
	Bin              map[string]string `json:"bin,omitempty"`
}

// Result is the outcome of Resolve.
type Result struct {
	Section  Section
	Warnings []string // npm's ERESOLVE "overriding peer dependency" warnings
}

// Error is a resolution failure: E-RESOLVE (BOB-E-1015).
type Error struct {
	Root, Reason string
	Err          error
}

func (e *Error) Error() string { return e.Diag().Error() }
func (e *Error) Unwrap() error { return e.Err }

// Diag returns the user-facing diagnostic.
func (e *Error) Diag() *diag.Error {
	return diag.New(1015, "ecosystem", "npm", "root", e.Root, "reason", e.Reason)
}

// InputsHash is the section's "inputs": sha256 of the canonical JSON of the declarations and
// the options that change the result (R-FW-062).
func InputsHash(in Input, nodeVersion string) string {
	type canon struct {
		Importers []Importer
		Extend    []Extension
		Node      string
	}
	c := canon{Node: nodeVersion}
	for _, im := range in.Importers {
		im.Deps = append([]Dep(nil), im.Deps...)
		im.Peers = append([]Dep(nil), im.Peers...)
		sort.Slice(im.Deps, func(i, j int) bool { return im.Deps[i].Alias < im.Deps[j].Alias })
		sort.Slice(im.Peers, func(i, j int) bool { return im.Peers[i].Alias < im.Peers[j].Alias })
		c.Importers = append(c.Importers, im)
	}
	sort.Slice(c.Importers, func(i, j int) bool { return c.Importers[i].Dir < c.Importers[j].Dir })
	for _, x := range in.Extend {
		x.Deps = append([]Dep(nil), x.Deps...)
		x.Peers = append([]string(nil), x.Peers...)
		sort.Slice(x.Deps, func(i, j int) bool { return x.Deps[i].Alias < x.Deps[j].Alias })
		sort.Strings(x.Peers)
		c.Extend = append(c.Extend, x)
	}
	sort.Slice(c.Extend, func(i, j int) bool { return c.Extend[i].Package < c.Extend[j].Package })
	b, _ := json.Marshal(c)
	sum := sha256.Sum256(b)
	return "sha256-" + base64.StdEncoding.EncodeToString(sum[:])
}

func validDir(d string) bool {
	if d == "." {
		return true
	}
	if d == "" || strings.HasPrefix(d, "/") || path.Clean(d) != d || d == ".." || strings.HasPrefix(d, "../") {
		return false
	}
	return !strings.ContainsAny(d, "\\\x00")
}

func (o *Options) root() string {
	if o.Root != "" {
		return o.Root
	}
	return "the repository"
}

// Resolve resolves in against the registry. Errors are *Error (E-RESOLVE) unless ctx ends.
func Resolve(ctx context.Context, in Input, o Options) (*Result, error) {
	fail := func(err error) (*Result, error) {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		var e *Error
		if errors.As(err, &e) {
			return nil, e
		}
		return nil, &Error{Root: o.root(), Reason: err.Error(), Err: err}
	}
	if o.Registry == nil {
		o.Registry = &Registry{}
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	b := &builder{
		ctx: ctx, reg: o.Registry, nodeVersion: strings.TrimPrefix(o.NodeVersion, "v"), prefer: o.Prefer,
		extend: map[string][]Dep{}, seen: map[*node]bool{}, failed: map[*node]bool{},
		psrc: map[*node]*node{}, vroots: map[*node]*node{}, indexes: map[*node]*edgeIndex{},
		specs: map[string]specResult{}, picks: map[string]*manifest{}, sat: map[string]bool{},
	}
	if err := b.setup(in); err != nil {
		return fail(err)
	}
	if err := b.build(); err != nil {
		return fail(err)
	}
	sec, err := b.output(in)
	if err != nil {
		return fail(err)
	}
	sec.Inputs = InputsHash(in, b.nodeVersion)
	return &Result{Section: *sec, Warnings: b.warnings}, nil
}

// importerManifest is the package.json bob generates for an importer, as npm reads it:
// dependency objects sorted by name.
func importerManifest(im Importer, name string) *manifest {
	m := &manifest{Name: name, Version: "0.0.0", Dependencies: &orderedMap{}, OptionalDependencies: &orderedMap{},
		PeerDependencies: &orderedMap{}, devDependencies: &orderedMap{}, normalized: true}
	deps := append([]Dep(nil), im.Deps...)
	sort.Slice(deps, func(i, j int) bool { return deps[i].Alias < deps[j].Alias })
	for _, d := range deps {
		switch {
		case d.Dev:
			m.devDependencies.Set(d.Alias, d.spec())
		case d.Optional:
			m.OptionalDependencies.Set(d.Alias, d.spec())
		default:
			m.Dependencies.Set(d.Alias, d.spec())
		}
	}
	peers := append([]Dep(nil), im.Peers...)
	sort.Slice(peers, func(i, j int) bool { return peers[i].Alias < peers[j].Alias })
	for _, d := range peers {
		m.PeerDependencies.Set(d.Alias, d.spec())
	}
	return m
}

func syntheticName(dir string) string {
	r := strings.NewReplacer("/", "-", ".", "-")
	return "@bob-importer/" + strings.Trim(r.Replace(dir), "-")
}

// setup validates the declarations and builds the tree's starting point: the root importer
// with one workspace (link + target) per other importer, like the package.json bob
// generates for a repository.
func (b *builder) setup(in Input) error {
	dirs := map[string]bool{}
	names := map[string]string{}
	for _, im := range in.Importers {
		if !validDir(im.Dir) {
			return resolveErr("invalid importer directory %q", im.Dir)
		}
		if dirs[im.Dir] {
			return resolveErr("two importers in directory %s", im.Dir)
		}
		dirs[im.Dir] = true
		if im.Name != "" {
			if !ValidName(im.Name) {
				return resolveErr("invalid package name %q for importer %s", im.Name, im.Dir)
			}
			if d, dup := names[im.Name]; dup {
				return resolveErr("importers %s and %s are both named %s", d, im.Dir, im.Name)
			}
			names[im.Name] = im.Dir
		}
		for _, d := range append(append([]Dep(nil), im.Deps...), im.Peers...) {
			if err := b.checkDep(d, dirs, in); err != nil {
				return fmt.Errorf("importer %s: %w", im.Dir, err)
			}
		}
	}
	for _, x := range in.Extend {
		if !ValidName(x.Package) {
			return resolveErr("npm.Extend: invalid package name %q", x.Package)
		}
		for _, d := range x.Deps {
			if d.Link != "" {
				return resolveErr("npm.Extend(%s): local libraries cannot be added to registry packages", x.Package)
			}
			if err := b.checkDep(d, dirs, in); err != nil {
				return fmt.Errorf("npm.Extend(%s): %w", x.Package, err)
			}
			b.extend[x.Package] = append(b.extend[x.Package], d)
		}
		for _, p := range x.Peers {
			if !ValidName(p) {
				return resolveErr("npm.Extend(%s): invalid package name %q", x.Package, p)
			}
		}
	}

	b.seq++
	root := &node{name: "root", children: map[string]*node{}, basePath: "/r", projRoot: true, seq: b.seq}
	var rootIm Importer
	var others []Importer
	for _, im := range in.Importers {
		if im.Dir == "." {
			rootIm = im
		} else {
			others = append(others, im)
		}
	}
	sort.Slice(others, func(i, j int) bool { return others[i].Dir < others[j].Dir })
	rootName := rootIm.Name
	if rootName == "" {
		rootName = "bob-root"
	}
	root.pkg = importerManifest(rootIm, rootName)
	root.loadDeps(true, root.pkg.devDependencies)
	b.root = root
	for _, im := range others {
		name := im.Name
		if name == "" {
			name = syntheticName(im.Dir)
		}
		linked := false
		for _, d := range rootIm.Deps {
			linked = linked || d.Link == im.Dir && d.Alias == name
		}
		if root.resolve(name) != nil || !linked && root.edge(name) != nil {
			return resolveErr("importer %s is named %s, which is also a dependency of the root importer", im.Dir, name)
		}
		b.seq++
		ws := &node{name: path.Base(im.Dir), children: map[string]*node{}, basePath: "/r/" + im.Dir, workspace: im.Dir,
			fsParent: root, seq: b.seq}
		ws.pkg = importerManifest(im, name)
		ws.loadDeps(true, ws.pkg.devDependencies)
		root.fsKids = append(root.fsKids, ws)
		b.seq++
		l := &node{name: name, link: true, target: ws, children: map[string]*node{}, edgeIdx: map[string]*edge{}, seq: b.seq}
		root.addEdge(name, "file:"+im.Dir, edgeWorkspace)
		b.setParent(l, root)
	}
	return nil
}

// checkDep validates one declared dependency (names come from bob.go: hostile-input rules
// apply all the same).
func (b *builder) checkDep(d Dep, dirs map[string]bool, in Input) error {
	if !ValidName(d.Alias) {
		return resolveErr("invalid package name %q", d.Alias)
	}
	if d.Link != "" {
		for _, im := range in.Importers {
			if im.Dir == d.Link {
				return nil
			}
		}
		return resolveErr("%s links importer %s, which does not exist", d.Alias, d.Link)
	}
	if _, err := parseSpec(d.Alias, d.spec()); err != nil {
		return resolveErr("%s", err.Error())
	}
	return nil
}

// applyExtend adds npm.Extend dependencies to a picked manifest (once).
func (b *builder) applyExtend(m *manifest) {
	ext := b.extend[m.Name]
	if len(ext) == 0 {
		return
	}
	if m.Dependencies == nil {
		m.Dependencies = &orderedMap{}
	}
	for _, d := range ext {
		if _, ok := m.Dependencies.Get(d.Alias); !ok {
			m.Dependencies.Set(d.Alias, d.spec())
		}
	}
}

// output runs phase 2 and builds the lock section.
func (b *builder) output(in Input) (*Section, error) {
	p := &phase2{b: b, byID: map[string]*inst{}, ext: map[string][]string{}}
	for _, x := range in.Extend {
		p.ext[x.Package] = append(p.ext[x.Package], x.Peers...)
	}
	type imRec struct {
		dir   string
		o     *owner
		decl  []Dep
		roots map[string]*inst
	}
	var ims []*imRec
	for _, im := range in.Importers {
		t := b.root
		if im.Dir != "." {
			for _, w := range b.root.fsKids {
				if w.workspace == im.Dir {
					t = w
				}
			}
		}
		r := &imRec{dir: im.Dir, o: &owner{t: t, deps: map[string]*inst{}, links: map[string]string{}},
			decl: append(append([]Dep(nil), im.Deps...), im.Peers...), roots: map[string]*inst{}}
		ims = append(ims, r)
	}
	sort.Slice(ims, func(i, j int) bool { return ims[i].dir < ims[j].dir })
	sec := &Section{Registry: b.reg.base(), Importers: map[string]ImporterEntry{}, Packages: map[string]Package{}}
	for _, r := range ims {
		sort.Slice(r.decl, func(i, j int) bool { return r.decl[i].Alias < r.decl[j].Alias })
		rec := ImporterEntry{Deps: map[string]string{}}
		for _, d := range r.decl {
			i, link := p.view(r.o, d.Alias)
			switch {
			case link != "":
				rec.Deps[d.Alias] = link
				r.o.links[d.Alias] = link
			case i != nil:
				r.o.deps[d.Alias] = i
				r.roots[d.Alias] = i
			case d.Optional:
				continue
			default:
				if t := r.o.t.resolve(d.Alias); t != nil && len(t.errs) > 0 {
					return nil, resolveErr("importer %s: %v", r.dir, t.errs[0])
				}
				return nil, resolveErr("importer %s: %s@%s could not be resolved", r.dir, d.Alias, d.spec())
			}
			if d.Dev || isPeer(d, in, r.dir) {
				rec.Dev = append(rec.Dev, d.Alias)
			}
		}
		sort.Strings(rec.Dev)
		sec.Importers[r.dir] = rec
		p.expand()
	}
	p.expand()

	// optional flags: reachable from an importer without crossing an optional edge
	nonOpt := map[*inst]bool{}
	var stack []*inst
	for _, r := range ims {
		for _, d := range r.decl {
			if i := r.roots[d.Alias]; i != nil && !d.Optional && !nonOpt[i] {
				nonOpt[i] = true
				stack = append(stack, i)
			}
		}
	}
	for len(stack) > 0 {
		i := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		for a, d := range i.deps {
			if !i.optEdge[a] && !nonOpt[d] {
				nonOpt[d] = true
				stack = append(stack, d)
			}
		}
	}
	// instances missing a required dependency: an error unless only optional paths lead there
	dropped := map[*inst]bool{}
	for changed := true; changed; {
		changed = false
		for _, i := range p.list {
			if dropped[i] {
				continue
			}
			bad := i.requiredMissing != ""
			for a, d := range i.deps {
				if dropped[d] && !i.optEdge[a] {
					bad = true
				}
			}
			if !bad {
				continue
			}
			if nonOpt[i] {
				what := i.requiredMissing
				if what == "" {
					what = "an optional package it requires"
				}
				return nil, resolveErr("%s@%s needs %s, which could not be resolved", i.t.packageName(), i.t.version(), what)
			}
			dropped[i] = true
			changed = true
		}
	}
	var live []*inst
	for _, i := range p.list {
		if !dropped[i] {
			live = append(live, i)
		}
	}
	for _, i := range live {
		for a, d := range i.deps {
			if dropped[d] {
				delete(i.deps, a)
			}
		}
	}
	for dir, rec := range sec.Importers {
		for _, r := range ims {
			if r.dir != dir {
				continue
			}
			for a, i := range r.roots {
				if dropped[i] {
					delete(rec.Deps, a)
				}
			}
		}
	}

	if err := b.fetchLibc(live); err != nil {
		return nil, err
	}

	cls, hashes := classes(live)
	labels := map[string]map[int]bool{}
	for n, i := range live {
		l := i.label()
		if labels[l] == nil {
			labels[l] = map[int]bool{}
		}
		labels[l][cls[n]] = true
	}
	keyOf := func(n int) string {
		l := live[n].label()
		if len(labels[l]) > 1 {
			return l + "_" + hashes[cls[n]][:8]
		}
		return l
	}
	idx := map[*inst]int{}
	for n, i := range live {
		idx[i] = n
	}
	for n, i := range live {
		k := keyOf(n)
		pk, exists := sec.Packages[k]
		if exists {
			if nonOpt[i] {
				pk.Optional = false
				sec.Packages[k] = pk
			}
			continue
		}
		m := i.t.pkg
		pk = Package{Name: m.Name, Version: m.Version, Integrity: m.Dist.Integrity, Optional: !nonOpt[i],
			OS: m.os, CPU: m.cpu, Libc: m.libc, HasInstallScript: m.HasInstallScript, Bin: m.bins}
		if def := b.reg.DefaultTarball(m.Name, m.Version); m.Dist.Tarball != def {
			pk.Tarball = m.Dist.Tarball
		}
		if len(m.bundled) > 0 {
			pk.Bundled = append([]string(nil), m.bundled...)
			sort.Strings(pk.Bundled)
		}
		if len(i.deps)+len(i.links) > 0 {
			pk.Deps = map[string]string{}
			for a, d := range i.deps {
				pk.Deps[a] = keyOf(idx[d])
			}
			for a, l := range i.links {
				pk.Deps[a] = l
			}
		}
		pk.Peers = append([]string(nil), i.peers...)
		if len(pk.Peers) == 0 {
			pk.Peers = nil
		}
		sec.Packages[k] = pk
	}
	for dir, rec := range sec.Importers {
		for _, r := range ims {
			if r.dir != dir {
				continue
			}
			for a, i := range r.roots {
				if !dropped[i] {
					rec.Deps[a] = keyOf(idx[i])
				}
			}
		}
	}
	return sec, nil
}

func isPeer(d Dep, in Input, dir string) bool {
	for _, im := range in.Importers {
		if im.Dir != dir {
			continue
		}
		for _, p := range im.Peers {
			if p.Alias == d.Alias {
				for _, x := range im.Deps {
					if x.Alias == d.Alias {
						return false
					}
				}
				return true
			}
		}
	}
	return false
}

// fetchLibc reads libc from the per-version documents of packages with os or cpu (the
// abbreviated packument does not carry it).
func (b *builder) fetchLibc(list []*inst) error {
	seen := map[*manifest]bool{}
	var ms []*manifest
	for _, i := range list {
		m := i.t.pkg
		if m == nil || seen[m] || len(m.os) == 0 && len(m.cpu) == 0 || len(m.libc) > 0 {
			continue
		}
		seen[m] = true
		ms = append(ms, m)
	}
	errs := make([]error, len(ms))
	libc := make([][]string, len(ms))
	var wg sync.WaitGroup
	for n, m := range ms {
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() {
				if r := recover(); r != nil {
					errs[n] = fmt.Errorf("registry: internal error: %v", r)
				}
			}()
			full, err := b.reg.versionDoc(b.ctx, m.Name, m.Version)
			if err != nil {
				if errors.Is(err, ErrNotFound) {
					return
				}
				errs[n] = err
				return
			}
			libc[n] = stringList(full.Libc)
		}()
	}
	wg.Wait()
	for n, m := range ms {
		if errs[n] != nil {
			return errs[n]
		}
		for _, x := range libc[n] {
			if len(x) > 64 || strings.ContainsAny(x, "\x00\n") {
				return fmt.Errorf("%s@%s: invalid libc entry %q", m.Name, m.Version, x)
			}
		}
		m.libc = libc[n]
	}
	return nil
}
