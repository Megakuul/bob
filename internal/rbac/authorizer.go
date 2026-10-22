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
	rec, _, err := store.GetCached(ctx, r.control, &r.cache, &store.RoleRecord{ID: id})
	switch {
	case errors.Is(err, store.ErrNotFound):
		return nil, false, nil
	case err != nil:
		return nil, false, fmt.Errorf("rbac: read role %s: %w", id, err)
	}
	return rec.Permissions, true, nil
}

// Authorizer is the RBAC api.Authorizer (SPEC §6.4.1).
type Authorizer struct{ roles *Roles }

var _ api.Authorizer = (*Authorizer)(nil)

// NewAuthorizer evaluates bindings with the roles of r.
func NewAuthorizer(r *Roles) *Authorizer { return &Authorizer{roles: r} }

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
