package overview

import (
	"context"

	"connectrpc.com/connect"

	"github.com/megakuul/bob/internal/api"
	registryv1 "github.com/megakuul/bob/internal/gen/bob/registry/v1"
	"github.com/megakuul/bob/internal/gen/bob/registry/v1/registryv1connect"
	releasev1 "github.com/megakuul/bob/internal/gen/bob/release/v1"
	"github.com/megakuul/bob/internal/gen/bob/release/v1/releasev1connect"
	"github.com/megakuul/bob/internal/project"
)

// Releases answers ReleaseService until releases exist on the server (M16.4): no code
// creates a release yet, so every list is empty; the lookups and changes answer
// "not implemented: bob.release.v1.ReleaseService/<method>". M16.4 replaces it.
type Releases struct {
	releasev1connect.UnimplementedReleaseServiceHandler
}

// ListReleases implements ReleaseService (validated request, empty list).
func (Releases) ListReleases(_ context.Context, req *connect.Request[releasev1.ListReleasesRequest]) (*connect.Response[releasev1.ListReleasesResponse], error) {
	m := req.Msg
	if _, err := api.PageSize(m.GetPageSize()); err != nil {
		return nil, err
	}
	if _, err := api.DecodePageToken("releases/"+m.GetProjectId()+"/"+m.GetName(), m.GetPageToken()); err != nil {
		return nil, err
	}
	if p := m.GetProjectId(); p != "" && !project.ValidID(p) {
		return nil, api.InvalidArgument("projectId", "invalid project id")
	}
	return connect.NewResponse(&releasev1.ListReleasesResponse{}), nil
}

// Registry answers RegistryService until the OCI registry exists (M16.3): there are no
// repositories, so lists are empty and every repository is NotFound (GetRepository answers
// "not implemented"). M16.3 replaces it.
type Registry struct {
	registryv1connect.UnimplementedRegistryServiceHandler
}

// ListRepositories implements RegistryService (validated request, empty list).
func (Registry) ListRepositories(_ context.Context, req *connect.Request[registryv1.ListRepositoriesRequest]) (*connect.Response[registryv1.ListRepositoriesResponse], error) {
	m := req.Msg
	if _, err := api.PageSize(m.GetPageSize()); err != nil {
		return nil, err
	}
	if _, err := api.DecodePageToken("repositories/"+m.GetProjectId(), m.GetPageToken()); err != nil {
		return nil, err
	}
	return connect.NewResponse(&registryv1.ListRepositoriesResponse{}), nil
}

// ListTags implements RegistryService: no repository exists yet.
func (Registry) ListTags(_ context.Context, req *connect.Request[registryv1.ListTagsRequest]) (*connect.Response[registryv1.ListTagsResponse], error) {
	if _, err := api.PageSize(req.Msg.GetPageSize()); err != nil {
		return nil, err
	}
	return nil, api.NotFound("repository")
}
