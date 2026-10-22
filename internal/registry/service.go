package registry

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"slices"
	"sort"
	"strings"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/megakuul/bob/internal/api"
	registryv1 "github.com/megakuul/bob/internal/gen/bob/registry/v1"
	"github.com/megakuul/bob/internal/gen/bob/registry/v1/registryv1connect"
	"github.com/megakuul/bob/internal/project"
	"github.com/megakuul/bob/internal/store"
)

const (
	listRepositoriesMethod = "bob.registry.v1.RegistryService/ListRepositories"
	listTagsMethod         = "bob.registry.v1.RegistryService/ListTags"
	maxIndexPages          = 2000
)

// Service is RegistryService (SPEC §6.5.12): repositories come from the release index
// (§05.5.2 "Registry": ridxm + ridx pages), tags from the repository index; only what the
// caller may see is listed (public, or the method's permission on the project).
type Service struct {
	R     *Registry
	Scope project.Scoper
}

var _ registryv1connect.RegistryServiceHandler = (*Service)(nil)

// pullHost is the registry host of pull_ref (the base URL's host).
func (s *Service) pullHost() string {
	u, err := url.Parse(s.R.c.BaseURL)
	if err != nil || u.Host == "" {
		return ""
	}
	return u.Host
}

type repoSummary struct {
	name, project string
	public        bool
	updated       time.Time
}

// repositories reads the release index (or one project's pages) and groups the image
// repositories of published releases.
func (s *Service) repositories(ctx context.Context, projectID string) (map[string]*repoSummary, error) {
	out := map[string]*repoSummary{}
	add := func(p string, e store.ReleaseEntry) {
		if e.State != store.ReleasePublished {
			return
		}
		for _, r := range e.Repos {
			sum := out[r]
			if sum == nil {
				sum = &repoSummary{name: r, project: p}
				out[r] = sum
			}
			sum.public = sum.public || e.Public
			if e.Created.After(sum.updated) {
				sum.updated = e.Created
			}
		}
	}
	if projectID != "" {
		es, err := store.ProjectReleases(ctx, s.R.c.Control, projectID)
		if err != nil {
			return nil, err
		}
		for _, e := range es {
			add(projectID, e)
		}
		return out, nil
	}
	n, err := store.ReleaseIndexSlots(ctx, s.R.c.Control)
	if err != nil {
		return nil, err
	}
	idx := store.ReleaseIndex(s.R.c.Control)
	for k := uint64(0); n > 0 && k <= idx.LastPage(n) && k < maxIndexPages; k++ {
		es, err := idx.Page(ctx, k)
		if errors.Is(err, store.ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		for _, e := range es {
			if e.N != 0 {
				add(e.Project, e.ReleaseEntry)
			}
		}
	}
	return out, nil
}

func (s *Service) message(sum *repoSummary, tags int) *registryv1.Repository {
	r := &registryv1.Repository{Name: sum.name, ProjectId: sum.project, HasPublicTags: sum.public, TagCount: int32(tags)}
	if !sum.updated.IsZero() {
		r.UpdatedAt = timestamppb.New(sum.updated)
	}
	if h := s.pullHost(); h != "" {
		r.PullRef = h + "/" + sum.name
	}
	return r
}

// ListRepositories implements RegistryService.
func (s *Service) ListRepositories(ctx context.Context, req *connect.Request[registryv1.ListRepositoriesRequest]) (*connect.Response[registryv1.ListRepositoriesResponse], error) {
	m := req.Msg
	size, err := api.PageSize(m.GetPageSize())
	if err != nil {
		return nil, err
	}
	pos, err := api.DecodePageToken("repositories/"+m.GetProjectId()+"/"+m.GetQuery(), m.GetPageToken())
	if err != nil {
		return nil, err
	}
	if p := m.GetProjectId(); p != "" && !project.ValidID(p) {
		return nil, api.InvalidArgument("projectId", "invalid project id")
	}
	repos, err := s.repositories(ctx, m.GetProjectId())
	if err != nil {
		return nil, api.Unavailable("the registry is not available, try again")
	}
	allowed, err := project.Allowed(ctx, s.Scope, listRepositoriesMethod)
	if err != nil {
		return nil, err
	}
	var names []string
	for n, sum := range repos {
		if (sum.public || allowed(sum.project)) && strings.Contains(n, m.GetQuery()) {
			names = append(names, n)
		}
	}
	sort.Strings(names)
	out := &registryv1.ListRepositoriesResponse{}
	for i := int(min(pos, uint64(len(names)))); i < len(names) && len(out.Repositories) < size; i++ {
		out.Repositories = append(out.Repositories, s.message(repos[names[i]], 0))
		if len(out.Repositories) == size && i+1 < len(names) {
			out.NextPageToken = api.EncodePageToken("repositories/"+m.GetProjectId()+"/"+m.GetQuery(), uint64(i+1))
		}
	}
	return connect.NewResponse(out), nil
}

// visibleIndex returns the repository index and the tags the caller of ctx may see with
// method (NotFound when none: a missing repository and a private one answer alike).
func (s *Service) visibleIndex(ctx context.Context, repo, method string) (*RepoIndex, []string, map[string]bool, error) {
	if !ValidRepo(repo) {
		return nil, nil, nil, api.NotFound("repository")
	}
	idx, _, err := s.R.Repos.Get(ctx, repo)
	if err != nil {
		return nil, nil, nil, api.Unavailable("the registry is not available, try again")
	}
	if idx == nil {
		return nil, nil, nil, api.NotFound("repository")
	}
	allowed, err := project.Allowed(ctx, s.Scope, method)
	if err != nil {
		return nil, nil, nil, err
	}
	public := map[string]bool{}
	var tags []string
	for _, t := range idx.SortedTags() {
		doc, err := s.R.c.Vis.Get(ctx, strings.TrimPrefix(idx.Tags[t].D, "sha256:"))
		if err != nil {
			return nil, nil, nil, api.Unavailable("the registry is not available, try again")
		}
		if doc == nil {
			continue
		}
		if len(doc.Public) > 0 {
			public[t] = true
			tags = append(tags, t)
		} else if slices.ContainsFunc(doc.Projects, allowed) {
			tags = append(tags, t)
		}
	}
	if len(tags) == 0 {
		return nil, nil, nil, api.NotFound("repository")
	}
	return idx, tags, public, nil
}

// GetRepository implements RegistryService.
func (s *Service) GetRepository(ctx context.Context, req *connect.Request[registryv1.GetRepositoryRequest]) (*connect.Response[registryv1.GetRepositoryResponse], error) {
	name := req.Msg.GetName()
	idx, tags, public, err := s.visibleIndex(ctx, name, GetRepositoryMethod)
	if err != nil {
		return nil, err
	}
	sum := &repoSummary{name: name, project: RepoProject(name), public: len(public) > 0}
	for _, t := range tags {
		if at := time.Unix(idx.Tags[t].T, 0); at.After(sum.updated) {
			sum.updated = at
		}
	}
	return connect.NewResponse(&registryv1.GetRepositoryResponse{Repository: s.message(sum, len(tags))}), nil
}

// ListTags implements RegistryService: visible tags, newest first.
func (s *Service) ListTags(ctx context.Context, req *connect.Request[registryv1.ListTagsRequest]) (*connect.Response[registryv1.ListTagsResponse], error) {
	m := req.Msg
	size, err := api.PageSize(m.GetPageSize())
	if err != nil {
		return nil, err
	}
	pos, err := api.DecodePageToken("tags/"+m.GetRepository(), m.GetPageToken())
	if err != nil {
		return nil, err
	}
	idx, tags, public, err := s.visibleIndex(ctx, m.GetRepository(), listTagsMethod)
	if err != nil {
		return nil, err
	}
	sort.SliceStable(tags, func(i, j int) bool { return idx.Tags[tags[i]].T > idx.Tags[tags[j]].T })
	out := &registryv1.ListTagsResponse{}
	for i := int(min(pos, uint64(len(tags)))); i < len(tags) && len(out.Tags) < size; i++ {
		t := tags[i]
		e := idx.Tags[t]
		tag := &registryv1.Tag{Tag: t, Digest: e.D, MediaType: e.M, ReleaseId: e.R, Public: public[t],
			CreatedAt: timestamppb.New(time.Unix(e.T, 0))}
		tag.Platforms, tag.Size = s.describe(ctx, e.D)
		out.Tags = append(out.Tags, tag)
		if len(out.Tags) == size && i+1 < len(tags) {
			out.NextPageToken = api.EncodePageToken("tags/"+m.GetRepository(), uint64(i+1))
		}
	}
	return connect.NewResponse(out), nil
}

// describe reads the platforms of an index and the layer size of its largest platform
// (best effort: a read error leaves them empty).
func (s *Service) describe(ctx context.Context, digest string) ([]string, int64) {
	body, _, err := s.R.c.Objects.Read(ctx, BlobKey(digest), maxManifest)
	if err != nil {
		return nil, 0
	}
	var x struct {
		Manifests []struct {
			Digest   string `json:"digest"`
			Platform *struct {
				OS           string `json:"os"`
				Architecture string `json:"architecture"`
			} `json:"platform"`
		} `json:"manifests"`
		Layers []struct {
			Size int64 `json:"size"`
		} `json:"layers"`
	}
	if json.Unmarshal(body, &x) != nil {
		return nil, 0
	}
	layers := func(b []byte) int64 {
		var n int64
		var y struct {
			Layers []struct {
				Size int64 `json:"size"`
			} `json:"layers"`
		}
		if json.Unmarshal(b, &y) == nil {
			for _, l := range y.Layers {
				n += l.Size
			}
		}
		return n
	}
	if len(x.Manifests) == 0 {
		return nil, layers(body)
	}
	var plats []string
	var size int64
	for i, c := range x.Manifests {
		if c.Platform != nil && c.Platform.OS != "unknown" {
			plats = append(plats, c.Platform.OS+"/"+c.Platform.Architecture)
		}
		if i >= 8 || !ValidDigest(c.Digest) {
			continue
		}
		if b, _, err := s.R.c.Objects.Read(ctx, BlobKey(c.Digest), maxManifest); err == nil {
			size = max(size, layers(b))
		}
	}
	return plats, size
}
