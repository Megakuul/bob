package rbac

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/megakuul/bob/internal/api"
	"github.com/megakuul/bob/internal/store"
)

// Roles reads role permissions from the control bucket. Every read is a conditional GET
// (GetCached): a changed or deleted role takes effect on the next call, on every orchestrator.
type Roles struct {
	control *store.Bucket
	cache   store.Cache[store.RoleRecord]
}

// NewRoles reads roles from the control bucket.
func NewRoles(control *store.Bucket) *Roles { return &Roles{control: control} }

// Permissions returns the permissions of role id; ok is false for a role that does not exist.
func (r *Roles) Permissions(ctx context.Context, id string) (perms []Permission, ok bool, err error) {
	if id == Admin {
		return adminPermissions(), true, nil
	}
	if !ValidRoleID(id) {
		return nil, false, nil
	}
	rec, _, err := store.GetCached(ctx, r.control, &r.cache, store.RoleKey(id))
	switch {
	case errors.Is(err, store.ErrNotFound):
		return nil, false, nil
	case err != nil:
		return nil, false, fmt.Errorf("rbac: read role %s: %w", id, err)
	}
	return rec.Permissions.Value(), true, nil
}

// Authorizer is the RBAC api.Authorizer (SPEC §6.4.1) and the escalation check of
// CreateRoleBinding (R-AUTH-152).
type Authorizer struct {
	roles   RoleSource
	catalog *Catalog
}

// RoleSource returns the permissions of a role; ok is false for a role that does not exist.
// *Roles is the store-backed one.
type RoleSource interface {
	Permissions(ctx context.Context, id string) (perms []Permission, ok bool, err error)
}

var _ api.Authorizer = (*Authorizer)(nil)

// NewAuthorizer evaluates bindings with the roles of r; c is the permission catalogue.
func NewAuthorizer(r RoleSource, c *Catalog) *Authorizer { return &Authorizer{roles: r, catalog: c} }

// Catalog is the permission catalogue.
func (a *Authorizer) Catalog() *Catalog { return a.catalog }

// CheckGrant is the escalation check of CreateRoleBinding (R-AUTH-152): binding role on
// project is allowed only if every effective permission of the new binding is an effective
// permission of the caller. It answers PermissionDenied "binding would grant permissions you
// do not have" otherwise, and NotFound "role not found" for an unknown role.
func (a *Authorizer) CheckGrant(ctx context.Context, caller *api.Principal, role, project string) error {
	perms, ok, err := a.roles.Permissions(ctx, role)
	if err != nil {
		return err
	}
	if !ok {
		return api.NotFound("role")
	}
	var held []Permission
	if caller != nil {
		if held, err = a.Effective(ctx, caller.Bindings); err != nil {
			return err
		}
	}
	if !a.catalog.Covers(held, Effective(perms, project)) {
		return api.Forbidden("binding would grant permissions you do not have")
	}
	return nil
}

// CheckRolePermissions is the escalation check of a role change: permissions added to a role
// that may already be bound on every project must each be held by the caller on every project
// ("*"). UpdateRole should call it with the permissions it adds.
func (a *Authorizer) CheckRolePermissions(ctx context.Context, caller *api.Principal, added []Permission) error {
	var held []Permission
	if caller != nil {
		var err error
		if held, err = a.Effective(ctx, caller.Bindings); err != nil {
			return err
		}
	}
	if !a.catalog.Covers(held, added) {
		return api.Forbidden("role would grant permissions you do not have")
	}
	return nil
}

// Effective returns the union of the effective permissions of bindings. A binding of a role
// that does not exist grants nothing.
func (a *Authorizer) Effective(ctx context.Context, bindings []api.Binding) ([]Permission, error) {
	var eff []Permission
	for _, b := range bindings {
		perms, ok, err := a.roles.Permissions(ctx, b.Role)
		if err != nil {
			return nil, err
		}
		if ok {
			eff = append(eff, Effective(perms, b.Project)...)
		}
	}
	return eff, nil
}

// Access implements api.Authorizer. A principal with some permission on the project but not
// on method is Denied, one without any is NoAccess (the API answers NotFound, so existence
// never leaks). A call on project "*" has no existence to hide: it is Allowed or Denied.
func (a *Authorizer) Access(ctx context.Context, p *api.Principal, method, project string) (api.Access, error) {
	if p == nil {
		return api.NoAccess, nil
	}
	eff, err := a.Effective(ctx, p.Bindings)
	if err != nil {
		return api.NoAccess, err
	}
	switch {
	case Allows(eff, method, project):
		return api.Allowed, nil
	case project == Any || Touches(eff, project):
		return api.Denied, nil
	}
	return api.NoAccess, nil
}

// Scope is the set of projects where a principal may call a method (LIST_FILTERED).
type Scope struct {
	All      bool     // every project ("*" permission)
	Projects []string // sorted, when not All
}

// Contains reports whether project is in the scope ("*" only when All).
func (s Scope) Contains(project string) bool {
	return s.All || project != Any && slices.Contains(s.Projects, project)
}

// Scope returns the projects where p may call method.
func (a *Authorizer) Scope(ctx context.Context, p *api.Principal, method string) (Scope, error) {
	if p == nil {
		return Scope{}, nil
	}
	eff, err := a.Effective(ctx, p.Bindings)
	if err != nil {
		return Scope{}, err
	}
	var s Scope
	for _, e := range eff {
		if !MethodMatches(e.Method, method) {
			continue
		}
		if e.Project == Any {
			return Scope{All: true}, nil
		}
		if !slices.Contains(s.Projects, e.Project) {
			s.Projects = append(s.Projects, e.Project)
		}
	}
	slices.Sort(s.Projects)
	return s, nil
}

// New returns the authorizer of an orchestrator: roles from the control bucket, the
// catalogue of every RPC the API serves (api.AllRules).
func New(control *store.Bucket) (*Authorizer, error) {
	if control == nil {
		return nil, errors.New("rbac: the control bucket is required")
	}
	rules, err := api.AllRules()
	if err != nil {
		return nil, err
	}
	return NewAuthorizer(NewRoles(control), NewCatalog(rules)), nil
}
