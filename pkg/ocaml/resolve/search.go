package resolve

import (
	"fmt"
	"slices"
)

// The search is a depth-first branch and bound over the pruned cone with constraint
// propagation: clauses with a single viable alternative force their packages and narrow their
// version domains, conflicts and conflict classes remove versions, and a branch is cut when
// its lower bound (cost so far + the cheapest remaining version of every forced package) is
// not better than the best solution found. Branching tries the cheapest (newest) version
// first, so the first solution is the greedy newest-first one and later ones only improve it.

type undoKind uint8

const (
	undoDom undoKind = iota
	undoVal
	undoReq
)

type undo struct {
	kind undoKind
	n, w int
	old  uint64
}

type checkpoint struct {
	trail, reqs, open int
	cur               cost
}

func (s *solver) checkpoint() checkpoint {
	return checkpoint{len(s.trail), len(s.reqs), len(s.open), s.cur}
}

func (s *solver) restore(cp checkpoint) {
	for k := len(s.trail) - 1; k >= cp.trail; k-- {
		u := s.trail[k]
		switch u.kind {
		case undoDom:
			s.dom[u.n][u.w] = u.old
		case undoVal:
			s.val[u.n] = int(int64(u.old))
		case undoReq:
			s.req[u.n] = false
		}
	}
	s.trail = s.trail[:cp.trail]
	s.reqs = s.reqs[:cp.reqs]
	s.open = s.open[:cp.open]
	s.cur = cp.cur
}

// narrow intersects the domain of n with keep (or removes drop when keep is nil).
func (s *solver) narrow(n int, keep, drop bitset) (changed bool) {
	d := s.dom[n]
	for w := range d {
		nw := d[w]
		if keep != nil {
			nw &= keep[w]
		} else {
			nw &^= drop[w]
		}
		if nw != d[w] {
			s.trail = append(s.trail, undo{kind: undoDom, n: n, w: w, old: d[w]})
			d[w] = nw
			changed = true
		}
	}
	return changed
}

func (s *solver) require(n int) bool {
	if s.req[n] {
		return false
	}
	s.trail = append(s.trail, undo{kind: undoReq, n: n})
	s.req[n] = true
	s.reqs = append(s.reqs, n)
	return true
}

func (s *solver) search() error {
	s.dom = make([]bitset, len(s.pkgs))
	s.val = make([]int, len(s.pkgs))
	s.req = make([]bool, len(s.pkgs))
	for n, p := range s.pkgs {
		s.dom[n] = p.live.clone()
		s.val[n] = -1
		if p.provided {
			s.val[n] = 0
			s.req[n] = true
		}
	}
	for k := range s.roots {
		s.open = append(s.open, &s.roots[k])
	}
	return s.node(0)
}

func (s *solver) stopped() bool { return s.steps >= s.max }

func (s *solver) node(depth int) error {
	if s.stopped() {
		return nil
	}
	s.steps++
	if s.steps%1024 == 0 {
		if err := s.ctx.Err(); err != nil {
			return fmt.Errorf("opam resolution: %w", err)
		}
	}
	if !s.propagate(depth) {
		return nil
	}
	if s.found && !s.lowerBound().less(s.bestC) {
		return nil
	}
	// Strong branching: probe every version of every forced, undecided package and branch on
	// the package whose best version raises the bound most (ties: fewest versions). Packages
	// coupled to the fixed toolchain (dune-rpc, stdune, … with {= version}) are decided first,
	// so the independent cheap choices below them are pruned by an exact bound.
	pick, pickLB := -1, cost{}
	var vals []int
	for _, n := range s.reqs {
		if s.val[n] >= 0 {
			continue
		}
		order, lb := s.valueOrder(n, depth)
		if len(order) == 0 {
			return nil
		}
		if pick < 0 || pickLB.less(lb) || (lb == pickLB && len(order) < len(vals)) {
			pick, pickLB, vals = n, lb, order
		}
	}
	if pick >= 0 {
		for _, i := range vals {
			cp := s.checkpoint()
			if s.assign(pick, i) {
				if err := s.node(depth + 1); err != nil {
					return err
				}
			}
			s.restore(cp)
			if s.stopped() {
				return nil
			}
		}
		return nil
	}
	for _, c := range s.open {
		st, _ := s.status(c)
		if st != stOpen {
			continue
		}
		for _, alt := range c.alts {
			if !s.viable(alt) {
				continue
			}
			cp := s.checkpoint()
			if _, ok := s.requireAlt(alt); ok {
				if err := s.node(depth + 1); err != nil {
					return err
				}
			}
			s.restore(cp)
			if s.stopped() {
				return nil
			}
		}
		return nil
	}
	// Every clause is satisfied: a solution (undecided packages stay uninstalled).
	s.found, s.bestC = true, s.cur
	s.best = append(s.best[:0], s.val...)
	return nil
}

// valueOrder probes every version left for package n (choose it, propagate, take the lower
// bound) and returns the consistent ones, lowest bound first. Probing sees the cost of what a
// version drags in: for dream, caqti-lwt 2.3.2 forces caqti 2.3.x, which forces dune-site,
// whose versions under the fixed dune are all old (high version lag); caqti-lwt 2.0.1 does
// not. Trying the cheapest-looking version first makes the first solution good, so the bound
// prunes early.
func (s *solver) valueOrder(n, depth int) ([]int, cost) {
	type cand struct {
		i  int
		lb cost
	}
	var cs []cand
	for _, i := range s.pkgs[n].order {
		if !s.dom[n].has(i) {
			continue
		}
		s.steps++ // probes count against the budget like nodes
		cp := s.checkpoint()
		if s.assign(n, i) && s.propagate(depth+1) {
			if lb := s.lowerBound(); !s.found || lb.less(s.bestC) {
				cs = append(cs, cand{i, lb})
			}
		}
		s.restore(cp)
	}
	slices.SortStableFunc(cs, func(a, b cand) int {
		switch {
		case a.lb.less(b.lb):
			return -1
		case b.lb.less(a.lb):
			return 1
		}
		return 0
	})
	out := make([]int, len(cs))
	for k, c := range cs {
		out[k] = c.i
	}
	if len(cs) == 0 {
		return nil, cost{}
	}
	return out, cs[0].lb
}

// lowerBound is the cost so far plus, once per package, the cheapest version left of every
// package that must still be chosen: the forced ones and, transitively, those every remaining
// version of a forced package depends on unconditionally. Each package is counted once, so
// the bound never exceeds the cost of a completion.
func (s *solver) lowerBound() cost {
	if s.seen == nil {
		s.seen = make([]int, len(s.pkgs))
	}
	s.stamp++
	lb := s.cur
	queue := s.queue[:0]
	for _, n := range s.reqs {
		if s.val[n] < 0 {
			s.seen[n] = s.stamp
			queue = append(queue, n)
		}
	}
	for k := 0; k < len(queue); k++ {
		n := queue[k]
		j := s.cheapest(n, nil)
		if j < 0 {
			continue
		}
		lb = lb.add(s.pkgs[n].vers[j].cost)
		queue = s.implied(n, queue)
	}
	s.queue = queue
	return lb
}

// implied appends to queue the undecided, unforced, unseen packages that every version left
// for n needs through a single-alternative clause.
func (s *solver) implied(n int, queue []int) []int {
	p := s.pkgs[n]
	common := s.common[:0]
	first := true
	for i := range p.vers {
		if !s.dom[n].has(i) {
			continue
		}
		var mine []int
		for _, cl := range p.vers[i].deps {
			if len(cl.alts) != 1 {
				continue
			}
			for _, a := range cl.alts[0] {
				if m := a.n; m >= 0 && s.val[m] < 0 && s.seen[m] != s.stamp && (first || slices.Contains(common, m)) {
					mine = append(mine, m)
				}
			}
		}
		common, first = append(common[:0], mine...), false
		if len(common) == 0 {
			break
		}
	}
	for _, m := range common {
		if s.seen[m] != s.stamp {
			s.seen[m] = s.stamp
			queue = append(queue, m)
		}
	}
	s.common = common
	return queue
}

type clauseState uint8

const (
	stSat clauseState = iota
	stNone
	stUnit
	stOpen
)

func (s *solver) viable(alt []catom) bool {
	for _, a := range alt {
		if a.n < 0 {
			return false
		}
		if v := s.val[a.n]; v >= 0 {
			if !a.set.has(v) {
				return false
			}
		} else if !a.set.intersects(s.dom[a.n]) {
			return false
		}
	}
	return true
}

func (s *solver) satisfied(alt []catom) bool {
	for _, a := range alt {
		if a.n < 0 || s.val[a.n] < 0 || !a.set.has(s.val[a.n]) {
			return false
		}
	}
	return true
}

// status classifies a clause; for stUnit it also returns the only viable alternative.
func (s *solver) status(c *cclause) (clauseState, int) {
	unit, viable := -1, 0
	for k, alt := range c.alts {
		if s.satisfied(alt) {
			return stSat, k
		}
		if s.viable(alt) {
			unit = k
			viable++
		}
	}
	switch viable {
	case 0:
		return stNone, -1
	case 1:
		return stUnit, unit
	}
	return stOpen, -1
}

func (s *solver) propagate(depth int) bool {
	for changed := true; changed; {
		changed = false
		for k := 0; k < len(s.open); k++ {
			c := s.open[k]
			st, alt := s.status(c)
			switch st {
			case stNone:
				s.noteFail(depth, c)
				return false
			case stUnit:
				ch, ok := s.requireAlt(c.alts[alt])
				if !ok {
					s.noteFail(depth, c)
					return false
				}
				changed = changed || ch
			}
		}
	}
	return true
}

// requireAlt forces every package of alt (narrowed to the matching versions) and chooses
// those left with a single version.
func (s *solver) requireAlt(alt []catom) (changed, ok bool) {
	for _, a := range alt {
		if a.n < 0 {
			return changed, false
		}
		if v := s.val[a.n]; v >= 0 {
			if !a.set.has(v) {
				return changed, false
			}
			continue
		}
		if s.require(a.n) {
			changed = true
		}
		if s.narrow(a.n, a.set, nil) {
			changed = true
		}
		switch s.dom[a.n].count() {
		case 0:
			return changed, false
		case 1:
			if !s.assign(a.n, s.dom[a.n].first()) {
				return changed, false
			}
			changed = true
		}
	}
	return changed, true
}

// assign chooses version i of package n and applies its conflicts.
func (s *solver) assign(n, i int) bool {
	p, v := s.pkgs[n], s.pkgs[n].vers[i]
	s.trail = append(s.trail, undo{kind: undoVal, n: n, old: uint64(int64(s.val[n]))})
	s.val[n] = i
	s.require(n)
	one := newBitset(len(p.vers))
	one.set(i)
	s.narrow(n, one, nil)
	s.cur = s.cur.add(v.cost)
	for k := range v.deps {
		s.open = append(s.open, &v.deps[k])
	}
	exclude := func(t int, set bitset) bool {
		if x := s.val[t]; x >= 0 {
			return !set.has(x)
		}
		s.narrow(t, nil, set)
		return !s.req[t] || !s.dom[t].empty()
	}
	for _, a := range v.conflicts {
		if !exclude(a.n, a.set) {
			return false
		}
	}
	for _, r := range p.revConf[i] {
		set := newBitset(len(s.pkgs[r.n].vers))
		set.set(r.i)
		if !exclude(r.n, set) {
			return false
		}
	}
	for _, c := range v.classes {
		for _, m := range s.members[c] {
			if m.n != n && !exclude(m.n, m.set) {
				return false
			}
		}
	}
	return true
}

// noteFail remembers the deepest failing clause for the error message.
func (s *solver) noteFail(depth int, c *cclause) {
	if s.fail != "" && depth < s.failAt {
		return
	}
	s.failAt = depth
	owner := "the request"
	if c.owner.n >= 0 {
		p := s.pkgs[c.owner.n]
		owner = p.name + "." + p.vers[c.owner.i].version
	}
	s.fail = fmt.Sprintf("%s needs %s, which conflicts with the other packages", owner, clauseString(c.src))
}

// cheapest returns the cheapest version of m left in its domain (and in set, if set is not
// nil), or -1.
func (s *solver) cheapest(m int, set bitset) int {
	for _, k := range s.pkgs[m].order {
		if s.dom[m].has(k) && (set == nil || set.has(k)) {
			return k
		}
	}
	return -1
}
