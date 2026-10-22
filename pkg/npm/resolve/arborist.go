package resolve

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
)

// This file ports @npmcli/arborist's buildIdealTree for a fresh install without a lockfile
// (npm 11.19.0, `npm install --package-lock-only`, hoisted strategy, no --force, no
// --legacy-peer-deps, no overrides): the queue of nodes with problem edges, virtual roots
// for peer sets (#nodeFromEdge, #loadPeerSet) and placement (place-dep.js, can-place-dep.js).
// npm's tree decides which versions are installed and which copy every dependency resolves
// to; bob takes both from it, so bob's name@version set and edges equal npm's.

// errResolve is a resolution failure (ERESOLVE, ETARGET, …): reported as E-RESOLVE.
type errResolve struct{ msg string }

func (e *errResolve) Error() string { return e.msg }

func resolveErr(format string, a ...any) error { return &errResolve{fmt.Sprintf(format, a...)} }

type builder struct {
	ctx         context.Context
	reg         *Registry
	nodeVersion string
	extend      map[string][]Dep // non-peer Extend deps, by package name

	root    *node
	queue   []*node
	sorted  bool
	seen    map[*node]bool
	failed  map[*node]bool
	psrc    map[*node]*node // #peerSetSource
	vroots  map[*node]*node // #virtualRoots
	seq     int
	epoch   int
	indexes map[*node]*edgeIndex

	specs    map[string]specResult
	picks    map[string]*manifest
	sat      map[string]bool // version + "\x00" + range → satisfies
	warnings []string
}

type specResult struct {
	s   spec
	err error
}

type edgeIndex struct {
	epoch int
	in    map[*node][]*edge
	inv   []*node
}

func (b *builder) warn(format string, a ...any) {
	b.warnings = append(b.warnings, fmt.Sprintf(format, a...))
}

func (b *builder) parse(name, raw string) (spec, error) {
	k := name + "\x00" + raw
	if r, ok := b.specs[k]; ok {
		return r.s, r.err
	}
	s, err := parseSpec(name, raw)
	b.specs[k] = specResult{s, err}
	return s, err
}

// depValid is dep-valid.js for the spec types bob supports.
func (b *builder) depValid(child *node, raw string) bool {
	if dir, ok := strings.CutPrefix(raw, "file:"); ok {
		return child.link && child.target != nil && child.target.workspace == dir
	}
	s, err := b.parse(child.name, raw)
	if err != nil {
		return false
	}
	switch s.typ {
	case specRange:
		if s.fetch == "*" {
			return true
		}
		return b.satisfiesRange(child.version(), s.fetch)
	case specVersion:
		return b.satisfiesRange(child.version(), s.fetch)
	case specTag:
		return child.resolved != "" && !child.link
	}
	return false
}

func (b *builder) satisfiesRange(version, rng string) bool {
	k := version + "\x00" + rng
	if r, ok := b.sat[k]; ok {
		return r
	}
	r := Satisfies(version, rng)
	b.sat[k] = r
	return r
}

// prefetchEdges starts fetching the packuments the given edges need.
func (b *builder) prefetchEdges(es []*edge) {
	var names []string
	for _, e := range es {
		if s, err := b.parse(e.name, e.spec); err == nil {
			names = append(names, s.name)
		}
	}
	b.reg.prefetch(b.ctx, names)
}

// newNode creates a node for a package and loads its edges (non-top: no devDependencies).
func (b *builder) newNode(name string, m *manifest) *node {
	b.seq++
	n := &node{name: name, pkg: m, children: map[string]*node{}, seq: b.seq}
	if m != nil {
		n.resolved = m.Dist.Tarball
		n.integrity = m.Dist.Integrity
	}
	n.loadDeps(false, nil)
	return n
}

// setParent is the Node.parent setter: it moves n (with its subtree) under p, clobbering a
// node of the same name there.
func (b *builder) setParent(n, p *node) {
	if p.link && p.target != nil {
		p = p.target
	}
	if n == p || n.parent == p && p.children[lower(n.name)] == n {
		return
	}
	b.unlink(n)
	k := lower(n.name)
	if old := p.children[k]; old != nil && old != n {
		b.detach(old)
	}
	p.children[k] = n
	n.parent = p
	b.epoch++
}

func (b *builder) unlink(n *node) {
	if n.parent != nil && n.parent.children[lower(n.name)] == n {
		delete(n.parent.children, lower(n.name))
	}
	n.parent = nil
	if n.fsParent != nil {
		kids := n.fsParent.fsKids
		for i, c := range kids {
			if c == n {
				n.fsParent.fsKids = append(kids[:i:i], kids[i+1:]...)
				break
			}
		}
		n.fsParent = nil
	}
}

// detach is `node.root = null`: n leaves its tree with its subtree.
func (b *builder) detach(n *node) {
	if n.parent == nil && n.fsParent == nil {
		return
	}
	b.unlink(n)
	b.epoch++
}

func (b *builder) index(r *node) *edgeIndex {
	if ix := b.indexes[r]; ix != nil && ix.epoch == b.epoch {
		return ix
	}
	ix := &edgeIndex{epoch: b.epoch, in: map[*node][]*edge{}}
	var walk func(n *node)
	walk = func(n *node) {
		ix.inv = append(ix.inv, n)
		kids := make([]*node, 0, len(n.children))
		for _, c := range n.children {
			kids = append(kids, c)
		}
		sort.Slice(kids, func(i, j int) bool { return kids[i].seq < kids[j].seq })
		for _, c := range kids {
			walk(c)
		}
		for _, c := range n.fsKids {
			walk(c)
		}
	}
	walk(r)
	for _, n := range ix.inv {
		for _, e := range n.edges {
			if to := e.to(); to != nil {
				ix.in[to] = append(ix.in[to], e)
			}
		}
	}
	b.indexes[r] = ix
	return ix
}

func (b *builder) edgesIn(n *node) []*edge { return b.index(n.root()).in[n] }

func (b *builder) inventory(r *node) []*node { return b.index(r).inv }

func (b *builder) satisfies(n *node, e *edge) bool {
	return strings.EqualFold(n.name, e.name) && e.satisfiedBy(b, n)
}

// matches is Node.matches.
func (b *builder) matches(n, o *node) bool {
	switch {
	case n == o:
		return true
	case !strings.EqualFold(n.name, o.name):
		return false
	case n.link:
		return o.link && n.target != nil && o.target != nil && b.matches(n.target, o.target)
	case n.isProjectRoot() && o.isProjectRoot():
		return n.path() == o.path()
	case n.integrity != "" && o.integrity != "":
		return n.integrity == o.integrity
	case n.resolved != "" && o.resolved != "":
		return n.resolved == o.resolved
	}
	return n.packageName() != "" && n.packageName() == o.packageName() && n.version() != "" && n.version() == o.version()
}

// canReplaceWith is Node.canReplaceWith(r): every dependent of n outside n's own dependency
// set would accept r.
func (b *builder) canReplaceWith(n, r *node, ignorePeers map[string]bool) bool {
	if !strings.EqualFold(n.name, r.name) || n.packageName() != r.packageName() {
		return false
	}
	depSet := b.gatherDepSet([]*node{n}, func(e *edge) bool { return e.to() != n && e.valid(b) })
	for _, e := range b.edgesIn(n) {
		if !n.isTop() && e.from.parent == n.parent && e.peer() && ignorePeers[e.from.name] {
			continue
		}
		if !depSet.has(e.from) && !e.satisfiedBy(b, r) {
			return false
		}
	}
	return true
}

// canReplace is r.canReplace(n) == n.canReplaceWith(r).
func (b *builder) canReplace(r, n *node, ignorePeers map[string]bool) bool {
	return b.canReplaceWith(n, r, ignorePeers)
}

func cmpVersions(a, c string) (int, bool) {
	va, err1 := ParseVersion(a, true)
	vc, err2 := ParseVersion(c, true)
	if err1 != nil || err2 != nil {
		return 0, false
	}
	return va.Compare(vc), true
}

// canDedupe is Node.canDedupe(preferDedupe=false, explicitRequest=false).
func (b *builder) canDedupe(n *node, preferDedupe bool) bool {
	rp := n.resolveParent()
	if rp == nil || rp.resolveParent() == nil {
		return false
	}
	if len(b.edgesIn(n)) == 0 {
		return true
	}
	other := rp.resolveParent().resolve(n.name)
	if other == nil {
		return false
	}
	if b.matches(other, n) {
		return true
	}
	if !b.canReplace(other, n, nil) {
		return false
	}
	c, ok := cmpVersions(other.version(), n.version())
	if preferDedupe || ok && c == 0 {
		return true
	}
	return ok && c > 0
}

// nodeSet is an insertion-ordered set (JS Set semantics for the loops that grow it).
type nodeSet struct {
	list []*node
	in   map[*node]bool
}

func newNodeSet(ns ...*node) *nodeSet {
	s := &nodeSet{in: map[*node]bool{}}
	for _, n := range ns {
		s.add(n)
	}
	return s
}

func (s *nodeSet) add(n *node) {
	if !s.in[n] {
		s.in[n] = true
		s.list = append(s.list, n)
	}
}

func (s *nodeSet) has(n *node) bool { return s.in[n] }

func (s *nodeSet) del(n *node) {
	if s.in[n] {
		delete(s.in, n)
		for i, x := range s.list {
			if x == n {
				s.list = append(s.list[:i:i], s.list[i+1:]...)
				break
			}
		}
	}
}

func (s *nodeSet) size() int { return len(s.list) }

// gatherDepSet is gather-dep-set.js: the dependency closure of set (following edges that
// pass filter), minus every node with a dependent outside it.
func (b *builder) gatherDepSet(set []*node, filter func(*edge) bool) *nodeSet {
	deps := newNodeSet(set...)
	for i := 0; i < len(deps.list); i++ {
		for _, e := range deps.list[i].edges {
			if to := e.to(); to != nil && filter(e) {
				deps.add(to)
			}
		}
	}
	for changed := true; changed && deps.size() > 0; {
		changed = false
		for _, d := range append([]*node(nil), deps.list...) {
			if !deps.has(d) {
				continue
			}
			for _, e := range b.edgesIn(d) {
				if !deps.has(e.from) && filter(e) {
					changed = true
					deps.del(d)
					break
				}
			}
		}
	}
	return deps
}

// peerEntrySets is peer-entry-sets.js: the entry edges into the peer sets n belongs to.
func (b *builder) peerEntrySets(n *node) ([]*edge, map[*edge]*nodeSet) {
	union := newNodeSet(n)
	for i := 0; i < len(union.list); i++ {
		x := union.list[i]
		for _, e := range x.edges {
			if e.peer() && e.valid(b) {
				if to := e.to(); to != nil {
					union.add(to)
				}
			}
		}
		for _, e := range b.edgesIn(x) {
			if e.peer() && e.valid(b) {
				union.add(e.from)
			}
		}
	}
	var keys []*edge
	sets := map[*edge]*nodeSet{}
	for _, p := range union.list {
		for _, e := range b.edgesIn(p) {
			if !e.valid(b) {
				continue
			}
			if !e.peer() || e.from.isTop() {
				sub := newNodeSet(p)
				for i := 0; i < len(sub.list); i++ {
					for _, pe := range sub.list[i].edges {
						if pe.peer() && pe.valid(b) {
							if to := pe.to(); to != nil {
								sub.add(to)
							}
						}
					}
				}
				if sub.has(n) {
					if _, ok := sets[e]; !ok {
						keys = append(keys, e)
					}
					sets[e] = sub
				}
			}
		}
	}
	return keys, sets
}

// deepestNestingTarget is deepest-nesting-target.js.
func deepestNestingTarget(start *node, name string) *node {
	for t := start; t != nil; t = t.resolveParent() {
		if t.isProjectRoot() || t.resolveParent() == nil {
			return t
		}
		te := t.edge(name)
		if te == nil || !te.peer() {
			return t
		}
	}
	return nil
}

// --- the deps queue ---

func (b *builder) push(n *node) {
	for _, q := range b.queue {
		if q == n {
			return
		}
	}
	b.queue = append(b.queue, n)
	b.sorted = false
}

func (b *builder) pop() *node {
	if !b.sorted {
		sort.SliceStable(b.queue, func(i, j int) bool {
			di, dj := b.queue[i].depth(), b.queue[j].depth()
			if di != dj {
				return di < dj
			}
			return localeCompare(b.queue[i].path(), b.queue[j].path()) < 0
		})
		b.sorted = true
	}
	n := b.queue[0]
	b.queue = b.queue[1:]
	return n
}

// virtualRoot is #virtualRoot: a detached node with src's dependencies, where a peer set is
// loaded before it is placed.
func (b *builder) virtualRoot(src *node, reuse bool) *node {
	if reuse {
		if vr := b.vroots[src]; vr != nil {
			return vr
		}
	}
	b.seq++
	vr := &node{name: src.name, pkg: src.pkg, children: map[string]*node{}, sourceRef: src, basePath: src.path(), seq: b.seq}
	vr.loadDeps(src.isTop(), devDeps(src))
	for _, c := range src.sortedChildren() {
		if c.link && c.target != nil {
			b.seq++
			cp := &node{name: c.target.name, pkg: c.target.pkg, children: map[string]*node{}, sourceRef: c.target,
				basePath: c.target.path(), seq: b.seq, fsParent: vr}
			cp.loadDeps(c.target.isTop(), devDeps(c.target))
			vr.fsKids = append(vr.fsKids, cp)
		}
	}
	b.vroots[src] = vr
	b.epoch++
	return vr
}

// devDeps returns the devDependencies of a top node (importers carry them on their manifest).
func devDeps(n *node) *orderedMap {
	if n.pkg == nil {
		return nil
	}
	return n.pkg.devDependencies
}

// problemEdges is #problemEdges for a fresh `npm install`.
func (b *builder) problemEdges(n *node) []*edge {
	bundled := map[string]bool{}
	if !n.isProjectRoot() && !n.isWorkspace() && n.pkg != nil {
		for _, x := range n.pkg.bundled {
			bundled[x] = true
		}
	}
	var out []*edge
	for _, e := range n.edges {
		if bundled[e.name] {
			continue
		}
		to := e.to()
		if to != nil && b.failed[to] {
			continue
		}
		if to == nil {
			if e.typ != edgePeerOptional {
				out = append(out, e)
			}
			continue
		}
		if !e.valid(b) {
			out = append(out, e)
		}
	}
	return out
}

func (b *builder) findHoistableNode(r *node, e *edge) *node {
	queue := r.sortedChildrenBySeq()
	for len(queue) > 0 {
		n := queue[0]
		queue = queue[1:]
		if strings.EqualFold(n.name, e.name) && b.satisfies(n, e) {
			return n
		}
		queue = append(queue, n.sortedChildrenBySeq()...)
	}
	return nil
}

func (n *node) sortedChildrenBySeq() []*node {
	out := make([]*node, 0, len(n.children))
	for _, c := range n.children {
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].seq < out[j].seq })
	return out
}

// build runs #buildDeps until the queue is empty, then the workspace targets (#resolveLinks).
func (b *builder) build() error {
	b.push(b.root)
	for {
		if len(b.queue) == 0 {
			for _, w := range b.root.fsKids {
				if !b.seen[w] {
					b.push(w)
				}
			}
			if len(b.queue) == 0 {
				return nil
			}
		}
		if err := b.ctx.Err(); err != nil {
			return err
		}
		n := b.pop()
		if b.seen[n] || n.root() != b.root {
			continue
		}
		b.seen[n] = true
		if err := b.step(n); err != nil {
			return err
		}
	}
}

type task struct {
	e   *edge
	dep *node
}

func (b *builder) step(n *node) error {
	var tasks []task
	peerSource := n
	if s := b.psrc[n]; s != nil {
		peerSource = s
	}
	problems := b.problemEdges(n)
	b.prefetchEdges(problems)
	for _, e := range problems {
		if e.peerConflicted {
			continue
		}
		source := n
		if e.peer() {
			source = peerSource
		}
		vr := b.virtualRoot(source, true)
		var dep *node
		if vre := vr.edge(e.name); vre != nil && vre.valid(b) {
			if d := vre.to(); d != nil && b.satisfies(d, e) {
				dep = d
			}
		}
		if dep == nil && e.typ == edgePeerOptional {
			start := e.from.resolveParent()
			if start == nil {
				start = e.from
			}
			dep = b.findHoistableNode(start, e)
		}
		if dep == nil {
			var parent *node
			if e.peer() {
				parent = vr
			}
			var err error
			dep, err = b.nodeFromEdge(e, parent, nil, map[*node]bool{e.from: true})
			if err != nil {
				return err
			}
		}
		tasks = append(tasks, task{e, dep})
	}
	sort.SliceStable(tasks, func(i, j int) bool { return localeCompare(tasks[i].e.name, tasks[j].e.name) < 0 })

	var prefetch []string
	for _, t := range tasks {
		pd, err := b.newPlaceDep(t.dep, t.e, nil)
		if err != nil {
			return err
		}
		var visit func(pd *placeDep)
		visit = func(pd *placeDep) {
			if p := pd.placed; p != nil {
				if len(p.errs) > 0 {
					b.failed[p] = true
				}
				cpd := pd.canPlace
				switch cpd.canPlaceSelf {
				case cpOK:
					for _, ei := range b.edgesIn(p) {
						if ei == pd.edge || ei.peerConflicted || ei.valid(b) {
							continue
						}
						delete(b.seen, ei.from) // options.save is true for npm install
						b.push(ei.from)
					}
				case cpReplace:
					for _, ei := range b.edgesIn(p) {
						if ei == pd.edge || ei.peerConflicted || ei.valid(b) {
							continue
						}
						delete(b.seen, ei.from)
						b.push(ei.from)
					}
				}
				b.push(p)
				for _, d := range pd.needEvaluation.list {
					delete(b.seen, d)
					b.push(d)
				}
				for _, e := range b.problemEdges(p) {
					if s, err := b.parse(e.name, e.spec); err == nil {
						prefetch = append(prefetch, s.name)
					}
				}
			}
			for _, c := range pd.children {
				visit(c)
			}
		}
		visit(pd)
	}
	b.reg.prefetch(b.ctx, prefetch)
	return nil
}

// nodeFromEdge is #nodeFromEdge: a node for the edge's spec in a virtual root, with its peer
// set loaded next to it.
func (b *builder) nodeFromEdge(e *edge, parent *node, second *edge, required map[*node]bool) (*node, error) {
	if parent == nil {
		parent = b.virtualRoot(e.from, false)
	}
	first, err := b.nodeFromSpec(e.name, e.spec, parent)
	if err != nil {
		return nil, err
	}
	var sec *node
	if second != nil && !second.valid(b) {
		sec, err = b.nodeFromSpec(e.name, second.spec, parent)
		if err != nil {
			return nil, err
		}
	}
	n := first
	if sec != nil && e.valid(b) {
		n = sec
	}
	b.setParent(n, parent)
	if required[e.from] && e.typ != edgePeerOptional || second != nil && required[second.from] && second.typ != edgePeerOptional {
		required[n] = true
	}
	b.psrc[n] = parent.sourceRef
	return b.loadPeerSet(n, required)
}

// loadPeerSet is #loadPeerSet.
func (b *builder) loadPeerSet(n *node, required map[*node]bool) (*node, error) {
	var peerEdges []*edge
	for _, e := range n.edges {
		if e.peer() && !(e.valid(b) && e.to() != nil) {
			peerEdges = append(peerEdges, e)
		}
	}
	sort.SliceStable(peerEdges, func(i, j int) bool { return localeCompare(peerEdges[i].name, peerEdges[j].name) < 0 })
	b.prefetchEdges(peerEdges)
	for _, e := range peerEdges {
		if n.parent == nil {
			break
		}
		if e.valid(b) && e.to() != nil {
			continue
		}
		parentEdge := n.parent.edge(e.name)
		src := n.parent.sourceRef
		isMine := src != nil && (src.isProjectRoot() || src.isWorkspace())
		conflictOK := !isMine
		if e.to() == nil {
			if parentEdge == nil {
				if _, err := b.nodeFromEdge(e, n.parent, nil, required); err != nil {
					return nil, err
				}
				continue
			}
			dep, err := b.nodeFromEdge(parentEdge, n.parent, e, required)
			if err != nil {
				return nil, err
			}
			if e.valid(b) {
				continue
			}
			if conflictOK || !required[dep] {
				e.peerConflicted = true
				continue
			}
			if n.parent == nil {
				break
			}
			return nil, b.peerConflict(e, parentEdge)
		}
		current := e.to()
		dep, err := b.nodeFromEdge(e, nil, nil, required)
		if err != nil {
			return nil, err
		}
		if b.canReplace(dep, current, nil) {
			if _, err := b.nodeFromEdge(e, n.parent, nil, required); err != nil {
				return nil, err
			}
			continue
		}
		if conflictOK || !required[e.from] {
			continue
		}
		return nil, b.peerConflict(e, parentEdge)
	}
	return n, nil
}

func (b *builder) describe(n *node) string {
	if n == nil {
		return "nothing"
	}
	if n.projRoot {
		return "the root importer"
	}
	if n.workspace != "" {
		return "importer " + n.workspace
	}
	if n.sourceRef != nil {
		return b.describe(n.sourceRef)
	}
	return n.packageName() + "@" + n.version()
}

func (b *builder) peerConflict(e, current *edge) error {
	have := e.to()
	msg := fmt.Sprintf("conflicting peer dependency: %s needs %s@%s", b.describe(e.from), e.name, e.spec)
	if have != nil {
		msg += fmt.Sprintf(", but %s@%s is required", have.packageName(), have.version())
	}
	if current != nil && current.from != nil {
		msg += fmt.Sprintf(" (%s wants %s@%s)", b.describe(current.from), current.name, current.spec)
	}
	return resolveErr("%s", msg)
}

// nodeFromSpec is #nodeFromSpec for registry specs: fetch, pick, validate, create in parent.
func (b *builder) nodeFromSpec(name, raw string, parent *node) (*node, error) {
	s, err := b.parse(name, raw)
	if err != nil {
		return nil, resolveErr("%s", err.Error())
	}
	m, perr := b.pick(s)
	if perr != nil {
		var re *errResolve
		var fe *fatalError
		if errors.As(perr, &fe) || b.ctx.Err() != nil {
			return nil, perr
		}
		if !errors.As(perr, &re) {
			perr = resolveErr("%s", perr.Error())
		}
		n := b.newNode(name, &manifest{Name: s.name})
		n.resolved, n.integrity = "", ""
		n.errs = []error{perr}
		b.failed[n] = true
		b.setParent(n, parent)
		return n, nil
	}
	n := b.newNode(name, m)
	b.setParent(n, parent)
	return n, nil
}

// fatalError is hostile registry input: it aborts resolution even for optional dependencies.
type fatalError struct{ err error }

func (e *fatalError) Error() string { return e.err.Error() }
func (e *fatalError) Unwrap() error { return e.err }

// pick is pacote.manifest + npm-pick-manifest for a registry spec.
func (b *builder) pick(s spec) (*manifest, error) {
	key := s.name + "\x00" + s.fetch + fmt.Sprint(s.typ)
	if m, ok := b.picks[key]; ok {
		return m, nil
	}
	p, err := b.reg.packument(b.ctx, s.name)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil, resolveErr("%s is not in the registry", s.name)
		}
		return nil, &fatalError{err}
	}
	m := b.pickManifest(p, s)
	if m == nil {
		return nil, resolveErr("no version of %s matches %q", s.name, s.fetch)
	}
	m.normalize()
	if err := validateManifest(m); err != nil {
		return nil, &fatalError{fmt.Errorf("%s@%s: %w", m.Name, m.Version, err)}
	}
	b.applyExtend(m)
	if m.HasShrinkwrap {
		b.warn("%s@%s ships npm-shrinkwrap.json; bob resolves its dependencies from the registry instead", m.Name, m.Version)
	}
	b.picks[key] = m
	return m, nil
}

func (b *builder) engineOK(m *manifest) bool {
	m.normalize()
	if m.nodeEngine == "" || b.nodeVersion == "" {
		return true
	}
	return satisfiesEngine(b.nodeVersion, m.nodeEngine)
}

// pickManifest is npm-pick-manifest with defaultTag "latest" and no before/avoid/staged.
func (b *builder) pickManifest(p *packument, s spec) *manifest {
	byRaw := map[string]*manifest{}
	for _, v := range p.versions {
		byRaw[v.raw] = v.m
	}
	switch s.typ {
	case specTag:
		return byRaw[p.DistTags[s.fetch]]
	case specVersion:
		v, err := ParseVersion(s.fetch, true)
		if err != nil {
			return nil
		}
		return byRaw[v.String()]
	}
	if latest := p.DistTags["latest"]; latest != "" && (s.fetch == "*" || b.satisfiesRange(latest, s.fetch)) {
		if m := byRaw[latest]; m != nil {
			m.normalize()
			if b.engineOK(m) && !m.deprecated {
				return m
			}
		}
	}
	r, err := ParseRange(s.fetch)
	if err != nil {
		return nil
	}
	var cands []pv
	for _, v := range p.versions {
		if r.Test(v.v) {
			cands = append(cands, v)
		}
	}
	if len(cands) == 0 {
		return nil
	}
	sort.SliceStable(cands, func(i, j int) bool {
		a, c := cands[i], cands[j]
		a.m.normalize()
		c.m.normalize()
		ae, ce := b.engineOK(a.m), b.engineOK(c.m)
		ag, cg := !a.m.deprecated && ae, !c.m.deprecated && ce
		if ag != cg {
			return ag
		}
		if ae != ce {
			return ae
		}
		if a.m.deprecated != c.m.deprecated {
			return !a.m.deprecated
		}
		return a.v.Compare(c.v) > 0
	})
	return cands[0].m
}
