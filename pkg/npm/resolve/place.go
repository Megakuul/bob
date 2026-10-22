package resolve

import (
	"sort"
)

// Port of place-dep.js and can-place-dep.js (@npmcli/arborist, npm 11.19.0).

type cpResult int

const (
	cpNone cpResult = iota
	cpConflict
	cpOK
	cpReplace
	cpKeep
)

type canPlaceDep struct {
	b            *builder
	canPlace     cpResult
	canPlaceSelf cpResult
	dep, target  *node
	edge         *edge
	parent       *canPlaceDep
	children     []*canPlaceDep
	peerPath     []*node
	preferDedupe bool
	current      *node
	targetEdge   *edge
	edgeOverride bool
	peersResult  cpResult
}

func (b *builder) newCanPlaceDep(dep, target *node, e *edge, parent *canPlaceDep, peerPath []*node, preferDedupe bool) *canPlaceDep {
	c := &canPlaceDep{b: b, dep: dep, target: target, edge: e, parent: parent, peerPath: peerPath,
		preferDedupe: preferDedupe || e.peer()}
	c.current = target.children[lower(e.name)]
	c.targetEdge = target.edge(e.name)
	c.edgeOverride = !b.satisfies(dep, e)
	c.canPlace = c.check()
	if c.canPlaceSelf == cpNone {
		c.canPlaceSelf = c.canPlace
	}
	return c
}

func (c *canPlaceDep) top() *canPlaceDep {
	if c.parent != nil {
		return c.parent.top()
	}
	return c
}

func (c *canPlaceDep) peerEntryEdge() *edge { return c.top().edge }

func (c *canPlaceDep) deepestNestingTarget() *node {
	start := c.edge.from
	if c.parent != nil {
		start = c.parent.deepestNestingTarget()
	}
	return deepestNestingTarget(start, c.edge.name)
}

func (c *canPlaceDep) check() cpResult {
	b := c.b
	if len(c.dep.errs) > 0 {
		if c.current != nil {
			return cpReplace
		}
		return cpOK
	}
	if c.targetEdge != nil && c.targetEdge.peer() && !c.target.isTop() {
		return cpConflict
	}
	if c.current == nil && c.targetEdge != nil && !b.satisfies(c.dep, c.targetEdge) && c.targetEdge != c.edge {
		return cpConflict
	}
	if c.current != nil {
		return c.checkCurrent()
	}
	return c.checkNoCurrent()
}

func (c *canPlaceDep) checkCurrent() cpResult {
	b, cur, dep, e, target := c.b, c.current, c.dep, c.edge, c.target
	if b.matches(dep, cur) {
		if b.satisfies(cur, e) || c.edgeOverride {
			return cpKeep
		}
	}
	tryReplace := false
	if cur.version() != "" && dep.version() != "" {
		if r, ok := cmpVersions(dep.version(), cur.version()); ok && r >= 0 {
			tryReplace = true
		}
	}
	if tryReplace && b.canReplace(dep, cur, nil) {
		if cpp := c.canPlacePeers(cpReplace); cpp != cpConflict {
			return cpp
		}
	}
	if b.satisfies(cur, e) {
		return cpKeep
	}
	if c.preferDedupe && !tryReplace && b.canReplace(dep, cur, nil) {
		if cpp := c.canPlacePeers(cpReplace); cpp != cpConflict {
			return cpp
		}
	}
	if target != c.deepestNestingTarget() {
		return cpConflict
	}
	if !e.peer() && target == e.from {
		return c.canPlacePeers(cpReplace)
	}
	if c.parent == nil && !e.peer() {
		return cpConflict
	}
	canReplace := true
	keys, sets := b.peerEntrySets(cur)
	vr := dep.parent
	for _, entry := range keys {
		currentPeers := sets[entry]
		if entry == c.edge || entry == c.peerEntryEdge() {
			continue
		}
		entryNode := entry.to()
		var entryRep *node
		if vr != nil && entryNode != nil {
			entryRep = vr.children[lower(entryNode.name)]
		}
		if entryRep != nil {
			ignore := map[string]bool{}
			for _, ch := range vr.children {
				ignore[ch.name] = true
			}
			if b.canReplace(entryRep, entryNode, ignore) {
				continue
			}
		}
		canClobber := entryRep == nil
		if entryRep == nil && entryNode != nil {
			walk := newNodeSet(entryNode)
		outer:
			for i := 0; i < len(walk.list); i++ {
				for _, pe := range walk.list[i].edges {
					if !pe.peer() || !pe.valid(b) {
						continue
					}
					var rep *node
					if vr != nil {
						rep = vr.children[lower(pe.name)]
					}
					if rep == nil {
						if to := pe.to(); to != nil {
							walk.add(to)
						}
						continue
					}
					if !b.satisfies(rep, pe) {
						canClobber = false
						break outer
					}
				}
			}
		}
		if canClobber {
			continue
		}
		for _, cp := range currentPeers.list {
			curDeep := deepestNestingTarget(entry.from, cp.name)
			if curDeep == target || target.isDescendantOf(curDeep) {
				canReplace = false
				break
			}
		}
	}
	if canReplace {
		return c.canPlacePeers(cpReplace)
	}
	return cpConflict
}

func (c *canPlaceDep) checkNoCurrent() cpResult {
	b := c.b
	if c.target != c.peerEntryEdge().from {
		if cur := c.target.resolve(c.edge.name); cur != nil {
			for _, e := range b.edgesIn(cur) {
				if e.from.isDescendantOf(c.target) && e.valid(b) && !b.satisfies(c.dep, e) {
					return cpConflict
				}
			}
		}
	}
	return c.canPlacePeers(cpOK)
}

func (c *canPlaceDep) canPlacePeers(state cpResult) cpResult {
	c.canPlaceSelf = state
	if c.peersResult != cpNone {
		return c.peersResult
	}
	peerPath := append(append([]*node(nil), c.peerPath...), c.dep)
	saw := false
	for _, pe := range c.dep.edges {
		if !pe.peer() {
			continue
		}
		peer := pe.to()
		if peer == nil || containsNode(peerPath, peer) {
			continue
		}
		target := deepestNestingTarget(c.target, peer.name)
		cpp := c.b.newCanPlaceDep(peer, target, pe, c, peerPath, true)
		c.children = append(c.children, cpp)
		if cpp.canPlace == cpConflict {
			saw = true
		}
	}
	if saw {
		c.peersResult = cpConflict
	} else {
		c.peersResult = state
	}
	return c.peersResult
}

func containsNode(l []*node, n *node) bool {
	for _, x := range l {
		if x == n {
			return true
		}
	}
	return false
}

type placeDep struct {
	b              *builder
	dep            *node
	edge           *edge
	parent         *placeDep
	canPlace       *canPlaceDep
	canPlaceSelf   *canPlaceDep
	children       []*placeDep
	needEvaluation *nodeSet
	placed         *node
	oldDep         *node
}

func (pd *placeDep) top() *placeDep {
	if pd.parent != nil {
		return pd.parent.top()
	}
	return pd
}

func (pd *placeDep) startNode() *node {
	from := pd.edge.from
	if pd.parent != nil {
		if s := pd.parent.startNode(); s != nil {
			from = s
		}
	}
	return deepestNestingTarget(from, pd.edge.name)
}

func (b *builder) newPlaceDep(dep *node, e *edge, parent *placeDep) (*placeDep, error) {
	pd := &placeDep{b: b, dep: dep, edge: e, parent: parent, needEvaluation: newNodeSet()}
	if e.to() != nil && e.valid(b) {
		return pd, nil
	}
	for target := pd.startNode(); target != nil; target = target.resolveParent() {
		if te := target.edge(e.name); !target.isTop() && te != nil && te.peer() {
			continue
		}
		var pcp *canPlaceDep
		if parent != nil {
			pcp = parent.canPlace
		}
		cpd := b.newCanPlaceDep(dep, target, e, pcp, nil, false)
		if cpd.canPlaceSelf != cpConflict {
			pd.canPlaceSelf = cpd
		}
		if cpd.canPlace != cpConflict {
			pd.canPlace = cpd
		} else {
			break
		}
		if len(dep.errs) > 0 {
			break
		}
	}
	if pd.canPlace == nil {
		if pd.isMine() {
			return nil, pd.conflict()
		}
		if pd.canPlaceSelf == nil {
			pd.warnPeerConflict(nil)
			return pd, nil
		}
		pd.canPlace = pd.canPlaceSelf
	}
	target := pd.canPlace.target
	placement := pd.canPlace.canPlace
	if placement == cpConflict {
		placement = pd.canPlace.canPlaceSelf
	}
	if placement == cpKeep {
		if e.peer() && !e.valid(b) {
			pd.warnPeerConflict(nil)
		}
		pd.pruneDedupable(target, true)
		return pd, nil
	}
	for p := target; p != nil; p = p.resolveParent() {
		if b.matches(p, dep) && !p.isTop() {
			b.seq++
			l := &node{name: dep.name, link: true, target: p, children: map[string]*node{}, edgeIdx: map[string]*edge{}, seq: b.seq}
			b.setParent(l, target)
			pd.placed = l
			return pd, nil
		}
	}
	vr := dep.parent
	placed := b.newNode(dep.name, dep.pkg)
	placed.resolved, placed.integrity = dep.resolved, dep.integrity
	placed.errs = dep.errs
	pd.placed = placed
	if old := target.children[lower(pd.edge.name)]; old != nil {
		pd.oldDep = old
		pd.replaceOldDep()
	} else {
		b.setParent(placed, target)
	}
	if e.peer() && !b.satisfies(placed, e) {
		pd.warnPeerConflict(nil)
	}
	if to := e.to(); to != nil && e.valid(b) && to != placed {
		pd.pruneDedupable(to, false)
	}
	var same []*node
	for _, n := range b.inventory(target.root()) {
		if lower(n.name) == lower(pd.edge.name) && n.isDescendantOf(target) && !n.isTop() {
			same = append(same, n)
		}
	}
	for _, n := range same {
		pd.pruneDedupable(n, false)
		if n.root() == target.root() {
			for _, kid := range n.sortedChildrenBySeq() {
				pd.pruneDedupable(kid, false)
			}
		}
	}
	for _, pe := range placed.edges {
		if pe.valid(b) || !pe.peer() || pe.peerConflicted {
			continue
		}
		if vr == nil {
			continue
		}
		peer := vr.children[lower(pe.name)]
		if peer == nil || !b.satisfies(peer, pe) {
			continue
		}
		child, err := b.newPlaceDep(peer, pe, pd)
		if err != nil {
			return nil, err
		}
		pd.children = append(pd.children, child)
	}
	return pd, nil
}

func (pd *placeDep) replaceOldDep() {
	b, old, placed := pd.b, pd.oldDep, pd.placed
	target := old.parent
	var oldDeps []*node
	for _, e := range old.edges {
		to := e.to()
		if placed.edge(e.name) == nil && to != nil {
			oldDeps = append(oldDeps, b.gatherDepSet([]*node{to}, func(x *edge) bool { return x.to() != to }).list...)
		}
	}
	var prune []*node
	for _, e := range b.edgesIn(old) {
		if b.satisfies(placed, e) || !e.peer() || e.from.parent != target || e.peerConflicted {
			continue
		}
		keys, _ := b.peerEntrySets(e.from)
		for _, entry := range keys {
			entryNode := entry.to()
			if entryNode == nil {
				continue
			}
			deepest := entryNode // deepestNestingTarget(entryNode) without a name stops at once
			if deepest != target && !(entry.from.isProjectRoot() || entry.from.isWorkspace()) {
				prune = append(prune, b.gatherDepSet([]*node{entryNode}, func(x *edge) bool {
					return x.to() != entryNode && !x.peerConflicted
				}).list...)
			} else {
				pd.warnPeerConflict(e)
			}
		}
	}
	// placed.replace(old): take its place and its children
	for _, kid := range old.sortedChildrenBySeq() {
		b.setParent(kid, placed)
	}
	b.setParent(placed, target)
	pd.pruneForReplacement(placed, oldDeps)
	for _, d := range prune {
		for _, e := range b.edgesIn(d) {
			pd.needEvaluation.add(e.from)
		}
		b.detach(d)
	}
}

func (pd *placeDep) pruneForReplacement(n *node, oldDeps []*node) {
	b := pd.b
	invalid := newNodeSet()
	for _, e := range n.edges {
		if to := e.to(); to != nil && !e.valid(b) {
			invalid.add(to)
		}
	}
	for _, d := range oldDeps {
		for _, x := range b.gatherDepSet([]*node{d}, func(e *edge) bool { return e.to() != d && e.valid(b) }).list {
			invalid.add(x)
		}
	}
	deps := b.gatherDepSet(invalid.list, func(e *edge) bool { return e.from != n && e.to() != n && e.valid(b) })
	for _, d := range deps.list {
		b.detach(d)
	}
}

func (pd *placeDep) pruneDedupable(n *node, descend bool) {
	b := pd.b
	if b.canDedupe(n, false) {
		deps := b.gatherDepSet([]*node{n}, func(e *edge) bool { return e.to() != n && e.valid(b) })
		for _, d := range deps.list {
			b.detach(d)
		}
		return
	}
	if !descend {
		return
	}
	for _, c := range n.sortedChildren() {
		pd.pruneDedupable(c, true)
	}
	fs := append([]*node(nil), n.fsKids...)
	sort.SliceStable(fs, func(i, j int) bool { return localeCompare(fs[i].location(), fs[j].location()) < 0 })
	for _, t := range fs {
		for _, c := range t.sortedChildren() {
			pd.pruneDedupable(c, true)
		}
	}
}

// isMine is PlaceDep.isMine: the conflict is the project's own (an importer's dependency).
func (pd *placeDep) isMine() bool {
	b := pd.b
	e := pd.top().edge
	n := e.from
	if n.isWorkspace() || n.isProjectRoot() {
		return true
	}
	if !e.peer() {
		return false
	}
	hasPeer := false
	for _, ei := range b.edgesIn(n) {
		if ei.peer() {
			hasPeer = true
			continue
		}
		if ei.from.isWorkspace() || ei.from.isProjectRoot() {
			return true
		}
	}
	if hasPeer {
		keys, _ := b.peerEntrySets(n)
		for _, k := range keys {
			if k.from.isWorkspace() || k.from.isProjectRoot() {
				return true
			}
		}
	}
	return false
}

func (pd *placeDep) warnPeerConflict(e *edge) {
	if e == nil {
		e = pd.edge
	}
	e.peerConflicted = true
	from := "?"
	if e.from != nil {
		from = pd.b.describe(e.from)
	}
	pd.b.warn("overriding peer dependency: %s wants %s@%s, using %s@%s", from, e.name, e.spec, pd.dep.packageName(), pd.dep.version())
}

func (pd *placeDep) conflict() error {
	top := pd.top()
	e, dep := top.edge, top.dep
	cur := "nothing"
	if e.from != nil {
		if c := e.from.resolve(e.name); c != nil {
			cur = c.packageName() + "@" + c.version()
		}
	}
	return resolveErr("cannot place %s@%s for %s (wants %s@%s): conflicts with %s already in the tree",
		dep.packageName(), dep.version(), pd.b.describe(e.from), e.name, e.spec, cur)
}
