package main

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"

	"connectrpc.com/connect"

	"github.com/megakuul/bob/internal/api"
	"github.com/megakuul/bob/internal/auth"
	"github.com/megakuul/bob/internal/build/jobspec"
	"github.com/megakuul/bob/internal/cache"
	"github.com/megakuul/bob/internal/gen/bob/build/v1/buildv1connect"
	bhttp "github.com/megakuul/bob/internal/http"
	"github.com/megakuul/bob/internal/rbac"
	"github.com/megakuul/bob/internal/server"
	"github.com/megakuul/bob/internal/store/s3client"
)

// createBuild is the method an upload needs on its project (R-PIPE-009).
var createBuild = strings.TrimPrefix(buildv1connect.BuildServiceCreateBuildProcedure, "/")

// uploadParts wires PUT /upload/source/sha256/<hex> (SPEC §6.9.7): snapshots of `--remote`
// builds into src/<project>/<sha>.tar.zst of the artifacts bucket. It needs identity and RBAC.
func uploadParts(ctx context.Context, env *server.Env, parts *server.OrchestratorParts) {
	var id *auth.Identity
	for _, a := range parts.Authn {
		if x, ok := a.(*auth.Identity); ok {
			id = x
		}
	}
	authz, _ := parts.Authz.(*rbac.Authorizer)
	if id == nil || authz == nil || env.Storage == nil {
		env.Log.Error("source upload not available: identity, RBAC or storage is missing")
		return
	}
	st := env.Storage.Config
	obj, err := cache.OpenObjects(ctx, s3client.Options{Endpoint: st.Endpoint, Region: st.Region}, st.Artifacts)
	if err != nil {
		env.Log.Error("source upload not available", "err", err.Error())
		return
	}
	if parts.Routes == nil {
		parts.Routes = map[string]http.Handler{}
	}
	parts.Routes[bhttp.UploadPattern] = bhttp.NewSourceUpload(bhttp.SourceUpload{
		Authorize: func(r *http.Request, project string) error { return authorizeUpload(r, project, id, authz) },
		Sources:   sources{obj},
		Log:       env.Log,
	})
}

// authorizeUpload accepts an API credential (not a download token) whose principal may call
// CreateBuild on project; without that permission the project does not exist for the caller.
func authorizeUpload(r *http.Request, project string, id *auth.Identity, authz *rbac.Authorizer) error {
	p, err := id.HTTPPrincipal(r)
	if err != nil || p == nil || p.Scope == "download" {
		if err != nil && !isUnauthenticated(err) {
			return err
		}
		return bhttp.ErrUnauthenticated
	}
	acc, err := authz.Access(r.Context(), p, createBuild, project)
	if err != nil {
		return err
	}
	if acc != api.Allowed {
		return bhttp.ErrNoAccess
	}
	return nil
}

// isUnauthenticated reports whether err refuses the credential (rather than failing to check it).
func isUnauthenticated(err error) bool {
	return connect.CodeOf(err) == connect.CodeUnauthenticated
}

// sources is bhttp.Sources over the artifacts bucket.
type sources struct{ obj *cache.Objects }

func (s sources) Has(ctx context.Context, project, sha string) (bool, error) {
	_, err := s.obj.Head(ctx, jobspec.UploadKey(project, sha))
	if errors.Is(err, cache.ErrNotFound) {
		return false, nil
	}
	return err == nil, err
}

func (s sources) Create(ctx context.Context, project, sha string, r io.ReadSeeker, size int64) error {
	err := s.obj.CreateFile(ctx, jobspec.UploadKey(project, sha), r, size, "application/zstd")
	if errors.Is(err, cache.ErrPrecondition) {
		return nil // uploaded concurrently (content-addressed, verified before the write)
	}
	return err
}
