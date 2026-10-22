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

// TestRFLEET005Planner runs the planner on the S14 graphs: the fixture is not split; go200,
// rust129 and wide2000 at W = 100 workers with 2 slots are split, and the predicted wall times
// are within 25 % of the S14 model (docs/spikes/s14-sched.md §3.2, W = 100, 2-slot workers:
// closure "a" and best split).
func TestRFLEET005Planner(t *testing.T) {
	parity.Covers(t, "R-FLEET-005")
	cases := []struct {
		g          *s14graph.Graph
		split      bool
		closureRef float64 // S14 mode a, seconds
		splitRef   float64 // S14 best split mode, seconds (0: not split)
	}{
		{s14graph.Fixture(), false, 14.7, 0},
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
		if !within(res.ClosureS, c.closureRef, 0.25) {
			t.Errorf("%s: predicted closure time %.1f s is not within 25%% of the S14 model's %.1f s", c.g.Name, res.ClosureS, c.closureRef)
		}
		if c.split && !within(res.SplitS, c.splitRef, 0.25) {
			t.Errorf("%s: predicted split time %.1f s is not within 25%% of the S14 model's %.1f s", c.g.Name, res.SplitS, c.splitRef)
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
