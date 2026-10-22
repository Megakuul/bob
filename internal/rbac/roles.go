package rbac

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/megakuul/bob/internal/api"
	accessv1 "github.com/megakuul/bob/internal/gen/bob/access/v1"
	"github.com/megakuul/bob/internal/store"
)

// Role CRUD of RoleService. A role is `role/<id>`; `roles/all` lists the ids; `rmem/<id>`
// lists its bindings (bindings.go). The built-in role admin has no record.

// MaxRoles bounds roles/all (SPEC §05.4.2).
const MaxRoles = 1000

// maxDescription bounds Role.description.
const maxDescription = 1000

func errBuiltIn() error { return api.FailedPrecondition("role admin is built in") }

func builtinAdmin() *accessv1.Role {
	return &accessv1.Role{
		Id: Admin, Description: "every method on every project (built in)", BuiltIn: true,
		Permissions: []*accessv1.Permission{{Method: Any, Project: Any}},
	}
}

func roleMsg(r *store.RoleRecord) *accessv1.Role {
	out := &accessv1.Role{Id: r.ID, Description: r.Description, Etag: r.ETag}
	for _, p := range r.Permissions {
		out.Permissions = append(out.Permissions, &accessv1.Permission{Method: p.Method, Project: p.Project})
	}
	return out
}

func permsOf(ps []*accessv1.Permission) []Permission {
	out := make([]Permission, 0, len(ps))
	for _, p := range ps {
		out = append(out, Permission{Method: p.GetMethod(), Project: p.GetProject()})
	}
	return out
}

func checkDescription(d string) error {
	if len(d) > maxDescription {
		return api.InvalidArgument("role.description", fmt.Sprintf("description is longer than %d bytes", maxDescription))
	}
	return nil
}

// getRole reads a stored role; a missing one is NotFound "role not found".
func (s *Service) getRole(ctx context.Context, id string) (*store.RoleRecord, error) {
	if !ValidRoleID(id) {
		return nil, api.NotFound("role")
	}
	r, err := store.Get(ctx, s.control, &store.RoleRecord{ID: id})
	if errors.Is(err, store.ErrNotFound) {
		return nil, api.NotFound("role")
	}
	return r, err
}

// CreateRole implements RoleService.
func (s *Service) CreateRole(ctx context.Context, req *connect.Request[accessv1.CreateRoleRequest]) (*connect.Response[accessv1.CreateRoleResponse], error) {
	in := req.Msg.GetRole()
	id := in.GetId()
	switch {
	case id == Admin:
		return nil, api.AlreadyExists("role admin already exists (built in)")
	case !ValidRoleID(id):
		return nil, api.InvalidArgument("role.id", "role id must match ^[a-z][a-z0-9-]{1,38}[a-z0-9]$")
	}
	if err := checkDescription(in.GetDescription()); err != nil {
		return nil, err
	}
	perms, err := s.catalog.CheckPermissions(permsOf(in.GetPermissions()))
	if err != nil {
		return nil, err
	}
	rec := &store.RoleRecord{ID: id, Description: in.GetDescription(), Permissions: perms, Created: s.now().UTC()}
	// Reserve the slot in the index first: a full index refuses before anything is written.
	if err := s.indexRole(ctx, id, true); err != nil {
		return nil, err
	}
	if err := store.Create(ctx, s.control, rec); err != nil {
		if errors.Is(err, store.ErrExists) {
			return nil, api.AlreadyExists("role " + id + " already exists")
		}
		return nil, err
	}
	// A role deleted earlier left its member list closed: open it for the new role.
	if _, err := store.CAS(ctx, s.control, &store.RoleMembers{Role: id}, func(cur *store.RoleMembers) (*store.RoleMembers, error) {
		if cur == nil || !cur.Closed {
			return nil, store.ErrNoChange
		}
		return nil, nil
	}); err != nil {
		return nil, err
	}
	return connect.NewResponse(&accessv1.CreateRoleResponse{Role: roleMsg(rec)}), nil
}

// indexRole adds id to (add) or removes it from roles/all.
func (s *Service) indexRole(ctx context.Context, id string, add bool) error {
	_, err := store.CAS(ctx, s.control, store.RoleIndexKey(), func(cur *store.RoleIndex) (*store.RoleIndex, error) {
		if cur == nil {
			cur = store.RoleIndexKey()
		}
		i, found := slices.BinarySearch(cur.Roles, id)
		switch {
		case add && found, !add && !found:
			return nil, store.ErrNoChange
		case add && len(cur.Roles) >= MaxRoles:
			return nil, api.ResourceExhausted(fmt.Sprintf("a server has at most %d roles", MaxRoles))
		case add:
			cur.Roles = slices.Insert(cur.Roles, i, id)
		default:
			cur.Roles = slices.Delete(cur.Roles, i, i+1)
		}
		return cur, nil
	})
	return err
}

// GetRole implements RoleService.
func (s *Service) GetRole(ctx context.Context, req *connect.Request[accessv1.GetRoleRequest]) (*connect.Response[accessv1.GetRoleResponse], error) {
	if req.Msg.GetId() == Admin {
		return connect.NewResponse(&accessv1.GetRoleResponse{Role: builtinAdmin()}), nil
	}
	r, err := s.getRole(ctx, req.Msg.GetId())
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&accessv1.GetRoleResponse{Role: roleMsg(r)}), nil
}

// roleIDs returns admin followed by the stored role ids.
func (s *Service) roleIDs(ctx context.Context) ([]string, error) {
	idx, err := store.Get(ctx, s.control, store.RoleIndexKey())
	switch {
	case errors.Is(err, store.ErrNotFound):
		return []string{Admin}, nil
	case err != nil:
		return nil, err
	}
	return append([]string{Admin}, idx.Roles...), nil
}

// ListRoles implements RoleService: admin first, then the stored roles by id; query filters
// by id prefix.
func (s *Service) ListRoles(ctx context.Context, req *connect.Request[accessv1.ListRolesRequest]) (*connect.Response[accessv1.ListRolesResponse], error) {
	size, err := api.PageSize(req.Msg.GetPageSize())
	if err != nil {
		return nil, err
	}
	const scope = "roles"
	pos, err := api.DecodePageToken(scope, req.Msg.GetPageToken())
	if err != nil {
		return nil, err
	}
	ids, err := s.roleIDs(ctx)
	if err != nil {
		return nil, err
	}
	ids = slices.DeleteFunc(ids, func(id string) bool { return !strings.HasPrefix(id, req.Msg.GetQuery()) })
	out := &accessv1.ListRolesResponse{}
	for i := pos; i < uint64(len(ids)) && len(out.Roles) < size; i++ {
		if ids[i] == Admin {
			out.Roles = append(out.Roles, builtinAdmin())
		} else {
			r, err := store.Get(ctx, s.control, &store.RoleRecord{ID: ids[i]})
			switch {
			case errors.Is(err, store.ErrNotFound):
				// deleted between the index read and now (or an interrupted create)
			case err != nil:
				return nil, err
			default:
				out.Roles = append(out.Roles, roleMsg(r))
			}
		}
		if len(out.Roles) == size && i+1 < uint64(len(ids)) {
			out.NextPageToken = api.EncodePageToken(scope, i+1)
		}
	}
	return connect.NewResponse(out), nil
}

// UpdateRole implements RoleService: description and permissions. Adding permissions to a
// role is checked like a binding (R-AUTH-152): each added permission must be one the caller
// holds on every project, because the role may be bound @* already.
func (s *Service) UpdateRole(ctx context.Context, req *connect.Request[accessv1.UpdateRoleRequest]) (*connect.Response[accessv1.UpdateRoleResponse], error) {
	in := req.Msg.GetRole()
	if in.GetId() == Admin {
		return nil, errBuiltIn()
	}
	if err := api.CheckMask(req.Msg.GetUpdateMask(), in, "description", "permissions"); err != nil {
		return nil, err
	}
	var setDesc, setPerms bool
	for _, p := range req.Msg.GetUpdateMask().GetPaths() {
		setDesc = setDesc || p == "description"
		setPerms = setPerms || p == "permissions"
	}
	if err := checkDescription(in.GetDescription()); err != nil {
		return nil, err
	}
	var perms []Permission
	if setPerms {
		var err error
		if perms, err = s.catalog.CheckPermissions(permsOf(in.GetPermissions())); err != nil {
			return nil, err
		}
	}
	if _, err := s.getRole(ctx, in.GetId()); err != nil {
		return nil, err
	}
	held, err := s.callerPermissions(ctx)
	if err != nil {
		return nil, err
	}
	rec, err := store.CAS(ctx, s.control, &store.RoleRecord{ID: in.GetId()}, func(cur *store.RoleRecord) (*store.RoleRecord, error) {
		if cur == nil {
			return nil, api.NotFound("role")
		}
		if err := api.CheckETag("role.etag", in.GetEtag(), cur.ETag); err != nil {
			return nil, err
		}
		if setDesc {
			cur.Description = in.GetDescription()
		}
		if setPerms {
			var added []Permission
			for _, p := range perms {
				if !slices.Contains(cur.Permissions, p) {
					added = append(added, p)
				}
			}
			if !s.catalog.Covers(held, added) {
				return nil, api.Forbidden("role would grant permissions you do not have")
			}
			cur.Permissions = perms
		}
		return cur, nil
	})
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&accessv1.UpdateRoleResponse{Role: roleMsg(rec)}), nil
}

// callerPermissions are the effective permissions of the calling principal.
func (s *Service) callerPermissions(ctx context.Context) ([]Permission, error) {
	p := api.PrincipalFrom(ctx)
	if p == nil {
		return nil, nil
	}
	return s.authz.Effective(ctx, p.Bindings)
}

// DeleteRole implements RoleService. A bound role is refused; etag is optional (when given it
// must match). The member list is closed first, so a binding written concurrently either
// lands before (and the delete is refused) or is refused itself.
func (s *Service) DeleteRole(ctx context.Context, req *connect.Request[accessv1.DeleteRoleRequest]) (*connect.Response[accessv1.DeleteRoleResponse], error) {
	id := req.Msg.GetId()
	if id == Admin {
		return nil, errBuiltIn()
	}
	r, err := s.getRole(ctx, id)
	if err != nil {
		return nil, err
	}
	if e := req.Msg.GetEtag(); e != "" && e != r.ETag {
		return nil, api.Aborted("the resource was changed by someone else (etag mismatch); reload and retry")
	}
	if _, err := store.CAS(ctx, s.control, &store.RoleMembers{Role: id}, func(cur *store.RoleMembers) (*store.RoleMembers, error) {
		if cur == nil {
			cur = &store.RoleMembers{Role: id}
		}
		if len(cur.Members) > 0 {
			return nil, api.FailedPrecondition(fmt.Sprintf("role %s is still bound (%d bindings); remove them first", id, len(cur.Members)))
		}
		if cur.Closed {
			return nil, store.ErrNoChange
		}
		cur.Closed = true
		return cur, nil
	}); err != nil {
		return nil, err
	}
	if _, err := store.CAS(ctx, s.control, &store.RoleRecord{ID: id}, func(cur *store.RoleRecord) (*store.RoleRecord, error) {
		if cur == nil {
			return nil, store.ErrNoChange
		}
		if e := req.Msg.GetEtag(); e != "" && e != cur.ETag {
			return nil, api.Aborted("the resource was changed by someone else (etag mismatch); reload and retry")
		}
		return nil, nil
	}); err != nil {
		// The role stays: open its member list again.
		if _, rerr := store.CAS(context.WithoutCancel(ctx), s.control, &store.RoleMembers{Role: id}, func(cur *store.RoleMembers) (*store.RoleMembers, error) {
			if cur == nil || !cur.Closed {
				return nil, store.ErrNoChange
			}
			cur.Closed = false
			return cur, nil
		}); rerr != nil {
			s.log.ErrorContext(ctx, "rbac: role member list stays closed; delete the role again", "role", id, "err", rerr.Error())
		}
		return nil, err
	}
	if err := s.indexRole(ctx, id, false); err != nil {
		return nil, err
	}
	return connect.NewResponse(&accessv1.DeleteRoleResponse{}), nil
}

// stamp is t as a Timestamp, nil when zero.
func stamp(t time.Time) *timestamppb.Timestamp {
	if t.IsZero() {
		return nil
	}
	return timestamppb.New(t)
}
