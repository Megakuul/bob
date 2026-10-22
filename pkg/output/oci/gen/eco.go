package gen

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/megakuul/bob/pkg/engine"

	// The OCI API registers the oci.image kind the IR names.
	_ "github.com/megakuul/bob/pkg/output/oci"
)

func init() { engine.Register(Ecosystem{}) }

// Ecosystem plans OCI images (SPEC §04.5). It has no lock section and no native files.
type Ecosystem struct{}

// Name is the kind prefix "oci".
func (Ecosystem) Name() string { return "oci" }

// Lock has nothing to resolve: images are built from packages other ecosystems pin.
func (Ecosystem) Lock(context.Context, engine.LockRequest) (json.RawMessage, error) { return nil, nil }

// Native writes no files.
func (Ecosystem) Native(context.Context, engine.PlanRequest) ([]engine.GeneratedFile, error) {
	return nil, nil
}

// Plan plans every image node (PlanImage).
func (Ecosystem) Plan(ctx context.Context, req engine.PlanRequest) (res engine.PlanResult, err error) {
	defer func() { // a planner bug must not crash bob
		if v := recover(); v != nil {
			err = fmt.Errorf("oci planner: internal error: %v", v)
		}
	}()
	jobs := engine.NewJobs(req)
	res.Nodes = map[string]engine.Resolved{}
	for _, n := range req.Nodes {
		rv, err := PlanImage(ctx, req, jobs, n, nil)
		if err != nil {
			return engine.PlanResult{}, err
		}
		res.Nodes[n.ID] = rv
	}
	res.Drvs = jobs.Drvs
	return res, nil
}
