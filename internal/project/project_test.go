package project_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/fieldmaskpb"

	"github.com/megakuul/bob/internal/api"
	buildctl "github.com/megakuul/bob/internal/build"
	"github.com/megakuul/bob/internal/build/jobspec"
	"github.com/megakuul/bob/internal/forge"
	"github.com/megakuul/bob/internal/forge/integration"
	forgev1 "github.com/megakuul/bob/internal/gen/bob/forge/v1"
	projectv1 "github.com/megakuul/bob/internal/gen/bob/project/v1"
	"github.com/megakuul/bob/internal/gen/bob/project/v1/projectv1connect"
	"github.com/megakuul/bob/internal/project"
	"github.com/megakuul/bob/internal/store"
	"github.com/megakuul/bob/tests/store/env"
)

// links is a fake integration registry: repositories by name, links by repo id.
type links struct {
	mu       sync.Mutex
	repos    map[string]forge.Repo
	linked   map[int64]string // repo id → project
	hookErr  error
	health   int32
	unlinked []int64
}

func (l *links) LinkRepo(_ context.Context, forgeID, fullName, proj, _ string) (integration.Linked, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if forgeID != "fj" {
		return integration.Linked{}, integration.ErrUnknown
	}
	r, ok := l.repos[fullName]
	if !ok {
		return integration.Linked{}, forge.ErrNotFound
	}
	if p, ok := l.linked[r.ID]; ok && p != proj {
		return integration.Linked{}, integration.ErrLinked
	}
	l.linked[r.ID] = proj
	return integration.Linked{Repo: r, HookErr: l.hookErr}, nil
}

func (l *links) UnlinkRepo(_ context.Context, _ string, id int64) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.linked, id)
	l.unlinked = append(l.unlinked, id)
	return nil
}

func (l *links) Health(_ context.Context, id string) (int32, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.health, nil
}

func code(err error) connect.Code {
	var ce *connect.Error
	if errors.As(err, &ce) {
		return ce.Code()
	}
	return 0
}

const (
	adminTok = "admin-token"
	noneTok  = "none-token"
)

func req[T any](m *T) *connect.Request[T] { return reqAs(adminTok, m) }

func reqAs[T any](tok string, m *T) *connect.Request[T] {
	r := connect.NewRequest(m)
	r.Header().Set("Authorization", "Bearer "+tok)
	return r
}

// serve puts svc behind the real API layer (authentication, rules, error model).
func serve(t *testing.T, svc *project.Service) projectv1connect.ProjectServiceClient {
	t.Helper()
	s, err := api.New(api.Config{Services: api.Services{Project: svc},
		Authn: api.Chain{
			&api.StaticToken{Token: adminTok, Principal: api.Principal{ID: "user:admin", Scope: "api", Bindings: []api.Binding{{Role: "admin", Project: "*"}}}},
			&api.StaticToken{Token: noneTok, Principal: api.Principal{ID: "user:none", Scope: "api"}},
		},
		Log: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(s)
	t.Cleanup(ts.Close)
	return projectv1connect.NewProjectServiceClient(ts.Client(), ts.URL+"/api")
}

func TestProjects(t *testing.T) {
	if testing.Short() {
		t.Skip("starts RustFS")
	}
	be := env.Start(t)
	control := be.Open(t, "control")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	clock := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	now := func() time.Time { return clock }
	l := &links{linked: map[int64]string{}, repos: map[string]forge.Repo{
		"alice/demo":  {ID: 1, FullName: "alice/demo", DefaultBranch: "trunk", WebURL: "https://f/alice/demo"},
		"alice/other": {ID: 2, FullName: "alice/other", DefaultBranch: "main"},
	}}
	p, err := project.New(project.Config{Control: control, Links: l, BaseURL: "https://bob.example", Now: now})
	if err != nil {
		t.Fatal(err)
	}
	svc := serve(t, &project.Service{P: p})
	create := func(m *projectv1.Project) (*projectv1.Project, error) {
		r, err := svc.CreateProject(ctx, req(&projectv1.CreateProjectRequest{Project: m}))
		if err != nil {
			return nil, err
		}
		return r.Msg.GetProject(), nil
	}

	// create with a link; the default branch and repo id come from the forge
	acme, err := create(&projectv1.Project{Id: "acme", DisplayName: "Acme", ForgeIntegrationId: "fj", RepoFullName: "alice/demo"})
	if err != nil {
		t.Fatal(err)
	}
	if acme.GetRepoId() != 1 || acme.GetDefaultBranch() != "trunk" || acme.GetLinkHealth() != projectv1.LinkHealth_LINK_HEALTH_OK || acme.GetEtag() == "" {
		t.Errorf("CreateProject = %+v", acme)
	}
	for _, c := range []struct {
		name string
		m    *projectv1.Project
		want connect.Code
	}{
		{"same id", &projectv1.Project{Id: "acme"}, connect.CodeAlreadyExists},
		{"repository linked elsewhere (R-PIPE-002)", &projectv1.Project{Id: "acme2", ForgeIntegrationId: "fj", RepoFullName: "alice/demo"}, connect.CodeAlreadyExists},
		{"unknown integration", &projectv1.Project{Id: "x1", ForgeIntegrationId: "nope", RepoFullName: "alice/demo"}, connect.CodeInvalidArgument},
		{"unknown repository", &projectv1.Project{Id: "x2", ForgeIntegrationId: "fj", RepoFullName: "alice/none"}, connect.CodeInvalidArgument},
		{"repository without integration", &projectv1.Project{Id: "x3", RepoFullName: "alice/demo"}, connect.CodeInvalidArgument},
		{"bad id", &projectv1.Project{Id: "Not_A_Slug"}, connect.CodeInvalidArgument},
		{"empty", &projectv1.Project{}, connect.CodeInvalidArgument},
	} {
		if _, err := create(c.m); code(err) != c.want {
			t.Errorf("%s: %v, want %v", c.name, err, c.want)
		}
	}
	// a project without a repository (remote builds only); a short id like the suites' p1
	if _, err := create(&projectv1.Project{Id: "p1"}); err != nil {
		t.Fatal(err)
	}
	l.hookErr = errors.New("403")
	hooked, err := create(&projectv1.Project{Id: "web", ForgeIntegrationId: "fj", RepoFullName: "alice/other"})
	if err != nil || hooked.GetLinkHealth() != projectv1.LinkHealth_LINK_HEALTH_WEBHOOK_MISSING {
		t.Errorf("CreateProject with a webhook error: %+v %v, want WEBHOOK_MISSING", hooked, err)
	}
	l.hookErr = nil

	// the build controller's view
	bp, err := p.Project(ctx, "acme")
	if err != nil || bp.Repo != "fj/1" || bp.DefaultBranch != "trunk" {
		t.Errorf("Project(acme) = %+v %v", bp, err)
	}
	if _, err := p.Project(ctx, "nope"); !errors.Is(err, buildctl.ErrNoProject) {
		t.Errorf("Project(nope) = %v, want ErrNoProject", err)
	}

	// list: newest first, query, paging
	list := func(lr *projectv1.ListProjectsRequest) ([]string, string) {
		t.Helper()
		r, err := svc.ListProjects(ctx, req(lr))
		if err != nil {
			t.Fatal(err)
		}
		var ids []string
		for _, x := range r.Msg.GetProjects() {
			ids = append(ids, x.GetId())
		}
		return ids, r.Msg.GetNextPageToken()
	}
	if ids, _ := list(&projectv1.ListProjectsRequest{}); len(ids) != 3 || ids[0] != "web" || ids[2] != "acme" {
		t.Errorf("ListProjects = %v, want [web p1 acme]", ids)
	}
	if ids, _ := list(&projectv1.ListProjectsRequest{Query: "ac"}); len(ids) != 1 || ids[0] != "acme" {
		t.Errorf("ListProjects{query: ac} = %v", ids)
	}
	ids, tok := list(&projectv1.ListProjectsRequest{PageSize: 2})
	rest, tok2 := list(&projectv1.ListProjectsRequest{PageSize: 2, PageToken: tok})
	if len(ids) != 2 || tok == "" || len(rest) != 1 || rest[0] != "acme" || tok2 != "" {
		t.Errorf("paging: %v %q then %v %q", ids, tok, rest, tok2)
	}
	// LIST_FILTERED: a principal bound to nothing sees nothing
	if r, err := svc.ListProjects(ctx, reqAs(noneTok, &projectv1.ListProjectsRequest{})); err != nil || len(r.Msg.GetProjects()) != 0 {
		t.Errorf("ListProjects without bindings = %v %v", r, err)
	}

	// update: etag, mask, relink
	upd := func(m *projectv1.Project, paths ...string) (*projectv1.Project, error) {
		r, err := svc.UpdateProject(ctx, req(&projectv1.UpdateProjectRequest{Project: m, UpdateMask: &fieldmaskpb.FieldMask{Paths: paths}}))
		if err != nil {
			return nil, err
		}
		return r.Msg.GetProject(), nil
	}
	if _, err := upd(&projectv1.Project{Id: "acme", Description: "x", Etag: "stale"}, "description"); code(err) != connect.CodeAborted {
		t.Errorf("stale etag: %v, want Aborted", err)
	}
	if _, err := upd(&projectv1.Project{Id: "acme", RepoId: 9}, "repoId"); code(err) != connect.CodeInvalidArgument {
		t.Errorf("output-only field in the mask: %v", err)
	}
	moved, err := upd(&projectv1.Project{Id: "p1", ForgeIntegrationId: "fj", RepoFullName: "alice/demo"}, "forgeIntegrationId", "repoFullName")
	if code(err) != connect.CodeAlreadyExists {
		t.Errorf("relink p1 to acme's repository: %+v %v, want AlreadyExists", moved, err)
	}
	got, err := upd(&projectv1.Project{Id: "acme", DisplayName: "Acme Corp", Etag: acme.GetEtag()}, "displayName")
	if err != nil || got.GetDisplayName() != "Acme Corp" || got.GetRepoFullName() != "alice/demo" {
		t.Errorf("UpdateProject = %+v %v", got, err)
	}
	if ids, _ := list(&projectv1.ListProjectsRequest{Query: "acme c"}); len(ids) != 1 {
		t.Errorf("the index entry was not updated: %v", ids)
	}

	// pipelines cache: a default-branch evaluation, then a branch one (ignored), then the end
	rep := &jobspec.Report{Pipelines: []jobspec.Pipeline{{Name: "ci", Triggers: []string{"push trunk"}, Concurrency: "queue"},
		{Name: "release", Triggers: []string{"tag v*"}, Releases: []string{"api"}}}, Matched: []string{"ci"}}
	// build records as the build controller reads them (fetched values)
	persist := func(r *store.BuildRecord) *store.BuildRecord {
		t.Helper()
		if err := store.Create(ctx, control, r); err != nil {
			t.Fatal(err)
		}
		got, err := store.Get(ctx, control, &store.BuildRecord{ID: r.ID})
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	b1 := persist(&store.BuildRecord{ID: store.ID("01aaaaaaaaaaaaaaaaaaaaaaa1"), Project: store.Set[string]("acme"), Number: store.Set[uint64](1), Branch: store.Set[string]("trunk"), Commit: store.Set[string]("c1"),
		Trigger: store.Set(store.BuildTrigger{Ref: "refs/heads/trunk"})})
	if err := p.Evaluated(ctx, b1, rep); err != nil {
		t.Fatal(err)
	}
	b2 := persist(&store.BuildRecord{ID: store.ID("01aaaaaaaaaaaaaaaaaaaaaaa2"), Project: store.Set[string]("acme"), Number: store.Set[uint64](2), Branch: store.Set[string]("feature"), Commit: store.Set[string]("c2"),
		Trigger: store.Set(store.BuildTrigger{Ref: "refs/heads/feature"})})
	if err := p.Evaluated(ctx, b2, &jobspec.Report{Pipelines: []jobspec.Pipeline{{Name: "other"}}}); err != nil {
		t.Fatal(err)
	}
	b1, err = store.CAS(ctx, control, &store.BuildRecord{ID: b1.ID}, func(cur *store.BuildRecord) (*store.BuildRecord, error) {
		cur.State, cur.Pipelines = store.Set[string](buildctl.StateSucceeded), store.Set([]store.PipelineRun{{Name: "ci", State: buildctl.StateSucceeded}})
		return cur, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Finished(ctx, b1); err != nil {
		t.Fatal(err)
	}
	pl, err := svc.ListPipelines(ctx, req(&projectv1.ListPipelinesRequest{ProjectId: "acme"}))
	if err != nil {
		t.Fatal(err)
	}
	if m := pl.Msg; m.GetEvaluatedSha() != "c1" || len(m.GetPipelines()) != 2 || m.GetPipelines()[0].GetLastBuildId() != b1.ID.Value() ||
		m.GetPipelines()[0].GetLastState() != "BUILD_STATE_SUCCEEDED" || m.GetPipelines()[0].GetConcurrency() != projectv1.Concurrency_CONCURRENCY_QUEUE ||
		!m.GetPipelines()[1].GetReleases() || m.GetPipelines()[1].GetTriggers()[0] != "tag v*" {
		t.Errorf("ListPipelines = %v", m)
	}

	// link health follows the integration
	l.health = int32(forgev1.Health_HEALTH_AUTH_FAILED)
	if r, err := svc.GetProject(ctx, req(&projectv1.GetProjectRequest{Id: "acme"})); err != nil ||
		r.Msg.GetProject().GetLinkHealth() != projectv1.LinkHealth_LINK_HEALTH_AUTH_FAILED {
		t.Errorf("GetProject with a failing integration: %v %v", r, err)
	}
	l.health = 0

	// delete: refused while a build runs, then unlinks and disappears
	if _, err := store.CAS(ctx, control, &store.RunningBuilds{Name: store.ID("running")}, func(*store.RunningBuilds) (*store.RunningBuilds, error) {
		return &store.RunningBuilds{Name: store.ID("running"), Builds: store.Set(map[string]store.RunningBuild{"b": {Project: "acme", N: 3}})}, nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.DeleteProject(ctx, req(&projectv1.DeleteProjectRequest{Id: "acme"})); code(err) != connect.CodeFailedPrecondition {
		t.Errorf("DeleteProject with a running build: %v", err)
	}
	if _, err := store.CAS(ctx, control, &store.RunningBuilds{Name: store.ID("running")}, func(*store.RunningBuilds) (*store.RunningBuilds, error) {
		return &store.RunningBuilds{Name: store.ID("running"), Builds: store.Set(map[string]store.RunningBuild{})}, nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.DeleteProject(ctx, req(&projectv1.DeleteProjectRequest{Id: "acme"})); err != nil {
		t.Fatal(err)
	}
	if _, ok := l.linked[1]; ok {
		t.Errorf("the repository is still linked after DeleteProject")
	}
	if _, err := svc.GetProject(ctx, req(&projectv1.GetProjectRequest{Id: "acme"})); code(err) != connect.CodeNotFound {
		t.Errorf("GetProject after delete: %v", err)
	}
	if ids, _ := list(&projectv1.ListProjectsRequest{}); len(ids) != 2 {
		t.Errorf("ListProjects after delete = %v", ids)
	}
	// the repository is free again
	if _, err := create(&projectv1.Project{Id: "acme2", ForgeIntegrationId: "fj", RepoFullName: "alice/demo"}); err != nil {
		t.Errorf("CreateProject on the freed repository: %v", err)
	}

	// intent first: a pending entry whose record never came is hidden, and repaired away after
	// 5 min; a pending entry whose record exists is shown again
	idx := project.Index(control)
	n := uint64(4) // slots used so far: acme, p1, web, acme2
	if err := idx.Set(ctx, n, project.Entry{ID: "ghost", Pending: true, At: clock}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CAS(ctx, control, &project.IndexCounter{Name: store.ID("all")}, func(c *project.IndexCounter) (*project.IndexCounter, error) {
		return &project.IndexCounter{Name: store.ID("all"), Count: store.Set(c.Count.Value() + 1)}, nil
	}); err != nil {
		t.Fatal(err)
	}
	web, err := p.Get(ctx, "web")
	if err != nil {
		t.Fatal(err)
	}
	if err := idx.Set(ctx, web.Slot.Value()-1, project.Entry{ID: "web", Pending: true, At: clock}); err != nil {
		t.Fatal(err)
	}
	if ids, _ := list(&projectv1.ListProjectsRequest{}); len(ids) != 2 { // p1, acme2 (web hidden, ghost hidden)
		t.Errorf("pending entries are listed: %v", ids)
	}
	if err := p.Repair(ctx); err != nil {
		t.Fatal(err)
	}
	if ids, _ := list(&projectv1.ListProjectsRequest{}); len(ids) != 2 {
		t.Errorf("repair touched entries younger than 5 min: %v", ids)
	}
	clock = clock.Add(6 * time.Minute)
	if err := p.Repair(ctx); err != nil {
		t.Fatal(err)
	}
	ids, _ = list(&projectv1.ListProjectsRequest{})
	if len(ids) != 3 || ids[0] != "acme2" {
		t.Errorf("after repair: %v, want [acme2 web p1]", ids)
	}
	page, err := idx.Page(ctx, 0)
	if err != nil || page[n].ID != "" {
		t.Errorf("the ghost entry was not cleared: %+v %v", page[n], err)
	}
}
