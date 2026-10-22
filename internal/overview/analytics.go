// Package overview serves the read-mostly RPCs of the UI's overview, analytics, cache and
// settings pages (SPEC §6.5.10, §6.5.13, §6.5.16) over the §05 index documents, and the
// placeholders of the release and registry lists until M16.3/M16.4.
package overview

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"connectrpc.com/connect"

	"github.com/megakuul/bob/internal/api"
	buildctl "github.com/megakuul/bob/internal/build"
	analyticsv1 "github.com/megakuul/bob/internal/gen/bob/analytics/v1"
	"github.com/megakuul/bob/internal/gen/bob/analytics/v1/analyticsv1connect"
	buildv1 "github.com/megakuul/bob/internal/gen/bob/build/v1"
	"github.com/megakuul/bob/internal/project"
	"github.com/megakuul/bob/internal/store"
)

// Limits of the analytics RPCs (SPEC §6.5.16).
const (
	maxDays          = 400
	defaultDays      = 30
	maxRunning       = 20
	maxFailures      = 10
	defaultLimit     = 20
	maxLimit         = 100
	maxHistoryPages  = 40  // 2,000 builds per BuildHistory call
	maxHistoryChecks = 500 // build records read for a pipeline filter
	workersTTL       = 30 * time.Second
	headTTL          = 10 * time.Second
)

const (
	methodListBuilds     = "bob.build.v1.BuildService/ListBuilds"
	methodRecentFailures = "bob.analytics.v1.AnalyticsService/RecentFailures"
)

// Analytics is AnalyticsService.
type Analytics struct {
	Builds       *buildctl.Builds
	Control      *store.Bucket
	Fleet        *store.Bucket
	Scope        project.Scoper
	WorkerShards int           // fleet.worker_shards (widx)
	GoneAfter    time.Duration // fleet.gone_after: older heartbeats are stale
	Now          func() time.Time

	running store.Cache[store.RunningBuilds]
	failed  store.Cache[store.FailedBuilds]
	cfg     store.Cache[store.FleetConfig]
	ctrs    store.Cache[store.ProjectCounters]

	mu      sync.Mutex
	queues  map[string]*store.SeqLog[store.QueueSlot]
	heads   map[string]cachedHead
	workers *workerSnapshot
}

type cachedHead struct {
	n  uint64
	at time.Time
}

type workerSnapshot struct {
	at  time.Time
	all map[string]store.WorkerIndexEntry
}

var _ analyticsv1connect.AnalyticsServiceHandler = (*Analytics)(nil)

func (a *Analytics) now() time.Time {
	if a.Now != nil {
		return a.Now().UTC()
	}
	return time.Now().UTC()
}

// days validates a days field: 0 is the default, the maximum is 400.
func days(n int32) (int, error) {
	switch {
	case n == 0:
		return defaultDays, nil
	case n < 0 || n > maxDays:
		return 0, api.InvalidArgument("days", fmt.Sprintf("days must be between 1 and %d", maxDays))
	}
	return int(n), nil
}

// build reads a build record into its API form; a missing record (an index entry ahead of
// its record, or retention) is nil.
func (a *Analytics) build(ctx context.Context, id string) (*buildv1.Build, error) {
	rec, err := a.Builds.Get(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return a.Builds.ToProto(ctx, rec)
}

// GetOverview implements AnalyticsService (§05.5.2: bidx/running, bidx/failed, fleet/cfg and
// the queue tail of each class; the visible builds' records). Builds are filtered to the
// projects the caller may list builds of; fleet numbers are aggregate counts.
func (a *Analytics) GetOverview(ctx context.Context, _ *connect.Request[analyticsv1.GetOverviewRequest]) (*connect.Response[analyticsv1.GetOverviewResponse], error) {
	allowed, err := project.Allowed(ctx, a.Scope, methodListBuilds)
	if err != nil {
		return nil, err
	}
	out := &analyticsv1.GetOverviewResponse{}
	running, _, err := store.GetCached(ctx, a.Control, &a.running, &store.RunningBuilds{Name: store.ID("running")})
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return nil, err
	}
	if running != nil {
		ids := make([]string, 0, len(running.Builds.Value()))
		for id, e := range running.Builds.Value() {
			if allowed(e.Project) {
				ids = append(ids, id)
			}
		}
		slices.Sort(ids)
		slices.Reverse(ids) // ids sort by creation time: newest first
		for _, id := range ids {
			if len(out.Running) == maxRunning {
				break
			}
			rec, err := a.Builds.Get(ctx, id)
			if errors.Is(err, store.ErrNotFound) {
				continue
			}
			if err != nil {
				return nil, err
			}
			if buildctl.Terminal(rec.State.Value()) {
				continue // finished, not yet settled out of bidx/running
			}
			b, err := a.Builds.ToProto(ctx, rec)
			if err != nil {
				return nil, err
			}
			out.Running = append(out.Running, b)
		}
	}
	if out.RecentFailures, err = a.failures(ctx, "", maxFailures, allowed); err != nil {
		return nil, err
	}
	if out.Fleet, out.QueueDepth, err = a.fleet(ctx); err != nil {
		return nil, err
	}
	return connect.NewResponse(out), nil
}

// failures returns up to limit builds of the bidx/failed ring, newest first.
func (a *Analytics) failures(ctx context.Context, projectID string, limit int, allowed func(string) bool) ([]*buildv1.Build, error) {
	ring, _, err := store.GetCached(ctx, a.Control, &a.failed, &store.FailedBuilds{Name: store.ID("failed")})
	if errors.Is(err, store.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []*buildv1.Build
	for i := len(ring.Builds.Value()) - 1; i >= 0 && len(out) < limit; i-- {
		f := ring.Builds.Value()[i]
		if projectID != "" && f.Project != projectID || !allowed(f.Project) {
			continue
		}
		b, err := a.build(ctx, f.Build)
		if err != nil {
			return nil, err
		}
		if b != nil {
			out = append(out, b)
		}
	}
	return out, nil
}

// fleet aggregates the workers per platform (widx, read at most every 30 s) and the queue
// depth per platform (tail − head of each class; heads re-read at most every 10 s, the
// depth is approximate by design).
func (a *Analytics) fleet(ctx context.Context) ([]*analyticsv1.PlatformFleet, int32, error) {
	if a.Fleet == nil {
		return nil, 0, nil
	}
	per := map[string]*analyticsv1.PlatformFleet{}
	get := func(platform string) *analyticsv1.PlatformFleet {
		if per[platform] == nil {
			per[platform] = &analyticsv1.PlatformFleet{Platform: platform}
		}
		return per[platform]
	}
	workers, err := a.workerIndex(ctx)
	if err != nil {
		return nil, 0, err
	}
	now := a.now()
	for _, w := range workers {
		pf := get(w.Platform)
		switch {
		case w.State == "stale" || (w.State == "active" || w.State == "draining") && a.GoneAfter > 0 && !w.Heartbeat.IsZero() && now.Sub(w.Heartbeat) > a.GoneAfter:
			pf.Stale++
		case w.State == "active":
			pf.Active++
			if w.Jobs > 0 {
				pf.Busy++
			}
		}
	}
	cfg, _, err := store.GetCached(ctx, a.Fleet, &a.cfg, store.FleetConfigKey())
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return nil, 0, err
	}
	var depth int32
	if cfg != nil {
		for _, c := range cfg.Classes.Value() {
			d, err := a.depth(ctx, c.ID)
			if err != nil {
				return nil, 0, err
			}
			get(c.Platform).QueuedJobs += d
			depth += d
		}
	}
	out := make([]*analyticsv1.PlatformFleet, 0, len(per))
	for _, pf := range per {
		out = append(out, pf)
	}
	slices.SortFunc(out, func(x, y *analyticsv1.PlatformFleet) int { return cmp.Compare(x.Platform, y.Platform) })
	return out, depth, nil
}

func (a *Analytics) workerIndex(ctx context.Context) (map[string]store.WorkerIndexEntry, error) {
	a.mu.Lock()
	snap := a.workers
	a.mu.Unlock()
	if snap != nil && a.now().Sub(snap.at) < workersTTL {
		return snap.all, nil
	}
	shards := a.WorkerShards
	if shards <= 0 {
		shards = 4
	}
	all, err := store.WorkerIndex(a.Fleet, shards).All(ctx)
	if err != nil {
		return nil, err
	}
	a.mu.Lock()
	a.workers = &workerSnapshot{at: a.now(), all: all}
	a.mu.Unlock()
	return all, nil
}

// depth is tail − head of the queue of class.
func (a *Analytics) depth(ctx context.Context, class string) (int32, error) {
	a.mu.Lock()
	if a.queues == nil {
		a.queues, a.heads = map[string]*store.SeqLog[store.QueueSlot]{}, map[string]cachedHead{}
	}
	q, ok := a.queues[class]
	if !ok {
		q = store.Queue(a.Fleet, class)
		a.queues[class] = q
	}
	h, fresh := a.heads[class]
	a.mu.Unlock()
	tail, err := q.Tail(ctx)
	if err != nil {
		return 0, err
	}
	if !fresh || a.now().Sub(h.at) >= headTTL {
		n, err := q.Head(ctx)
		if err != nil {
			return 0, err
		}
		h = cachedHead{n: n, at: a.now()}
		a.mu.Lock()
		a.heads[class] = h
		a.mu.Unlock()
	}
	if tail <= h.n {
		return 0, nil
	}
	return int32(min(tail-h.n, 1<<30)), nil
}

// PackageDurations implements AnalyticsService. No per-package timing data is recorded yet
// (the analytics tables of the lake have no writer): the answer is valid and empty.
func (a *Analytics) PackageDurations(_ context.Context, req *connect.Request[analyticsv1.PackageDurationsRequest]) (*connect.Response[analyticsv1.PackageDurationsResponse], error) {
	if _, err := days(req.Msg.GetDays()); err != nil {
		return nil, err
	}
	if l := req.Msg.GetLabel(); len(l) > 1024 || strings.ContainsAny(l, "\x00\n") {
		return nil, api.InvalidArgument("label", "invalid label")
	}
	return connect.NewResponse(&analyticsv1.PackageDurationsResponse{}), nil
}

// RecentFailures implements AnalyticsService: the bidx/failed ring, newest first, filtered to
// the projects the caller may call RecentFailures on.
func (a *Analytics) RecentFailures(ctx context.Context, req *connect.Request[analyticsv1.RecentFailuresRequest]) (*connect.Response[analyticsv1.RecentFailuresResponse], error) {
	limit := int(req.Msg.GetLimit())
	switch {
	case limit == 0:
		limit = defaultLimit
	case limit < 0 || limit > maxLimit:
		return nil, api.InvalidArgument("limit", fmt.Sprintf("limit must be between 1 and %d", maxLimit))
	}
	allowed, err := project.Allowed(ctx, a.Scope, methodRecentFailures)
	if err != nil {
		return nil, err
	}
	bs, err := a.failures(ctx, req.Msg.GetProjectId(), limit, allowed)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&analyticsv1.RecentFailuresResponse{Builds: bs}), nil
}

// dayStats collects one day of BuildHistory.
type dayStats struct {
	day                          string
	succeeded, failed, cancelled int32
	durs                         []float64
}

// BuildHistory implements AnalyticsService: per UTC day the terminal builds of a project by
// outcome and their p50/p90 duration, from the build pages (newest first, at most 2,000
// builds; with a pipeline filter at most 500 build records are read).
func (a *Analytics) BuildHistory(ctx context.Context, req *connect.Request[analyticsv1.BuildHistoryRequest]) (*connect.Response[analyticsv1.BuildHistoryResponse], error) {
	m := req.Msg
	n, err := days(m.GetDays())
	if err != nil {
		return nil, err
	}
	if p := m.GetPipeline(); len(p) > 64 || strings.ContainsAny(p, "\x00\n/ ") {
		return nil, api.InvalidArgument("pipeline", "invalid pipeline name")
	}
	if !project.ValidID(m.GetProjectId()) {
		return nil, api.NotFound("project")
	}
	now := a.now()
	cutoff := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC).AddDate(0, 0, -(n - 1))
	ctr, _, err := store.GetCached(ctx, a.Control, &a.ctrs, &store.ProjectCounters{Project: store.ID(m.GetProjectId())})
	if errors.Is(err, store.ErrNotFound) {
		return connect.NewResponse(&analyticsv1.BuildHistoryResponse{}), nil
	}
	if err != nil {
		return nil, err
	}
	pages := store.BuildPages(a.Control, m.GetProjectId())
	stats := map[string]*dayStats{}
	checks := 0
	done := false
	for k, read := int64(pages.LastPage(ctr.Builds.Value())), 0; k >= 0 && read < maxHistoryPages && !done; k, read = k-1, read+1 {
		page, err := pages.Page(ctx, uint64(k))
		if errors.Is(err, store.ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		for i := len(page) - 1; i >= 0; i-- {
			e := page[i]
			if e.Build == "" {
				continue
			}
			if e.Created.Before(cutoff) {
				done = true
				break
			}
			if !buildctl.Terminal(e.State) {
				continue
			}
			if m.GetPipeline() != "" {
				if checks == maxHistoryChecks {
					done = true
					break
				}
				checks++
				rec, err := a.Builds.Get(ctx, e.Build)
				if errors.Is(err, store.ErrNotFound) {
					continue
				}
				if err != nil {
					return nil, err
				}
				if !slices.ContainsFunc(rec.Pipelines.Value(), func(p store.PipelineRun) bool { return p.Name == m.GetPipeline() }) {
					continue
				}
			}
			day := e.Created.UTC().Format(time.DateOnly)
			s := stats[day]
			if s == nil {
				s = &dayStats{day: day}
				stats[day] = s
			}
			switch e.State {
			case buildctl.StateSucceeded:
				s.succeeded++
			case buildctl.StateFailed:
				s.failed++
			case buildctl.StateCancelled, buildctl.StateSuperseded:
				s.cancelled++
			}
			if e.Duration > 0 {
				s.durs = append(s.durs, e.Duration.Seconds())
			}
		}
	}
	out := &analyticsv1.BuildHistoryResponse{}
	for _, s := range stats {
		slices.Sort(s.durs)
		out.Days = append(out.Days, &analyticsv1.HistoryDay{Day: s.day, Succeeded: s.succeeded, Failed: s.failed,
			Cancelled: s.cancelled, P50DurationS: quantile(s.durs, 0.5), P90DurationS: quantile(s.durs, 0.9)})
	}
	slices.SortFunc(out.Days, func(x, y *analyticsv1.HistoryDay) int { return cmp.Compare(x.Day, y.Day) })
	return connect.NewResponse(out), nil
}

// quantile is the nearest-rank quantile of sorted values (0 for none).
func quantile(sorted []float64, q float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	i := int(q*float64(len(sorted))+0.999999) - 1
	return sorted[max(0, min(i, len(sorted)-1))]
}
