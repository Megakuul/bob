package crates

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

// Local is a workspace crate declared in bob.go (here: as data).
type Local struct {
	Name     string              `json:"name"`
	Version  string              `json:"version"`
	Deps     []IndexDep          `json:"deps"` // Registry "local" = another Local by Package/Name
	Features map[string][]string `json:"features"`
}

func (l *Local) entry() *IndexEntry {
	v, _ := ParseVersion(l.Version)
	return &IndexEntry{Name: l.Name, Vers: l.Version, Deps: l.Deps, Features: l.Features, version: v}
}

// Pkg is one resolved package.
type Pkg struct {
	Entry *IndexEntry
	Local bool
	// Edge i of Entry.Deps -> resolved key ("name@version"); missing when the edge is inactive.
	Edges map[int]string
	Feats map[string]bool // features activated during lock resolution (diagnostics only)
}

func (p *Pkg) Key() string { return p.Entry.Name + "@" + p.Entry.Vers }

// Resolution is the crate graph: what bob.lock records.
type Resolution struct {
	Pkgs  map[string]*Pkg
	Roots []string
	Stats struct{ Iterations, IndexFiles int }
}

// ResolveOpts mirrors cargo's lockfile resolution: all features of local crates, dev-deps of
// local crates, all targets (the lock is platform-independent).
type ResolveOpts struct {
	RustVersion string // toolchain version; newer rust_version requirements are avoided (fallback)
	Prefetch    int
}

type activation struct {
	feats map[string]bool
}

type resolver struct {
	ctx    context.Context
	ix     *Index
	locals map[string]*Local
	rustV  Version
	// choice[name][compat] = version chosen for that semver-compatibility class
	choice map[string]map[string]string
	// reqs seen per class in the current pass
	reqs map[string]map[string][]Req
	done map[*Pkg]bool
}

// Resolve resolves the local crates against crates.io.
func Resolve(ctx context.Context, ix *Index, locals []*Local, o ResolveOpts) (*Resolution, error) {
	r := &resolver{ctx: ctx, ix: ix, locals: map[string]*Local{}, choice: map[string]map[string]string{}}
	if o.RustVersion != "" {
		r.rustV, _ = ParseVersion(o.RustVersion)
	}
	for _, l := range locals {
		r.locals[l.Name] = l
	}
	for it := 1; it <= 50; it++ {
		res, changed, err := r.pass(locals, o)
		if err != nil {
			return nil, err
		}
		if !changed {
			res.Stats.Iterations = it
			res.Stats.IndexFiles = len(ix.entries)
			return res, nil
		}
	}
	return nil, fmt.Errorf("crate resolution did not converge after 50 passes")
}

func depPkg(d IndexDep) string {
	if d.Package != "" {
		return d.Package
	}
	return d.Name
}

// candidates returns matching versions, best first.
func (r *resolver) candidates(name string, req Req) ([]*IndexEntry, error) {
	all, err := r.ix.Get(r.ctx, name)
	if err != nil {
		return nil, err
	}
	var ok, old []*IndexEntry
	for _, e := range all {
		if e.Yanked || !req.Matches(e.version) {
			continue
		}
		if e.RustVersion != "" && r.rustV.Major > 0 {
			if rv, err := ParseVersion(normalizeRV(e.RustVersion)); err == nil && rv.Compare(r.rustV) > 0 {
				old = append(old, e)
				continue
			}
		}
		ok = append(ok, e)
	}
	sortDesc := func(xs []*IndexEntry) {
		sort.Slice(xs, func(i, j int) bool { return xs[i].version.Compare(xs[j].version) > 0 })
	}
	sortDesc(ok)
	sortDesc(old)
	return append(ok, old...), nil
}

func normalizeRV(s string) string {
	switch strings.Count(s, ".") {
	case 0:
		return s + ".0.0"
	case 1:
		return s + ".0"
	}
	return s
}

// pass walks the graph from the roots with the current class choices. It records every
// requirement per class; if a class's best version changes, another pass is needed.
func (r *resolver) pass(locals []*Local, o ResolveOpts) (*Resolution, bool, error) {
	r.reqs = map[string]map[string][]Req{}
	r.done = map[*Pkg]bool{}
	res := &Resolution{Pkgs: map[string]*Pkg{}}
	acts := map[string]*activation{}
	type item struct {
		key   string
		feats []string
		deflt bool
	}
	var queue []item
	for _, l := range locals {
		e := l.entry()
		k := e.Name + "@" + e.Vers
		res.Pkgs[k] = &Pkg{Entry: e, Local: true, Edges: map[int]string{}}
		res.Roots = append(res.Roots, k)
		var all []string
		for f := range l.Features {
			all = append(all, f)
		}
		queue = append(queue, item{k, all, true})
	}
	for len(queue) > 0 {
		it := queue[0]
		queue = queue[1:]
		p := res.Pkgs[it.key]
		a := acts[it.key]
		if a == nil {
			a = &activation{feats: map[string]bool{}}
			acts[it.key] = a
		}
		req := append([]string{}, it.feats...)
		if it.deflt {
			req = append(req, "default")
		}
		grew := false
		for _, f := range req {
			if !a.feats[f] {
				grew = true
			}
		}
		if !grew && r.done[p] {
			continue
		}
		fx := Expand(p.Entry, req, a.feats, true)
		var names []string
		for _, d := range p.Entry.Deps {
			if d.Registry != "local" && (d.Kind != "dev" || p.Local) && (!d.Optional || fx.Deps[d.Name]) {
				names = append(names, depPkg(d))
			}
		}
		r.ix.Prefetch(r.ctx, names, 16) // parallel index fetches; results are cached for pick()
		for f := range fx.Feats {
			a.feats[f] = true
		}
		p.Feats = a.feats
		for i, d := range p.Entry.Deps {
			if d.Kind == "dev" && !p.Local {
				continue
			}
			if d.Optional && !fx.Deps[d.Name] {
				continue
			}
			key, err := r.pick(res, d)
			if err != nil {
				return nil, false, fmt.Errorf("%s depends on %s %s: %w", p.Key(), depPkg(d), d.Req, err)
			}
			if key == "" {
				continue // class conflict recorded; resolved in the next pass
			}
			p.Edges[i] = key
			feats := append(append([]string{}, d.Features...), fx.DepFeats[d.Name]...)
			queue = append(queue, item{key, feats, d.DefaultFeatures})
		}
		r.done[p] = true
	}
	// re-choose each class from all requirements seen
	changed := false
	for name, classes := range r.reqs {
		for class, reqs := range classes {
			best, err := r.best(name, class, reqs)
			if err != nil {
				return nil, false, err
			}
			if r.choice[name] == nil {
				r.choice[name] = map[string]string{}
			}
			if r.choice[name][class] != best {
				r.choice[name][class] = best
				changed = true
			}
		}
	}
	// drop classes nobody asked for in this pass
	for name, classes := range r.choice {
		for class := range classes {
			if _, ok := r.reqs[name][class]; !ok {
				delete(classes, class)
				changed = true
			}
		}
	}
	// links: at most one package per `links` value (Cargo rule)
	links := map[string]string{}
	for k, p := range res.Pkgs {
		if l := p.Entry.Links; l != "" {
			if o, dup := links[l]; dup {
				return nil, false, fmt.Errorf("%s and %s both link native library %q; only one may be in the graph", o, k, l)
			}
			links[l] = k
		}
	}
	return res, changed, nil
}

// best is the highest version in class satisfying every requirement.
func (r *resolver) best(name, class string, reqs []Req) (string, error) {
	all, err := r.ix.Get(r.ctx, name)
	if err != nil {
		return "", err
	}
	var cands []*IndexEntry
	for _, e := range all {
		if e.Yanked || e.version.Compat() != class {
			continue
		}
		ok := true
		for _, q := range reqs {
			if !q.Matches(e.version) {
				ok = false
				break
			}
		}
		if ok {
			cands = append(cands, e)
		}
	}
	if len(cands) == 0 {
		var rs []string
		for _, q := range reqs {
			rs = append(rs, q.String())
		}
		return "", fmt.Errorf("no version of %s satisfies all of: %s", name, strings.Join(uniqS(rs), ", "))
	}
	sort.Slice(cands, func(i, j int) bool { return r.prefer(cands[i], cands[j]) })
	return cands[0].Vers, nil
}

// prefer orders by rust-version compatibility, then version descending.
func (r *resolver) prefer(a, b *IndexEntry) bool {
	ca, cb := r.rvOK(a), r.rvOK(b)
	if ca != cb {
		return ca
	}
	return a.version.Compare(b.version) > 0
}

func (r *resolver) rvOK(e *IndexEntry) bool {
	if e.RustVersion == "" || r.rustV.Major == 0 {
		return true
	}
	rv, err := ParseVersion(normalizeRV(e.RustVersion))
	return err != nil || rv.Compare(r.rustV) <= 0
}

func uniqS(xs []string) []string {
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

// pick chooses the version for one dependency edge given the current class choices.
func (r *resolver) pick(res *Resolution, d IndexDep) (string, error) {
	name := depPkg(d)
	if d.Registry == "local" {
		l := r.locals[name]
		if l == nil {
			return "", fmt.Errorf("unknown local crate %s", name)
		}
		return l.Name + "@" + l.Version, nil
	}
	req, err := ParseReq(d.Req)
	if err != nil {
		return "", err
	}
	cands, err := r.candidates(name, req)
	if err != nil {
		return "", err
	}
	if len(cands) == 0 {
		return "", fmt.Errorf("no published version matches")
	}
	// Prefer a class that already has a chosen version matching req (highest such class).
	classes := r.choice[name]
	var chosen *IndexEntry
	for _, c := range cands {
		if v, ok := classes[c.version.Compat()]; ok {
			if v == c.Vers {
				chosen = c
				break
			}
			continue
		}
	}
	class := ""
	if chosen != nil {
		class = chosen.version.Compat()
	} else {
		class = cands[0].version.Compat()
		// the best candidate's class may already be chosen (non-matching): record and wait
	}
	if r.reqs[name] == nil {
		r.reqs[name] = map[string][]Req{}
	}
	r.reqs[name][class] = append(r.reqs[name][class], req)
	if chosen == nil {
		if v, ok := classes[class]; ok && v != "" {
			return "", nil // class has a non-matching choice; next pass re-chooses it
		}
		chosen = cands[0]
		if classes == nil {
			r.choice[name] = map[string]string{}
		}
		r.choice[name][class] = chosen.Vers
	}
	k := chosen.Name + "@" + chosen.Vers
	if res.Pkgs[k] == nil {
		res.Pkgs[k] = &Pkg{Entry: chosen, Edges: map[int]string{}}
	}
	return k, nil
}
