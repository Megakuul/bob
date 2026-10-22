package api

import (
	"context"
	"slices"
	"strings"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	accessv1 "github.com/megakuul/bob/internal/gen/bob/access/v1"
	"github.com/megakuul/bob/internal/gen/bob/access/v1/accessv1connect"
	authv1 "github.com/megakuul/bob/internal/gen/bob/auth/v1"
	"github.com/megakuul/bob/internal/gen/bob/auth/v1/authv1connect"
	authzv1 "github.com/megakuul/bob/internal/gen/bob/authz/v1"
	commonv1 "github.com/megakuul/bob/internal/gen/bob/common/v1"
)

// builtinAuth answers the AuthService methods that need nothing but the server's identity and
// the authenticated principal. Login, sessions and passwords are identity (M15.2).
type builtinAuth struct {
	authv1connect.UnimplementedAuthServiceHandler
	info Info
}

func (a *builtinAuth) nix() *authv1.NixInfo {
	if a.info.BaseURL == "" {
		return nil
	}
	return &authv1.NixInfo{Substituter: strings.TrimSuffix(a.info.BaseURL, "/") + "/nix"}
}

func (a *builtinAuth) GetServerInfo(context.Context, *connect.Request[authv1.GetServerInfoRequest]) (*connect.Response[authv1.GetServerInfoResponse], error) {
	return connect.NewResponse(&authv1.GetServerInfoResponse{
		ServerName:   a.info.Name,
		BaseUrl:      a.info.BaseURL,
		Version:      a.info.Version,
		Nix:          a.nix(),
		LoginOptions: []*authv1.LoginOption{{DisplayName: "Password"}},
	}), nil
}

func (a *builtinAuth) WhoAmI(ctx context.Context, _ *connect.Request[authv1.WhoAmIRequest]) (*connect.Response[authv1.WhoAmIResponse], error) {
	return connect.NewResponse(WhoAmI(PrincipalFrom(ctx), a.nix())), nil
}

// WhoAmI renders a principal as WhoAmIResponse (shared with the identity implementation).
func WhoAmI(p *Principal, nix *authv1.NixInfo) *authv1.WhoAmIResponse {
	if p == nil {
		return &authv1.WhoAmIResponse{}
	}
	r := &authv1.WhoAmIResponse{
		Principal:          &commonv1.Principal{Id: p.ID, DisplayName: p.DisplayName},
		Login:              p.Login,
		TokenScope:         p.Scope,
		Nix:                nix,
		MustChangePassword: p.MustChangePassword,
	}
	if !p.Expires.IsZero() {
		r.TokenExpiresAt = timestamppb.New(p.Expires)
	}
	var projects []string
	for _, b := range p.Bindings {
		r.Bindings = append(r.Bindings, &commonv1.RoleBindingRef{Role: b.Role, Project: b.Project})
		if !slices.Contains(projects, b.Project) {
			projects = append(projects, b.Project)
		}
	}
	if slices.Contains(projects, "*") {
		projects = []string{"*"}
	}
	slices.Sort(projects)
	r.Projects = projects
	return r
}

// builtinRole serves ListPermissionCatalog from the rule table; the rest of RoleService is
// RBAC (M15.4).
type builtinRole struct {
	accessv1connect.UnimplementedRoleServiceHandler
	s *Server
}

func (b *builtinRole) ListPermissionCatalog(context.Context, *connect.Request[accessv1.ListPermissionCatalogRequest]) (*connect.Response[accessv1.ListPermissionCatalogResponse], error) {
	return connect.NewResponse(&accessv1.ListPermissionCatalogResponse{Entries: b.s.Catalog()}), nil
}

// Catalog is every non-worker RPC with its rule kind, sorted by method (ListPermissionCatalog).
// Descriptions stay empty: the generated descriptors carry no comments.
func (s *Server) Catalog() []*accessv1.CatalogEntry {
	var out []*accessv1.CatalogEntry
	for _, r := range s.rules {
		if r.Kind == authzv1.Kind_KIND_WORKER {
			continue
		}
		out = append(out, &accessv1.CatalogEntry{Method: r.Method, Kind: r.KindName()})
	}
	slices.SortFunc(out, func(a, b *accessv1.CatalogEntry) int { return strings.Compare(a.Method, b.Method) })
	return out
}
