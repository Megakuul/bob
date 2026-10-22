package project

import (
	"context"
	"errors"
	"strings"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/megakuul/bob/internal/api"
	projectv1 "github.com/megakuul/bob/internal/gen/bob/project/v1"
	"github.com/megakuul/bob/internal/gen/bob/project/v1/projectv1connect"
	"github.com/megakuul/bob/internal/rbac"
	"github.com/megakuul/bob/internal/store"
)

// Scoper answers which projects a principal may call a method on (rbac.Authorizer); the
// LIST_FILTERED methods filter their rows with it.
type Scoper interface {
	Scope(ctx context.Context, p *api.Principal, method string) (rbac.Scope, error)
}

// Allowed returns the filter of method for the caller of ctx: with a nil Scoper only admin
// bindings ({*, *} via the role "admin" @*) see everything.
func Allowed(ctx context.Context, s Scoper, method string) (func(project string) bool, error) {
	p := api.PrincipalFrom(ctx)
	if p == nil {
		return func(string) bool { return false }, nil
	}
	if s == nil {
		return func(project string) bool {
			a, _ := api.AdminBindings{}.Access(ctx, p, method, project)
			return a == api.Allowed
		}, nil
	}
	sc, err := s.Scope(ctx, p, method)
	if err != nil {
		return nil, err
	}
	return sc.Contains, nil
}

// Service is ProjectService (SPEC §6.5.6).
type Service struct {
	P     *Projects
	Scope Scoper
}

var _ projectv1connect.ProjectServiceHandler = (*Service)(nil)

const methodListProjects = "bob.project.v1.ProjectService/ListProjects"

// apiError maps Projects errors to the §6.1 error model.
func apiError(err error) error {
	var inv *InvalidError
	var link *LinkError
	switch {
	case err == nil:
		return nil
	case errors.As(err, &inv):
		return api.InvalidArgument(inv.Field, inv.Msg)
	case errors.As(err, &link) && link.Linked:
		return api.AlreadyExists(link.Msg)
	case errors.As(err, &link):
		return api.FailedPrecondition(link.Msg)
	case errors.Is(err, ErrNotFound):
		return api.NotFound("project")
	case errors.Is(err, ErrExists):
		return api.AlreadyExists(err.Error())
	case errors.Is(err, ErrConflict):
		return api.Aborted(err.Error())
	case errors.Is(err, ErrRunning), errors.Is(err, ErrReleases):
		return api.FailedPrecondition(err.Error())
	}
	return err
}

func (s *Service) message(ctx context.Context, rec *Record) *projectv1.Project {
	return &projectv1.Project{Id: rec.ID.Value(), DisplayName: rec.DisplayName.Value(), Description: rec.Description.Value(),
		ForgeIntegrationId: rec.Forge.Value(), RepoFullName: rec.RepoName.Value(), RepoId: rec.RepoID.Value(),
		DefaultBranch: rec.DefaultBranch.Value(), RepoWebUrl: rec.WebURL.Value(), LinkHealth: s.P.Health(ctx, rec),
		CreatedAt: timestamppb.New(rec.Created.Value()), Etag: store.ETagOf(rec.ETag)}
}

func entryMessage(e Entry) *projectv1.Project {
	return &projectv1.Project{Id: e.ID, DisplayName: e.DisplayName, Description: e.Description,
		ForgeIntegrationId: e.Forge, RepoFullName: e.RepoName, RepoId: e.RepoID, DefaultBranch: e.DefaultBranch,
		RepoWebUrl: e.WebURL, LinkHealth: projectv1.LinkHealth(e.Health), CreatedAt: timestamppb.New(e.Created)}
}

func specOf(m *projectv1.Project) Spec {
	return Spec{ID: m.GetId(), DisplayName: strings.TrimSpace(m.GetDisplayName()), Description: m.GetDescription(),
		Forge: m.GetForgeIntegrationId(), RepoName: strings.TrimSpace(m.GetRepoFullName())}
}

// CreateProject implements ProjectService: links the repository and ensures the webhook. A
// webhook that could not be ensured leaves the link standing and shows as WEBHOOK_MISSING.
func (s *Service) CreateProject(ctx context.Context, req *connect.Request[projectv1.CreateProjectRequest]) (*connect.Response[projectv1.CreateProjectResponse], error) {
	if req.Msg.GetProject() == nil {
		return nil, api.InvalidArgument("project", "project is required")
	}
	rec, err := s.P.Create(ctx, specOf(req.Msg.GetProject()))
	if err != nil {
		return nil, apiError(err)
	}
	return connect.NewResponse(&projectv1.CreateProjectResponse{Project: s.message(ctx, rec)}), nil
}

// GetProject implements ProjectService.
func (s *Service) GetProject(ctx context.Context, req *connect.Request[projectv1.GetProjectRequest]) (*connect.Response[projectv1.GetProjectResponse], error) {
	rec, err := s.P.Get(ctx, req.Msg.GetId())
	if err != nil {
		return nil, apiError(err)
	}
	return connect.NewResponse(&projectv1.GetProjectResponse{Project: s.message(ctx, rec)}), nil
}

// ListProjects implements ProjectService: newest first from the projects index, filtered to
// the projects the caller may list; query matches id or display-name prefixes.
func (s *Service) ListProjects(ctx context.Context, req *connect.Request[projectv1.ListProjectsRequest]) (*connect.Response[projectv1.ListProjectsResponse], error) {
	m := req.Msg
	size, err := api.PageSize(m.GetPageSize())
	if err != nil {
		return nil, err
	}
	const scope = "projects"
	pos, err := api.DecodePageToken(scope, m.GetPageToken())
	if err != nil {
		return nil, err
	}
	allowed, err := Allowed(ctx, s.Scope, methodListProjects)
	if err != nil {
		return nil, err
	}
	q := strings.ToLower(strings.TrimSpace(m.GetQuery()))
	rows, next, err := s.P.List(ctx, pos, size, func(e Entry) bool {
		if q != "" && !strings.HasPrefix(e.ID, q) && !strings.HasPrefix(strings.ToLower(e.DisplayName), q) {
			return false
		}
		return allowed(e.ID)
	})
	if err != nil {
		return nil, err
	}
	out := &projectv1.ListProjectsResponse{}
	for _, e := range rows {
		out.Projects = append(out.Projects, entryMessage(e))
	}
	if next > 0 {
		out.NextPageToken = api.EncodePageToken(scope, next)
	}
	return connect.NewResponse(out), nil
}

// updatable are the fields UpdateProject changes (proto names).
var updatable = []string{"display_name", "description", "forge_integration_id", "repo_full_name"}

// UpdateProject implements ProjectService. A changed repository is linked before the record
// changes and the old one unlinked after it.
func (s *Service) UpdateProject(ctx context.Context, req *connect.Request[projectv1.UpdateProjectRequest]) (*connect.Response[projectv1.UpdateProjectResponse], error) {
	m := req.Msg
	if m.GetProject() == nil {
		return nil, api.InvalidArgument("project", "project is required")
	}
	if err := api.CheckMask(m.GetUpdateMask(), m.GetProject(), updatable...); err != nil {
		return nil, err
	}
	var mask []string
	for _, p := range m.GetUpdateMask().GetPaths() {
		mask = append(mask, snake(p))
	}
	rec, err := s.P.Update(ctx, specOf(m.GetProject()), mask, m.GetProject().GetEtag())
	if err != nil {
		return nil, apiError(err)
	}
	return connect.NewResponse(&projectv1.UpdateProjectResponse{Project: s.message(ctx, rec)}), nil
}

// DeleteProject implements ProjectService: refused while the project has unfinished builds.
func (s *Service) DeleteProject(ctx context.Context, req *connect.Request[projectv1.DeleteProjectRequest]) (*connect.Response[projectv1.DeleteProjectResponse], error) {
	if req.Msg.GetId() == "" {
		return nil, api.InvalidArgument("id", "id is required")
	}
	if err := s.P.Delete(ctx, req.Msg.GetId(), req.Msg.GetEtag()); err != nil {
		return nil, apiError(err)
	}
	return connect.NewResponse(&projectv1.DeleteProjectResponse{}), nil
}

// concurrency maps the bob.go spelling to the API enum.
var concurrency = map[string]projectv1.Concurrency{
	"supersede": projectv1.Concurrency_CONCURRENCY_SUPERSEDE,
	"queue":     projectv1.Concurrency_CONCURRENCY_QUEUE,
	"parallel":  projectv1.Concurrency_CONCURRENCY_PARALLEL,
}

// ListPipelines implements ProjectService: the pipelines of the newest accepted evaluation of
// the default branch (the pipelines cache), each with its last build.
func (s *Service) ListPipelines(ctx context.Context, req *connect.Request[projectv1.ListPipelinesRequest]) (*connect.Response[projectv1.ListPipelinesResponse], error) {
	id := req.Msg.GetProjectId()
	if _, err := s.P.Get(ctx, id); err != nil {
		return nil, apiError(err)
	}
	rec, err := s.P.Pipelines(ctx, id)
	if err != nil {
		return nil, err
	}
	out := &projectv1.ListPipelinesResponse{}
	if rec == nil {
		return connect.NewResponse(out), nil
	}
	out.EvaluatedSha = rec.SHA.Value()
	for _, d := range rec.Defs.Value() {
		pl := &projectv1.Pipeline{Name: d.Name, Triggers: d.Triggers, Concurrency: concurrency[d.Concurrency], Releases: d.Releases}
		if last, ok := rec.Last.Value()[d.Name]; ok {
			pl.LastBuildId, pl.LastState = last.Build, stateName(last.State)
		}
		out.Pipelines = append(out.Pipelines, pl)
	}
	return connect.NewResponse(out), nil
}

// snake turns a protojson mask path ("displayName") into the proto name ("display_name").
func snake(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r >= 'A' && r <= 'Z' {
			b.WriteByte('_')
			r += 'a' - 'A'
		}
		b.WriteRune(r)
	}
	return b.String()
}
