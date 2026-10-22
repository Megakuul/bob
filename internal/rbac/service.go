package rbac

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"connectrpc.com/connect"

	"github.com/megakuul/bob/internal/api"
	accessv1 "github.com/megakuul/bob/internal/gen/bob/access/v1"
	"github.com/megakuul/bob/internal/gen/bob/access/v1/accessv1connect"
	"github.com/megakuul/bob/internal/store"
)

// Config configures the RBAC service of an orchestrator.
type Config struct {
	Control    *store.Bucket
	Rules      api.Rules  // the API's rule table (api.AllRules): the permission catalogue
	Principals Principals // identity's principal store; nil: binding RPCs answer Unimplemented
	Now        func() time.Time
	Log        *slog.Logger
}

// Service is RBAC: RoleService (roles, bindings, permission catalogue), the OIDC mapping
// methods of OidcService, the Authorizer and the admin invariant.
type Service struct {
	accessv1connect.UnimplementedRoleServiceHandler
	control    *store.Bucket
	roles      *Roles
	authz      *Authorizer
	catalog    *Catalog
	principals Principals
	now        func() time.Time
	log        *slog.Logger
	mappings   store.Cache[store.OidcMappings]
}

var _ accessv1connect.RoleServiceHandler = (*Service)(nil)

// New builds the RBAC service.
func New(cfg Config) (*Service, error) {
	if cfg.Control == nil {
		return nil, errors.New("rbac: Config.Control is required")
	}
	if len(cfg.Rules) == 0 {
		return nil, errors.New("rbac: Config.Rules is required")
	}
	s := &Service{
		control: cfg.Control, roles: NewRoles(cfg.Control), catalog: NewCatalog(cfg.Rules),
		principals: cfg.Principals, now: cfg.Now, log: cfg.Log,
	}
	s.authz = NewAuthorizer(s.roles)
	if s.now == nil {
		s.now = time.Now
	}
	if s.log == nil {
		s.log = slog.Default()
	}
	return s, nil
}

// Authorizer is the api.Authorizer of this service's roles.
func (s *Service) Authorizer() *Authorizer { return s.authz }

// Catalog is the permission catalogue.
func (s *Service) Catalog() *Catalog { return s.catalog }

// errNoIdentity answers binding RPCs while no principal store is configured.
func errNoIdentity() error {
	return connect.NewError(connect.CodeUnimplemented, errors.New("role bindings need the principal store (identity)"))
}

// ListPermissionCatalog implements RoleService: every non-worker RPC with its rule kind.
func (s *Service) ListPermissionCatalog(context.Context, *connect.Request[accessv1.ListPermissionCatalogRequest]) (*connect.Response[accessv1.ListPermissionCatalogResponse], error) {
	out := &accessv1.ListPermissionCatalogResponse{}
	for _, m := range s.catalog.Methods() {
		out.Entries = append(out.Entries, &accessv1.CatalogEntry{Method: m, Kind: s.catalog.Kind(m)})
	}
	return connect.NewResponse(out), nil
}
