package main

import (
	"sort"
	"sync"
	"time"
)

const (
	originWorker = 0 // pushed by a worker to this orchestrator
	originPeer   = 1 // received from a peer orchestrator
	originLake   = 2 // read from the persisted log
)

type entry struct {
	l      Line
	origin uint8
}

type lkey struct {
	pkg string
	att int32
	seq int64
}

// Buf is the per-build live buffer of one orchestrator: an append-only, capped arrival log
// with dedup by (pkg, attempt, seq). Followers read it by absolute offset.
type Buf struct {
	mu       sync.Mutex
	base     int64 // absolute offset of ents[0]
	ents     []entry
	seen     map[lkey]struct{}
	restarts map[string]map[int32]int64 // pkg -> att -> from
	end      []Final
	ch       chan struct{}
	lastLive time.Time // last line from a worker or a peer
	wantLake bool
	lakeKeys map[string]bool
	followers int
	idleSince time.Time
	cap      int
}

func newBuf(cap int) *Buf {
	return &Buf{seen: map[lkey]struct{}{}, restarts: map[string]map[int32]int64{}, ch: make(chan struct{}),
		lakeKeys: map[string]bool{}, cap: cap, idleSince: time.Now()}
}

func (b *Buf) notify() { close(b.ch); b.ch = make(chan struct{}) }

// add appends new lines; returns how many were new.
func (b *Buf) add(ls []Line, rs []Restart, end []Final, origin uint8) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := 0
	for _, r := range rs {
		m := b.restarts[r.Pkg]
		if m == nil {
			m = map[int32]int64{}
			b.restarts[r.Pkg] = m
		}
		if old, ok := m[r.Att]; !ok || r.From < old {
			m[r.Att] = r.From
			n++
		}
	}
	for _, l := range ls {
		k := lkey{l.Pkg, l.Att, l.Seq}
		if _, ok := b.seen[k]; ok {
			continue
		}
		b.seen[k] = struct{}{}
		b.ents = append(b.ents, entry{l, origin})
		n++
	}
	if len(end) > 0 && b.end == nil {
		b.end = end
		n++
	}
	if origin != originLake && (len(ls) > 0 || len(end) > 0) {
		b.lastLive = time.Now()
	}
	if over := len(b.ents) - b.cap; over > 0 {
		for _, e := range b.ents[:over] {
			delete(b.seen, lkey{e.l.Pkg, e.l.Att, e.l.Seq})
		}
		b.ents = append([]entry(nil), b.ents[over:]...)
		b.base += int64(over)
	}
	if n > 0 {
		b.notify()
	}
	return n
}

type snap struct {
	ents     []entry
	next     int64
	restarts []Restart
	end      []Final
	ch       chan struct{}
	lastLive time.Time
	evicted  bool // the reader's offset was evicted
}

func (b *Buf) read(off int64) snap {
	b.mu.Lock()
	defer b.mu.Unlock()
	s := snap{ch: b.ch, end: b.end, lastLive: b.lastLive}
	if off < b.base {
		s.evicted = off > 0
		off = b.base
	}
	i := off - b.base
	s.ents = append([]entry(nil), b.ents[i:]...)
	s.next = b.base + int64(len(b.ents))
	for p, m := range b.restarts {
		for a, f := range m {
			s.restarts = append(s.restarts, Restart{p, a, f})
		}
	}
	return s
}

// hwm returns, per package, the highest attempt seen and the contiguous high-water mark of
// that attempt's valid lines. Used as the cursor when (re)subscribing to a peer.
func (b *Buf) hwm() map[string]Cursor {
	b.mu.Lock()
	defer b.mu.Unlock()
	m := newMerger(nil)
	for p, rs := range b.restarts {
		for a, f := range rs {
			m.restart(Restart{p, a, f})
		}
	}
	for _, e := range b.ents {
		m.offer(e.l)
	}
	m.drain()
	return m.cursor()
}

// ---------- merger: one per follower ----------

// merger turns lines from any source (worker push, peer, lake), in any order and with
// duplicates, into a gap-free, duplicate-free stream per package.
// Valid line for seq s of package p: the one whose attempt is max{a : from(p,a) <= s},
// with from(p,1) = 1.
type merger struct {
	cur     map[string]Cursor           // last delivered
	pending map[string]map[int64]Line   // seq -> best line
	from    map[string]map[int32]int64  // known restarts
	out     []Line
	outR    []Restart
	sentR   map[Restart]bool
	gapSince map[string]time.Time
}

func newMerger(c map[string]Cursor) *merger {
	m := &merger{cur: map[string]Cursor{}, pending: map[string]map[int64]Line{}, from: map[string]map[int32]int64{},
		sentR: map[Restart]bool{}, gapSince: map[string]time.Time{}}
	for p, v := range c {
		m.cur[p] = v
	}
	return m
}

func (m *merger) attFor(p string, s int64) int32 {
	best := int32(1)
	for a, f := range m.from[p] {
		if f <= s && a > best {
			best = a
		}
	}
	return best
}

func (m *merger) restart(r Restart) {
	fm := m.from[r.Pkg]
	if fm == nil {
		fm = map[int32]int64{}
		m.from[r.Pkg] = fm
	}
	if old, ok := fm[r.Att]; ok && old <= r.From {
		return
	}
	fm[r.Att] = r.From
	c := m.cur[r.Pkg]
	// Lines >= From of older attempts that were already delivered are void.
	if c.Att < r.Att && c.Seq >= r.From {
		if !m.sentR[r] {
			m.outR = append(m.outR, r)
			m.sentR[r] = true
		}
		m.cur[r.Pkg] = Cursor{r.From - 1, r.Att}
	}
	for s, l := range m.pending[r.Pkg] {
		if l.Att < m.attFor(r.Pkg, s) {
			delete(m.pending[r.Pkg], s)
		}
	}
}

func (m *merger) offer(l Line) {
	if l.Att < m.attFor(l.Pkg, l.Seq) {
		return
	}
	c := m.cur[l.Pkg]
	if l.Seq <= c.Seq && l.Att <= c.Att {
		return
	}
	pm := m.pending[l.Pkg]
	if pm == nil {
		pm = map[int64]Line{}
		m.pending[l.Pkg] = pm
	}
	if old, ok := pm[l.Seq]; !ok || l.Att > old.Att {
		pm[l.Seq] = l
	}
}

// drain moves contiguous lines to out; records when a package has a gap.
func (m *merger) drain() {
	pkgs := make([]string, 0, len(m.pending))
	for p := range m.pending {
		pkgs = append(pkgs, p)
	}
	sort.Strings(pkgs)
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
			m.cur[p] = Cursor{l.Seq, l.Att}
		}
		if len(pm) > 0 {
			if m.gapSince[p].IsZero() {
				m.gapSince[p] = time.Now()
			}
		} else {
			delete(m.gapSince, p)
		}
	}
}

func (m *merger) gapOlderThan(d time.Duration) bool {
	for _, t := range m.gapSince {
		if time.Since(t) > d {
			return true
		}
	}
	return false
}

func (m *merger) cursor() map[string]Cursor {
	c := map[string]Cursor{}
	for p, v := range m.cur {
		c[p] = v
	}
	return c
}

func (m *merger) done(end []Final) bool {
	if end == nil {
		return false
	}
	for _, f := range end {
		if m.cur[f.Pkg].Seq != f.Seq {
			return false
		}
	}
	return true
}
