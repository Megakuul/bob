package resolve

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
)

// Phase 2: from npm's tree to bob's peer-correct package instances.
//
// Every dependency resolves to the tree node npm would load (walking up node_modules from the
// dependent's location), so versions and non-peer edges equal npm's. Peer dependencies resolve
// in the dependent's context instead (pnpm semantics): an instance links the copy its
// dependent sees, and a package whose dependents see different peers becomes several
// instances, keyed name@version(peer@version,…). Where npm's hoisting gives a package a peer
// its dependent does not see, bob follows the dependent (SPEC §03.4.2, the peer-split case).

type inst struct {
	seq   int
	t     *node
	peerT map[string]*node // edge name → tree node of the resolved peer
	deps  map[string]*inst // alias → instance (peers included)
	links map[string]string
	peers []string
	// requiredMissing names a required dependency that resolved to nothing or a failed node.
	requiredMissing string
	optEdge         map[string]bool // alias reached through an optional edge
}

type owner struct {
	t     *node
	peerT map[string]*node
	deps  map[string]*inst
	links map[string]string
}

type phase2 struct {
	b    *builder
	byID map[string]*inst
	list []*inst
	work []*inst
	ext  map[string][]string // Extend peers by package name
}

func instID(t *node, peerT map[string]*node) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "%d", t.seq)
	keys := make([]string, 0, len(peerT))
	for k := range peerT {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Fprintf(&sb, "|%s=%d", k, peerT[k].seq)
	}
	return sb.String()
}

// viewTree is the tree node o would link for name.
func (o *owner) viewTree(name string) *node {
	if t, ok := o.peerT[lower(name)]; ok {
		return t
	}
	return o.t.resolve(name)
}

// view returns the instance (or workspace link) owner o links for name.
func (p *phase2) view(o *owner, name string) (*inst, string) {
	if i, ok := o.deps[name]; ok {
		return i, ""
	}
	if l, ok := o.links[name]; ok {
		return nil, l
	}
	t := o.viewTree(name)
	i, l := p.instOf(t, o)
	return i, l
}

func (p *phase2) instOf(t *node, o *owner) (*inst, string) {
	if t == nil || p.b.failed[t] {
		return nil, ""
	}
	if t.link {
		if t.target == nil {
			return nil, ""
		}
		if t.target.workspace != "" {
			return nil, "link:" + t.target.workspace
		}
		t = t.target
	}
	return p.get(t, o), ""
}

// get returns the instance of tree node t as seen by its dependent o.
func (p *phase2) get(t *node, o *owner) *inst {
	b := p.b
	peerT := map[string]*node{}
	fromView := map[string]bool{}
	type peerReq struct {
		name     string
		e        *edge // nil for an Extend peer
		optional bool
	}
	var reqs []peerReq
	for _, e := range t.edges {
		if e.peer() {
			reqs = append(reqs, peerReq{e.name, e, e.typ == edgePeerOptional})
		}
	}
	for _, n := range p.ext[t.packageName()] {
		if t.edge(n) == nil {
			reqs = append(reqs, peerReq{n, nil, true})
		}
	}
	for _, r := range reqs {
		cand, alt := o.viewTree(r.name), t.resolve(r.name)
		ok := func(x *node) bool {
			return x != nil && !b.failed[x] && (r.e == nil || b.satisfies(x, r.e))
		}
		var chosen *node
		switch {
		case ok(cand):
			chosen = cand
		case ok(alt):
			chosen = alt
		case cand != nil && !b.failed[cand]:
			chosen = cand
		case alt != nil && !b.failed[alt]:
			chosen = alt
		}
		if chosen == nil {
			continue
		}
		peerT[lower(r.name)] = chosen
		fromView[lower(r.name)] = chosen == cand
	}
	id := instID(t, peerT)
	if i := p.byID[id]; i != nil {
		return i
	}
	i := &inst{seq: len(p.list), t: t, peerT: peerT, deps: map[string]*inst{}, links: map[string]string{}, optEdge: map[string]bool{}}
	p.byID[id] = i
	p.list = append(p.list, i)
	for _, r := range reqs {
		k := lower(r.name)
		pt, ok := peerT[k]
		if !ok {
			continue
		}
		var pi *inst
		var link string
		if fromView[k] {
			pi, link = p.view(o, r.name)
		} else {
			pi, link = p.instOf(pt, o)
		}
		switch {
		case link != "":
			i.links[r.name] = link
		case pi != nil:
			i.deps[r.name] = pi
		default:
			continue
		}
		i.peers = append(i.peers, r.name)
		i.optEdge[r.name] = r.optional
	}
	sort.Strings(i.peers)
	p.work = append(p.work, i)
	return i
}

// expand resolves the non-peer dependencies of every instance.
func (p *phase2) expand() {
	for len(p.work) > 0 {
		i := p.work[0]
		p.work = p.work[1:]
		o := &owner{t: i.t, peerT: i.peerT, deps: i.deps, links: i.links}
		bundled := map[string]bool{}
		if i.t.pkg != nil {
			for _, x := range i.t.pkg.bundled {
				bundled[x] = true
			}
		}
		for _, e := range i.t.edges {
			if e.peer() || e.typ == edgeDev || e.typ == edgeWorkspace || bundled[e.name] {
				continue
			}
			if _, done := i.deps[e.name]; done {
				continue
			}
			c, link := p.view(o, e.name)
			switch {
			case link != "":
				i.links[e.name] = link
			case c != nil:
				i.deps[e.name] = c
				i.optEdge[e.name] = e.typ == edgeOptional
			case e.typ != edgeOptional && i.requiredMissing == "":
				i.requiredMissing = e.name + "@" + e.spec
				if t := i.t.resolve(e.name); t != nil && len(t.errs) > 0 {
					i.requiredMissing += " (" + t.errs[0].Error() + ")"
				}
			}
		}
	}
}

// label is the SPEC instance key: name@version plus the sorted resolved peers.
func (i *inst) label() string {
	s := i.t.packageName() + "@" + i.t.version()
	if len(i.peers) == 0 {
		return s
	}
	parts := make([]string, 0, len(i.peers))
	for _, a := range i.peers {
		if d := i.deps[a]; d != nil {
			parts = append(parts, d.t.packageName()+"@"+d.t.version())
		} else {
			parts = append(parts, i.links[a])
		}
	}
	return s + "(" + strings.Join(parts, ",") + ")"
}

// classes merges structurally equal instances (same label, equal dependencies) and returns a
// class id per instance plus the canonical hash of each class. It is partition refinement by
// iterated hashing, so the result does not depend on discovery order.
func classes(list []*inst) (cls []int, hashes []string) {
	h := make([]string, len(list))
	idx := map[*inst]int{}
	for n, i := range list {
		idx[i] = n
		h[n] = i.label()
	}
	count := -1
	for {
		next := make([]string, len(list))
		for n, i := range list {
			var parts []string
			for a, d := range i.deps {
				parts = append(parts, a+"="+h[idx[d]])
			}
			for a, l := range i.links {
				parts = append(parts, a+"="+l)
			}
			sort.Strings(parts)
			sum := sha256.Sum256([]byte(i.label() + "\x00" + strings.Join(parts, "\x00")))
			next[n] = hex.EncodeToString(sum[:])
		}
		distinct := map[string]bool{}
		for _, x := range next {
			distinct[x] = true
		}
		h = next
		if len(distinct) == count {
			break
		}
		count = len(distinct)
	}
	ids := map[string]int{}
	cls = make([]int, len(list))
	for n := range list {
		id, ok := ids[h[n]]
		if !ok {
			id = len(hashes)
			ids[h[n]] = id
			hashes = append(hashes, h[n])
		}
		cls[n] = id
	}
	return cls, hashes
}
