package orchestrator

import "github.com/megakuul/bob/pkg/bob/diag"

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

// Plan decides between the closure job and eval plus ready-set batches.
func Plan(in PlanInput) (PlanResult, error) {
	return PlanResult{}, diag.NotImplemented("orchestrator.Plan")
}
