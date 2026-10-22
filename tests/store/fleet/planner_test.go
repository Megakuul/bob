package fleet

import (
	"math"
	"testing"

	"github.com/megakuul/bob/internal/orchestrator"
	"github.com/megakuul/bob/tests/parity"
	"github.com/megakuul/bob/tests/store/env"
	"github.com/megakuul/bob/tests/store/fleet/s14graph"
)

func planInput(g *s14graph.Graph, workers, slots int) orchestrator.PlanInput {
	in := orchestrator.PlanInput{IdleWorkers: workers, SlotsPerWorker: slots}
	for _, n := range g.Nodes {
		in.Nodes = append(in.Nodes, orchestrator.PlanNode{Deps: n.Deps, Cost: n.Cost})
	}
	return in
}

func within(got, want, frac float64) bool { return math.Abs(got-want) <= frac*want }

// TestRFLEET005Planner runs the planner on the S14 graphs (rule, ruling G2: split = I >= 2 and
// T1 - Tsplit >= max(10 s, 0.3 x T1)): the fixture is not split; go200,
// rust129 and wide2000 at W = 100 workers with 2 slots are split, and every decision equals the
// one the same rule takes on the S14 simulation's times (docs/spikes/s14-sched.md §3.2, W = 100,
// 2-slot workers: closure "a" and best split). The formula's times are not compared with the
// simulation's: they differ by more than 25 % (SPEC R-FLEET-005 acceptance, amended 2026-10-22).
func TestRFLEET005Planner(t *testing.T) {
	parity.Covers(t, "R-FLEET-005")
	cases := []struct {
		g          *s14graph.Graph
		split      bool
		closureRef float64 // S14 mode a, seconds
		splitRef   float64 // S14 best split mode, seconds
	}{
		{s14graph.Fixture(), false, 14.7, 19.4},
		{s14graph.Go200(), true, 30.0, 16.4},
		{s14graph.Rust(129, 300, 2.2, 2), true, 327.4, 217.8},
		{s14graph.Wide2000(), true, 223.6, 16.6},
	}
	for _, c := range cases {
		res, err := orchestrator.Plan(planInput(c.g, 100, 2))
		if env.Red(t, err) {
			return
		}
		if err != nil {
			t.Errorf("%s: %v", c.g.Name, err)
			continue
		}
		if res.Split != c.split {
			t.Errorf("%s at W=100, 2 slots: Split = %v, want %v (closure %.1f s, split %.1f s)", c.g.Name, res.Split, c.split, res.ClosureS, res.SplitS)
		}
		simSplit := c.closureRef-c.splitRef >= math.Max(10, 0.3*c.closureRef)
		if simSplit != c.split {
			t.Fatalf("harness error: %s: the S14 times %.1f s / %.1f s give split %v, the table says %v", c.g.Name, c.closureRef, c.splitRef, simSplit, c.split)
		}
		if res.Split != simSplit {
			t.Errorf("%s: Split = %v, the S14 simulation (closure %.1f s, split %.1f s) decides %v", c.g.Name, res.Split, c.closureRef, c.splitRef, simSplit)
		}
		if !c.split {
			if len(res.Batches) != 0 || res.EvalJob {
				t.Errorf("%s: a closure build has no eval job and no batches, got %d batches (eval %v)", c.g.Name, len(res.Batches), res.EvalJob)
			}
			continue
		}
		// Split plan: an eval job, batches of at most 64 derivations that partition the graph,
		// and at most 2 x eligible workers batch jobs queued at once.
		if !res.EvalJob {
			t.Errorf("%s: a split build starts with an eval job", c.g.Name)
		}
		seen := make([]int, len(c.g.Nodes))
		for _, b := range res.Batches {
			if len(b) == 0 || len(b) > 64 {
				t.Errorf("%s: a batch has %d derivations, want 1..64", c.g.Name, len(b))
			}
			for _, n := range b {
				if n < 0 || n >= len(seen) {
					t.Fatalf("%s: batch names node %d outside the graph", c.g.Name, n)
				}
				seen[n]++
			}
		}
		for n, k := range seen {
			if k != 1 {
				t.Errorf("%s: node %d is in %d batches, want exactly 1", c.g.Name, n, k)
				break
			}
		}
		if res.QueuedJobs > 2*100 {
			t.Errorf("%s: %d batch jobs queued at once, the fair-share limit is 2 x eligible workers = 200", c.g.Name, res.QueuedJobs)
		}
	}
	// One idle worker: every split is slower than the closure (S14: "with 1 worker every split
	// mode is slower"), so nothing is split.
	for _, g := range []*s14graph.Graph{s14graph.Go200(), s14graph.Wide2000()} {
		res, err := orchestrator.Plan(planInput(g, 1, 2))
		if err != nil || res.Split {
			t.Errorf("%s with one idle worker: %+v, %v; want no split", g.Name, res, err)
		}
	}
}

// model is the S14 planner model (docs/spikes/s14-sched.md §5.1), written out for graphs of
// independent equal-cost nodes (no dependencies: critical path = cost, depth = 1):
//
//	T1     = list-schedule makespan on S slots + 0.14 s per drv
//	Tsplit = max(critical path + depth x 1.0 s, (sum est + 0.6 s x drvs) / (S x max(I,1)))
//	split  = I >= 2 and T1 - Tsplit >= max(10 s, 0.3 x T1)       (ruling G2)
func model(n int, cost float64, slots, idle int) (t1, tsplit, threshold float64) {
	t1 = math.Ceil(float64(n)/float64(slots))*cost + 0.14*float64(n)
	tsplit = math.Max(cost+1.0, (float64(n)*cost+0.6*float64(n))/float64(slots*max(idle, 1)))
	return t1, tsplit, math.Max(10, 0.3*t1)
}

func independent(n int, cost float64) orchestrator.PlanInput {
	in := orchestrator.PlanInput{}
	for i := 0; i < n; i++ {
		in.Nodes = append(in.Nodes, orchestrator.PlanNode{Cost: cost})
	}
	return in
}

// TestRFLEET005PlannerBoundaries pins both thresholds of the split rule at, just below and just
// above the limit (the planner compares with a 1e-9 s tolerance, so "at" splits). The inputs are
// built from the model above: absolute rule rows have gain 9.9 / 10.0 / 10.1 s (T1 = 11.9..13.5 s,
// so 10 s binds); relative rule rows have gain 29.8 / 30.0 / 31.5 % of T1 (gain > 10 s, so 30 %
// binds).
func TestRFLEET005PlannerBoundaries(t *testing.T) {
	parity.Covers(t, "R-FLEET-005")
	cases := []struct {
		name        string
		n           int
		cost        float64
		slots, idle int
		split       bool
		gain        float64 // expected model gain (harness self-check)
	}{
		// 10 equal nodes, 2 slots, 100 idle workers: gain = 4 x cost + 0.4 s.
		{"gain 9.9 s (below 10 s)", 10, 2.375, 2, 100, false, 9.9},
		{"gain 10.0 s (at 10 s)", 10, 2.4, 2, 100, true, 10.0},
		{"gain 10.1 s (above 10 s)", 10, 2.425, 2, 100, true, 10.1},
		// 40 equal nodes, 1 slot, 2 idle workers.
		{"gain 29.8 % (below 30 %)", 40, 1.0, 1, 2, false, 13.6},
		{"gain 30.0 % (at 30 %)", 40, 1.01, 1, 2, true, 13.8},
		{"gain 31.5 % (above 30 %)", 40, 1.1, 1, 2, true, 15.6},
	}
	for _, c := range cases {
		t1, ts, thr := model(c.n, c.cost, c.slots, c.idle)
		if math.Abs(t1-ts-c.gain) > 1e-9 || (t1-ts >= thr-1e-9) != c.split {
			t.Fatalf("harness error: %s: model gain %.9f s, threshold %.9f s, split %v", c.name, t1-ts, thr, c.split)
		}
		in := independent(c.n, c.cost)
		in.IdleWorkers, in.SlotsPerWorker = c.idle, c.slots
		res, err := orchestrator.Plan(in)
		if env.Red(t, err) {
			return
		}
		if err != nil {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		if res.Split != c.split {
			t.Errorf("%s: Split = %v, want %v (model: T1 %.3f s, Tsplit %.3f s, gain %.3f s, threshold %.3f s)", c.name, res.Split, c.split, t1, ts, t1-ts, thr)
		}
		if !within(res.ClosureS, t1, 0.001) {
			t.Errorf("%s: predicted closure time %.3f s, the S14 formula gives %.3f s", c.name, res.ClosureS, t1)
		}
	}
	// I >= 2 is part of the rule: one idle worker never splits.
	in := independent(40, 1.01)
	in.IdleWorkers, in.SlotsPerWorker = 1, 1
	if res, err := orchestrator.Plan(in); err != nil || res.Split {
		t.Errorf("one idle worker: %+v, %v; want no split", res, err)
	}
}
