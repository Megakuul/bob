package buildctl

import (
	"context"
	"errors"
	"github.com/megakuul/bob/internal/build/jobspec"
	"slices"

	"github.com/megakuul/bob/internal/store"
)

// BuildNodes is `bnodes/<buildID>` (SPEC §05.4.2): the package graph of a build as its
// evaluation reports gave it, per job. Each closure job contributes its packages; a node's
// state is its package status. (The S14 bitmaps of split builds are not needed while every
// build runs closure jobs, decision D-IB11-2.)
type BuildNodes struct {
	ID    store.KeyField          `pk:"bnodes" cbor:"-"`
	ETag  store.ETagField         `etag:"true" cbor:"-"`
	Nodes store.DataField[[]Node] `cbor:"n,omitempty"`
}

// Draft returns a fresh update value with r's keys, ETag and data, for a CAS write.
func (r *BuildNodes) Draft() *BuildNodes {
	return &BuildNodes{ID: r.ID,
		ETag:  r.ETag,
		Nodes: store.Carry(r.Nodes)}
}

// Node is one package of a build graph.
type Node struct {
	Label    string            `cbor:"l"`
	Platform string            `cbor:"p,omitempty"`
	Status   string            `cbor:"s"` // jobspec.PackageReport.Status
	Job      string            `cbor:"j,omitempty"`
	Deps     []int             `cbor:"d,omitempty"` // indexes into Nodes
	Outputs  []string          `cbor:"o,omitempty"`
	Error    *store.BuildError `cbor:"e,omitempty"`
}

// writeNodes replaces the nodes of job in bnodes/<build> with those of report and stores the
// package counts of all nodes in the build record.
func (b *Builds) writeNodes(ctx context.Context, rec *store.BuildRecord, job string, r *jobspec.Report) (*store.BuildRecord, error) {
	bn, err := store.CAS(ctx, b.cfg.Control, &BuildNodes{ID: rec.ID}, func(cur *BuildNodes) (*BuildNodes, error) {
		next := &BuildNodes{ID: rec.ID}
		if cur != nil {
			if slices.ContainsFunc(cur.Nodes.Value(), func(n Node) bool { return n.Job == job }) {
				return nil, store.ErrNoChange
			}
			next.Nodes = store.Set(slices.Clone(cur.Nodes.Value()))
		}
		base := len(next.Nodes.Value())
		if base+len(r.Packages) > jobspec.MaxReportPackages {
			return nil, store.ErrNoChange // the graph view is capped; counts come from what is kept
		}
		for _, p := range r.Packages {
			n := Node{Label: p.Label, Platform: p.Platform, Status: p.Status, Job: job, Outputs: p.Outputs}
			for _, d := range p.Deps {
				n.Deps = append(n.Deps, base+d)
			}
			if p.Error != nil {
				n.Error = &store.BuildError{Code: p.Error.Code, Message: p.Error.Message, Label: p.Error.Label, Hint: p.Error.Hint}
			}
			next.Nodes = store.Set(append(next.Nodes.Value(), n))
		}
		return next, nil
	})
	if err != nil {
		return rec, err
	}
	if bn == nil {
		return rec, nil
	}
	c := countsOf(bn.Nodes.Value())
	return b.update(ctx, rec.ID.Value(), func(cur *store.BuildRecord) error {
		if cur.Counts.Value() == c {
			return store.ErrNoChange
		}
		cur.Counts = store.Set(c)
		return nil
	})
}

// countsOf sums the package states of nodes.
func countsOf(nodes []Node) store.PackageCounts {
	var c store.PackageCounts
	for _, n := range nodes {
		c.Total++
		switch n.Status {
		case "built", "pass":
			c.Built++
		case "cached":
			c.Cached++
		case "failed", "fail":
			c.Failed++
		case "skipped":
			c.Skipped++
		case "cancelled":
			c.Cancelled++
		}
	}
	return c
}

// Nodes reads bnodes/<build> (nil before any evaluation report).
func (b *Builds) Nodes(ctx context.Context, id string) (*BuildNodes, error) {
	n, err := store.Get(ctx, b.cfg.Control, &BuildNodes{ID: store.ID(id)})
	if errors.Is(err, store.ErrNotFound) {
		return nil, nil
	}
	return n, err
}
