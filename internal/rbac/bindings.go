package rbac

import (
	"context"
	"errors"
	"slices"
	"strings"

	"connectrpc.com/connect"

	"github.com/megakuul/bob/internal/api"
	accessv1 "github.com/megakuul/bob/internal/gen/bob/access/v1"
	commonv1 "github.com/megakuul/bob/internal/gen/bob/common/v1"
	"github.com/megakuul/bob/internal/store"
)

// Role bindings. The truth is the `ro` list of the principal's record (Principals); the
// member list `rmem/<role>` is derived. A binding is written in this order:
//
//  1. rmem: add the member as pending (refused when the role was deleted: Closed),
//  2. the principal's record: add the binding (CAS, Principals.UpdateBindings),
//  3. admin@* of an enabled principal: join the admin index,
//  4. rmem: confirm the member.
//
// and removed in reverse (admin index leave first). A crash leaves at most a pending member,
// which ListRoleBindings checks against the principal and DeleteRole counts as bound.

// ErrBindingExists is returned by Grant for a binding the principal already holds.
var ErrBindingExists = errors.New("binding exists")

func (s *Service) members(ctx context.Context, role string, fn func(cur *store.RoleMembers) error) error {
	_, err := store.CAS(ctx, s.control, &store.RoleMembers{Role: role}, func(cur *store.RoleMembers) (*store.RoleMembers, error) {
		if cur == nil {
			cur = &store.RoleMembers{Role: role}
		}
		if cur.Members == nil {
			cur.Members = map[string]store.RoleMember{}
		}
		if err := fn(cur); err != nil {
			return nil, err
		}
		return cur, nil
	})
	return err
}

func (s *Service) dropMember(ctx context.Context, role, id string) error {
	return s.members(ctx, role, func(cur *store.RoleMembers) error {
		if _, ok := cur.Members[id]; !ok {
			return store.ErrNoChange
		}
		delete(cur.Members, id)
		return nil
	})
}

// roleExists reports whether role exists (admin always does).
func (s *Service) roleExists(ctx context.Context, role string) ([]Permission, error) {
	perms, ok, err := s.roles.Permissions(ctx, role)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, api.NotFound("role")
	}
	return perms, nil
}

// Grant binds role on project to principal with source (store.BindingManual or
// store.BindingOIDC). It checks nothing about the caller: CreateRoleBinding checks
// escalation first; identity uses it for the bootstrap admin and OIDC logins. A binding the
// principal already holds is ErrBindingExists.
func (s *Service) Grant(ctx context.Context, principal, role, project, source string) (store.RoleBinding, error) {
	b := store.RoleBinding{ID: BindingID(principal, role, project), Role: role, Project: project, Source: source, Created: s.now().UTC()}
	if s.principals == nil {
		return b, errNoIdentity()
	}
	if _, err := s.roleExists(ctx, role); err != nil {
		return b, err
	}
	err := s.members(ctx, role, func(cur *store.RoleMembers) error {
		if cur.Closed {
			return api.NotFound("role")
		}
		if m, ok := cur.Members[b.ID]; ok && !m.Pending {
			return store.ErrNoChange // held already; step 2 says so
		}
		cur.Members[b.ID] = store.RoleMember{Principal: principal, Project: project, Source: source, Created: b.Created, Pending: true}
		return nil
	})
	if err != nil {
		return b, err
	}
	var existed, enabled bool
	err = s.principals.UpdateBindings(ctx, principal, func(cur []store.RoleBinding, en bool) ([]store.RoleBinding, error) {
		enabled = en
		if i := slices.IndexFunc(cur, func(x store.RoleBinding) bool { return x.ID == b.ID }); i >= 0 {
			existed = true
			b = cur[i]
			return nil, store.ErrNoChange
		}
		existed = false
		return append(slices.Clone(cur), b), nil
	})
	if err != nil {
		if derr := s.dropMember(context.WithoutCancel(ctx), role, b.ID); derr != nil {
			s.log.ErrorContext(ctx, "rbac: pending role member left behind", "role", role, "binding", b.ID, "err", derr.Error())
		}
		if errors.Is(err, ErrNoPrincipal) {
			return b, api.NotFound("principal")
		}
		return b, err
	}
	if isAdminBinding(role, project) && enabled {
		if err := s.join(ctx, principal); err != nil {
			return b, err
		}
	}
	if err := s.members(ctx, role, func(cur *store.RoleMembers) error {
		m := store.RoleMember{Principal: principal, Project: project, Source: b.Source, Created: b.Created}
		if cur.Members[b.ID] == m {
			return store.ErrNoChange
		}
		cur.Members[b.ID] = m
		return nil
	}); err != nil {
		return b, err
	}
	if existed {
		return b, ErrBindingExists
	}
	return b, nil
}

// Revoke removes binding id from its principal. Removing admin@* of the last admin is
// refused (FailedPrecondition "at least one admin must remain"). A binding that does not
// exist is NotFound "binding not found".
func (s *Service) Revoke(ctx context.Context, id string) error {
	principal, role, project, ok := ParseBindingID(id)
	if !ok {
		return api.NotFound("binding")
	}
	if s.principals == nil {
		return errNoIdentity()
	}
	cur, enabled, err := s.principals.Bindings(ctx, principal)
	switch {
	case errors.Is(err, ErrNoPrincipal):
		return api.NotFound("binding")
	case err != nil:
		return err
	case !slices.ContainsFunc(cur, func(b store.RoleBinding) bool { return b.ID == id }):
		// not held (any more): make sure the member list agrees
		if err := s.dropMember(ctx, role, id); err != nil {
			return err
		}
		return api.NotFound("binding")
	}
	admin := isAdminBinding(role, project) && enabled
	if admin {
		if err := s.leave(ctx, principal); err != nil {
			return err
		}
	}
	err = s.principals.UpdateBindings(ctx, principal, func(cur []store.RoleBinding, _ bool) ([]store.RoleBinding, error) {
		i := slices.IndexFunc(cur, func(b store.RoleBinding) bool { return b.ID == id })
		if i < 0 {
			return nil, store.ErrNoChange
		}
		return slices.Delete(slices.Clone(cur), i, i+1), nil
	})
	if err != nil {
		if admin {
			return s.rejoin(ctx, principal, err)
		}
		return err
	}
	return s.dropMember(ctx, role, id)
}

// CreateRoleBinding implements RoleService. The interceptor checked CreateRoleBinding on the
// binding's project; here every effective permission of the new binding must be one the
// caller holds (R-AUTH-152).
func (s *Service) CreateRoleBinding(ctx context.Context, req *connect.Request[accessv1.CreateRoleBindingRequest]) (*connect.Response[accessv1.CreateRoleBindingResponse], error) {
	m := req.Msg
	if !ValidPrincipal(m.GetPrincipalId()) {
		return nil, api.InvalidArgument("principalId", `principal must be "user:<id>" or "sa:<id>"`)
	}
	if !ValidProject(m.GetProject()) {
		return nil, api.InvalidArgument("project", `project must be a project id or "*"`)
	}
	perms, err := s.roleExists(ctx, m.GetRole())
	if err != nil {
		return nil, err
	}
	held, err := s.callerPermissions(ctx)
	if err != nil {
		return nil, err
	}
	if !s.catalog.Covers(held, Effective(perms, m.GetProject())) {
		return nil, api.Forbidden("binding would grant permissions you do not have")
	}
	b, err := s.Grant(ctx, m.GetPrincipalId(), m.GetRole(), m.GetProject(), store.BindingManual)
	if errors.Is(err, ErrBindingExists) {
		return nil, api.AlreadyExists("the principal holds role " + m.GetRole() + " on " + m.GetProject() + " already")
	}
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&accessv1.CreateRoleBindingResponse{Binding: bindingMsg(m.GetPrincipalId(), b)}), nil
}

// DeleteRoleBinding implements RoleService. The request's project must be the binding's (the
// interceptor authorized that project).
func (s *Service) DeleteRoleBinding(ctx context.Context, req *connect.Request[accessv1.DeleteRoleBindingRequest]) (*connect.Response[accessv1.DeleteRoleBindingResponse], error) {
	_, _, project, ok := ParseBindingID(req.Msg.GetId())
	if !ok || project != req.Msg.GetProject() {
		return nil, api.NotFound("binding")
	}
	if err := s.Revoke(ctx, req.Msg.GetId()); err != nil {
		return nil, err
	}
	return connect.NewResponse(&accessv1.DeleteRoleBindingResponse{}), nil
}

func bindingMsg(principal string, b store.RoleBinding) *accessv1.RoleBinding {
	src := accessv1.BindingSource_BINDING_SOURCE_MANUAL
	if b.Source == store.BindingOIDC {
		src = accessv1.BindingSource_BINDING_SOURCE_OIDC
	}
	return &accessv1.RoleBinding{
		Id: b.ID, Principal: &commonv1.Principal{Id: principal}, Role: b.Role, Project: b.Project,
		Source: src, CreatedAt: stamp(b.Created),
	}
}

// ListRoleBindings implements RoleService (LIST_FILTERED): only bindings on projects where
// the caller may call ListRoleBindings; an explicit project filter outside them is NotFound.
// With principal_id the principal's record is read, otherwise the member lists of the role
// (or of every role).
func (s *Service) ListRoleBindings(ctx context.Context, req *connect.Request[accessv1.ListRoleBindingsRequest]) (*connect.Response[accessv1.ListRoleBindingsResponse], error) {
	m := req.Msg
	size, err := api.PageSize(m.GetPageSize())
	if err != nil {
		return nil, err
	}
	scopeName := "bindings:" + m.GetPrincipalId() + ":" + m.GetRole() + ":" + m.GetProject()
	pos, err := api.DecodePageToken(scopeName, m.GetPageToken())
	if err != nil {
		return nil, err
	}
	scope, err := s.authz.Scope(ctx, api.PrincipalFrom(ctx), listBindingsMethod)
	if err != nil {
		return nil, err
	}
	if p := m.GetProject(); p != "" && !scope.Contains(p) {
		return nil, api.NotFound("project")
	}
	if s.principals == nil {
		return nil, errNoIdentity()
	}
	all, err := s.collectBindings(ctx, m.GetPrincipalId(), m.GetRole())
	if err != nil {
		return nil, err
	}
	all = slices.DeleteFunc(all, func(b *accessv1.RoleBinding) bool {
		return (m.GetRole() != "" && b.Role != m.GetRole()) ||
			(m.GetProject() != "" && b.Project != m.GetProject()) ||
			!scope.Contains(b.Project)
	})
	slices.SortFunc(all, func(a, b *accessv1.RoleBinding) int {
		for _, c := range [][2]string{{a.Role, b.Role}, {a.Principal.GetId(), b.Principal.GetId()}, {a.Project, b.Project}} {
			if n := strings.Compare(c[0], c[1]); n != 0 {
				return n
			}
		}
		return 0
	})
	out := &accessv1.ListRoleBindingsResponse{}
	if pos < uint64(len(all)) {
		end := min(pos+uint64(size), uint64(len(all)))
		out.Bindings = all[pos:end]
		if end < uint64(len(all)) {
			out.NextPageToken = api.EncodePageToken(scopeName, end)
		}
	}
	return connect.NewResponse(out), nil
}

// listBindingsMethod is the permission LIST_FILTERED checks for ListRoleBindings.
const listBindingsMethod = "bob.access.v1.RoleService/ListRoleBindings"

// collectBindings returns the bindings of principal (when set), else of role (when set), else
// of every role.
func (s *Service) collectBindings(ctx context.Context, principal, role string) ([]*accessv1.RoleBinding, error) {
	if principal != "" {
		if !ValidPrincipal(principal) {
			return nil, nil
		}
		bs, _, err := s.principals.Bindings(ctx, principal)
		if errors.Is(err, ErrNoPrincipal) {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		out := make([]*accessv1.RoleBinding, 0, len(bs))
		for _, b := range bs {
			out = append(out, bindingMsg(principal, b))
		}
		return out, nil
	}
	roles := []string{role}
	if role == "" {
		var err error
		if roles, err = s.roleIDs(ctx); err != nil {
			return nil, err
		}
	}
	var out []*accessv1.RoleBinding
	for _, r := range roles {
		if r != Admin && !ValidRoleID(r) {
			continue
		}
		mem, err := store.Get(ctx, s.control, &store.RoleMembers{Role: r})
		if errors.Is(err, store.ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		for id, m := range mem.Members {
			b := store.RoleBinding{ID: id, Role: r, Project: m.Project, Source: m.Source, Created: m.Created}
			if m.Pending {
				// An interrupted write: list it only if the principal holds it.
				held, _, err := s.principals.Bindings(ctx, m.Principal)
				if errors.Is(err, ErrNoPrincipal) {
					continue
				}
				if err != nil {
					return nil, err
				}
				i := slices.IndexFunc(held, func(x store.RoleBinding) bool { return x.ID == id })
				if i < 0 {
					continue
				}
				b = held[i]
			}
			out = append(out, bindingMsg(m.Principal, b))
		}
	}
	return out, nil
}
