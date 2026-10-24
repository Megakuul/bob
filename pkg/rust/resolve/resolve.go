package resolve

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
)

// Member is a workspace crate (a local crate of the repository). Its dependencies on other
// members are Dep{Local: true, Package: <member name>}.
type Member struct {
	Name     string
	Version  string // "0.0.0" for bob's generated manifests
	Deps     []Dep  // in Cargo.toml order (see the package documentation)
	Features map[string][]string
	// ProcMacro marks a proc-macro crate (host unit); only Unify reads it.
	ProcMacro bool
}

// Options tune the resolution.
type Options struct {
	// RustVersion is the toolchain version ("1.95.0"). When set, versions whose rust_version
	// is newer are tried only after all others (Cargo resolver 3, incompatible-rust-versions =
	// "fallback"). Empty means resolver 2: rust_version is ignored.
	RustVersion string
	// MaxSteps bounds the work (activations) of one resolution; 0 means 200000.
	MaxSteps int
	// Locked are previously locked crates ("name@version"; bob lock with an existing bob.lock).
	// A locked version that satisfies a requirement is tried before every other candidate,
	// even when yanked, so a re-resolution moves only what it must (Cargo's preference for the
	// versions in Cargo.lock).
	Locked []string
}

// ErrUnresolvable wraps every "no version satisfies" outcome.
var ErrUnresolvable = errors.New("cannot resolve crates")

// Edge is a resolved dependency: Entry.Deps[Dep] of the parent resolved to To.
type Edge struct {
	Dep int
	To  *Entry
}

// Resolution is the crate graph of a workspace: what bob.lock records.
type Resolution struct {
	Members []*Entry
	Crates  []*Entry // registry crates sorted by name, then version
	edges   map[*Entry][]Edge
}

// Edges returns the resolved dependencies of e, ordered by dependency index then crate key.
func (r *Resolution) Edges(e *Entry) []Edge { return r.edges[e] }

// Resolve resolves the members against reg exactly like `cargo generate-lockfile`: all
// features of every member, dev-dependencies of members, every platform, one version per
// semver-compatibility class, at most one crate per `links` value, highest non-yanked version
// first, with Cargo's dependency order and backtracking.
func Resolve(ctx context.Context, reg Registry, members []Member, o Options) (*Resolution, error) {
	r := &resolver{ctx: ctx, reg: reg, opts: o, versions: map[string][]*Entry{}, query: map[string][]*Entry{},
		members: map[string]*Entry{}, act: map[string]*activation{}, links: map[string]*Entry{}, feats: map[*Entry]map[string]bool{},
		locked: map[string]bool{}}
	for _, l := range o.Locked {
		r.locked[l] = true
	}
	if o.RustVersion != "" {
		v, err := parsePartial(o.RustVersion)
		if err != nil {
			return nil, fmt.Errorf("toolchain version %q: %w", o.RustVersion, err)
		}
		r.rustVersion = &v
	}
	if r.opts.MaxSteps <= 0 {
		r.opts.MaxSteps = 200000
	}
	var roots []*Entry
	for _, m := range members {
		e, err := memberEntry(m)
		if err != nil {
			return nil, err
		}
		if r.members[e.Name] != nil {
			return nil, fmt.Errorf("two workspace crates are named %s", e.Name)
		}
		r.members[e.Name] = e
		roots = append(roots, e)
	}
	if err := r.run(roots); err != nil {
		return nil, err
	}
	return r.result(roots), nil
}

func memberEntry(m Member) (*Entry, error) {
	if !ValidName(m.Name) {
		return nil, fmt.Errorf("invalid crate name %q", m.Name)
	}
	vs := m.Version
	if vs == "" {
		vs = "0.0.0"
	}
	v, err := ParseVersion(vs)
	if err != nil {
		return nil, fmt.Errorf("crate %s: %w", m.Name, err)
	}
	for _, d := range m.Deps {
		if !ValidName(d.Name) || !ValidName(d.Package) {
			return nil, fmt.Errorf("crate %s: invalid dependency name %q", m.Name, d.Name)
		}
	}
	table := m.Features
	if table == nil {
		table = map[string][]string{}
	}
	return &Entry{Name: m.Name, Version: v, Vers: vs, Deps: m.Deps, Table: table, Features: FeatureMap(table, m.Deps), Local: true}, nil
}

// activation is a crate version in the graph and the age at which it entered.
type activation struct {
	e   *Entry
	age int
}

// edgeList is a persistent list of recorded edges (cheap to keep in backtrack frames).
type edgeList struct {
	from *Entry
	dep  int
	to   *Entry
	next *edgeList
}

// undo is one trail entry: how to revert a change of the resolver state.
type undo struct {
	actKey   string
	linksKey string
	featsOf  *Entry
	oldFeats map[string]bool
}

// depInfo is one dependency to resolve: its candidates (best first) and the features asked.
type depInfo struct {
	dep        int
	cands      []*Entry
	features   []string
	useDefault bool
}

// frame is a crate's list of dependencies still to resolve (Cargo's DepsFrame).
type frame struct {
	parent *Entry
	deps   []depInfo
	next   int
	time   int
}

func (f *frame) minCandidates() int {
	if f.next >= len(f.deps) {
		return 0
	}
	return len(f.deps[f.next].cands)
}

// candIter walks the candidates of one dependency, skipping those that clash with the graph
// and peeking one ahead to know whether an alternative remains (Cargo's RemainingCandidates).
type candIter struct {
	cands   []*Entry
	pos     int
	pending *Entry
}

type backtrackFrame struct {
	mark      int
	age       int
	edges     *edgeList
	remaining []frame
	cands     candIter
	parent    *Entry
	dep       depInfo
	conflicts map[*Entry]bool
}

type resolver struct {
	ctx         context.Context
	reg         Registry
	opts        Options
	rustVersion *Version
	locked      map[string]bool // Options.Locked

	versions map[string][]*Entry // crate -> all versions (one fetch per crate)
	query    map[string][]*Entry // dependency -> candidates, best first
	missing  map[string]error
	members  map[string]*Entry

	// the state backtracking restores
	act   map[string]*activation
	links map[string]*Entry
	feats map[*Entry]map[string]bool
	edges *edgeList
	age   int
	trail []undo
	time  int
	steps int
}

func actKey(e *Entry) string {
	src := "r"
	if e.Local {
		src = "l"
	}
	return e.Name + "\x00" + src + "\x00" + e.Version.Compat()
}

func (r *resolver) undoTo(mark int) {
	for len(r.trail) > mark {
		u := r.trail[len(r.trail)-1]
		r.trail = r.trail[:len(r.trail)-1]
		switch {
		case u.actKey != "":
			delete(r.act, u.actKey)
		case u.linksKey != "":
			delete(r.links, u.linksKey)
		case u.featsOf != nil:
			if u.oldFeats == nil {
				delete(r.feats, u.featsOf)
			} else {
				r.feats[u.featsOf] = u.oldFeats
			}
		}
	}
}

// next returns the next candidate that fits the graph and whether another one follows.
func (c *candIter) next(r *resolver, conflicts map[*Entry]bool) (*Entry, bool) {
	for c.pos < len(c.cands) {
		b := c.cands[c.pos]
		c.pos++
		if a := r.act[actKey(b)]; a != nil && a.e != b {
			conflicts[a.e] = true
			continue
		}
		if b.Links != "" {
			if a := r.links[b.Links]; a != nil && a != b {
				conflicts[a] = true
				continue
			}
		}
		prev := c.pending
		c.pending = b
		if prev != nil {
			return prev, true
		}
	}
	prev := c.pending
	c.pending = nil
	return prev, false
}

// conflictError is an activation that failed for a reason tied to an active crate.
type conflictError struct {
	cause *Entry
	msg   string
}

func (e *conflictError) Error() string { return e.msg }

func (r *resolver) run(roots []*Entry) error {
	var remaining []frame
	for _, m := range roots {
		f, err := r.activate(nil, depInfo{}, m, true)
		if err != nil {
			return err
		}
		if f != nil {
			remaining = append(remaining, *f)
		}
	}
	var stack []backtrackFrame
	var lastErr error
	for {
		if err := r.ctx.Err(); err != nil {
			return err
		}
		best := -1
		for i := range remaining {
			if best < 0 || remaining[i].minCandidates() < remaining[best].minCandidates() ||
				remaining[i].minCandidates() == remaining[best].minCandidates() && remaining[i].time < remaining[best].time {
				best = i
			}
		}
		if best < 0 {
			return nil
		}
		if remaining[best].next >= len(remaining[best].deps) {
			// copy: backtrack frames share the old slice
			remaining = append(append([]frame(nil), remaining[:best]...), remaining[best+1:]...)
			continue
		}
		parent, di := remaining[best].parent, remaining[best].deps[remaining[best].next]
		remaining = append([]frame(nil), remaining...) // frames are shared with backtrack frames
		remaining[best].next++

		it := candIter{cands: di.cands}
		conflicts := map[*Entry]bool{}
		backtracked := false
		for {
			cand, hasAnother := it.next(r, conflicts)
			if cand == nil {
				maxAge := -1
				if !backtracked {
					maxAge = r.conflictAge(parent, conflicts)
				}
				var bf *backtrackFrame
				for len(stack) > 0 {
					top := stack[len(stack)-1]
					stack = stack[:len(stack)-1]
					r.undoTo(top.mark)
					r.age, r.edges = top.age, top.edges
					c, h := top.cands.next(r, top.conflicts)
					if c == nil || maxAge >= 0 && top.age >= maxAge {
						continue
					}
					cand, hasAnother, bf = c, h, &top
					break
				}
				if bf == nil {
					return r.failure(parent, di, lastErr)
				}
				remaining, it, parent, di, conflicts = bf.remaining, bf.cands, bf.parent, bf.dep, bf.conflicts
				backtracked = true
			}
			mark, age, edges := len(r.trail), r.age, r.edges
			var bt *backtrackFrame
			if hasAnother {
				cc := make(map[*Entry]bool, len(conflicts))
				for k := range conflicts {
					cc[k] = true
				}
				bt = &backtrackFrame{mark: mark, age: age, edges: edges, remaining: remaining, cands: it,
					parent: parent, dep: di, conflicts: cc}
			}
			f, err := r.activate(parent, di, cand, false)
			var ce *conflictError
			if errors.As(err, &ce) {
				conflicts[ce.cause] = true
				lastErr = err
				r.undoTo(mark)
				r.age, r.edges = age, edges
				continue
			}
			if err != nil {
				return err
			}
			if bt != nil {
				stack = append(stack, *bt)
			}
			if f != nil {
				remaining = append(append([]frame(nil), remaining...), *f)
			}
			break
		}
	}
}

// conflictAge is the newest activation among parent and the conflicts: backtracking to a
// decision taken after it cannot remove the conflict (Cargo's is_conflicting).
func (r *resolver) conflictAge(parent *Entry, conflicts map[*Entry]bool) int {
	m := 0
	for _, e := range append([]*Entry{parent}, keysOf(conflicts)...) {
		a := r.act[actKey(e)]
		if a == nil || a.e != e {
			return -1
		}
		m = max(m, a.age)
	}
	return m
}

func keysOf(m map[*Entry]bool) []*Entry {
	out := make([]*Entry, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func (r *resolver) failure(parent *Entry, di depInfo, last error) error {
	d := parent.Deps[di.dep]
	if d.Local {
		return fmt.Errorf("%w: %s depends on workspace crate %s: %v", ErrUnresolvable, parent.Key(), d.Package, last)
	}
	if err := r.missing[d.Package]; err != nil {
		return fmt.Errorf("%w: %s depends on %s %s: %w", ErrUnresolvable, parent.Key(), d.Package, d.Req, err)
	}
	if len(di.cands) == 0 {
		return fmt.Errorf("%w: %s depends on %s %s: no published version matches", ErrUnresolvable, parent.Key(), d.Package, d.Req)
	}
	if last != nil {
		return fmt.Errorf("%w: %s depends on %s %s: %v", ErrUnresolvable, parent.Key(), d.Package, d.Req, last)
	}
	return fmt.Errorf("%w: %s depends on %s %s: every matching version clashes with another version already in the graph (one per semver-compatible range and native library)", ErrUnresolvable, parent.Key(), d.Package, d.Req)
}

// activate adds cand (requested through parent's dependency di) to the graph and returns the
// frame of its dependencies that this request newly enables (nil when nothing is new).
func (r *resolver) activate(parent *Entry, di depInfo, cand *Entry, root bool) (*frame, error) {
	r.steps++
	if r.steps > r.opts.MaxSteps {
		return nil, fmt.Errorf("%w: crate resolution gave up after %d steps", ErrUnresolvable, r.opts.MaxSteps)
	}
	r.age++
	if parent != nil {
		r.edges = &edgeList{from: parent, dep: di.dep, to: cand, next: r.edges}
	}
	key := actKey(cand)
	if r.act[key] == nil {
		r.act[key] = &activation{e: cand, age: r.age}
		r.trail = append(r.trail, undo{actKey: key})
		if cand.Links != "" {
			r.links[cand.Links] = cand
			r.trail = append(r.trail, undo{linksKey: cand.Links})
		}
	} else if !root {
		prev := r.feats[cand]
		_, hasDefault := cand.Features["default"]
		sub := true
		for _, f := range di.features {
			if !prev[f] {
				sub = false
			}
		}
		if sub && (!di.useDefault || prev["default"] || !hasDefault) {
			return nil, nil
		}
	}
	on, deps, missing := lockDeps(cand, di.features, root, root || di.useDefault, root)
	if missing != "" {
		msg := fmt.Sprintf("%s has no feature %q", cand.Key(), missing)
		if parent == nil {
			return nil, fmt.Errorf("%w: %s", ErrUnresolvable, msg)
		}
		return nil, &conflictError{cause: parent, msg: msg}
	}
	if len(on) > 0 {
		old := r.feats[cand]
		merged := make(map[string]bool, len(old)+len(on))
		for f := range old {
			merged[f] = true
		}
		for f := range on {
			merged[f] = true
		}
		r.feats[cand] = merged
		r.trail = append(r.trail, undo{featsOf: cand, oldFeats: old})
	}
	infos, err := r.depInfos(cand, deps)
	if err != nil {
		return nil, err
	}
	r.time++
	return &frame{parent: cand, deps: infos, time: r.time}, nil
}

// depInfos looks up the candidates of each active dependency, fewest candidates first.
func (r *resolver) depInfos(e *Entry, deps []activeDep) ([]depInfo, error) {
	if p, ok := r.reg.(Prefetcher); ok {
		var names []string
		for _, ad := range deps {
			d := e.Deps[ad.index]
			if _, have := r.versions[d.Package]; !have && !d.Local {
				names = append(names, d.Package)
			}
		}
		if len(names) > 1 {
			p.Prefetch(r.ctx, names)
		}
	}
	out := make([]depInfo, 0, len(deps))
	for _, ad := range deps {
		d := e.Deps[ad.index]
		cands, err := r.candidates(d)
		if err != nil {
			return nil, fmt.Errorf("%s depends on %s: %w", e.Key(), d.Package, err)
		}
		out = append(out, depInfo{dep: ad.index, cands: cands, features: ad.features, useDefault: d.Default})
	}
	sort.SliceStable(out, func(i, j int) bool { return len(out[i].cands) < len(out[j].cands) })
	return out, nil
}

func (r *resolver) candidates(d Dep) ([]*Entry, error) {
	if d.Local {
		if m := r.members[d.Package]; m != nil {
			return []*Entry{m}, nil
		}
		return nil, nil
	}
	qk := d.Package + "\x00" + d.Req.String()
	if c, ok := r.query[qk]; ok {
		return c, nil
	}
	all, ok := r.versions[d.Package]
	if !ok {
		var err error
		all, err = r.reg.Versions(r.ctx, d.Package)
		if errors.Is(err, ErrNoCrate) {
			if r.missing == nil {
				r.missing = map[string]error{}
			}
			r.missing[d.Package] = err
			all, err = nil, nil
		}
		if err != nil {
			return nil, err
		}
		r.versions[d.Package] = all
	}
	var out []*Entry
	for _, e := range all {
		if (!e.Yanked || r.isLocked(e)) && e.Name == d.Package && d.Req.Matches(e.Version) {
			out = append(out, e)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if li, lj := r.isLocked(out[i]), r.isLocked(out[j]); li != lj {
			return li
		}
		if r.rustVersion != nil {
			ci, cj := r.rustCompatible(out[i]), r.rustCompatible(out[j])
			if ci != cj {
				return ci
			}
		}
		return out[i].Version.Compare(out[j].Version) > 0
	})
	r.query[qk] = out
	return out, nil
}

// isLocked reports whether e is one of Options.Locked.
func (r *resolver) isLocked(e *Entry) bool {
	return len(r.locked) > 0 && r.locked[e.Name+"@"+e.Vers]
}

// rustCompatible reports whether e's rust_version is at most the toolchain's.
func (r *resolver) rustCompatible(e *Entry) bool {
	if e.RustVersion == "" {
		return true
	}
	v, err := parsePartial(e.RustVersion)
	if err != nil {
		return true
	}
	return v.Compare(*r.rustVersion) <= 0
}

// parsePartial parses "1", "1.70" or "1.70.0" (rust-version syntax), missing parts zero.
func parsePartial(s string) (Version, error) {
	s = strings.TrimSpace(s)
	switch strings.Count(s, ".") {
	case 0:
		s += ".0.0"
	case 1:
		s += ".0"
	}
	return ParseVersion(s)
}

func (r *resolver) result(roots []*Entry) *Resolution {
	res := &Resolution{Members: roots, edges: map[*Entry][]Edge{}}
	for _, a := range r.act {
		if !a.e.Local {
			res.Crates = append(res.Crates, a.e)
		}
	}
	sort.Slice(res.Crates, func(i, j int) bool { return entryLess(res.Crates[i], res.Crates[j]) })
	seen := map[edgeList]bool{}
	for l := r.edges; l != nil; l = l.next {
		k := edgeList{from: l.from, dep: l.dep, to: l.to}
		if seen[k] {
			continue
		}
		seen[k] = true
		res.edges[l.from] = append(res.edges[l.from], Edge{Dep: l.dep, To: l.to})
	}
	for _, es := range res.edges {
		sort.Slice(es, func(i, j int) bool {
			if es[i].Dep != es[j].Dep {
				return es[i].Dep < es[j].Dep
			}
			return entryLess(es[i].To, es[j].To)
		})
	}
	return res
}

func entryLess(a, b *Entry) bool {
	if a.Name != b.Name {
		return a.Name < b.Name
	}
	if c := a.Version.Compare(b.Version); c != 0 {
		return c < 0
	}
	return a.Local && !b.Local
}
