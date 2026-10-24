// Command sim is S14's discrete-event model of bob's fleet scheduling options:
//
//	a  one job = whole closure of the requested target on one worker
//	b  one job = one derivation, outputs passed between jobs through the cache
//	c  one job = a convex chunk of the graph (graph.ChunkGraph), worker affinity
//
// Parameters come from measurements (see params below and the write-up).
// Throwaway spike code.
package main

import (
	"container/heap"
	"flag"
	"fmt"
	"math"
	"math/rand"
	"os"
	"sort"
	"strings"

	"bobspike/s14/graph"
)

// Params, all seconds / bytes per second. Sources in comments.
type Params struct {
	Slots        int     // build slots per worker (reference 2c machine, R-NFR-008)
	Claim        float64 // claim + complete latency (S9: claim p50 28-90 ms; complete ~same)
	Enqueue      float64 // orchestrator notices completion and enqueues dependents (Put hint + Create live)
	PollI        float64 // empty-queue poll interval (S9: 5 s +-20 %)
	Push         bool    // workers are notified instead of polling (long-poll over the worker connection)
	JobStart     float64 // overlay /nix + nix process start per job (S13: 9 ms overlay; S14 real: nix start)
	DrvFetchBase float64 // nix copy of drv closure: fixed (S14 real run)
	DrvFetchPer  float64 // per drv file missing locally
	SubBase      float64 // substitution of inputs: fixed (S14 real run)
	SubPer       float64 // per path
	SubBW        float64 // bytes/s (S12: 49-91 MiB/s nix from bob)
	DrvFixed     float64 // per derivation inside a nix build (S3: 0.13-0.16 s; S14 real)
	UpBase       float64 // staging upload: fixed
	UpPer        float64 // per path
	UpBW         float64 // bytes/s (S12: 42-84 MiB/s)
	PromBase     float64 // orchestrator promotion of one job's outputs: fixed
	PromPer      float64 // per path
	PromBW       float64 // (S12: 100 MiB in 0.9-2.3 s)
	LeaseTTL     float64 // S9: 30 s
	Reconcile    float64 // S9: 10 s
	EvalInJob    bool    // (a) only: evaluation runs inside the build job, drvs never leave the worker
	Affinity     bool
	Fair         bool // pick the project with the fewest running jobs first
	DedupeLive   bool // a unit already queued/running in another build is joined, not rerun
	ClaimRate    float64 // global claims/s the job table sustains (S9 drain: RustFS 32/s, SeaweedFS 173/s)
	BatchMax     int     // mode d: max drvs per job
	BatchCost    float64 // mode d: max estimated slot-s per job
}

func defaults() Params {
	return Params{
		Slots: 2, Claim: 0.12, Enqueue: 0.06, PollI: 5, Push: false,
		JobStart: 0.12, DrvFetchBase: 0.12, DrvFetchPer: 0.002,
		SubBase: 0.09, SubPer: 0.01, SubBW: 60 << 20,
		DrvFixed: 0.14, UpBase: 0.08, UpPer: 0.005, UpBW: 50 << 20,
		PromBase: 0.05, PromPer: 0.01, PromBW: 80 << 20,
		LeaseTTL: 30, Reconcile: 10, EvalInJob: true, Affinity: true, Fair: true, DedupeLive: true, BatchMax: 64, BatchCost: 60, ClaimRate: 30,
	}
}

// S3 request prices (AWS-equivalent, as in S9): PUT/COPY/LIST $0.005/1k, GET/HEAD $0.0004/1k.
const pPut, pGet = 0.005 / 1000, 0.0004 / 1000

type Req struct{ Put, Get float64 }

func (r *Req) add(o Req) { r.Put += o.Put; r.Get += o.Get }
func (r Req) Cost() float64 { return r.Put*pPut + r.Get*pGet }
func (r Req) N() float64    { return r.Put + r.Get }

// ---------------------------------------------------------------------------

type Build struct {
	Proj    string
	G       *graph.Graph
	Ver     string // output identity: same Ver+node = same store path
	Arrive  float64
	Changed map[int]bool // nodes whose output identity changed relative to `Base` (nil = all new)
	Base    string       // Ver whose outputs are reused for unchanged nodes

	units    []*Unit
	left     int
	done     float64
	nodeVer  []string
	unitOf   []int
}

type Unit struct {
	b       *Build
	nodes   []int
	deps    []*Unit
	rdeps   []*Unit
	key     string
	waiting int
	state   int // 0 wait, 1 queued, 2 running, 3 built (promoting), 4 done
	readyAt float64
	alias   *Unit // dedupe: follows another build's identical unit
	followers []*Unit
	batch     []*Unit // mode d: the queued units this job runs
	need      int
	worker  *Worker
	start   float64
	attempt int
}

type Worker struct {
	id      int
	free    int
	alive   bool
	have    map[string]bool // output identities in local store
	haveDrv map[string]bool
	busy    float64
	nextPoll float64
	polling bool
	jobs    []*Unit
}

type Sim struct {
	p       Params
	mode    string
	capCost float64
	t       float64
	ev      evq
	ws      []*Worker
	queue   []*Unit
	builds  []*Build
	cache   map[string]bool // promoted outputs
	live    map[string]*Unit
	req     Req
	pollReq float64
	bytesUp, bytesSub, bytesDrv float64
	wasted  float64
	jobs    int
	rng     *rand.Rand
	running map[string]int
	claimNext float64
	claimWait float64
}

type event struct {
	t  float64
	f  func()
	id int
}
type evq []*event

func (q evq) Len() int            { return len(q) }
func (q evq) Less(i, j int) bool  { if q[i].t != q[j].t { return q[i].t < q[j].t }; return q[i].id < q[j].id }
func (q evq) Swap(i, j int)       { q[i], q[j] = q[j], q[i] }
func (q *evq) Push(x any)         { *q = append(*q, x.(*event)) }
func (q *evq) Pop() any           { o := *q; e := o[len(o)-1]; *q = o[:len(o)-1]; return e }

var evid int

func (s *Sim) at(t float64, f func()) { evid++; heap.Push(&s.ev, &event{t, f, evid}) }

func ident(b *Build, v int) string { return b.G.Name + "/" + b.nodeVer[v] + "/" + b.G.Nodes[v].ID }

func (s *Sim) addBuild(b *Build) {
	s.builds = append(s.builds, b)
	g := b.G
	n := len(g.Nodes)
	b.nodeVer = make([]string, n)
	// a changed node changes every dependent's identity (input addressing)
	ch := make([]bool, n)
	for i, nd := range g.Nodes {
		ch[i] = b.Changed == nil || b.Changed[i]
		for _, d := range nd.Deps {
			if ch[d] {
				ch[i] = true
			}
		}
		if ch[i] {
			b.nodeVer[i] = b.Ver
		} else {
			b.nodeVer[i] = b.Base
		}
	}
	var parts [][]int
	switch s.mode {
	case "a":
		all := make([]int, n)
		for i := range all {
			all[i] = i
		}
		parts = [][]int{all}
	case "b", "d":
		for i := 0; i < n; i++ {
			parts = append(parts, []int{i})
		}
	case "c":
		for _, c := range graph.ChunkGraph(g, s.capCost, s.p.DrvFixed) {
			parts = append(parts, c.Nodes)
		}
	}
	b.unitOf = make([]int, n)
	for ui, nodes := range parts {
		for _, v := range nodes {
			b.unitOf[v] = ui
		}
	}
	for _, nodes := range parts {
		// only nodes not already in the cache need building
		var need []int
		var keys []string
		for _, v := range nodes {
			if !s.cache[ident(b, v)] {
				need = append(need, v)
				keys = append(keys, ident(b, v))
			}
		}
		u := &Unit{b: b, nodes: need, key: strings.Join(keys, ",")}
		b.units = append(b.units, u)
	}
	for ui, u := range b.units {
		seen := map[int]bool{}
		for _, v := range parts[ui] {
			for _, d := range g.Nodes[v].Deps {
				du := b.unitOf[d]
				if du != ui && !seen[du] {
					seen[du] = true
					u.deps = append(u.deps, b.units[du])
					b.units[du].rdeps = append(b.units[du].rdeps, u)
				}
			}
		}
	}
	s.at(b.Arrive, func() {
		b.left = len(b.units)
		for _, u := range b.units {
			u.waiting = len(u.deps)
		}
		for _, u := range b.units {
			if u.waiting == 0 {
				s.ready(u)
			}
		}
	})
}

func (s *Sim) ready(u *Unit) {
	if len(u.nodes) == 0 {
		s.finish(u)
		return
	}
	if s.p.DedupeLive {
		if o, ok := s.live[u.key]; ok && o != u {
			if o.state == 4 {
				s.finish(u)
				return
			}
			u.alias = o
			o.followers = append(o.followers, u)
			return
		}
		s.live[u.key] = u
	}
	u.state = 1
	s.at(s.t+s.p.Enqueue, func() {
		u.readyAt = s.t
		s.queue = append(s.queue, u)
		s.req.add(Req{Put: 2}) // hint + live create
		s.dispatch()
	})
}

func (s *Sim) finish(u *Unit) {
	if u.state == 4 {
		return
	}
	u.state = 4
	b := u.b
	b.left--
	if b.left == 0 {
		b.done = s.t
	}
	for _, r := range u.rdeps {
		r.waiting--
		if r.waiting == 0 {
			s.ready(r)
		}
	}
	for _, f := range u.followers {
		s.finish(f)
	}
}

// dispatch: in push mode every free worker takes a job now; in poll mode only
// workers whose poll fires (s.pollNow) do.
func (s *Sim) dispatch() {
	if !s.p.Push {
		// wake idle workers' polls are already scheduled; nothing to do now
		return
	}
	for _, w := range s.ws {
		for w.alive && w.free > 0 && len(s.queue) > 0 {
			if !s.take(w) {
				break
			}
		}
	}
}

func (s *Sim) cost(w *Worker, u *Unit) float64 {
	sc := 0
	for _, v := range u.nodes {
		for _, d := range u.b.G.Nodes[v].Deps {
			if w.have[ident(u.b, d)] {
				sc++
			}
		}
	}
	return float64(sc)
}

// take picks a job for worker w. Order: fair share across projects, then FIFO;
// among the first 50 candidates (S9 hint candidate set) prefer affinity.
func (s *Sim) take(w *Worker) bool {
	need := 1
	if s.mode != "b" && s.mode != "d" {
		need = s.p.Slots
	}
	if w.free < need || len(s.queue) == 0 {
		return false
	}
	q := s.queue
	order := make([]int, len(q))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(i, j int) bool {
		a, b := q[order[i]], q[order[j]]
		if s.p.Fair {
			ra, rb := s.running[a.b.Proj], s.running[b.b.Proj]
			if ra != rb {
				return ra < rb
			}
		}
		return a.readyAt < b.readyAt
	})
	best := order[0]
	if s.p.Affinity {
		bs := -1.0
		lim := 50
		if len(order) < lim {
			lim = len(order)
		}
		for _, i := range order[:lim] {
			if s.p.Fair && s.running[q[i].b.Proj] != s.running[q[order[0]].b.Proj] {
				break
			}
			c := s.cost(w, q[i])
			if c > bs {
				bs, best = c, i
			}
		}
	}
	u := q[best]
	s.queue = append(q[:best], q[best+1:]...)
	if s.mode == "d" {
		// batch: more ready units of the same build, about ready/idleWorkers of them
		idle := 0
		for _, x := range s.ws {
			if x.alive && x.free > 0 {
				idle++
			}
		}
		same := 1
		for _, x := range s.queue {
			if x.b == u.b {
				same++
			}
		}
		lim := (same + idle - 1) / max(idle, 1)
		lim = min(lim, s.p.BatchMax)
		m := &Unit{b: u.b, batch: []*Unit{u}, nodes: append([]int(nil), u.nodes...)}
		cost := u.b.G.Nodes[u.nodes[0]].Cost
		var rest []*Unit
		for _, x := range s.queue {
			if x.b == u.b && len(m.batch) < lim && cost < s.p.BatchCost {
				m.batch = append(m.batch, x)
				m.nodes = append(m.nodes, x.nodes...)
				cost += x.b.G.Nodes[x.nodes[0]].Cost
			} else {
				rest = append(rest, x)
			}
		}
		s.queue = rest
		sort.Ints(m.nodes)
		for _, x := range m.batch {
			x.state = 2
		}
		s.run(w, m, min(s.p.Slots, len(m.batch)))
		return true
	}
	s.run(w, u, need)
	return true
}

// localSchedule: list scheduling of nodes on `slots` slots, priority = bottom level.
func localSchedule(g *graph.Graph, nodes []int, slots int, fixed float64) float64 {
	in := map[int]bool{}
	for _, v := range nodes {
		in[v] = true
	}
	bl := g.BottomLevel(fixed)
	fin := map[int]float64{}
	slotFree := make([]float64, slots)
	// nodes are in topological order already
	order := append([]int(nil), nodes...)
	// ready-time list scheduling: repeatedly pick the ready node with highest bl
	doneSet := map[int]bool{}
	end := 0.0
	for len(doneSet) < len(order) {
		// earliest free slot
		si := 0
		for i := range slotFree {
			if slotFree[i] < slotFree[si] {
				si = i
			}
		}
		now := slotFree[si]
		bestV, bestBL, bestRT := -1, -1.0, math.Inf(1)
		for _, v := range order {
			if doneSet[v] {
				continue
			}
			rt, ok := 0.0, true
			for _, d := range g.Nodes[v].Deps {
				if in[d] {
					f, done := fin[d]
					if !done {
						ok = false
						break
					}
					rt = math.Max(rt, f)
				}
			}
			if !ok {
				continue
			}
			// prefer nodes ready by `now`; else the earliest ready
			if rt <= now {
				if bestRT > now || bl[v] > bestBL {
					bestV, bestBL, bestRT = v, bl[v], rt
				}
			} else if bestRT > now && rt < bestRT {
				bestV, bestBL, bestRT = v, bl[v], rt
			}
		}
		st := math.Max(now, bestRT)
		f := st + fixed + g.Nodes[bestV].Cost
		fin[bestV] = f
		doneSet[bestV] = true
		slotFree[si] = f
		end = math.Max(end, f)
	}
	return end
}

func (s *Sim) run(w *Worker, u *Unit, need int) {
	p := s.p
	b := u.b
	g := b.G
	s.jobs++
	s.running[b.Proj]++
	u.state = 2
	u.worker = w
	u.start = s.t
	u.attempt++
	u.need = need
	w.free -= need
	w.jobs = append(w.jobs, u)
	s.req.add(Req{Put: 6, Get: 6}) // S9: ~12 requests per job (claim + complete)
	cw := math.Max(0, s.claimNext-s.t)
	s.claimNext = math.Max(s.t, s.claimNext) + 1/p.ClaimRate
	s.claimWait += cw
	d := cw + p.Claim + p.JobStart
	inSet := map[int]bool{}
	for _, v := range u.nodes {
		inSet[v] = true
	}
	// drv closure (b, c; and a when evaluation is a separate job)
	if s.mode != "a" || !p.EvalInJob {
		miss := 0
		seen := map[int]bool{}
		var walk func(v int)
		walk = func(v int) {
			if seen[v] {
				return
			}
			seen[v] = true
			if !w.haveDrv[b.G.Name+"/"+b.nodeVer[v]+"/"+g.Nodes[v].ID] {
				miss++
			}
			for _, x := range g.Nodes[v].Deps {
				walk(x)
			}
		}
		for _, v := range u.nodes {
			walk(v)
		}
		d += p.DrvFetchBase + p.DrvFetchPer*float64(miss)
		s.req.add(Req{Get: 3 * float64(miss)})
		s.bytesDrv += 2048 * float64(miss)
		for v := range seen {
			w.haveDrv[b.G.Name+"/"+b.nodeVer[v]+"/"+g.Nodes[v].ID] = true
		}
	}
	// inputs from outside the unit not in the local store
	subN, subB := 0, 0.0
	seenIn := map[int]bool{}
	for _, v := range u.nodes {
		for _, x := range g.Nodes[v].Deps {
			if !inSet[x] && !seenIn[x] && !w.have[ident(b, x)] {
				seenIn[x] = true
				subN++
				subB += float64(g.Nodes[x].Size)
			}
		}
	}
	// nodes in a whole-closure job that are already cached are substituted too, when needed
	if subN > 0 {
		d += p.SubBase + p.SubPer*float64(subN) + subB/p.SubBW
		s.req.add(Req{Get: 3 * float64(subN)})
		s.bytesSub += subB
	}
	// Nix asks the cache for each output it is about to build (narinfo 404)
	s.req.add(Req{Get: float64(len(u.nodes))})
	build := localSchedule(g, u.nodes, need, p.DrvFixed)
	upB := 0.0
	for _, v := range u.nodes {
		upB += float64(g.Nodes[v].Size)
	}
	up := p.UpBase + p.UpPer*float64(len(u.nodes)) + upB/p.UpBW
	d += build + up
	s.req.add(Req{Put: 2*float64(len(u.nodes)) + 1}) // NAR + claim per path, COMPLETE.json
	// lease renewals during the job: GET+PUT every TTL/3
	s.req.add(Req{Put: d / (p.LeaseTTL / 3), Get: d / (p.LeaseTTL / 3)})
	s.bytesUp += upB
	att := u.attempt
	s.at(s.t+d, func() {
		if !w.alive || u.attempt != att || u.state != 2 {
			return
		}
		w.busy += d * float64(need)
		w.free += need
		w.jobs = remove(w.jobs, u)
		s.running[b.Proj]--
		for v := range seenIn {
			w.have[ident(b, v)] = true
		}
		for _, v := range u.nodes {
			w.have[ident(b, v)] = true
		}
		u.state = 3
		prom := p.PromBase + p.PromPer*float64(len(u.nodes)) + upB/p.PromBW
		s.req.add(Req{Put: 4 * float64(len(u.nodes)), Get: 2*float64(len(u.nodes)) + 2})
		s.at(s.t+prom, func() {
			for _, v := range u.nodes {
				s.cache[ident(b, v)] = true
			}
			if u.batch != nil {
				for _, x := range u.batch {
					s.finish(x)
				}
				return
			}
			s.finish(u)
		})
		s.workerIdle(w)
		if p.Push {
			s.dispatch()
		}
	})
}

func remove(js []*Unit, u *Unit) []*Unit {
	for i, x := range js {
		if x == u {
			return append(js[:i], js[i+1:]...)
		}
	}
	return js
}

// workerIdle: after a job the worker re-polls at once (S9); otherwise every PollI.
func (s *Sim) workerIdle(w *Worker) {
	if s.p.Push || !w.alive {
		return
	}
	s.poll(w)
}

func (s *Sim) poll(w *Worker) {
	if !w.alive {
		return
	}
	s.pollReq++ // one LIST
	for w.free > 0 && len(s.queue) > 0 {
		if !s.take(w) {
			break
		}
	}
	if w.polling {
		return
	}
	w.polling = true
	next := s.t + s.p.PollI*(0.8+0.4*s.rng.Float64())
	s.at(next, func() {
		w.polling = false
		if s.allDone() {
			return
		}
		s.poll(w)
	})
}

func (s *Sim) allDone() bool {
	for _, b := range s.builds {
		if b.left > 0 || b.done == 0 {
			return false
		}
	}
	return true
}

func (s *Sim) kill(wi int, t float64) {
	s.at(t, func() {
		w := s.ws[wi]
		w.alive = false
		for _, u := range w.jobs {
			s.wasted += (s.t - u.start) * float64(u.need)
			s.running[u.b.Proj]--
			if u.batch != nil {
				for _, x := range u.batch {
					xx := x
					s.at(s.t+s.p.LeaseTTL-s.p.LeaseTTL/6+s.p.Reconcile/2, func() {
						xx.state = 1
						xx.readyAt = s.t
						s.queue = append(s.queue, xx)
						s.req.add(Req{Put: 3, Get: 2})
						s.dispatch()
					})
				}
				continue
			}
			uu := u
			// lease expires (TTL after the last renewal, on average TTL - TTL/6),
			// the reconciler requeues on its next pass (on average Reconcile/2)
			s.at(s.t+s.p.LeaseTTL-s.p.LeaseTTL/6+s.p.Reconcile/2, func() {
				uu.state = 1
				uu.readyAt = s.t
				s.queue = append(s.queue, uu)
				s.req.add(Req{Put: 3, Get: 2})
				s.dispatch()
			})
		}
		w.jobs = nil
	})
}

type Result struct {
	Walls                       []float64
	Makespan                    float64
	Req                         Req
	PollReq                     float64
	Up, Sub, Drv                float64
	Util                        float64
	Wasted                      float64
	Jobs                        int
}

func newSim(mode string, p Params, W int, capCost float64) *Sim {
	s := &Sim{p: p, mode: mode, capCost: capCost, cache: map[string]bool{}, live: map[string]*Unit{}, rng: rand.New(rand.NewSource(1)), running: map[string]int{}}
	for i := 0; i < W; i++ {
		s.ws = append(s.ws, &Worker{id: i, free: p.Slots, alive: true, have: map[string]bool{}, haveDrv: map[string]bool{}})
	}
	return s
}

func (s *Sim) start() {
	for _, w := range s.ws {
		ww := w
		s.at(s.rng.Float64()*s.p.PollI, func() { s.poll(ww) })
	}
}

func (s *Sim) runAll() Result {
	s.start()
	for s.ev.Len() > 0 {
		e := heap.Pop(&s.ev).(*event)
		s.t = e.t
		e.f()
		if s.allDone() && !s.p.Push {
			// drain remaining non-poll events quickly: stop when done
			break
		}
	}
	r := Result{Req: s.req, PollReq: s.pollReq, Up: s.bytesUp, Sub: s.bytesSub, Drv: s.bytesDrv, Wasted: s.wasted, Jobs: s.jobs}
	first, last := math.Inf(1), 0.0
	for _, b := range s.builds {
		r.Walls = append(r.Walls, b.done-b.Arrive)
		first = math.Min(first, b.Arrive)
		last = math.Max(last, b.done)
	}
	r.Makespan = last - first
	busy := 0.0
	for _, w := range s.ws {
		busy += w.busy
	}
	r.Util = busy / (float64(len(s.ws)*s.p.Slots) * r.Makespan)
	return r
}

// capFor: chunk cap = max(30 s, total/(2*W*slots)) bounded at 600 s [proposal].
func capFor(g *graph.Graph, W, slots int) float64 {
	c := g.TotalCost() / float64(2*W)
	return math.Min(600, math.Max(30, c))
}

func mb(x float64) string { return fmt.Sprintf("%.1f", x/(1<<20)) }

func main() {
	scen := flag.String("scen", "all", "cold|inc|death|fair|dup|push|all")
	slotsF := flag.Int("slots", 2, "slots per worker")
	rateF := flag.Float64("rate", 30, "global claims/s")
	flag.Parse()
	p := defaults()
	p.Slots = *slotsF
	p.ClaimRate = *rateF
	modes := []string{"a", "b", "c", "d"}
	fleets := []int{1, 4, 20, 100}
	out := os.Stdout
	do := func(name string) bool { return *scen == "all" || *scen == name }

	if do("cold") || do("push") {
		for _, push := range []bool{false, true} {
			if (push && !do("push")) || (!push && !do("cold")) {
				continue
			}
			pp := p
			pp.Push = push
			fmt.Fprintf(out, "\n## cold build, one project, push=%v (wall s | S3 req excl. polling | $ per build | MiB up/sub | util %% | jobs)\n", push)
			fmt.Fprintf(out, "| workload | drvs | work slot-s | crit path s | W | a | b | c | d |\n|---|---|---|---|---|---|---|---|---|---|\n")
			for _, g := range graph.Workloads() {
				for _, W := range fleets {
					row := fmt.Sprintf("| %s | %d | %.0f | %.1f | %d |", g.Name, len(g.Nodes), g.TotalCost(), g.CriticalPath(p.DrvFixed), W)
					for _, m := range modes {
						s := newSim(m, pp, W, capFor(g, W, pp.Slots))
						s.addBuild(&Build{Proj: "p1", G: g, Ver: "v1", Arrive: 1})
						r := s.runAll()
						row += fmt.Sprintf(" %.1f s · %.0f req · $%.4f · %s/%s MiB · %.0f%% · %d |", r.Walls[0], r.Req.N(), r.Req.Cost(), mb(r.Up), mb(r.Sub+r.Drv), 100*r.Util, r.Jobs)
					}
					fmt.Fprintln(out, row)
				}
			}
		}
	}
	if do("inc") {
		fmt.Fprintf(out, "\n## incremental: cold build, then one base node changed (W=4 / 20), affinity on|off\n")
		fmt.Fprintf(out, "| workload | W | affinity | a wall · MiB sub | b wall · MiB sub | c wall · MiB sub |\n|---|---|---|---|---|---|\n")
		for _, g := range graph.Workloads() {
			for _, W := range []int{4, 20} {
				for _, aff := range []bool{true, false} {
					row := fmt.Sprintf("| %s | %d | %v |", g.Name, W, aff)
					for _, m := range modes {
						pp := p
						pp.Affinity = aff
						s := newSim(m, pp, W, capFor(g, W, pp.Slots))
						s.addBuild(&Build{Proj: "p1", G: g, Ver: "v1", Arrive: 1})
						s.runAll()
						// second build: a new event loop on the same fleet/cache
						if !aff {
							// without affinity the closure job lands on a worker that did not build v1
							for _, w := range s.ws {
								w.have = map[string]bool{}
								w.haveDrv = map[string]bool{}
							}
						}
						t0 := s.t + 10
						s.builds = nil
						s.ev = nil
						s.req = Req{}
						s.bytesSub, s.bytesDrv, s.bytesUp = 0, 0, 0
						changed := map[int]bool{0: true}
						s.addBuild(&Build{Proj: "p1", G: g, Ver: "v2", Base: "v1", Changed: changed, Arrive: t0})
						for _, w := range s.ws {
							w.polling = false
						}
						r := s.runAll()
						row += fmt.Sprintf(" %.1f s · %s MiB |", r.Walls[0], mb(r.Sub+r.Drv))
					}
					fmt.Fprintln(out, row)
				}
			}
		}
	}
	if do("death") {
		fmt.Fprintf(out, "\n## worker death: the worker running the longest job dies at 50 %% of the undisturbed wall time\n")
		fmt.Fprintf(out, "| workload | W | a: wall undisturbed → with death (lost slot-s) | b | c |\n|---|---|---|---|---|\n")
		for _, g := range graph.Workloads() {
			for _, W := range []int{4, 20} {
				row := fmt.Sprintf("| %s | %d |", g.Name, W)
				for _, m := range modes {
					s := newSim(m, p, W, capFor(g, W, p.Slots))
					s.addBuild(&Build{Proj: "p1", G: g, Ver: "v1", Arrive: 1})
					base := s.runAll().Walls[0]
					// find the worker busy at 50 %: run again, kill each candidate, take the worst
					worst := Result{}
					for wi := 0; wi < W; wi++ {
						s2 := newSim(m, p, W, capFor(g, W, p.Slots))
						s2.addBuild(&Build{Proj: "p1", G: g, Ver: "v1", Arrive: 1})
						s2.kill(wi, 1+base/2)
						r := s2.runAll()
						if len(worst.Walls) == 0 || r.Walls[0] > worst.Walls[0] {
							worst = r
						}
					}
					row += fmt.Sprintf(" %.1f → %.1f s (%.0f) |", base, worst.Walls[0], worst.Wasted)
				}
				fmt.Fprintln(out, row)
			}
		}
	}
	if do("fair") {
		fmt.Fprintf(out, "\n## fairness: p1 starts rust129 at t=0; p2..p5 each start the fixture at t=5 s\n")
		fmt.Fprintf(out, "| W | policy | a: rust129 / fixture mean (max) | b | c |\n|---|---|---|---|---|\n")
		for _, W := range []int{4, 20} {
			for _, fair := range []bool{false, true} {
				row := fmt.Sprintf("| %d | %s |", W, map[bool]string{true: "fair share", false: "FIFO"}[fair])
				for _, m := range modes {
					pp := p
					pp.Fair = fair
					s := newSim(m, pp, W, 30)
					big := graph.Rust(129, 300, 2.2, 2)
					s.addBuild(&Build{Proj: "p1", G: big, Ver: "v1", Arrive: 0.001})
					for i := 2; i <= 5; i++ {
						s.addBuild(&Build{Proj: fmt.Sprintf("p%d", i), G: graph.Fixture(), Ver: fmt.Sprintf("v%d", i), Arrive: 5})
					}
					r := s.runAll()
					sum, mx := 0.0, 0.0
					for _, w := range r.Walls[1:] {
						sum += w
						mx = math.Max(mx, w)
					}
					row += fmt.Sprintf(" %.0f / %.1f (%.1f) s |", r.Walls[0], sum/4, mx)
				}
				fmt.Fprintln(out, row)
			}
		}
	}
	if do("dup") {
		fmt.Fprintf(out, "\n## two builds of the same commit 2 s apart (e.g. push + PR), W=20: wall of 2nd build, total slot-s used\n")
		fmt.Fprintf(out, "| workload | a | b | c |\n|---|---|---|---|\n")
		for _, g := range graph.Workloads() {
			row := fmt.Sprintf("| %s |", g.Name)
			for _, m := range modes {
				s := newSim(m, p, 20, capFor(g, 20, p.Slots))
				s.addBuild(&Build{Proj: "p1", G: g, Ver: "v1", Arrive: 1})
				s.addBuild(&Build{Proj: "p1", G: g, Ver: "v1", Arrive: 3})
				r := s.runAll()
				busy := 0.0
				for _, w := range s.ws {
					busy += w.busy
				}
				row += fmt.Sprintf(" %.1f s · %.0f slot-s |", r.Walls[1], busy)
			}
			fmt.Fprintln(out, row)
		}
	}
	if do("chunks") {
		fmt.Fprintf(out, "\n## chunking: number of chunks and convexity per workload and cap\n")
		for _, g := range graph.Workloads() {
			for _, W := range fleets {
				c := capFor(g, W, p.Slots)
				cs := graph.ChunkGraph(g, c, p.DrvFixed)
				fmt.Fprintf(out, "%s W=%d cap=%.0f chunks=%d convex=%v\n", g.Name, W, c, len(cs), graph.Convex(cs))
			}
		}
	}
	if do("steady") {
		// steady-state request rates for W workers: polling + renewals (S9 model)
		fmt.Fprintf(out, "\n## steady state, AWS-priced $/month for polling alone (W/I LIST/s)\n")
		for _, W := range fleets {
			lps := float64(W) / p.PollI
			fmt.Fprintf(out, "W=%d: %.1f LIST/s = $%.0f/month\n", W, lps, lps*pPut*86400*30)
		}
	}
}
