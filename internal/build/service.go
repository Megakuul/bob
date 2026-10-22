package buildctl

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/megakuul/bob/internal/api"
	"github.com/megakuul/bob/internal/fleet"
	commonv1 "github.com/megakuul/bob/internal/gen/bob/common/v1"
	buildv1 "github.com/megakuul/bob/internal/gen/bob/build/v1"
	"github.com/megakuul/bob/internal/gen/bob/build/v1/buildv1connect"
	"github.com/megakuul/bob/internal/orchestrator"
	"github.com/megakuul/bob/internal/store"
)

// Service is BuildService and JobService (SPEC §6.5.7) over Builds.
type Service struct {
	B     *Builds
	Authz api.Authorizer // LIST_FILTERED scope of ListBuilds; nil = admin bindings
	// WatchEvery is the poll period of WatchBuild (1 s).
	WatchEvery time.Duration
}

var (
	_ buildv1connect.BuildServiceHandler = (*Service)(nil)
	_ buildv1connect.JobServiceHandler   = (*Service)(nil)
)

const methodListBuilds = "bob.build.v1.BuildService/ListBuilds"

// apiError maps controller errors to the §6.1 error model.
func apiError(err error, what string) error {
	var inv *InvalidError
	switch {
	case err == nil:
		return nil
	case errors.As(err, &inv):
		return api.InvalidArgument(protoField(inv.Field), inv.Msg)
	case errors.Is(err, ErrNoProject):
		return api.NotFound("project")
	case errors.Is(err, store.ErrNotFound):
		return api.NotFound(what)
	case errors.Is(err, ErrFinished):
		return api.FailedPrecondition(ErrFinished.Error())
	case errors.Is(err, ErrState):
		return api.FailedPrecondition(err.Error())
	case errors.Is(err, ErrLimit):
		return api.ResourceExhausted(err.Error())
	case errors.Is(err, store.ErrConflict):
		return api.Aborted(what + " changed concurrently; retry")
	}
	return err
}

// protoField turns "project_id" into the protojson path "projectId".
func protoField(f string) string {
	parts := strings.Split(f, ".")
	for i, p := range parts {
		words := strings.Split(p, "_")
		for j := 1; j < len(words); j++ {
			if words[j] != "" {
				words[j] = strings.ToUpper(words[j][:1]) + words[j][1:]
			}
		}
		parts[i] = strings.Join(words, "")
	}
	return strings.Join(parts, ".")
}

func principal(ctx context.Context) string {
	if p := api.PrincipalFrom(ctx); p != nil {
		return p.ID
	}
	return ""
}

// CreateBuild implements BuildService.
func (s *Service) CreateBuild(ctx context.Context, req *connect.Request[buildv1.CreateBuildRequest]) (*connect.Response[buildv1.CreateBuildResponse], error) {
	m := req.Msg
	r := Request{Project: m.GetProjectId(), Targets: m.GetTargets(), Platforms: slices.Clone(m.GetPlatforms()),
		Variants: m.GetVariants(), Test: m.GetTest(), Reason: m.GetReason()}
	if m.GetPipeline() != "" {
		r.Pipelines = []string{m.GetPipeline()}
	}
	r.Trigger = store.BuildTrigger{Kind: TriggerAPI, By: principal(ctx)}
	switch src := m.GetSource().GetKind().(type) {
	case *buildv1.Source_Git:
		r.Source = store.BuildSource{Ref: src.Git.GetRef(), SHA: src.Git.GetSha(), FetchRef: src.Git.GetFetchRef()}
		r.Trigger.Ref = src.Git.GetRef()
		r.Branch = strings.TrimPrefix(src.Git.GetRef(), "refs/heads/")
		if r.Branch == src.Git.GetRef() {
			r.Branch = ""
		}
	case *buildv1.Source_UploadSha256:
		r.Source = store.BuildSource{Upload: src.UploadSha256}
		r.Trigger.Kind = TriggerRemote // `bob build/test --remote` uploads its source
	default:
		return nil, api.InvalidArgument("source", "a git source or an uploaded snapshot is required")
	}
	rec, err := s.B.Create(ctx, r)
	if err != nil {
		return nil, apiError(err, "build")
	}
	b, err := s.B.ToProto(ctx, rec)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&buildv1.CreateBuildResponse{Build: b}), nil
}

// GetBuild implements BuildService.
func (s *Service) GetBuild(ctx context.Context, req *connect.Request[buildv1.GetBuildRequest]) (*connect.Response[buildv1.GetBuildResponse], error) {
	b, err := s.build(ctx, req.Msg.GetId())
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&buildv1.GetBuildResponse{Build: b}), nil
}

func (s *Service) build(ctx context.Context, id string) (*buildv1.Build, error) {
	rec, err := s.B.Get(ctx, id)
	if err != nil {
		return nil, apiError(err, "build")
	}
	return s.B.ToProto(ctx, rec)
}

// CancelBuild implements BuildService.
func (s *Service) CancelBuild(ctx context.Context, req *connect.Request[buildv1.CancelBuildRequest]) (*connect.Response[buildv1.CancelBuildResponse], error) {
	rec, err := s.B.Cancel(ctx, req.Msg.GetId(), principal(ctx))
	if err != nil {
		return nil, apiError(err, "build")
	}
	b, err := s.B.ToProto(ctx, rec)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&buildv1.CancelBuildResponse{Build: b}), nil
}

// RetryBuild implements BuildService.
func (s *Service) RetryBuild(ctx context.Context, req *connect.Request[buildv1.RetryBuildRequest]) (*connect.Response[buildv1.RetryBuildResponse], error) {
	rec, err := s.B.Retry(ctx, req.Msg.GetId(), principal(ctx))
	if err != nil {
		return nil, apiError(err, "build")
	}
	b, err := s.B.ToProto(ctx, rec)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&buildv1.RetryBuildResponse{Build: b}), nil
}

// RerunBuild implements BuildService.
func (s *Service) RerunBuild(ctx context.Context, req *connect.Request[buildv1.RerunBuildRequest]) (*connect.Response[buildv1.RerunBuildResponse], error) {
	rec, err := s.B.Rerun(ctx, req.Msg.GetId(), principal(ctx))
	if err != nil {
		return nil, apiError(err, "build")
	}
	b, err := s.B.ToProto(ctx, rec)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&buildv1.RerunBuildResponse{Build: b}), nil
}

// ListBuilds implements BuildService: newest first from bpage (one project), bbr (a ref of a
// project) or, without a project, from bidx/running and bidx/failed.
func (s *Service) ListBuilds(ctx context.Context, req *connect.Request[buildv1.ListBuildsRequest]) (*connect.Response[buildv1.ListBuildsResponse], error) {
	m := req.Msg
	size, err := api.PageSize(m.GetPageSize())
	if err != nil {
		return nil, err
	}
	match := func(rec *store.BuildRecord) bool {
		if len(m.GetStates()) > 0 && !slices.Contains(m.GetStates(), protoState(rec.State)) {
			return false
		}
		if m.GetTriggerKind() != buildv1.TriggerKind_TRIGGER_KIND_UNSPECIFIED && protoTrigger(rec.Trigger.Kind) != m.GetTriggerKind() {
			return false
		}
		if m.GetPipeline() != "" && !slices.ContainsFunc(rec.Pipelines, func(p store.PipelineRun) bool { return p.Name == m.GetPipeline() }) {
			return false
		}
		if t := m.GetCreatedAfter(); t != nil && !rec.Created.After(t.AsTime()) {
			return false
		}
		return m.GetRef() == "" || refOf(rec.Trigger, rec.Source, rec.Branch) == m.GetRef()
	}
	if m.GetProjectId() == "" {
		return s.listActive(ctx, m, size, match)
	}
	if ok, err := s.allowed(ctx, m.GetProjectId()); err != nil || !ok {
		return connect.NewResponse(&buildv1.ListBuildsResponse{}), err
	}
	scope := "builds/" + m.GetProjectId() + "/" + m.GetRef()
	pos, err := api.DecodePageToken(scope, m.GetPageToken())
	if err != nil {
		return nil, err
	}
	numbers, err := s.numbers(ctx, m.GetProjectId(), m.GetRef())
	if err != nil {
		return nil, err
	}
	out := &buildv1.ListBuildsResponse{}
	// numbers is newest first; pos counts the entries already returned. Filters are applied
	// on the loaded rows (§05.5.2); at most a few pages of records are read per call.
	for i := int(pos); i < len(numbers); i++ {
		if len(out.Builds) == size || i-int(pos) >= 4*size {
			out.NextPageToken = api.EncodePageToken(scope, uint64(i))
			break
		}
		id, err := s.B.ByNumber(ctx, m.GetProjectId(), numbers[i])
		if err != nil {
			return nil, err
		}
		if id == "" {
			continue
		}
		rec, err := s.B.Get(ctx, id)
		if errors.Is(err, store.ErrNotFound) {
			continue // an index entry ahead of its record (intent first)
		}
		if err != nil {
			return nil, err
		}
		if !match(rec) {
			continue
		}
		b, err := s.B.ToProto(ctx, rec)
		if err != nil {
			return nil, err
		}
		out.Builds = append(out.Builds, b)
	}
	return connect.NewResponse(out), nil
}

// numbers are the build numbers of a project (or of one ref of it), newest first.
func (s *Service) numbers(ctx context.Context, project, ref string) ([]uint64, error) {
	if ref == "" {
		ctr, err := store.Get(ctx, s.B.cfg.Control, &store.ProjectCounters{Project: project})
		if errors.Is(err, store.ErrNotFound) {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		out := make([]uint64, 0, ctr.Builds)
		for n := ctr.Builds; n > 0; n-- {
			out = append(out, n)
		}
		return out, nil
	}
	key, err := store.BranchCounterKey(project, ref)
	if err != nil {
		return nil, api.InvalidArgument("ref", "invalid ref")
	}
	ctr, err := store.Get(ctx, s.B.cfg.Control, key)
	if errors.Is(err, store.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	idx, err := store.BranchBuilds(s.B.cfg.Control, project, ref)
	if err != nil {
		return nil, err
	}
	var out []uint64
	for k := int64(idx.LastPage(ctr.Count)); k >= 0; k-- {
		page, err := idx.Page(ctx, uint64(k))
		if errors.Is(err, store.ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		for i := len(page) - 1; i >= 0; i-- {
			if page[i] > 0 {
				out = append(out, page[i])
			}
		}
	}
	return out, nil
}

// listActive lists the active (and recently failed) builds of every project in scope.
func (s *Service) listActive(ctx context.Context, m *buildv1.ListBuildsRequest, size int, match func(*store.BuildRecord) bool) (*connect.Response[buildv1.ListBuildsResponse], error) {
	ids := map[string]bool{}
	running, _, err := store.GetCached(ctx, s.B.cfg.Control, &s.B.running, &store.RunningBuilds{Name: "running"})
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return nil, err
	}
	if running != nil {
		for id := range running.Builds {
			ids[id] = true
		}
	}
	failed, err := store.Get(ctx, s.B.cfg.Control, &store.FailedBuilds{Name: "failed"})
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return nil, err
	}
	if failed != nil {
		for _, f := range failed.Builds {
			ids[f.Build] = true
		}
	}
	order := make([]string, 0, len(ids))
	for id := range ids {
		order = append(order, id)
	}
	slices.Sort(order)
	slices.Reverse(order) // ids sort by creation time: newest first
	out := &buildv1.ListBuildsResponse{}
	for _, id := range order {
		if len(out.Builds) == size {
			break
		}
		rec, err := s.B.Get(ctx, id)
		if errors.Is(err, store.ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if ok, err := s.allowed(ctx, rec.Project); err != nil || !ok || !match(rec) {
			if err != nil {
				return nil, err
			}
			continue
		}
		b, err := s.B.ToProto(ctx, rec)
		if err != nil {
			return nil, err
		}
		out.Builds = append(out.Builds, b)
	}
	return connect.NewResponse(out), nil
}

func (s *Service) allowed(ctx context.Context, project string) (bool, error) {
	p := api.PrincipalFrom(ctx)
	if p == nil {
		return false, nil
	}
	var authz api.Authorizer = api.AdminBindings{}
	if s.Authz != nil {
		authz = s.Authz
	}
	a, err := authz.Access(ctx, p, methodListBuilds, project)
	return a == api.Allowed, err
}

// GetBuildGraph implements BuildService.
func (s *Service) GetBuildGraph(ctx context.Context, req *connect.Request[buildv1.GetBuildGraphRequest]) (*connect.Response[buildv1.GetBuildGraphResponse], error) {
	rec, err := s.B.Get(ctx, req.Msg.GetBuildId())
	if err != nil {
		return nil, apiError(err, "build")
	}
	nodes, err := s.B.Nodes(ctx, rec.ID)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&buildv1.GetBuildGraphResponse{Nodes: protoNodes(nodes), Complete: rec.Evaluated}), nil
}

// WatchBuild implements BuildService: the build first, then every change (conditional GET
// every WatchEvery), with the nodes whose state changed; it ends when the build is terminal.
func (s *Service) WatchBuild(ctx context.Context, req *connect.Request[buildv1.WatchBuildRequest], stream *connect.ServerStream[buildv1.WatchBuildResponse]) error {
	every := s.WatchEvery
	if every <= 0 {
		every = time.Second
	}
	var cache store.Cache[store.BuildRecord]
	var last []*buildv1.PackageNode
	first := true
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		rec, changed, err := store.GetCached(ctx, s.B.cfg.Control, &cache, &store.BuildRecord{ID: req.Msg.GetId()})
		if err != nil {
			return apiError(err, "build")
		}
		if changed || first {
			b, err := s.B.ToProto(ctx, rec)
			if err != nil {
				return err
			}
			msg := &buildv1.WatchBuildResponse{Build: b}
			nodes, err := s.B.Nodes(ctx, rec.ID)
			if err != nil {
				return err
			}
			cur := protoNodes(nodes)
			for i, n := range cur {
				if i >= len(last) || last[i].GetState() != n.GetState() {
					msg.ChangedNodes = append(msg.ChangedNodes, int32(i))
					msg.Nodes = append(msg.Nodes, n)
				}
			}
			last, first = cur, false
			if err := stream.Send(msg); err != nil {
				return err
			}
			if Terminal(rec.State) {
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
		}
	}
}

// GetJob implements JobService: the live job record while it exists, else the build's row.
func (s *Service) GetJob(ctx context.Context, req *connect.Request[buildv1.GetJobRequest]) (*connect.Response[buildv1.GetJobResponse], error) {
	id := req.Msg.GetId()
	bid, ok := BuildOfJob(id)
	if !ok {
		return nil, api.NotFound("job")
	}
	rec, err := s.B.Get(ctx, bid)
	if err != nil {
		return nil, apiError(err, "job")
	}
	for _, row := range rec.Jobs {
		if row.ID == id {
			j, err := s.B.job(ctx, rec, row)
			if err != nil {
				return nil, err
			}
			return connect.NewResponse(&buildv1.GetJobResponse{Job: j}), nil
		}
	}
	return nil, api.NotFound("job")
}

// ListJobs implements JobService (the build's job rows, in creation order).
func (s *Service) ListJobs(ctx context.Context, req *connect.Request[buildv1.ListJobsRequest]) (*connect.Response[buildv1.ListJobsResponse], error) {
	m := req.Msg
	size, err := api.PageSize(m.GetPageSize())
	if err != nil {
		return nil, err
	}
	rec, err := s.B.Get(ctx, m.GetBuildId())
	if err != nil {
		return nil, apiError(err, "build")
	}
	scope := "jobs/" + rec.ID
	pos, err := api.DecodePageToken(scope, m.GetPageToken())
	if err != nil {
		return nil, err
	}
	out := &buildv1.ListJobsResponse{}
	for i := int(pos); i < len(rec.Jobs); i++ {
		if len(out.Jobs) == size {
			out.NextPageToken = api.EncodePageToken(scope, uint64(i))
			break
		}
		j, err := s.B.job(ctx, rec, rec.Jobs[i])
		if err != nil {
			return nil, err
		}
		out.Jobs = append(out.Jobs, j)
	}
	return connect.NewResponse(out), nil
}

// job is the Job message of a row: from the live job record while it exists.
func (b *Builds) job(ctx context.Context, rec *store.BuildRecord, row store.BuildJob) (*buildv1.Job, error) {
	j := &buildv1.Job{Id: row.ID, BuildId: rec.ID, Kind: protoJobKind(row.Kind), State: protoJobState(row.State),
		Platform: platformOf(row.Class), WorkerId: row.Worker, Attempt: int32(row.Attempts), Epoch: row.Epoch,
		CreatedAt: ts(rec.Created)}
	if orchestrator.Terminal(row.State) {
		return j, nil
	}
	live, err := store.Get(ctx, b.cfg.Jobs, &store.JobRecord{ID: row.ID})
	if errors.Is(err, store.ErrNotFound) {
		return j, nil
	}
	if err != nil {
		return nil, err
	}
	j.State, j.WorkerId, j.Attempt, j.Epoch = protoJobState(live.State), live.Owner, int32(live.Attempt), live.Epoch
	j.Promotion, j.NonReproducible = live.Promotion, live.NonReproducible
	j.ClaimedAt = ts(live.Claimed)
	if spec, ok := fleet.DecodeSpec(live.Spec); ok {
		j.CreatedAt = ts(time.Unix(spec.Created, 0))
	}
	if orchestrator.Terminal(live.State) {
		j.FinishedAt = ts(live.Changed)
		if res, ok := fleet.DecodeResult(live.Result); ok && res.Error != "" {
			j.Error = &buildv1.ErrorSummary{Code: codeRE.FindString(res.Error), Message: firstLine(res.Error)}
		}
	}
	return j, nil
}

// platformOf is the platform part of a class id ("aarch64-linux.k3j9d0a1b2").
func platformOf(class string) string {
	p, _, _ := strings.Cut(class, ".")
	return p
}

func ts(t time.Time) *timestamppb.Timestamp {
	if t.IsZero() {
		return nil
	}
	return timestamppb.New(t)
}

func protoState(s string) buildv1.BuildState {
	return buildv1.BuildState(buildv1.BuildState_value["BUILD_STATE_"+strings.ToUpper(s)])
}

func protoTrigger(k string) buildv1.TriggerKind {
	if k == TriggerPR {
		return buildv1.TriggerKind_TRIGGER_KIND_PULL_REQUEST
	}
	return buildv1.TriggerKind(buildv1.TriggerKind_value["TRIGGER_KIND_"+strings.ToUpper(k)])
}

func protoJobKind(k string) buildv1.JobKind {
	return buildv1.JobKind(buildv1.JobKind_value["JOB_KIND_"+strings.ToUpper(k)])
}

func protoJobState(s string) buildv1.JobState {
	return buildv1.JobState(buildv1.JobState_value["JOB_STATE_"+strings.ToUpper(s)])
}

func protoPackageState(s string) buildv1.PackageState {
	switch s {
	case "built", "pass":
		return buildv1.PackageState_PACKAGE_STATE_SUCCEEDED
	case "cached":
		return buildv1.PackageState_PACKAGE_STATE_CACHED
	case "failed", "fail":
		return buildv1.PackageState_PACKAGE_STATE_FAILED
	case "skipped":
		return buildv1.PackageState_PACKAGE_STATE_SKIPPED
	case "cancelled":
		return buildv1.PackageState_PACKAGE_STATE_CANCELLED
	}
	return buildv1.PackageState_PACKAGE_STATE_PENDING
}

func protoNodes(bn *BuildNodes) []*buildv1.PackageNode {
	if bn == nil {
		return nil
	}
	out := make([]*buildv1.PackageNode, len(bn.Nodes))
	for i, n := range bn.Nodes {
		pn := &buildv1.PackageNode{Package: &commonv1.PackageRef{Label: n.Label, Platform: n.Platform},
			State: protoPackageState(n.Status), JobId: n.Job, Outputs: n.Outputs}
		for _, d := range n.Deps {
			pn.Deps = append(pn.Deps, int32(d))
		}
		if n.Error != nil {
			pn.Error = protoError(*n.Error)
		}
		out[i] = pn
	}
	return out
}

func protoError(e store.BuildError) *buildv1.ErrorSummary {
	es := &buildv1.ErrorSummary{Code: e.Code, Message: e.Message, Hint: e.Hint}
	if e.Label != "" {
		es.Package = &commonv1.PackageRef{Label: e.Label}
	}
	return es
}

// ToProto is the Build message of rec. Build numbers it refers to (retry_of, rerun_of,
// superseded_by) are turned into build ids through bpage.
func (b *Builds) ToProto(ctx context.Context, rec *store.BuildRecord) (*buildv1.Build, error) {
	idOf := func(n uint64) (string, error) { return b.ByNumber(ctx, rec.Project, n) }
	tr := &buildv1.Trigger{Kind: protoTrigger(rec.Trigger.Kind), Ref: refOf(rec.Trigger, rec.Source, rec.Branch),
		PullRequest: rec.Trigger.PR, FromFork: rec.Trigger.Fork, Sender: rec.Trigger.Sender, DeliveryId: rec.Trigger.Event}
	if rec.Trigger.By != "" {
		tr.Principal = &commonv1.Principal{Id: rec.Trigger.By}
	}
	var err error
	for _, x := range []struct {
		n   uint64
		dst *string
	}{{rec.RetryOf, &tr.RetryOf}, {rec.RerunOf, &tr.RerunOf}, {rec.SupersededBy, &tr.SupersededBy}} {
		if x.n == 0 {
			continue
		}
		if *x.dst, err = idOf(x.n); err != nil {
			return nil, err
		}
	}
	out := &buildv1.Build{Id: rec.ID, ProjectId: rec.Project, Number: int64(rec.Number), State: protoState(rec.State),
		Trigger: tr, Targets: rec.Targets, Platforms: rec.Platforms, Variants: rec.Variants,
		Attempt: int32(max(rec.Attempt, 1)), CreatedAt: ts(rec.Created), StartedAt: ts(rec.Started),
		FinishedAt: ts(rec.Finished), WebUrl: b.WebURL(rec), Etag: rec.ETag}
	if rec.Source.Upload != "" {
		out.Source = &buildv1.Source{Kind: &buildv1.Source_UploadSha256{UploadSha256: rec.Source.Upload}}
	} else {
		sha := rec.Source.SHA
		if sha == "" {
			sha = rec.Commit
		}
		out.Source = &buildv1.Source{Kind: &buildv1.Source_Git{Git: &buildv1.GitSource{Ref: rec.Source.Ref, Sha: sha, FetchRef: rec.Source.FetchRef}}}
	}
	if !Terminal(rec.State) {
		out.Phase = buildv1.BuildPhase(buildv1.BuildPhase_value["BUILD_PHASE_"+strings.ToUpper(rec.Phase)])
	}
	for _, p := range rec.Pipelines {
		out.Pipelines = append(out.Pipelines, &buildv1.PipelineRun{Name: p.Name, State: protoState(p.State), ReleaseId: p.Release})
	}
	c := rec.Counts
	if !Terminal(rec.State) {
		for _, j := range rec.Jobs {
			switch j.State {
			case orchestrator.StateQueued:
				c.Queued++
			case orchestrator.StateClaimed, orchestrator.StateRunning:
				c.Running++
			}
		}
	}
	out.Counts = &buildv1.PackageCounts{Total: int32(c.Total), Cached: int32(c.Cached), Built: int32(c.Built),
		Running: int32(c.Running), Queued: int32(c.Queued), Failed: int32(c.Failed), Skipped: int32(c.Skipped),
		Cancelled: int32(c.Cancelled)}
	for _, e := range rec.Errors {
		out.Errors = append(out.Errors, protoError(e))
	}
	return out, nil
}

// JobProject resolves a job id to its project (rbac.Resolver.Job): through the build id
// inside the job id, so finalised jobs resolve too.
func (b *Builds) JobProject(ctx context.Context, jobID string) (string, error) {
	bid, ok := BuildOfJob(jobID)
	if !ok {
		return "", store.ErrNotFound
	}
	rec, err := b.Get(ctx, bid)
	if err != nil {
		return "", err
	}
	if !slices.ContainsFunc(rec.Jobs, func(j store.BuildJob) bool { return j.ID == jobID }) {
		return "", fmt.Errorf("job %s: %w", jobID, store.ErrNotFound)
	}
	return rec.Project, nil
}
