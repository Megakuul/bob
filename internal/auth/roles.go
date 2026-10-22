package auth

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/megakuul/bob/internal/api"
	accessv1 "github.com/megakuul/bob/internal/gen/bob/access/v1"
	commonv1 "github.com/megakuul/bob/internal/gen/bob/common/v1"
	"github.com/megakuul/bob/internal/store"
)

// adminRole is the only built-in role (SPEC §6.4.1, R-AUTH-003): {*, *}, never stored,
// immutable.
const adminRole = "admin"

// Limits of RoleService.
const (
	maxRolePermissions = 500
	maxRoles           = 1000
	maxBindings        = 500
)

var errLastAdmin = api.FailedPrecondition("at least one admin must remain")

// isAdmin reports whether bindings make an enabled principal count as an admin (§6.4.1).
func isAdmin(ro []store.RoleBindingEntry, disabled bool) bool {
	return !disabled && hasBinding(ro, adminRole, "*")
}

// adminAdd records principal in AdminIndex (idempotent).
func (id *Identity) adminAdd(ctx context.Context, principal string) error {
	_, err := store.CAS(ctx, id.control, store.AdminIndexKey(), func(cur *store.AdminIndex) (*store.AdminIndex, error) {
		if cur == nil {
			cur = store.AdminIndexKey()
		}
		if slices.Contains(cur.Principals, principal) {
			return nil, store.ErrNoChange
		}
		cur.Principals = append(cur.Principals, principal)
		slices.Sort(cur.Principals)
		return cur, nil
	})
	return storageError(err)
}

// adminRemove removes principal from AdminIndex; it refuses when principal is the last admin.
// It runs before the write that takes the admin binding away (and is undone when that write
// fails), so concurrent removals on several orchestrators leave at least one admin.
func (id *Identity) adminRemove(ctx context.Context, principal string) error {
	_, err := store.CAS(ctx, id.control, store.AdminIndexKey(), func(cur *store.AdminIndex) (*store.AdminIndex, error) {
		if cur == nil || !slices.Contains(cur.Principals, principal) {
			return nil, store.ErrNoChange
		}
		if len(cur.Principals) <= 1 {
			return nil, errLastAdmin
		}
		cur.Principals = slices.DeleteFunc(cur.Principals, func(p string) bool { return p == principal })
		return cur, nil
	})
	if errors.Is(err, errLastAdmin) {
		return errLastAdmin
	}
	return storageError(err)
}

// addMember / removeMember maintain rmem/<role>, the derived member list. Best effort: a
// failure is logged; DeleteRole re-checks the members' records.
func (id *Identity) addMember(ctx context.Context, role, principal string) {
	_, err := store.CAS(ctx, id.control, store.RoleMembersKey(role), func(cur *store.RoleMembers) (*store.RoleMembers, error) {
		if cur == nil {
			cur = store.RoleMembersKey(role)
		}
		if slices.Contains(cur.Principals, principal) {
			return nil, store.ErrNoChange
		}
		cur.Principals = append(cur.Principals, principal)
		return cur, nil
	})
	if err != nil {
		id.log.Warn("role member list not updated", "role", role, "err", err.Error())
	}
}

// removeMember drops principal from rmem/<role> unless remaining still binds the role.
func (id *Identity) removeMember(ctx context.Context, role, principal string, remaining []store.RoleBindingEntry) {
	if slices.ContainsFunc(remaining, func(b store.RoleBindingEntry) bool { return b.Role == role }) {
		return
	}
	_, err := store.CAS(ctx, id.control, store.RoleMembersKey(role), func(cur *store.RoleMembers) (*store.RoleMembers, error) {
		if cur == nil || !slices.Contains(cur.Principals, principal) {
			return nil, store.ErrNoChange
		}
		cur.Principals = slices.DeleteFunc(cur.Principals, func(p string) bool { return p == principal })
		return cur, nil
	})
	if err != nil {
		id.log.Warn("role member list not updated", "role", role, "err", err.Error())
	}
}

// Role returns a role's permissions for RBAC (M15.4): the built-in admin is {*, *}. An unknown
// role is store.ErrNotFound.
func (id *Identity) Role(ctx context.Context, roleID string) ([]store.Permission, error) {
	if roleID == adminRole {
		return []store.Permission{{Method: "*", Project: "*"}}, nil
	}
	r, err := id.role(ctx, roleID)
	if err != nil {
		return nil, err
	}
	return r.Permissions, nil
}

func (id *Identity) role(ctx context.Context, roleID string) (*store.RoleRecord, error) {
	if !slugRE.MatchString(roleID) {
		return nil, store.ErrNotFound
	}
	return store.Get(ctx, id.control, store.RoleKey(roleID))
}

func adminRoleMsg() *accessv1.Role {
	return &accessv1.Role{
		Id: adminRole, Description: "built-in: every method on every project", BuiltIn: true,
		Permissions: []*accessv1.Permission{{Method: "*", Project: "*"}}, Etag: "builtin",
	}
}

func roleMsg(r *store.RoleRecord) *accessv1.Role {
	m := &accessv1.Role{Id: r.ID, Description: r.Description, Etag: r.ETag}
	for _, p := range r.Permissions {
		m.Permissions = append(m.Permissions, &accessv1.Permission{Method: p.Method, Project: p.Project})
	}
	return m
}

// validPermissions checks method and project patterns of a role.
func validPermissions(ps []*accessv1.Permission) ([]store.Permission, error) {
	if len(ps) > maxRolePermissions {
		return nil, api.InvalidArgument("role.permissions", fmt.Sprintf("a role has at most %d permissions", maxRolePermissions))
	}
	out := make([]store.Permission, 0, len(ps))
	for i, p := range ps {
		field := fmt.Sprintf("role.permissions[%d]", i)
		if !validMethodPattern(p.GetMethod()) {
			return nil, api.InvalidArgument(field+".method", fmt.Sprintf("method %q is not \"*\", \"<package>.<Service>/*\" or \"<package>.<Service>/<Method>\"", p.GetMethod()))
		}
		if p.GetProject() != "*" && !slugRE.MatchString(p.GetProject()) {
			return nil, api.InvalidArgument(field+".project", "project must be a project id or \"*\"")
		}
		out = append(out, store.Permission{Method: p.GetMethod(), Project: p.GetProject()})
	}
	return out, nil
}

func validMethodPattern(m string) bool {
	if m == "*" {
		return true
	}
	svc, meth, ok := strings.Cut(m, "/")
	if !ok || !strings.HasPrefix(svc, "bob.") || strings.Count(svc, ".") < 3 || meth == "" || len(m) > 200 {
		return false
	}
	return meth == "*" || !strings.ContainsAny(meth, "/*.")
}

// roleService implements bob.access.v1.RoleService. RBAC's evaluation and the escalation
// check of CreateRoleBinding (R-AUTH-152) belong to M15.4; until then only principals the
// configured Authorizer allows reach these methods (admins with api.AdminBindings).
type roleService struct {
	id      *Identity
	catalog func() []*accessv1.CatalogEntry
}

func (s roleService) CreateRole(ctx context.Context, req *connect.Request[accessv1.CreateRoleRequest]) (*connect.Response[accessv1.CreateRoleResponse], error) {
	in := req.Msg.GetRole()
	if !slugRE.MatchString(in.GetId()) {
		return nil, api.InvalidArgument("role.id", "a role id must match ^[a-z][a-z0-9-]{1,38}[a-z0-9]$")
	}
	if in.GetId() == adminRole {
		return nil, api.AlreadyExists("role admin already exists")
	}
	if len(in.GetDescription()) > 500 {
		return nil, api.InvalidArgument("role.description", "description too long")
	}
	perms, err := validPermissions(in.GetPermissions())
	if err != nil {
		return nil, err
	}
	r := store.RoleKey(in.GetId())
	r.Description, r.Permissions, r.Created = in.GetDescription(), perms, s.id.now()
	if _, err := store.CAS(ctx, s.id.control, store.RolesIndexKey(), func(cur *store.IDList) (*store.IDList, error) {
		if cur == nil {
			cur = store.RolesIndexKey()
		}
		if slices.Contains(cur.IDs, r.ID) {
			return nil, store.ErrNoChange
		}
		if len(cur.IDs) >= maxRoles {
			return nil, api.FailedPrecondition(fmt.Sprintf("at most %d roles", maxRoles))
		}
		cur.IDs = append(cur.IDs, r.ID)
		slices.Sort(cur.IDs)
		return cur, nil
	}); err != nil {
		return nil, storageError(err)
	}
	if err := store.Create(ctx, s.id.control, r); err != nil {
		if errors.Is(err, store.ErrExists) {
			return nil, api.AlreadyExists(fmt.Sprintf("role %s already exists", r.ID))
		}
		return nil, storageError(err)
	}
	return connect.NewResponse(&accessv1.CreateRoleResponse{Role: roleMsg(r)}), nil
}

func (s roleService) GetRole(ctx context.Context, req *connect.Request[accessv1.GetRoleRequest]) (*connect.Response[accessv1.GetRoleResponse], error) {
	if req.Msg.GetId() == adminRole {
		return connect.NewResponse(&accessv1.GetRoleResponse{Role: adminRoleMsg()}), nil
	}
	r, err := s.id.role(ctx, req.Msg.GetId())
	if errors.Is(err, store.ErrNotFound) {
		return nil, api.NotFound("role")
	} else if err != nil {
		return nil, storageError(err)
	}
	return connect.NewResponse(&accessv1.GetRoleResponse{Role: roleMsg(r)}), nil
}

func (s roleService) ListRoles(ctx context.Context, req *connect.Request[accessv1.ListRolesRequest]) (*connect.Response[accessv1.ListRolesResponse], error) {
	size, err := api.PageSize(req.Msg.GetPageSize())
	if err != nil {
		return nil, err
	}
	pos, err := api.DecodePageToken("roles", req.Msg.GetPageToken())
	if err != nil {
		return nil, err
	}
	ids := []string{adminRole}
	idx, err := store.Get(ctx, s.id.control, store.RolesIndexKey())
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return nil, storageError(err)
	}
	if idx != nil {
		ids = append(ids, idx.IDs...)
	}
	out := &accessv1.ListRolesResponse{}
	q := req.Msg.GetQuery()
	for ; pos < uint64(len(ids)); pos++ {
		if len(out.Roles) == size {
			out.NextPageToken = api.EncodePageToken("roles", pos)
			break
		}
		rid := ids[pos]
		if q != "" && !strings.HasPrefix(rid, q) {
			continue
		}
		if rid == adminRole {
			out.Roles = append(out.Roles, adminRoleMsg())
			continue
		}
		r, err := store.Get(ctx, s.id.control, store.RoleKey(rid))
		if errors.Is(err, store.ErrNotFound) {
			continue
		} else if err != nil {
			return nil, storageError(err)
		}
		out.Roles = append(out.Roles, roleMsg(r))
	}
	return connect.NewResponse(out), nil
}

func (s roleService) UpdateRole(ctx context.Context, req *connect.Request[accessv1.UpdateRoleRequest]) (*connect.Response[accessv1.UpdateRoleResponse], error) {
	in := req.Msg.GetRole()
	if in.GetId() == adminRole {
		return nil, api.FailedPrecondition("role admin is built in")
	}
	if err := api.CheckMask(req.Msg.GetUpdateMask(), in, "description", "permissions"); err != nil {
		return nil, err
	}
	perms, err := validPermissions(in.GetPermissions())
	if err != nil {
		return nil, err
	}
	if len(in.GetDescription()) > 500 {
		return nil, api.InvalidArgument("role.description", "description too long")
	}
	if !slugRE.MatchString(in.GetId()) {
		return nil, api.NotFound("role")
	}
	paths := req.Msg.GetUpdateMask().GetPaths()
	next, err := store.CAS(ctx, s.id.control, store.RoleKey(in.GetId()), func(cur *store.RoleRecord) (*store.RoleRecord, error) {
		if cur == nil {
			return nil, api.NotFound("role")
		}
		if err := api.CheckETag("role.etag", in.GetEtag(), cur.ETag); err != nil {
			return nil, err
		}
		for _, p := range paths {
			switch p {
			case "description":
				cur.Description = in.GetDescription()
			case "permissions":
				cur.Permissions = perms
			}
		}
		return cur, nil
	})
	if err != nil {
		return nil, storageError(err)
	}
	return connect.NewResponse(&accessv1.UpdateRoleResponse{Role: roleMsg(next)}), nil
}

// DeleteRole is refused while any principal binds the role.
func (s roleService) DeleteRole(ctx context.Context, req *connect.Request[accessv1.DeleteRoleRequest]) (*connect.Response[accessv1.DeleteRoleResponse], error) {
	rid := req.Msg.GetId()
	if rid == adminRole {
		return nil, api.FailedPrecondition("role admin is built in")
	}
	r, err := s.id.role(ctx, rid)
	if errors.Is(err, store.ErrNotFound) {
		return nil, api.NotFound("role")
	} else if err != nil {
		return nil, storageError(err)
	}
	if e := req.Msg.GetEtag(); e != "" && e != r.ETag {
		return nil, api.Aborted("the resource was changed by someone else (etag mismatch); reload and retry")
	}
	if mem, err := store.Get(ctx, s.id.control, store.RoleMembersKey(rid)); err == nil {
		for _, p := range mem.Principals {
			ro, err := s.id.bindingsOfPrincipal(ctx, p)
			if err != nil && !errors.Is(err, store.ErrNotFound) {
				return nil, storageError(err)
			}
			if slices.ContainsFunc(ro, func(b store.RoleBindingEntry) bool { return b.Role == rid }) {
				return nil, api.FailedPrecondition(fmt.Sprintf("role %s is still bound (to %s)", rid, p))
			}
		}
	} else if !errors.Is(err, store.ErrNotFound) {
		return nil, storageError(err)
	}
	if err := store.Delete(ctx, s.id.control, r); err != nil {
		return nil, storageError(err)
	}
	if _, err := store.CAS(ctx, s.id.control, store.RolesIndexKey(), func(cur *store.IDList) (*store.IDList, error) {
		if cur == nil || !slices.Contains(cur.IDs, rid) {
			return nil, store.ErrNoChange
		}
		cur.IDs = slices.DeleteFunc(cur.IDs, func(x string) bool { return x == rid })
		return cur, nil
	}); err != nil {
		s.id.log.Warn("roles index not updated", "role", rid, "err", err.Error())
	}
	return connect.NewResponse(&accessv1.DeleteRoleResponse{}), nil
}

func (s roleService) ListPermissionCatalog(context.Context, *connect.Request[accessv1.ListPermissionCatalogRequest]) (*connect.Response[accessv1.ListPermissionCatalogResponse], error) {
	var entries []*accessv1.CatalogEntry
	if s.catalog != nil {
		entries = s.catalog()
	}
	return connect.NewResponse(&accessv1.ListPermissionCatalogResponse{Entries: entries}), nil
}

// bindingsOfPrincipal reads the `ro` bindings of "user:<id>" or "sa:<id>".
func (id *Identity) bindingsOfPrincipal(ctx context.Context, principal string) ([]store.RoleBindingEntry, error) {
	kind, pid, _ := strings.Cut(principal, ":")
	switch kind {
	case "user":
		if !userRE.MatchString(pid) {
			return nil, store.ErrNotFound
		}
		u, err := store.Get(ctx, id.control, store.UserKey(pid))
		if err != nil {
			return nil, err
		}
		return u.Bindings, nil
	case "sa":
		if !slugRE.MatchString(pid) {
			return nil, store.ErrNotFound
		}
		sa, err := store.Get(ctx, id.control, store.ServiceAccountKey(pid))
		if err != nil {
			return nil, err
		}
		return sa.Bindings, nil
	}
	return nil, store.ErrNotFound
}

// updateBindings applies fn to the bindings of principal (CAS on its record) and keeps
// AdminIndex consistent: losing admin status is recorded before the write (and refused for the
// last admin), gaining it after the write. It returns the bindings before and after.
func (id *Identity) updateBindings(ctx context.Context, principal string, fn func(ro []store.RoleBindingEntry) ([]store.RoleBindingEntry, error)) (before, after []store.RoleBindingEntry, err error) {
	kind, pid, _ := strings.Cut(principal, ":")
	var disabled bool
	switch kind {
	case "user":
		if !userRE.MatchString(pid) {
			return nil, nil, api.NotFound("principal")
		}
		u, err := store.Get(ctx, id.control, store.UserKey(pid))
		if errors.Is(err, store.ErrNotFound) {
			return nil, nil, api.NotFound("principal")
		} else if err != nil {
			return nil, nil, storageError(err)
		}
		before, disabled = u.Bindings, u.Disabled
	case "sa":
		if !slugRE.MatchString(pid) {
			return nil, nil, api.NotFound("principal")
		}
		sa, err := store.Get(ctx, id.control, store.ServiceAccountKey(pid))
		if errors.Is(err, store.ErrNotFound) {
			return nil, nil, api.NotFound("principal")
		} else if err != nil {
			return nil, nil, storageError(err)
		}
		before, disabled = sa.Bindings, sa.Disabled
	default:
		return nil, nil, api.InvalidArgument("principalId", `a principal is "user:<id>" or "sa:<id>"`)
	}
	planned, err := fn(slices.Clone(before))
	if err != nil {
		return nil, nil, err
	}
	wasAdmin, willAdmin := isAdmin(before, disabled), isAdmin(planned, disabled)
	if wasAdmin && !willAdmin {
		if err := id.adminRemove(ctx, principal); err != nil {
			return nil, nil, err
		}
	}
	apply := func(ro []store.RoleBindingEntry) ([]store.RoleBindingEntry, error) {
		before = ro
		next, err := fn(slices.Clone(ro))
		if err != nil {
			return nil, err
		}
		if isAdmin(ro, disabled) && !isAdmin(next, disabled) && !wasAdmin {
			// Admin status appeared concurrently; retry from the top rather than skip the check.
			return nil, store.ErrConflict
		}
		after = next
		return next, nil
	}
	switch kind {
	case "user":
		var next *store.UserRecord
		next, err = store.CAS(ctx, id.control, store.UserKey(pid), func(cur *store.UserRecord) (*store.UserRecord, error) {
			if cur == nil {
				return nil, api.NotFound("principal")
			}
			ro, err := apply(cur.Bindings)
			if err != nil {
				return nil, err
			}
			cur.Bindings = ro
			return cur, nil
		})
		if err == nil {
			id.users.put(pid, next)
		}
	case "sa":
		var next *store.ServiceAccountRecord
		next, err = store.CAS(ctx, id.control, store.ServiceAccountKey(pid), func(cur *store.ServiceAccountRecord) (*store.ServiceAccountRecord, error) {
			if cur == nil {
				return nil, api.NotFound("principal")
			}
			ro, err := apply(cur.Bindings)
			if err != nil {
				return nil, err
			}
			cur.Bindings = ro
			return cur, nil
		})
		if err == nil {
			id.sas.put(pid, next)
		}
	}
	if err != nil {
		if wasAdmin && !willAdmin {
			_ = id.adminAdd(ctx, principal)
		}
		return nil, nil, storageError(err)
	}
	if !wasAdmin && isAdmin(after, disabled) {
		if err := id.adminAdd(ctx, principal); err != nil {
			return nil, nil, err
		}
	}
	return before, after, nil
}

func bindingID() string {
	return "b" + randomString(crockford, 12)
}

func bindingSource(s string) accessv1.BindingSource {
	if s == sourceOIDC {
		return accessv1.BindingSource_BINDING_SOURCE_OIDC
	}
	return accessv1.BindingSource_BINDING_SOURCE_MANUAL
}

func bindingMsg(principal string, b store.RoleBindingEntry) *accessv1.RoleBinding {
	m := &accessv1.RoleBinding{
		Id: principal + "/" + b.ID, Principal: &commonv1.Principal{Id: principal},
		Role: b.Role, Project: b.Project, Source: bindingSource(b.Source),
	}
	if !b.Created.IsZero() {
		m.CreatedAt = timestamppb.New(b.Created)
	}
	return m
}

func (s roleService) CreateRoleBinding(ctx context.Context, req *connect.Request[accessv1.CreateRoleBindingRequest]) (*connect.Response[accessv1.CreateRoleBindingResponse], error) {
	m := req.Msg
	principal, role, project := m.GetPrincipalId(), m.GetRole(), m.GetProject()
	if project != "*" && !slugRE.MatchString(project) {
		return nil, api.InvalidArgument("project", "project must be a project id or \"*\"")
	}
	if role != adminRole {
		if _, err := s.id.role(ctx, role); errors.Is(err, store.ErrNotFound) {
			return nil, api.NotFound("role")
		} else if err != nil {
			return nil, storageError(err)
		}
	}
	now := s.id.now()
	entry := store.RoleBindingEntry{ID: bindingID(), Role: role, Project: project, Source: sourceManual, Created: now}
	_, _, err := s.id.updateBindings(ctx, principal, func(ro []store.RoleBindingEntry) ([]store.RoleBindingEntry, error) {
		for _, b := range ro {
			if b.Role == role && b.Project == project && b.Source == sourceManual {
				return nil, api.AlreadyExists(fmt.Sprintf("%s already has %s@%s", principal, role, project))
			}
		}
		if len(ro) >= maxBindings {
			return nil, api.FailedPrecondition(fmt.Sprintf("a principal has at most %d bindings", maxBindings))
		}
		return append(ro, entry), nil
	})
	if err != nil {
		return nil, err
	}
	s.id.addMember(ctx, role, principal)
	return connect.NewResponse(&accessv1.CreateRoleBindingResponse{Binding: bindingMsg(principal, entry)}), nil
}

// DeleteRoleBinding takes the binding id "<principal>/<entry id>" of RoleBinding.id; the
// request's project must equal the binding's (the RBAC rule was checked on it).
func (s roleService) DeleteRoleBinding(ctx context.Context, req *connect.Request[accessv1.DeleteRoleBindingRequest]) (*connect.Response[accessv1.DeleteRoleBindingResponse], error) {
	principal, entryID, ok := strings.Cut(req.Msg.GetId(), "/")
	if !ok || entryID == "" {
		return nil, api.NotFound("binding")
	}
	var role string
	_, after, err := s.id.updateBindings(ctx, principal, func(ro []store.RoleBindingEntry) ([]store.RoleBindingEntry, error) {
		i := slices.IndexFunc(ro, func(b store.RoleBindingEntry) bool { return b.ID == entryID })
		if i < 0 || ro[i].Project != req.Msg.GetProject() {
			return nil, api.NotFound("binding")
		}
		role = ro[i].Role
		return slices.Delete(ro, i, i+1), nil
	})
	if err != nil {
		var ce *connect.Error
		if errors.As(err, &ce) && ce.Code() == connect.CodeNotFound {
			return nil, api.NotFound("binding")
		}
		return nil, err
	}
	s.id.removeMember(ctx, role, principal, after)
	return connect.NewResponse(&accessv1.DeleteRoleBindingResponse{}), nil
}

// ListRoleBindings (LIST_FILTERED) lists the bindings of one principal, or of every member
// of one role, or of every role; results are filtered to projects where the caller may call
// ListRoleBindings.
func (s roleService) ListRoleBindings(ctx context.Context, req *connect.Request[accessv1.ListRoleBindingsRequest]) (*connect.Response[accessv1.ListRoleBindingsResponse], error) {
	m := req.Msg
	size, err := api.PageSize(m.GetPageSize())
	if err != nil {
		return nil, err
	}
	pos, err := api.DecodePageToken("bindings", m.GetPageToken())
	if err != nil {
		return nil, err
	}
	caller := api.PrincipalFrom(ctx)
	const method = "bob.access.v1.RoleService/ListRoleBindings"
	if p := m.GetProject(); p != "" {
		if a, err := s.id.authz.Access(ctx, caller, method, p); err != nil {
			return nil, err
		} else if a != api.Allowed {
			return nil, api.NotFound("project")
		}
	}
	var principals []string
	switch {
	case m.GetPrincipalId() != "":
		principals = []string{m.GetPrincipalId()}
	default:
		roles := []string{adminRole}
		if m.GetRole() != "" {
			roles = []string{m.GetRole()}
		} else if idx, err := store.Get(ctx, s.id.control, store.RolesIndexKey()); err == nil {
			roles = append(roles, idx.IDs...)
		} else if !errors.Is(err, store.ErrNotFound) {
			return nil, storageError(err)
		}
		for _, r := range roles {
			if !slugRE.MatchString(r) {
				continue
			}
			mem, err := store.Get(ctx, s.id.control, store.RoleMembersKey(r))
			if errors.Is(err, store.ErrNotFound) {
				continue
			} else if err != nil {
				return nil, storageError(err)
			}
			for _, p := range mem.Principals {
				if !slices.Contains(principals, p) {
					principals = append(principals, p)
				}
			}
		}
		slices.Sort(principals)
	}
	allowed := map[string]bool{}
	var all []*accessv1.RoleBinding
	for _, p := range principals {
		ro, err := s.id.bindingsOfPrincipal(ctx, p)
		if errors.Is(err, store.ErrNotFound) {
			continue
		} else if err != nil {
			return nil, storageError(err)
		}
		for _, b := range ro {
			if m.GetRole() != "" && b.Role != m.GetRole() || m.GetProject() != "" && b.Project != m.GetProject() {
				continue
			}
			ok, seen := allowed[b.Project]
			if !seen {
				a, err := s.id.authz.Access(ctx, caller, method, b.Project)
				if err != nil {
					return nil, err
				}
				ok = a == api.Allowed
				allowed[b.Project] = ok
			}
			if ok {
				all = append(all, bindingMsg(p, b))
			}
		}
	}
	out := &accessv1.ListRoleBindingsResponse{}
	for ; pos < uint64(len(all)); pos++ {
		if len(out.Bindings) == size {
			out.NextPageToken = api.EncodePageToken("bindings", pos)
			break
		}
		out.Bindings = append(out.Bindings, all[pos])
	}
	return connect.NewResponse(out), nil
}
