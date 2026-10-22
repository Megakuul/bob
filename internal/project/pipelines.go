package project

import (
	"cmp"
	"context"
	"errors"
	"maps"
	"slices"
	"strings"

	"github.com/megakuul/bob/internal/build/jobspec"
	"github.com/megakuul/bob/internal/store"
)

// Bounds of a cached pipeline definition (the report is bounded upstream; these keep the
// cache record small whatever it says).
const (
	maxTriggers = 32
	maxText     = 256
)

// Pipelines reads the pipelines cache of a project (nil when nothing was evaluated yet).
func (p *Projects) Pipelines(ctx context.Context, project string) (*PipelinesRecord, error) {
	rec, _, err := store.GetCached(ctx, p.cfg.Control, &p.ppl, &PipelinesRecord{Project: project})
	if errors.Is(err, store.ErrNotFound) {
		return nil, nil
	}
	return rec, err
}

func clip(s string) string {
	if len(s) > maxText {
		return s[:maxText]
	}
	return s
}

func sourceSHA(rec *store.BuildRecord) string {
	switch {
	case rec.Commit != "":
		return rec.Commit
	case rec.Source.SHA != "":
		return rec.Source.SHA
	}
	return rec.Source.Upload
}

// onDefault reports whether build rec ran on the project's default branch.
func (p *Projects) onDefault(ctx context.Context, rec *store.BuildRecord) bool {
	def := "main"
	if pr, err := p.Get(ctx, rec.Project); err == nil && pr.DefaultBranch != "" {
		def = pr.DefaultBranch
	}
	ref := rec.Trigger.Ref
	if ref == "" {
		ref = rec.Source.Ref
	}
	if ref == "" && rec.Branch != "" {
		ref = "refs/heads/" + rec.Branch
	}
	return ref == "refs/heads/"+def
}

// Evaluated records an accepted evaluation report of build rec (the build controller's hook,
// called once the report passed R-PIPE-003): the pipeline definitions replace the cached
// ones when they come from a newer build of the default branch (or, while no default-branch
// evaluation exists, from any newer build), and the pipelines the build runs get it as their
// last build.
func (p *Projects) Evaluated(ctx context.Context, rec *store.BuildRecord, r *jobspec.Report) error {
	def := p.onDefault(ctx, rec)
	defs := make([]PipelineDef, 0, min(len(r.Pipelines), maxPipelines))
	for _, pl := range r.Pipelines {
		if len(defs) == maxPipelines {
			break
		}
		d := PipelineDef{Name: clip(pl.Name), Concurrency: clip(pl.Concurrency), Releases: len(pl.Releases) > 0}
		for _, t := range pl.Triggers {
			if len(d.Triggers) == maxTriggers {
				break
			}
			d.Triggers = append(d.Triggers, clip(t))
		}
		defs = append(defs, d)
	}
	_, err := store.CAS(ctx, p.cfg.Control, &PipelinesRecord{Project: rec.Project}, func(cur *PipelinesRecord) (*PipelinesRecord, error) {
		next := PipelinesRecord{Project: rec.Project}
		if cur != nil {
			next = *cur
			next.Last = maps.Clone(cur.Last)
		}
		changed := false
		if cur == nil || def && (!cur.Default || rec.Number >= cur.N) || !def && !cur.Default && rec.Number >= cur.N {
			next.SHA, next.Build, next.N, next.Default, next.At, next.Defs = sourceSHA(rec), rec.ID, rec.Number, def, p.now(), defs
			changed = true
		}
		for _, name := range r.Matched {
			changed = setLast(&next, name, LastRun{Build: rec.ID, N: rec.Number, State: "running"}) || changed
		}
		if !changed {
			return nil, store.ErrNoChange
		}
		return &next, nil
	})
	return err
}

// Finished records the final state of every pipeline run of a terminal build.
func (p *Projects) Finished(ctx context.Context, rec *store.BuildRecord) error {
	if len(rec.Pipelines) == 0 {
		return nil
	}
	_, err := store.CAS(ctx, p.cfg.Control, &PipelinesRecord{Project: rec.Project}, func(cur *PipelinesRecord) (*PipelinesRecord, error) {
		next := PipelinesRecord{Project: rec.Project}
		if cur != nil {
			next = *cur
			next.Last = maps.Clone(cur.Last)
		}
		changed := false
		for _, run := range rec.Pipelines {
			changed = setLast(&next, run.Name, LastRun{Build: rec.ID, N: rec.Number, State: run.State}) || changed
		}
		if !changed {
			return nil, store.ErrNoChange
		}
		return &next, nil
	})
	return err
}

// setLast makes run the last build of pipeline name unless a newer build already is.
func setLast(r *PipelinesRecord, name string, run LastRun) bool {
	if name == "" {
		return false
	}
	name = clip(name)
	old, ok := r.Last[name]
	if ok && (old.N > run.N || old == run) {
		return false
	}
	if !ok && len(r.Last) >= maxPipelines {
		// drop the pipeline whose last build is the oldest
		oldest := slices.MinFunc(slices.Collect(maps.Keys(r.Last)), func(a, b string) int {
			return cmp.Compare(r.Last[a].N, r.Last[b].N)
		})
		delete(r.Last, oldest)
	}
	if r.Last == nil {
		r.Last = map[string]LastRun{}
	}
	r.Last[name] = run
	return true
}

// stateName is the BuildState enum name of a BuildRecord state ("running" → BUILD_STATE_RUNNING).
func stateName(s string) string {
	if s == "" {
		return ""
	}
	return "BUILD_STATE_" + strings.ToUpper(s)
}
