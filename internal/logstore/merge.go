package logstore

import (
	"slices"
	"strings"
	"time"
)

// lineKey identifies a stored line: readers deduplicate by it (a retried ambiguous commit may
// store a batch twice, R-LOG-001).
type lineKey struct {
	pkg string
	att int32
	seq int64
}

// starts returns from(a) per package for attempts a ≥ 2: the smallest seq of attempt a among
// lines (S17 §5.4: restarts in the lake are inferred from the first seq of each attempt).
func starts(lines []Line) map[string]map[int32]int64 {
	from := map[string]map[int32]int64{}
	for _, l := range lines {
		if l.Attempt < 2 {
			continue
		}
		m := from[l.Package]
		if m == nil {
			m = map[int32]int64{}
			from[l.Package] = m
		}
		if f, ok := m[l.Attempt]; !ok || l.Seq < f {
			m[l.Attempt] = l.Seq
		}
	}
	return from
}

// validAttempt is max{a : from(a) ≤ s} with from(1) = 1.
func validAttempt(from map[int32]int64, s int64) int32 {
	best := int32(1)
	for a, f := range from {
		if f <= s && a > best {
			best = a
		}
	}
	return best
}

// settle deduplicates lines by (package, attempt, seq), drops the lines the restart rule voids
// and orders the rest by (package, seq). It reports the inferred restarts as well.
func settle(lines []Line) ([]Line, []Restart) {
	seen := make(map[lineKey]bool, len(lines))
	uniq := lines[:0:0]
	for _, l := range lines {
		k := lineKey{l.Package, l.Attempt, l.Seq}
		if !seen[k] {
			seen[k] = true
			uniq = append(uniq, l)
		}
	}
	from := starts(uniq)
	out := uniq[:0]
	for _, l := range uniq {
		if l.Attempt == validAttempt(from[l.Package], l.Seq) {
			out = append(out, l)
		}
	}
	slices.SortFunc(out, func(a, b Line) int {
		if c := strings.Compare(a.Package, b.Package); c != 0 {
			return c
		}
		switch {
		case a.Seq < b.Seq:
			return -1
		case a.Seq > b.Seq:
			return 1
		}
		return 0
	})
	var rs []Restart
	for p, m := range from {
		for a, f := range m {
			rs = append(rs, Restart{Package: p, Attempt: a, FromSeq: f})
		}
	}
	return out, rs
}

// merger turns lines from any source (live chunks, lake reads), in any order and with
// duplicates, into a gap-free, duplicate-free stream per package for one follower
// (S17 §5.4, ADR 0019 decision 1).
type merger struct {
	cur      map[string]Cursor          // last delivered
	pending  map[string]map[int64]Line  // seq → best line
	from     map[string]map[int32]int64 // known restarts
	sent     map[Restart]bool
	gapSince map[string]time.Time
	now      func() time.Time

	out  []Line
	outR []Restart
}

func newMerger(cur map[string]Cursor, now func() time.Time) *merger {
	m := &merger{cur: map[string]Cursor{}, pending: map[string]map[int64]Line{},
		from: map[string]map[int32]int64{}, sent: map[Restart]bool{}, gapSince: map[string]time.Time{}, now: now}
	for p, c := range cur {
		m.cur[p] = c
	}
	return m
}

func (m *merger) attFor(p string, s int64) int32 { return validAttempt(m.from[p], s) }

// restart records that attempt r.Attempt starts at r.FromSeq. Lines ≥ FromSeq of older attempts
// that were already delivered are void: the follower gets the Restart and drops them.
func (m *merger) restart(r Restart) {
	if r.Attempt < 2 || r.FromSeq < 1 {
		return
	}
	fm := m.from[r.Package]
	if fm == nil {
		fm = map[int32]int64{}
		m.from[r.Package] = fm
	}
	if old, ok := fm[r.Attempt]; ok && old <= r.FromSeq {
		return
	}
	fm[r.Attempt] = r.FromSeq
	c := m.cur[r.Package]
	if c.Attempt < r.Attempt && c.Seq >= r.FromSeq {
		if !m.sent[r] {
			m.outR = append(m.outR, r)
			m.sent[r] = true
		}
		m.cur[r.Package] = Cursor{Seq: r.FromSeq - 1, Attempt: r.Attempt}
	}
	for s, l := range m.pending[r.Package] {
		if l.Attempt < m.attFor(r.Package, s) {
			delete(m.pending[r.Package], s)
		}
	}
}

// offer adds one line; stale and voided lines are dropped.
func (m *merger) offer(l Line) {
	if l.Seq < 1 || l.Attempt < m.attFor(l.Package, l.Seq) {
		return
	}
	c := m.cur[l.Package]
	if l.Seq <= c.Seq && l.Attempt <= c.Attempt {
		return
	}
	if l.Seq <= c.Seq {
		// A newer attempt for a delivered seq whose Restart is not known: restarts travel
		// with (or before) the lines of their attempt, so this line is not valid here.
		return
	}
	pm := m.pending[l.Package]
	if pm == nil {
		pm = map[int64]Line{}
		m.pending[l.Package] = pm
	}
	if old, ok := pm[l.Seq]; !ok || l.Attempt > old.Attempt {
		pm[l.Seq] = l
	}
}

// drain moves contiguous lines to out and notes since when a package has a gap.
func (m *merger) drain() {
	pkgs := make([]string, 0, len(m.pending))
	for p := range m.pending {
		pkgs = append(pkgs, p)
	}
	slices.Sort(pkgs)
	for _, p := range pkgs {
		pm := m.pending[p]
		for {
			c := m.cur[p]
			l, ok := pm[c.Seq+1]
			if !ok {
				break
			}
			delete(pm, c.Seq+1)
			m.out = append(m.out, l)
			m.cur[p] = Cursor{Seq: l.Seq, Attempt: l.Attempt}
		}
		if len(pm) > 0 {
			if m.gapSince[p].IsZero() {
				m.gapSince[p] = m.now()
			}
		} else {
			delete(m.gapSince, p)
			delete(m.pending, p)
		}
	}
}

// gapOlderThan reports a package whose next line has been missing for longer than d.
func (m *merger) gapOlderThan(d time.Duration) bool {
	for _, t := range m.gapSince {
		if m.now().Sub(t) > d {
			return true
		}
	}
	return false
}

// take returns and clears what drain produced.
func (m *merger) take() ([]Line, []Restart) {
	ls, rs := m.out, m.outR
	m.out, m.outR = nil, nil
	return ls, rs
}

// finals are the end markers of every delivered package.
func (m *merger) finals() []Final {
	out := make([]Final, 0, len(m.cur))
	for p, c := range m.cur {
		if c.Seq > 0 {
			out = append(out, Final{Package: p, LastSeq: c.Seq, Attempt: c.Attempt})
		}
	}
	slices.SortFunc(out, func(a, b Final) int { return strings.Compare(a.Package, b.Package) })
	return out
}
