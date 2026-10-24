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
	rec, _, err := store.GetCached(ctx, p.cfg.Control, &p.ppl, &PipelinesRecord{Project: store.ID(project)})
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
	case rec.Commit.Value() != "":
		return rec.Commit.Value()
	case rec.Source.Value().SHA != "":
		return rec.Source.Value().SHA
	}
	return rec.Source.Value().Upload
}

// onDefault reports whether build rec ran on the project's default branch.
func (p *Projects) onDefault(ctx context.Context, rec *store.BuildRecord) bool {
	def := "main"
	if pr, err := p.Get(ctx, rec.Project.Value()); err == nil && pr.DefaultBranch.Value() != "" {
		def = pr.DefaultBranch.Value()
	}
	ref := rec.Trigger.Value().Ref
	if ref == "" {
		ref = rec.Source.Value().Ref
	}
	if ref == "" && rec.Branch.Value() != "" {
		ref = "refs/heads/" + rec.Branch.Value()
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
	err := store.Change(ctx, p.cfg.Control, &PipelinesRecord{Project: store.ID(rec.Project.Value())}, func(cur *PipelinesRecord) (*PipelinesRecord, error) {
		next := PipelinesRecord{Project: store.ID(rec.Project.Value())}
		last := map[string]LastRun{}
		if cur != nil {
			next = *cur
			maps.Copy(last, cur.Last.Value())
		}
		changed := false
		if cur == nil || def && (!cur.Default.Value() || rec.Number.Value() >= cur.N.Value()) || !def && !cur.Default.Value() && rec.Number.Value() >= cur.N.Value() {
			next.SHA, next.Build, next.N, next.Default, next.At, next.Defs = store.Set(sourceSHA(rec)), store.Set(rec.ID.Value()), store.Set(rec.Number.Value()), store.Set(def), store.Set(p.now()), store.Set(defs)
			changed = true
		}
		for _, name := range r.Matched {
			changed = setLast(last, name, LastRun{Build: rec.ID.Value(), N: rec.Number.Value(), State: "running"}) || changed
		}
		next.Last = store.Set(last)
		if !changed {
			return nil, store.ErrNoChange
		}
		return &next, nil
	})
	return err
}

// Finished records the final state of every pipeline run of a terminal build.
func (p *Projects) Finished(ctx context.Context, rec *store.BuildRecord) error {
	if len(rec.Pipelines.Value()) == 0 {
		return nil
	}
	err := store.Change(ctx, p.cfg.Control, &PipelinesRecord{Project: store.ID(rec.Project.Value())}, func(cur *PipelinesRecord) (*PipelinesRecord, error) {
		next := PipelinesRecord{Project: store.ID(rec.Project.Value())}
		last := map[string]LastRun{}
		if cur != nil {
			next = *cur
			maps.Copy(last, cur.Last.Value())
		}
		changed := false
		for _, run := range rec.Pipelines.Value() {
			changed = setLast(last, run.Name, LastRun{Build: rec.ID.Value(), N: rec.Number.Value(), State: run.State}) || changed
		}
		next.Last = store.Set(last)
		if !changed {
			return nil, store.ErrNoChange
		}
		return &next, nil
	})
	return err
}

// setLast makes run the last build of pipeline name in last unless a newer build already is.
func setLast(last map[string]LastRun, name string, run LastRun) bool {
	if name == "" {
		return false
	}
	name = clip(name)
	old, ok := last[name]
	if ok && (old.N > run.N || old == run) {
		return false
	}
	if !ok && len(last) >= maxPipelines {
		// drop the pipeline whose last build is the oldest
		oldest := slices.MinFunc(slices.Collect(maps.Keys(last)), func(a, b string) int {
			return cmp.Compare(last[a].N, last[b].N)
		})
		delete(last, oldest)
	}
	last[name] = run
	return true
}

// stateName is the BuildState enum name of a BuildRecord state ("running" → BUILD_STATE_RUNNING).
func stateName(s string) string {
	if s == "" {
		return ""
	}
	return "BUILD_STATE_" + strings.ToUpper(s)
}
