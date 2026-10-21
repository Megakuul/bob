package npm

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
)

// Importer is a project root or a workspace package (stand-in for an npm.App/npm.Library in bob.go).
type Importer struct {
	Name         string            `json:"name,omitempty"`
	Version      string            `json:"version,omitempty"`
	Deps         map[string]string `json:"deps,omitempty"`
	DevDeps      map[string]string `json:"devDeps,omitempty"`
	OptionalDeps map[string]string `json:"optionalDeps,omitempty"`
	PeerDeps     map[string]string `json:"peerDeps,omitempty"`
}

// Input is the spike's stand-in for the npm part of bob.go.
type Input struct {
	Importers map[string]*Importer `json:"importers"` // key = path relative to repo root, "." = root
	// Extend adds optional peer dependencies to registry packages that use a package without
	// declaring it (phantom dependency), e.g. {"@fastify/cors": {"fastify": "*"}}. The peer
	// links whatever instance the consumer has (npm.Extend in bob.go).
	Extend map[string]map[string]string `json:"extend,omitempty"`
}

// LockPkg is one package instance in bob.lock (key: name@version or name@version(peer@v,...)).
type LockPkg struct {
	Name             string            `json:"name"`
	Version          string            `json:"version"`
	Integrity        string            `json:"integrity"`
	Tarball          string            `json:"tarball,omitempty"` // only when not the default registry URL
	Deps             map[string]string `json:"deps,omitempty"`    // alias -> lock key (or link:path)
	Peers            []string          `json:"peers,omitempty"`   // aliases in Deps that are peer links
	Optional         bool              `json:"optional,omitempty"`
	OS               []string          `json:"os,omitempty"`
	CPU              []string          `json:"cpu,omitempty"`
	Libc             []string          `json:"libc,omitempty"`
	HasInstallScript bool              `json:"hasInstallScript,omitempty"`
	Bundled          []string          `json:"bundled,omitempty"`
	Bin              bool              `json:"bin,omitempty"`
}

type LockImporter struct {
	Deps map[string]string `json:"deps,omitempty"` // alias -> lock key, includes dev
	Dev  []string          `json:"dev,omitempty"`
}

type Lock struct {
	Importers map[string]*LockImporter `json:"importers"`
	Packages  map[string]*LockPkg      `json:"packages"`
	Warnings  []string                 `json:"warnings,omitempty"`
}

// node = one resolved name@version (phase 1).
type node struct {
	m     *Manifest
	deps  map[string]string // alias -> name@version (or link:path)
	peers map[string]string // peer name -> range
	peerOpt map[string]bool
	opt   bool // reached only through optional edges (approximation: any optional edge)
	nonOpt bool
}

type Resolver struct {
	Reg      *Registry
	Platform Platform
	in       *Input
	nodes    map[string]*node
	chosen   map[string][]Version
	pk       map[string]*Packument
	ws       map[string]string // workspace package name -> importer path
	warnings []string
}

type Platform struct{ OS, CPU, Libc string }

func key(name, version string) string { return name + "@" + version }

func splitKey(k string) (name, version string) {
	i := strings.LastIndex(strings.SplitN(k, "(", 2)[0], "@")
	return k[:i], strings.SplitN(k[i+1:], "(", 2)[0]
}

type spec struct {
	alias, name string
	rng         *Range
	tag         string
	link        string
}

func (r *Resolver) parseSpec(alias, s string) (spec, error) {
	sp := spec{alias: alias, name: alias}
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "npm:") {
		rest := s[4:]
		at := strings.LastIndex(rest, "@")
		if at <= 0 {
			sp.name, s = rest, "*"
		} else {
			sp.name, s = rest[:at], rest[at+1:]
		}
	}
	if strings.HasPrefix(s, "workspace:") || r.ws[sp.name] != "" && !strings.Contains(s, ":") {
		if p := r.ws[sp.name]; p != "" {
			sp.link = p
			return sp, nil
		}
		return sp, fmt.Errorf("%s: no workspace package named %s", alias, sp.name)
	}
	if strings.Contains(s, ":") || strings.HasPrefix(s, "file") || strings.Contains(s, "/") && !strings.HasPrefix(s, "npm:") {
		return sp, fmt.Errorf("%s: unsupported dependency spec %q (only registry versions, npm: aliases and workspace packages)", alias, s)
	}
	if s == "" {
		s = "*"
	}
	rg, err := ParseRange(s)
	if err != nil {
		if validTag(s) {
			sp.tag = s
			return sp, nil
		}
		return sp, err
	}
	sp.rng = &rg
	return sp, nil
}

func validTag(s string) bool {
	for _, c := range s {
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' || c == '_' || c == '.') {
			return false
		}
	}
	return s != ""
}

// pick chooses a version like npm-pick-manifest: dist-tag "latest" if it satisfies, else the
// highest satisfying non-deprecated version, else the highest satisfying.
func pick(p *Packument, sp spec) (string, bool) {
	if sp.tag != "" {
		v, ok := p.DistTags[sp.tag]
		return v, ok && p.Versions[v] != nil
	}
	if l, ok := p.DistTags["latest"]; ok {
		if lv, err := ParseVersion(l); err == nil && sp.rng.Match(lv) && p.Versions[l] != nil {
			return l, true
		}
	}
	var best, bestDep *Version
	for i := range p.parsed {
		v := &p.parsed[i]
		if !sp.rng.Match(*v) {
			continue
		}
		if p.Versions[v.Raw].IsDeprecated() {
			if bestDep == nil || v.Compare(*bestDep) > 0 {
				bestDep = v
			}
			continue
		}
		if best == nil || v.Compare(*best) > 0 {
			best = v
		}
	}
	if best == nil {
		best = bestDep
	}
	if best == nil {
		return "", false
	}
	return best.Raw, true
}

type req struct {
	from     string // parent node key or "importer:<path>"
	alias    string
	spec     string
	optional bool
	peer     bool // auto-installed peer (npm 7+)
	depth    int
}

// Resolve runs phase 1 (versions) and phase 2 (peer-aware instances) and returns bob.lock content.
func Resolve(ctx context.Context, reg *Registry, in *Input) (*Lock, error) {
	r := &Resolver{Reg: reg, in: in, nodes: map[string]*node{}, chosen: map[string][]Version{},
		pk: map[string]*Packument{}, ws: map[string]string{}}
	for path, im := range in.Importers {
		if im.Name != "" && path != "." {
			r.ws[im.Name] = path
		}
	}
	impDeps := map[string]map[string]string{}
	var level []req
	paths := sortedKeys(in.Importers)
	for _, path := range paths {
		im := in.Importers[path]
		impDeps[path] = map[string]string{}
		add := func(m map[string]string, opt bool) {
			for _, a := range sortedKeys(m) {
				level = append(level, req{from: "importer:" + path, alias: a, spec: m[a], optional: opt})
			}
		}
		add(im.Deps, false)
		add(im.DevDeps, false)
		add(im.OptionalDeps, true)
	}
	for depth := 0; len(level) > 0; depth++ {
		if err := r.prefetch(ctx, level); err != nil {
			return nil, err
		}
		sort.SliceStable(level, func(i, j int) bool {
			if level[i].peer != level[j].peer {
				return !level[i].peer
			}
			return level[i].alias < level[j].alias
		})
		var next []req
		for _, q := range level {
			k, err := r.resolveReq(q)
			if err != nil {
				if q.optional {
					r.warn("skipped optional %s@%s (from %s): %v", q.alias, q.spec, q.from, err)
					continue
				}
				return nil, fmt.Errorf("%s: %w", q.from, err)
			}
			if strings.HasPrefix(q.from, "importer:") {
				if !q.peer {
					impDeps[strings.TrimPrefix(q.from, "importer:")][q.alias] = k
				}
			} else if !q.peer {
				r.nodes[q.from].deps[q.alias] = k
			}
			if strings.HasPrefix(k, "link:") {
				continue
			}
			n := r.nodes[k]
			if q.optional {
				n.opt = true
			} else {
				n.nonOpt = true
			}
			if n.deps != nil {
				continue // already expanded
			}
			n.deps = map[string]string{}
			n.peers = map[string]string{}
			n.peerOpt = map[string]bool{}
			bundled := map[string]bool{}
			for _, b := range n.m.Bundled() {
				bundled[b] = true
			}
			for _, a := range sortedKeys(n.m.Dependencies) {
				if bundled[a] {
					continue
				}
				if _, isOpt := n.m.OptionalDependencies[a]; isOpt {
					continue
				}
				next = append(next, req{from: k, alias: a, spec: n.m.Dependencies[a], optional: q.optional, depth: depth + 1})
			}
			for _, a := range sortedKeys(n.m.OptionalDependencies) {
				if bundled[a] {
					continue
				}
				next = append(next, req{from: k, alias: a, spec: n.m.OptionalDependencies[a], optional: true, depth: depth + 1})
			}
			for _, a := range sortedKeys(r.in.Extend[n.m.Name]) {
				if _, has := n.m.Dependencies[a]; !has {
					n.peers[a] = r.in.Extend[n.m.Name][a]
					n.peerOpt[a] = true
				}
			}
			for _, a := range sortedKeys(n.m.PeerDependencies) {
				n.peers[a] = n.m.PeerDependencies[a]
				opt := n.m.PeerDependenciesMeta[a].Optional
				n.peerOpt[a] = opt
				if !opt {
					// npm 7+: required peers are installed automatically when nobody provides them.
					next = append(next, req{from: k, alias: a, spec: n.m.PeerDependencies[a], peer: true, optional: q.optional, depth: depth + 1})
				}
			}
		}
		level = next
	}
	if err := r.fetchLibc(ctx); err != nil {
		return nil, err
	}
	return r.phase2(impDeps)
}

func (r *Resolver) warn(f string, a ...any) { r.warnings = append(r.warnings, fmt.Sprintf(f, a...)) }

func (r *Resolver) prefetch(ctx context.Context, level []req) error {
	names := map[string]bool{}
	for _, q := range level {
		sp, err := r.parseSpec(q.alias, q.spec)
		if err == nil && sp.link == "" && r.pk[sp.name] == nil {
			names[sp.name] = true
		}
	}
	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, 16)
	var firstErr error
	for n := range names {
		wg.Add(1)
		go func(n string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			p, err := r.Reg.Get(ctx, n)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				if firstErr == nil {
					firstErr = err
				}
				return
			}
			r.pk[n] = p
		}(n)
	}
	wg.Wait()
	_ = firstErr // reported per request (optional deps may legitimately be missing)
	return nil
}

func (r *Resolver) resolveReq(q req) (string, error) {
	sp, err := r.parseSpec(q.alias, q.spec)
	if err != nil {
		return "", err
	}
	if sp.link != "" {
		return "link:" + sp.link, nil
	}
	p := r.pk[sp.name]
	if p == nil {
		pp, err := r.Reg.Get(context.Background(), sp.name)
		if err != nil {
			return "", err
		}
		r.pk[sp.name], p = pp, pp
	}
	// Reuse an already chosen version when it satisfies (npm dedupes against the placed tree).
	if sp.rng != nil {
		var best *Version
		for i, v := range r.chosen[sp.name] {
			if sp.rng.Match(v) && (best == nil || v.Compare(*best) > 0) {
				best = &r.chosen[sp.name][i]
			}
		}
		if best != nil {
			return key(sp.name, best.Raw), nil
		}
	}
	if q.peer {
		// a peer already satisfied by any chosen version needs no auto-install
		if len(r.chosen[sp.name]) > 0 && sp.rng == nil {
			return key(sp.name, r.chosen[sp.name][0].Raw), nil
		}
	}
	v, ok := pick(p, sp)
	if !ok {
		return "", fmt.Errorf("no version of %s matches %q", sp.name, q.spec)
	}
	k := key(sp.name, v)
	if r.nodes[k] == nil {
		pv, _ := ParseVersion(v)
		pv.Raw = v
		r.chosen[sp.name] = append(r.chosen[sp.name], pv)
		r.nodes[k] = &node{m: p.Versions[v]}
	}
	return k, nil
}

func (r *Resolver) fetchLibc(ctx context.Context) error {
	var wg sync.WaitGroup
	sem := make(chan struct{}, 16)
	var mu sync.Mutex
	var firstErr error
	for k, n := range r.nodes {
		if len(n.m.OS) == 0 && len(n.m.CPU) == 0 {
			continue
		}
		wg.Add(1)
		go func(k string, n *node) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			name, v := splitKey(k)
			full, err := r.Reg.Full(ctx, name, v)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				if firstErr == nil {
					firstErr = err
				}
				return
			}
			n.m.Libc = full.Libc
		}(k, n)
	}
	wg.Wait()
	return firstErr
}

// phase2 builds peer-aware instances (pnpm-style keys) starting from the importers.
func (r *Resolver) phase2(impDeps map[string]map[string]string) (*Lock, error) {
	lock := &Lock{Importers: map[string]*LockImporter{}, Packages: map[string]*LockPkg{}}
	rootDeps := impDeps["."]
	var inst func(k string, ctxDeps map[string]string) string
	inst = func(k string, ctxDeps map[string]string) string {
		if strings.HasPrefix(k, "link:") {
			return k
		}
		n := r.nodes[k]
		name, ver := splitKey(k)
		// resolve peers against the parent's context (its deps + its resolved peers)
		peerRes := map[string]string{}
		for _, pn := range sortedKeys(n.peers) {
			rg, err := ParseRange(n.peers[pn])
			var cand string
			if c, ok := ctxDeps[pn]; ok {
				cand = c
			} else if c, ok := rootDeps[pn]; ok {
				cand = c
			} else {
				for _, v := range r.chosen[pn] {
					if err == nil && rg.Match(v) {
						cand = key(pn, v.Raw)
					}
				}
				if cand == "" && len(r.chosen[pn]) > 0 && !n.peerOpt[pn] {
					cand = key(pn, r.chosen[pn][0].Raw)
				}
			}
			if cand == "" {
				continue // unmet optional peer
			}
			if !strings.HasPrefix(cand, "link:") && err == nil {
				_, cv := splitKey(cand)
				if pv, e := ParseVersion(cv); e == nil && !rg.Match(pv) {
					r.warn("peer conflict: %s wants %s@%s, context provides %s", k, pn, n.peers[pn], cand)
				}
			}
			peerRes[pn] = cand
		}
		ik := k
		if len(peerRes) > 0 {
			var parts []string
			for _, pn := range sortedKeys(peerRes) {
				parts = append(parts, strings.SplitN(peerRes[pn], "(", 2)[0])
			}
			ik = k + "(" + strings.Join(parts, ",") + ")"
		}
		if lock.Packages[ik] != nil {
			return ik
		}
		lp := &LockPkg{Name: name, Version: ver, Integrity: n.m.Dist.Integrity, OS: n.m.OS, CPU: n.m.CPU,
			Libc: n.m.Libc, HasInstallScript: n.m.HasInstallScript, Bundled: n.m.Bundled(),
			Optional: n.opt && !n.nonOpt, Bin: len(n.m.Bin) > 0 && string(n.m.Bin) != "{}" && string(n.m.Bin) != "null",
			Deps: map[string]string{}}
		if lp.Integrity == "" && n.m.Dist.Shasum != "" {
			lp.Integrity = "sha1-hex:" + n.m.Dist.Shasum
		}
		if def := r.Reg.Base + "/" + name + "/-/" + name[strings.LastIndex(name, "/")+1:] + "-" + ver + ".tgz"; n.m.Dist.Tarball != def {
			lp.Tarball = n.m.Dist.Tarball
		}
		lock.Packages[ik] = lp // before recursion: cycles terminate
		// child context: own deps + own peers
		cctx := map[string]string{}
		for a, d := range n.deps {
			cctx[a] = d
		}
		for a, d := range peerRes {
			cctx[a] = d
		}
		for _, a := range sortedKeys(n.deps) {
			lp.Deps[a] = inst(n.deps[a], cctx)
		}
		for _, a := range sortedKeys(peerRes) {
			if _, own := lp.Deps[a]; own {
				continue
			}
			// peer instance: same instance the parent sees
			lp.Deps[a] = r.ctxInstance(lock, peerRes[a], ctxDeps, inst)
			lp.Peers = append(lp.Peers, a)
		}
		return ik
	}
	for _, path := range sortedKeys(impDeps) {
		li := &LockImporter{Deps: map[string]string{}}
		ctx := impDeps[path]
		for _, a := range sortedKeys(ctx) {
			li.Deps[a] = inst(ctx[a], ctx)
		}
		im := r.in.Importers[path]
		for a := range im.DevDeps {
			li.Dev = append(li.Dev, a)
		}
		sort.Strings(li.Dev)
		lock.Importers[path] = li
	}
	lock.Warnings = r.warnings
	return lock, nil
}

// ctxInstance returns the instance key for a phase-1 key as seen from a parent context.
func (r *Resolver) ctxInstance(lock *Lock, k string, ctxDeps map[string]string, inst func(string, map[string]string) string) string {
	return inst(k, ctxDeps)
}

func sortedKeys[V any](m map[string]V) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}
