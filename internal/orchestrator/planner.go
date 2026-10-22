package orchestrator

import (
	"container/heap"
	"fmt"
	"math"
	"sort"
)

// PlanNode is one derivation of a build graph: its dependencies (indexes of earlier nodes) and
// the build time on one slot in seconds.
type PlanNode struct {
	Deps []int
	Cost float64
}

// PlanInput is what the build controller knows when it plans a build (SPEC R-FLEET-005, S14 §5.1).
type PlanInput struct {
	Nodes          []PlanNode
	IdleWorkers    int // eligible workers with a fresh heartbeat and a free slot
	SlotsPerWorker int
}

// PlanResult is the planner's decision.
type PlanResult struct {
	Split      bool    // false: one evalbuild closure job
	ClosureS   float64 // predicted wall time as one closure job
	SplitS     float64 // predicted wall time with the batches (== ClosureS when !Split)
	Batches    [][]int // node indexes per batch job, each at most 64
	EvalJob    bool    // an eval job precedes the batches
	MaxPerJob  int
	QueuedJobs int // batch jobs queued at once, at most 2 × eligible workers
}

// The S14 planner model (docs/spikes/s14-sched.md §5.1, ADR 0016 amended 2026-10-22).
const (
	closurePerDrvS = 0.14 // Nix start, sandbox and register per derivation inside a closure job
	splitPerLevelS = 1.0  // job hop per graph level in a split build (claim, fetch, upload, promote)
	splitPerDrvS   = 0.6  // per-derivation cost of a split build (substitution, upload)
	minGainS       = 10.0 // split only for an absolute gain of at least 10 s …
	minGainFrac    = 0.3  // … and of at least 30 % of the closure time
	MaxBatch       = 64   // derivations per job (R-FLEET-005)
	maxBatchSlotS  = 60.0 // estimated slot-seconds per batch job
	maxPlanNodes   = 10000
	tolerance      = 1e-9 // a gain exactly at a bound splits
)

// Plan decides between the closure job and eval plus ready-set batches:
//
//	T1     = list-schedule makespan on S slots + 0.14 s per drv
//	Tsplit = max(critical path + depth × 1.0 s, (Σ est + 0.6 s × drvs) / (S × max(I,1)))
//	split  = I ≥ 2 and T1 − Tsplit ≥ max(10 s, 0.3 × T1)
func Plan(in PlanInput) (PlanResult, error) {
	n := len(in.Nodes)
	if n > maxPlanNodes {
		return PlanResult{}, fmt.Errorf("orchestrator: build graph has %d derivations, the planner limit is %d", n, maxPlanNodes)
	}
	sum := 0.0
	for i, nd := range in.Nodes {
		if nd.Cost < 0 || math.IsNaN(nd.Cost) || math.IsInf(nd.Cost, 0) {
			return PlanResult{}, fmt.Errorf("orchestrator: derivation %d has cost %v", i, nd.Cost)
		}
		for _, d := range nd.Deps {
			if d < 0 || d >= i {
				return PlanResult{}, fmt.Errorf("orchestrator: derivation %d depends on %d: the graph must be in topological order", i, d)
			}
		}
		sum += nd.Cost
	}
	slots, idle := max(in.SlotsPerWorker, 1), max(in.IdleWorkers, 0)
	bottom := bottomLevels(in.Nodes)
	t1 := listSchedule(in.Nodes, bottom, slots) + closurePerDrvS*float64(n)
	cp, depth := criticalPath(in.Nodes)
	tsplit := math.Max(cp+float64(depth)*splitPerLevelS, (sum+splitPerDrvS*float64(n))/float64(slots*max(idle, 1)))
	res := PlanResult{ClosureS: t1, SplitS: t1, MaxPerJob: MaxBatch}
	if idle < 2 || t1-tsplit < math.Max(minGainS, minGainFrac*t1)-tolerance {
		return res, nil
	}
	res.Split, res.SplitS, res.EvalJob = true, tsplit, true
	res.Batches, res.QueuedJobs = batches(in.Nodes, bottom, idle)
	return res, nil
}

// bottomLevels is, per node, the cost of the longest path from it to a sink (inclusive).
func bottomLevels(nodes []PlanNode) []float64 {
	succ := make([][]int, len(nodes))
	for i, nd := range nodes {
		for _, d := range nd.Deps {
			succ[d] = append(succ[d], i)
		}
	}
	bl := make([]float64, len(nodes))
	for i := len(nodes) - 1; i >= 0; i-- {
		m := 0.0
		for _, s := range succ[i] {
			m = math.Max(m, bl[s])
		}
		bl[i] = m + nodes[i].Cost
	}
	return bl
}

// criticalPath returns the longest path by cost and the longest path by node count (depth).
func criticalPath(nodes []PlanNode) (cost float64, depth int) {
	c := make([]float64, len(nodes))
	d := make([]int, len(nodes))
	for i, nd := range nodes {
		for _, p := range nd.Deps {
			c[i] = math.Max(c[i], c[p])
			d[i] = max(d[i], d[p])
		}
		c[i] += nd.Cost
		d[i]++
		cost, depth = math.Max(cost, c[i]), max(depth, d[i])
	}
	return cost, depth
}

// listSchedule is the makespan of the graph on `slots` slots, starting ready nodes by bottom
// level (longest remaining path first) whenever a slot is free.
func listSchedule(nodes []PlanNode, bottom []float64, slots int) float64 {
	n := len(nodes)
	waiting := make([]int, n)
	succ := make([][]int, n)
	for i, nd := range nodes {
		waiting[i] = len(nd.Deps)
		for _, d := range nd.Deps {
			succ[d] = append(succ[d], i)
		}
	}
	ready := &prio{bottom: bottom}
	for i := range nodes {
		if waiting[i] == 0 {
			heap.Push(ready, i)
		}
	}
	running := &finishes{}
	now, free := 0.0, slots
	for ready.Len() > 0 || running.Len() > 0 {
		for free > 0 && ready.Len() > 0 {
			i := heap.Pop(ready).(int)
			heap.Push(running, event{at: now + nodes[i].Cost, node: i})
			free--
		}
		ev := heap.Pop(running).(event)
		now, free = ev.at, free+1
		for _, s := range succ[ev.node] {
			if waiting[s]--; waiting[s] == 0 {
				heap.Push(ready, s)
			}
		}
	}
	return now
}

// batches cuts the graph into ready-set waves (all inputs of a wave are built by earlier
// waves), each wave ordered by bottom level and cut into jobs of k = ceil(|ready| / idle)
// derivations, at most 64 and at most 60 estimated slot-seconds per job. queued is the number
// of batch jobs of the first wave queued at once (fair share: at most 2 × idle workers).
func batches(nodes []PlanNode, bottom []float64, idle int) (out [][]int, queued int) {
	n := len(nodes)
	wave := make([]int, n)
	waves := 0
	for i, nd := range nodes {
		for _, d := range nd.Deps {
			wave[i] = max(wave[i], wave[d]+1)
		}
		waves = max(waves, wave[i]+1)
	}
	byWave := make([][]int, waves)
	for i := range nodes {
		byWave[wave[i]] = append(byWave[wave[i]], i)
	}
	for w, ready := range byWave {
		sort.SliceStable(ready, func(a, b int) bool { return bottom[ready[a]] > bottom[ready[b]] })
		k := min(MaxBatch, (len(ready)+idle-1)/idle)
		var cur []int
		est := 0.0
		for _, i := range ready {
			if len(cur) > 0 && (len(cur) == k || est+nodes[i].Cost > maxBatchSlotS) {
				out = append(out, cur)
				cur, est = nil, 0
			}
			cur = append(cur, i)
			est += nodes[i].Cost
		}
		if len(cur) > 0 {
			out = append(out, cur)
		}
		if w == 0 {
			queued = min(len(out), 2*idle)
		}
	}
	return out, queued
}

// prio is a max-heap of node indexes by bottom level (ties: lower index first).
type prio struct {
	items  []int
	bottom []float64
}

func (p *prio) Len() int { return len(p.items) }
func (p *prio) Less(a, b int) bool {
	x, y := p.items[a], p.items[b]
	if p.bottom[x] != p.bottom[y] {
		return p.bottom[x] > p.bottom[y]
	}
	return x < y
}
func (p *prio) Swap(a, b int) { p.items[a], p.items[b] = p.items[b], p.items[a] }
func (p *prio) Push(x any)    { p.items = append(p.items, x.(int)) }
func (p *prio) Pop() any {
	x := p.items[len(p.items)-1]
	p.items = p.items[:len(p.items)-1]
	return x
}

type event struct {
	at   float64
	node int
}

// finishes is a min-heap of running nodes by finish time.
type finishes []event

func (f finishes) Len() int { return len(f) }
func (f finishes) Less(a, b int) bool {
	return f[a].at < f[b].at || (f[a].at == f[b].at && f[a].node < f[b].node)
}
func (f finishes) Swap(a, b int) { f[a], f[b] = f[b], f[a] }
func (f *finishes) Push(x any)   { *f = append(*f, x.(event)) }
func (f *finishes) Pop() any {
	old := *f
	x := old[len(old)-1]
	*f = old[:len(old)-1]
	return x
}
