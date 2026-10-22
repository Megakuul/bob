package auth

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/megakuul/bob/internal/api"
	accessv1 "github.com/megakuul/bob/internal/gen/bob/access/v1"
	"github.com/megakuul/bob/internal/store"
)

// Name rules of SPEC §6.1.
var (
	slugRE  = regexp.MustCompile(`^[a-z][a-z0-9-]{1,38}[a-z0-9]$`)
	loginRE = regexp.MustCompile(`^[a-z][a-z0-9._-]{1,62}$`)
	userRE  = regexp.MustCompile(`^u[0-9a-hjkmnp-tv-z]{26}$`)
	// saRE: service-account ids are slugs, but 1–40 characters: SPEC's own examples
	// (§6.3.1 "sa:ci", R-AUTH-005 SA "ci") are shorter than the 3-character slug rule.
	saRE = regexp.MustCompile(`^[a-z]([a-z0-9-]{0,38}[a-z0-9])?$`)
)

// Sources of users and bindings as stored.
const (
	sourceLocal  = "local"
	sourceOIDC   = "oidc"
	sourceManual = "manual"
)

// NewLocalUser is the input of CreateLocalUser.
type NewLocalUser struct {
	Login, DisplayName, Email string
	Password                  Secret
	MustChange                bool
	Admin                     bool // also bind admin@* (bootstrap)
}

// CreateLocalUser creates a local user (UserService.CreateUser and `bob-server admin
// create-user`) and returns its id. Order: index slot, user record, then the unique login
// name; a taken login removes the fresh record again. A crash in between leaves an empty
// index slot or an unreachable user record, never a login that points nowhere.
func (id *Identity) CreateLocalUser(ctx context.Context, in NewLocalUser) (string, error) {
	if !loginRE.MatchString(in.Login) {
		return "", api.InvalidArgument("user.login", "a login must match ^[a-z][a-z0-9._-]{1,62}$")
	}
	if err := checkPasswordPolicy("initialPassword", in.Password); err != nil {
		return "", err
	}
	if len(in.DisplayName) > 200 || len(in.Email) > 254 {
		return "", api.InvalidArgument("user", "display name or email too long")
	}
	phc, err := id.hasher.hash(ctx, in.Password)
	if err != nil {
		return "", err
	}
	now := id.now()
	u := store.UserKey("u" + newID(now))
	u.Login, u.DisplayName, u.Email, u.Source = store.Set(in.Login), store.Set(in.DisplayName), store.Set(in.Email), store.Set[string](sourceLocal)
	u.PasswordHash, u.MustChange, u.Created = store.Set(phc), store.Set(in.MustChange), store.Set(now)
	if in.Admin {
		u.Bindings = store.Set([]store.RoleBindingEntry{{ID: bindingID(), Role: adminRole, Project: "*", Source: sourceManual, Created: now}})
	}
	if err := id.createUser(ctx, u, store.UserNameKey(in.Login)); err != nil {
		return "", err
	}
	if in.Admin {
		if err := id.adminAdd(ctx, "user:"+u.ID.Value()); err != nil {
			return "", err
		}
		id.addMember(ctx, adminRole, "user:"+u.ID.Value())
	}
	return u.ID.Value(), nil
}

// createUser writes a new user record with its index slot and its unique name record (uname
// for local users, oidcsub for OIDC users).
func (id *Identity) createUser(ctx context.Context, u *store.UserRecord, name any) error {
	meta, err := store.CAS(ctx, id.control, store.UserIndexMetaKey(), func(cur *store.UserIndexMeta) (*store.UserIndexMeta, error) {
		if cur == nil {
			cur = store.UserIndexMetaKey()
		}
		cur.Count = store.Set(cur.Count.Value() + 1)
		return cur, nil
	})
	if err != nil {
		return storageError(err)
	}
	u.Index = store.Set(meta.Count.Value())
	if err := store.Create(ctx, id.control, u); err != nil {
		return storageError(err)
	}
	switch n := name.(type) {
	case *store.NameRecord:
		n.ID = store.Set(u.ID.Value())
		err = store.Create(ctx, id.control, n)
	case *store.OidcSubRecord:
		n.ID = store.Set(u.ID.Value())
		err = store.Create(ctx, id.control, n)
	}
	if err != nil {
		if derr := store.Delete(ctx, id.control, u); derr != nil {
			id.log.Warn("user record of a failed create not removed", "user", u.ID.Value(), "err", derr.Error())
		}
		if errors.Is(err, store.ErrExists) {
			return api.AlreadyExists(fmt.Sprintf("user %s already exists", u.Login.Value()))
		}
		return storageError(err)
	}
	if err := id.indexUser(ctx, u); err != nil {
		id.log.Warn("user not added to the users index", "user", u.ID.Value(), "err", err.Error())
	}
	return nil
}

func (id *Identity) indexUser(ctx context.Context, u *store.UserRecord) error {
	if u.Index.Value() == 0 {
		return nil
	}
	return store.UserIndex(id.control).Set(ctx, u.Index.Value()-1, store.UserIndexEntry{ID: u.ID.Value(), Login: u.Login.Value(), Source: u.Source.Value(), Disabled: u.Disabled.Value()})
}

func userSource(s string) accessv1.UserSource {
	if s == sourceOIDC {
		return accessv1.UserSource_USER_SOURCE_OIDC
	}
	return accessv1.UserSource_USER_SOURCE_LOCAL
}

func userMsg(u *store.UserRecord) *accessv1.User {
	m := &accessv1.User{
		Id: u.ID.Value(), Login: u.Login.Value(), DisplayName: u.DisplayName.Value(), Email: u.Email.Value(),
		Source: userSource(u.Source.Value()), OidcProviderId: u.Provider.Value(), Disabled: u.Disabled.Value(),
		MustChangePassword: u.MustChange.Value(), Etag: store.ETagOf(u.ETag),
	}
	if !u.Created.Value().IsZero() {
		m.CreatedAt = timestamppb.New(u.Created.Value())
	}
	if !u.LastLogin.Value().IsZero() {
		m.LastLoginAt = timestamppb.New(u.LastLogin.Value())
	}
	return m
}

// userService implements bob.access.v1.UserService.
type userService struct{ id *Identity }

func (s userService) CreateUser(ctx context.Context, req *connect.Request[accessv1.CreateUserRequest]) (*connect.Response[accessv1.CreateUserResponse], error) {
	in := req.Msg.GetUser()
	uid, err := s.id.CreateLocalUser(ctx, NewLocalUser{
		Login: in.GetLogin(), DisplayName: in.GetDisplayName(), Email: in.GetEmail(),
		Password: Secret(req.Msg.GetInitialPassword()), MustChange: in.GetMustChangePassword(),
	})
	if err != nil {
		return nil, err
	}
	u, err := store.Get(ctx, s.id.control, store.UserKey(uid))
	if err != nil {
		return nil, storageError(err)
	}
	return connect.NewResponse(&accessv1.CreateUserResponse{User: userMsg(u)}), nil
}

// getUser reads a user by API id; a malformed or unknown id is NotFound.
func (id *Identity) getUser(ctx context.Context, uid string) (*store.UserRecord, error) {
	if !userRE.MatchString(uid) {
		return nil, api.NotFound("user")
	}
	u, err := store.Get(ctx, id.control, store.UserKey(uid))
	if errors.Is(err, store.ErrNotFound) {
		return nil, api.NotFound("user")
	}
	if err != nil {
		return nil, storageError(err)
	}
	return u, nil
}

func (s userService) GetUser(ctx context.Context, req *connect.Request[accessv1.GetUserRequest]) (*connect.Response[accessv1.GetUserResponse], error) {
	u, err := s.id.getUser(ctx, req.Msg.GetId())
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&accessv1.GetUserResponse{User: userMsg(u)}), nil
}

func (s userService) ListUsers(ctx context.Context, req *connect.Request[accessv1.ListUsersRequest]) (*connect.Response[accessv1.ListUsersResponse], error) {
	m := req.Msg
	size, err := api.PageSize(m.GetPageSize())
	if err != nil {
		return nil, err
	}
	pos, err := api.DecodePageToken("users", m.GetPageToken())
	if err != nil {
		return nil, err
	}
	meta, err := store.Get(ctx, s.id.control, store.UserIndexMetaKey())
	if errors.Is(err, store.ErrNotFound) {
		return connect.NewResponse(&accessv1.ListUsersResponse{}), nil
	} else if err != nil {
		return nil, storageError(err)
	}
	idx := store.UserIndex(s.id.control)
	query := strings.ToLower(m.GetQuery())
	out := &accessv1.ListUsersResponse{}
	var page []store.UserIndexEntry
	pageNo := ^uint64(0)
	for ; pos < meta.Count.Value(); pos++ {
		if len(out.Users) == size {
			out.NextPageToken = api.EncodePageToken("users", pos)
			break
		}
		if p := pos / store.UserIndexP; p != pageNo {
			pageNo = p
			page, err = idx.Page(ctx, p)
			if errors.Is(err, store.ErrNotFound) {
				page = nil
			} else if err != nil {
				return nil, storageError(err)
			}
		}
		off := int(pos % store.UserIndexP)
		if off >= len(page) {
			continue
		}
		e := page[off]
		switch {
		case e.ID == "":
			continue
		case e.Disabled && !m.GetIncludeDisabled():
			continue
		case m.GetSource() != accessv1.UserSource_USER_SOURCE_UNSPECIFIED && userSource(e.Source) != m.GetSource():
			continue
		case query != "" && !strings.HasPrefix(strings.ToLower(e.Login), query) && !strings.HasPrefix(e.ID, query):
			continue
		}
		u, err := store.Get(ctx, s.id.control, store.UserKey(e.ID))
		if errors.Is(err, store.ErrNotFound) {
			continue
		} else if err != nil {
			return nil, storageError(err)
		}
		out.Users = append(out.Users, userMsg(u))
	}
	return connect.NewResponse(out), nil
}

func (s userService) UpdateUser(ctx context.Context, req *connect.Request[accessv1.UpdateUserRequest]) (*connect.Response[accessv1.UpdateUserResponse], error) {
	in := req.Msg.GetUser()
	if err := api.CheckMask(req.Msg.GetUpdateMask(), in, "display_name", "email", "disabled", "must_change_password"); err != nil {
		return nil, err
	}
	cur, err := s.id.getUser(ctx, in.GetId())
	if err != nil {
		return nil, err
	}
	if err := checkETag("user.etag", in.GetEtag(), store.ETagOf(cur.ETag)); err != nil {
		return nil, err
	}
	paths := map[string]bool{}
	for _, p := range req.Msg.GetUpdateMask().GetPaths() {
		paths[strings.ToLower(strings.ReplaceAll(p, "_", ""))] = true
	}
	if paths["email"] && cur.Source.Value() != sourceLocal {
		return nil, api.InvalidArgument("user.email", "the email of an OIDC user comes from its provider")
	}
	if len(in.GetDisplayName()) > 200 || len(in.GetEmail()) > 254 {
		return nil, api.InvalidArgument("user", "display name or email too long")
	}
	principal := "user:" + cur.ID.Value()
	wasAdmin := isAdmin(cur.Bindings.Value(), cur.Disabled.Value())
	willAdmin := isAdmin(cur.Bindings.Value(), paths["disabled"] && in.GetDisabled() || !paths["disabled"] && cur.Disabled.Value())
	if wasAdmin && !willAdmin {
		if err := s.id.adminRemove(ctx, principal); err != nil {
			return nil, err
		}
	}
	next, err := store.CAS(ctx, s.id.control, store.UserKey(cur.ID.Value()), func(u *store.UserRecord) (*store.UserRecord, error) {
		if u == nil {
			return nil, api.NotFound("user")
		}
		if in.GetEtag() != "" && store.ETagOf(u.ETag) != in.GetEtag() {
			return nil, api.Aborted("the resource was changed by someone else (etag mismatch); reload and retry")
		}
		if paths["displayname"] {
			u.DisplayName = store.Set(in.GetDisplayName())
		}
		if paths["email"] {
			u.Email = store.Set(in.GetEmail())
		}
		if paths["disabled"] {
			u.Disabled = store.Set(in.GetDisabled())
		}
		if paths["mustchangepassword"] {
			u.MustChange = store.Set(in.GetMustChangePassword())
		}
		return u, nil
	})
	if err != nil {
		if wasAdmin && !willAdmin {
			_ = s.id.adminAdd(ctx, principal)
		}
		return nil, storageError(err)
	}
	s.id.users.put(next.ID.Value(), next)
	if !wasAdmin && willAdmin {
		if err := s.id.adminAdd(ctx, principal); err != nil {
			return nil, err
		}
	}
	if next.Disabled.Value() != cur.Disabled.Value() {
		if err := s.id.indexUser(ctx, next); err != nil {
			s.id.log.Warn("users index entry not updated", "user", next.ID.Value(), "err", err.Error())
		}
		if next.Disabled.Value() {
			if _, err := s.id.revokeSessions(ctx, next.ID.Value(), func(store.SessionRef) bool { return false }); err != nil {
				return nil, err
			}
		}
	}
	return connect.NewResponse(&accessv1.UpdateUserResponse{User: userMsg(next)}), nil
}

// DeleteUser revokes the user's sessions and tokens and deletes its bindings with it.
func (s userService) DeleteUser(ctx context.Context, req *connect.Request[accessv1.DeleteUserRequest]) (*connect.Response[accessv1.DeleteUserResponse], error) {
	cur, err := s.id.getUser(ctx, req.Msg.GetId())
	if err != nil {
		return nil, err
	}
	if req.Msg.GetEtag() != "" && req.Msg.GetEtag() != store.ETagOf(cur.ETag) {
		return nil, api.Aborted("the resource was changed by someone else (etag mismatch); reload and retry")
	}
	if err := s.id.deleteUser(ctx, cur); err != nil {
		return nil, err
	}
	return connect.NewResponse(&accessv1.DeleteUserResponse{}), nil
}

func (id *Identity) deleteUser(ctx context.Context, cur *store.UserRecord) error {
	principal := "user:" + cur.ID.Value()
	admin := isAdmin(cur.Bindings.Value(), cur.Disabled.Value())
	if admin {
		if err := id.adminRemove(ctx, principal); err != nil {
			return err
		}
	}
	if _, err := id.revokeSessions(ctx, cur.ID.Value(), func(store.SessionRef) bool { return false }); err != nil {
		if admin {
			_ = id.adminAdd(ctx, principal)
		}
		return err
	}
	if err := store.Delete(ctx, id.control, cur); err != nil {
		if admin {
			_ = id.adminAdd(ctx, principal)
		}
		if errors.Is(err, store.ErrConflict) {
			return api.Aborted("the resource was changed by someone else (etag mismatch); reload and retry")
		}
		return storageError(err)
	}
	id.users.drop(cur.ID.Value())
	for _, b := range cur.Bindings.Value() {
		id.removeMember(ctx, b.Role, principal, nil)
	}
	switch cur.Source.Value() {
	case sourceLocal:
		id.deleteName(ctx, store.UserNameKey(cur.Login.Value()), cur.ID.Value())
	case sourceOIDC:
		k := store.OidcSubKey(cur.Issuer.Value(), cur.Subject.Value())
		if n, err := store.Get(ctx, id.control, k); err == nil && n.ID.Value() == cur.ID.Value() {
			_ = store.Delete(ctx, id.control, n)
		}
	}
	if cur.Index.Value() > 0 {
		if err := store.UserIndex(id.control).Set(ctx, cur.Index.Value()-1, store.UserIndexEntry{}); err != nil {
			id.log.Warn("users index entry not cleared", "user", cur.ID.Value(), "err", err.Error())
		}
	}
	return nil
}

func (id *Identity) deleteName(ctx context.Context, key *store.NameRecord, uid string) {
	if n, err := store.Get(ctx, id.control, key); err == nil && n.ID.Value() == uid {
		if err := store.Delete(ctx, id.control, n); err != nil {
			id.log.Warn("login name not released", "user", uid, "err", err.Error())
		}
	}
}

func (s userService) SetUserPassword(ctx context.Context, req *connect.Request[accessv1.SetUserPasswordRequest]) (*connect.Response[accessv1.SetUserPasswordResponse], error) {
	pw := Secret(req.Msg.GetNewPassword())
	if err := checkPasswordPolicy("newPassword", pw); err != nil {
		return nil, err
	}
	cur, err := s.id.getUser(ctx, req.Msg.GetId())
	if err != nil {
		return nil, err
	}
	if cur.Source.Value() != sourceLocal {
		return nil, api.FailedPrecondition("an OIDC user has no password")
	}
	if err := s.id.setPassword(ctx, cur.ID.Value(), pw, req.Msg.GetMustChange()); err != nil {
		return nil, err
	}
	if _, err := s.id.revokeSessions(ctx, cur.ID.Value(), func(store.SessionRef) bool { return false }); err != nil {
		return nil, err
	}
	return connect.NewResponse(&accessv1.SetUserPasswordResponse{}), nil
}

func (id *Identity) setPassword(ctx context.Context, uid string, pw Secret, mustChange bool) error {
	phc, err := id.hasher.hash(ctx, pw)
	if err != nil {
		return err
	}
	next, err := store.CAS(ctx, id.control, store.UserKey(uid), func(u *store.UserRecord) (*store.UserRecord, error) {
		if u == nil {
			return nil, api.NotFound("user")
		}
		u.PasswordHash, u.MustChange = store.Set(phc), store.Set(mustChange)
		return u, nil
	})
	if err != nil {
		return storageError(err)
	}
	id.users.put(uid, next)
	return nil
}

// lookupLogin returns the local user with login, or nil.
func (id *Identity) lookupLogin(ctx context.Context, login string) (*store.UserRecord, error) {
	if !loginRE.MatchString(login) {
		return nil, nil
	}
	n, err := store.Get(ctx, id.control, store.UserNameKey(login))
	if errors.Is(err, store.ErrNotFound) {
		return nil, nil
	} else if err != nil {
		return nil, storageError(err)
	}
	u, err := store.Get(ctx, id.control, store.UserKey(n.ID.Value()))
	if errors.Is(err, store.ErrNotFound) {
		return nil, nil
	} else if err != nil {
		return nil, storageError(err)
	}
	if u.Source.Value() != sourceLocal || u.Login.Value() != login {
		return nil, nil
	}
	return u, nil
}

func hasBinding(ro []store.RoleBindingEntry, role, project string) bool {
	return slices.ContainsFunc(ro, func(b store.RoleBindingEntry) bool { return b.Role == role && b.Project == project })
}

// checkETag is api.CheckETag, except that an update without an etag is unconditional (the
// read-modify-write still runs as one CAS); a given etag must match.
func checkETag(field, requested, stored string) error {
	if requested == "" {
		return nil
	}
	return api.CheckETag(field, requested, stored)
}
