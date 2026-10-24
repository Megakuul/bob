package resolve

import (
	"sort"
	"strings"
)

// This file models @npmcli/arborist's Node and Edge (npm 11.19.0, ISC) as far as building an
// ideal tree needs them. Differences on purpose: an edge's target and validity are computed on
// demand instead of being cached and reloaded on every tree change (the result is what
// Arborist's reload logic maintains), and edgesIn is derived from a per-tree index rebuilt when
// the tree changes.

type edgeType int

const (
	edgeProd edgeType = iota
	edgeDev
	edgeOptional
	edgePeer
	edgePeerOptional
	edgeWorkspace
)

type edge struct {
	from           *node
	name           string
	spec           string
	typ            edgeType
	peerConflicted bool
}

func (e *edge) peer() bool     { return e.typ == edgePeer || e.typ == edgePeerOptional }
func (e *edge) optional() bool { return e.typ == edgeOptional || e.typ == edgePeerOptional }

func (e *edge) to() *node {
	if e.from == nil {
		return nil
	}
	return e.from.resolve(e.name)
}

// errKind is Edge.error: "" when valid, else MISSING, PEER LOCAL or INVALID.
func (e *edge) errKind(b *builder) string {
	to := e.to()
	switch {
	case to == nil:
		if e.optional() {
			return ""
		}
		return "MISSING"
	case e.peer() && e.from == to.parent && !e.from.isTop():
		return "PEER LOCAL"
	case !e.satisfiedBy(b, to):
		return "INVALID"
	}
	return ""
}

func (e *edge) valid(b *builder) bool { return e.errKind(b) == "" }

// satisfiedBy is Edge.satisfiedBy: same name and depValid.
func (e *edge) satisfiedBy(b *builder, n *node) bool {
	if !strings.EqualFold(n.name, e.name) || e.from == nil {
		return false
	}
	return b.depValid(n, e.spec)
}

// node is an Arborist Node (or Link when link is set).
type node struct {
	name     string    // folder name (the alias for npm: specs)
	pkg      *manifest // package data
	parent   *node
	fsParent *node
	children map[string]*node // key: lower-cased name (Arborist uses a case-insensitive map)
	fsKids   []*node          // workspace targets of the project root; link-target copies in a virtual root
	edges    []*edge          // edgesOut in insertion order
	edgeIdx  map[string]*edge // key: lower-cased name

	link   bool
	target *node // link target

	sourceRef *node // virtual root: the node whose dependencies it mirrors
	basePath  string
	workspace string // non-empty for workspace targets: the importer directory
	projRoot  bool   // the ideal tree's root

	resolved  string
	integrity string
	errs      []error
	seq       int // creation order, for deterministic iteration
}

func lower(s string) string { return strings.ToLower(s) }

func (n *node) version() string {
	if n.link && n.target != nil {
		return n.target.version()
	}
	if n.pkg == nil {
		return ""
	}
	return n.pkg.Version
}

func (n *node) packageName() string {
	if n.link && n.target != nil {
		return n.target.packageName()
	}
	if n.pkg == nil {
		return ""
	}
	return n.pkg.Name
}

func (n *node) isTop() bool { return n.parent == nil }

func (n *node) resolveParent() *node {
	if n.parent != nil {
		return n.parent
	}
	return n.fsParent
}

// isProjectRoot is Node.isProjectRoot: the node is the root of its tree (the ideal tree's root,
// a virtual root, or a detached node).
func (n *node) isProjectRoot() bool { return n.parent == nil && n.fsParent == nil }

func (n *node) root() *node {
	r := n
	for p := r.resolveParent(); p != nil; p = r.resolveParent() {
		r = p
	}
	return r
}

func (n *node) isWorkspace() bool {
	if n.link && n.target != nil && n.target.workspace != "" && n.parent != nil && n.parent.projRoot {
		return true
	}
	return n.workspace != "" && n.fsParent != nil && n.fsParent.projRoot
}

func (n *node) depth() int {
	d := 0
	for p := n; p.parent != nil; p = p.parent {
		d++
	}
	return d
}

func (n *node) path() string {
	if n.parent != nil {
		return n.parent.path() + "/node_modules/" + n.name
	}
	return n.basePath
}

// location is the path relative to the tree root.
func (n *node) location() string {
	r := n.root()
	p, rp := n.path(), r.path()
	if p == rp {
		return ""
	}
	return strings.TrimPrefix(p, rp+"/")
}

func (n *node) resolve(name string) *node {
	k := lower(name)
	for p := n; p != nil; p = p.resolveParent() {
		if c := p.children[k]; c != nil {
			return c
		}
	}
	return nil
}

func (n *node) isDescendantOf(o *node) bool {
	for p := n; p != nil; p = p.resolveParent() {
		if p == o {
			return true
		}
	}
	return false
}

func (n *node) edge(name string) *edge { return n.edgeIdx[lower(name)] }

// addEdge is new Edge(): it replaces an edge of the same name, moving it to the end.
func (n *node) addEdge(name, spec string, typ edgeType) {
	k := lower(name)
	if old := n.edgeIdx[k]; old != nil {
		for i, e := range n.edges {
			if e == old {
				n.edges = append(n.edges[:i:i], n.edges[i+1:]...)
				break
			}
		}
		old.from = nil
	}
	e := &edge{from: n, name: name, spec: spec, typ: typ}
	n.edges = append(n.edges, e)
	n.edgeIdx[k] = e
}

// loadDeps is Node[_loadDeps]: peer, peerOptional, prod, optional, then dev for tops.
func (n *node) loadDeps(withDev bool, dev *orderedMap) {
	n.edgeIdx = map[string]*edge{}
	m := n.pkg
	if m == nil {
		return
	}
	if m.PeerDependencies.Len() > 0 {
		var req, opt []string
		for _, k := range m.PeerDependencies.Keys() {
			if m.PeerDependenciesMeta[k].Optional {
				opt = append(opt, k)
			} else {
				req = append(req, k)
			}
		}
		for _, k := range req {
			v, _ := m.PeerDependencies.Get(k)
			n.loadDepType(k, v, edgePeer)
		}
		for _, k := range opt {
			v, _ := m.PeerDependencies.Get(k)
			n.loadDepType(k, v, edgePeerOptional)
		}
	}
	for _, k := range m.Dependencies.Keys() {
		v, _ := m.Dependencies.Get(k)
		n.loadDepType(k, v, edgeProd)
	}
	for _, k := range m.OptionalDependencies.Keys() {
		v, _ := m.OptionalDependencies.Get(k)
		n.loadDepType(k, v, edgeOptional)
	}
	if withDev {
		for _, k := range dev.Keys() {
			v, _ := dev.Get(k)
			n.loadDepType(k, v, edgeDev)
		}
	}
}

func (n *node) loadDepType(name, spec string, typ edgeType) {
	if cur := n.edge(name); cur != nil && cur.typ == edgeWorkspace {
		return
	}
	n.addEdge(name, spec, typ)
}

// sortedChildren returns the children ordered by location (localeCompare), as Arborist sorts
// them where order matters.
func (n *node) sortedChildren() []*node {
	out := make([]*node, 0, len(n.children))
	for _, c := range n.children {
		out = append(out, c)
	}
	sort.SliceStable(out, func(i, j int) bool { return localeCompare(out[i].location(), out[j].location()) < 0 })
	return out
}
