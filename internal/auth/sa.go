package auth

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

// Service-account limits (SPEC §6.3.1, §6.5.5).
const (
	maxSAs          = 1000
	maxSATokens     = 20
	defaultSATokenD = 90
	maxSATokenD     = 365
)

func saMsg(sa *store.ServiceAccountRecord) *accessv1.ServiceAccount {
	m := &accessv1.ServiceAccount{Id: sa.ID.Value(), Description: sa.Description.Value(), Disabled: sa.Disabled.Value(), Etag: store.ETagOf(sa.ETag)}
	if !sa.Created.Value().IsZero() {
		m.CreatedAt = timestamppb.New(sa.Created.Value())
	}
	return m
}

func scopeMsg(s string) accessv1.TokenScope {
	if s == "api" {
		return accessv1.TokenScope_TOKEN_SCOPE_API
	}
	return accessv1.TokenScope_TOKEN_SCOPE_DOWNLOAD
}

func saTokenMsg(t store.SATokenEntry) *accessv1.ServiceAccountToken {
	m := &accessv1.ServiceAccountToken{Id: t.ID, Name: t.Name, Scope: scopeMsg(t.Scope)}
	if !t.Expires.IsZero() {
		m.ExpiresAt = timestamppb.New(t.Expires)
	}
	if !t.LastUsed.IsZero() {
		m.LastUsedOn = timestamppb.New(t.LastUsed)
	}
	if !t.Created.IsZero() {
		m.CreatedAt = timestamppb.New(t.Created)
	}
	return m
}

// saService implements bob.access.v1.ServiceAccountService.
type saService struct{ id *Identity }

func (s saService) get(ctx context.Context, saID string) (*store.ServiceAccountRecord, error) {
	if !saRE.MatchString(saID) {
		return nil, api.NotFound("service account")
	}
	sa, err := store.Get(ctx, s.id.control, store.ServiceAccountKey(saID))
	if errors.Is(err, store.ErrNotFound) {
		return nil, api.NotFound("service account")
	} else if err != nil {
		return nil, storageError(err)
	}
	return sa, nil
}

func (s saService) CreateServiceAccount(ctx context.Context, req *connect.Request[accessv1.CreateServiceAccountRequest]) (*connect.Response[accessv1.CreateServiceAccountResponse], error) {
	in := req.Msg.GetServiceAccount()
	if !saRE.MatchString(in.GetId()) {
		return nil, api.InvalidArgument("serviceAccount.id", "a service account id must match ^[a-z]([a-z0-9-]{0,38}[a-z0-9])?$")
	}
	if len(in.GetDescription()) > 500 {
		return nil, api.InvalidArgument("serviceAccount.description", "description too long")
	}
	sa := store.ServiceAccountKey(in.GetId())
	sa.Description, sa.Disabled, sa.Created = store.Set(in.GetDescription()), store.Set(in.GetDisabled()), store.Set(s.id.now())
	if _, err := store.CAS(ctx, s.id.control, store.SAIndexKey(), func(cur *store.SAIndex) (*store.SAIndex, error) {
		var vals []string
		if cur == nil {
			cur = store.SAIndexKey()
		} else {
			vals = cur.IDs.Value()
		}
		if slices.Contains(vals, sa.ID.Value()) {
			return nil, store.ErrNoChange
		}
		if len(vals) >= maxSAs {
			return nil, api.FailedPrecondition(fmt.Sprintf("at most %d service accounts", maxSAs))
		}
		vals = append(vals, sa.ID.Value())
		slices.Sort(vals)
		cur.IDs = store.Set(vals)
		return cur, nil
	}); err != nil {
		return nil, storageError(err)
	}
	if err := store.Create(ctx, s.id.control, sa); err != nil {
		if errors.Is(err, store.ErrExists) {
			return nil, api.AlreadyExists(fmt.Sprintf("service account %s already exists", sa.ID.Value()))
		}
		return nil, storageError(err)
	}
	created, err := store.Get(ctx, s.id.control, &store.ServiceAccountRecord{ID: sa.ID})
	if err != nil {
		return nil, storageError(err)
	}
	return connect.NewResponse(&accessv1.CreateServiceAccountResponse{ServiceAccount: saMsg(created)}), nil
}

func (s saService) GetServiceAccount(ctx context.Context, req *connect.Request[accessv1.GetServiceAccountRequest]) (*connect.Response[accessv1.GetServiceAccountResponse], error) {
	sa, err := s.get(ctx, req.Msg.GetId())
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&accessv1.GetServiceAccountResponse{ServiceAccount: saMsg(sa)}), nil
}

func (s saService) ListServiceAccounts(ctx context.Context, req *connect.Request[accessv1.ListServiceAccountsRequest]) (*connect.Response[accessv1.ListServiceAccountsResponse], error) {
	size, err := api.PageSize(req.Msg.GetPageSize())
	if err != nil {
		return nil, err
	}
	pos, err := api.DecodePageToken("serviceaccounts", req.Msg.GetPageToken())
	if err != nil {
		return nil, err
	}
	idx, err := store.Get(ctx, s.id.control, store.SAIndexKey())
	if errors.Is(err, store.ErrNotFound) {
		return connect.NewResponse(&accessv1.ListServiceAccountsResponse{}), nil
	} else if err != nil {
		return nil, storageError(err)
	}
	out := &accessv1.ListServiceAccountsResponse{}
	for ; pos < uint64(len(idx.IDs.Value())); pos++ {
		if len(out.ServiceAccounts) == size {
			out.NextPageToken = api.EncodePageToken("serviceaccounts", pos)
			break
		}
		if q := req.Msg.GetQuery(); q != "" && !strings.HasPrefix(idx.IDs.Value()[pos], q) {
			continue
		}
		sa, err := store.Get(ctx, s.id.control, store.ServiceAccountKey(idx.IDs.Value()[pos]))
		if errors.Is(err, store.ErrNotFound) {
			continue
		} else if err != nil {
			return nil, storageError(err)
		}
		out.ServiceAccounts = append(out.ServiceAccounts, saMsg(sa))
	}
	return connect.NewResponse(out), nil
}

func (s saService) UpdateServiceAccount(ctx context.Context, req *connect.Request[accessv1.UpdateServiceAccountRequest]) (*connect.Response[accessv1.UpdateServiceAccountResponse], error) {
	in := req.Msg.GetServiceAccount()
	if err := api.CheckMask(req.Msg.GetUpdateMask(), in, "description", "disabled"); err != nil {
		return nil, err
	}
	cur, err := s.get(ctx, in.GetId())
	if err != nil {
		return nil, err
	}
	if err := checkETag("serviceAccount.etag", in.GetEtag(), store.ETagOf(cur.ETag)); err != nil {
		return nil, err
	}
	setDisabled, setDesc := false, false
	for _, p := range req.Msg.GetUpdateMask().GetPaths() {
		setDisabled = setDisabled || p == "disabled"
		setDesc = setDesc || p == "description"
	}
	if len(in.GetDescription()) > 500 {
		return nil, api.InvalidArgument("serviceAccount.description", "description too long")
	}
	principal := "sa:" + cur.ID.Value()
	disabled := cur.Disabled.Value()
	if setDisabled {
		disabled = in.GetDisabled()
	}
	wasAdmin, willAdmin := isAdmin(cur.Bindings.Value(), cur.Disabled.Value()), isAdmin(cur.Bindings.Value(), disabled)
	if wasAdmin && !willAdmin {
		if err := s.id.adminRemove(ctx, principal); err != nil {
			return nil, err
		}
	}
	next, err := store.CAS(ctx, s.id.control, store.ServiceAccountKey(cur.ID.Value()), func(sa *store.ServiceAccountRecord) (*store.ServiceAccountRecord, error) {
		if sa == nil {
			return nil, api.NotFound("service account")
		}
		if in.GetEtag() != "" && store.ETagOf(sa.ETag) != in.GetEtag() {
			return nil, api.Aborted("the resource was changed by someone else (etag mismatch); reload and retry")
		}
		if setDesc {
			sa.Description = store.Set(in.GetDescription())
		}
		sa.Disabled = store.Set(disabled)
		return sa, nil
	})
	if err != nil {
		if wasAdmin && !willAdmin {
			_ = s.id.adminAdd(ctx, principal)
		}
		return nil, storageError(err)
	}
	s.id.sas.put(next.ID.Value(), next)
	if !wasAdmin && willAdmin {
		if err := s.id.adminAdd(ctx, principal); err != nil {
			return nil, err
		}
	}
	return connect.NewResponse(&accessv1.UpdateServiceAccountResponse{ServiceAccount: saMsg(next)}), nil
}

func (s saService) DeleteServiceAccount(ctx context.Context, req *connect.Request[accessv1.DeleteServiceAccountRequest]) (*connect.Response[accessv1.DeleteServiceAccountResponse], error) {
	cur, err := s.get(ctx, req.Msg.GetId())
	if err != nil {
		return nil, err
	}
	if e := req.Msg.GetEtag(); e != "" && e != store.ETagOf(cur.ETag) {
		return nil, api.Aborted("the resource was changed by someone else (etag mismatch); reload and retry")
	}
	principal := "sa:" + cur.ID.Value()
	admin := isAdmin(cur.Bindings.Value(), cur.Disabled.Value())
	if admin {
		if err := s.id.adminRemove(ctx, principal); err != nil {
			return nil, err
		}
	}
	if err := store.Delete(ctx, s.id.control, cur); err != nil {
		if admin {
			_ = s.id.adminAdd(ctx, principal)
		}
		if errors.Is(err, store.ErrConflict) {
			return nil, api.Aborted("the resource was changed by someone else (etag mismatch); reload and retry")
		}
		return nil, storageError(err)
	}
	s.id.sas.drop(cur.ID.Value())
	for _, t := range cur.Tokens.Value() {
		s.id.deleteToken(ctx, t.Hash)
	}
	for _, b := range cur.Bindings.Value() {
		s.id.removeMember(ctx, b.Role, principal, nil)
	}
	if _, err := store.CAS(ctx, s.id.control, store.SAIndexKey(), func(idx *store.SAIndex) (*store.SAIndex, error) {
		if idx == nil || !slices.Contains(idx.IDs.Value(), cur.ID.Value()) {
			return nil, store.ErrNoChange
		}
		idx.IDs = store.Set(slices.DeleteFunc(idx.IDs.Value(), func(x string) bool { return x == cur.ID.Value() }))
		return idx, nil
	}); err != nil {
		s.id.log.Warn("service account index not updated", "sa", cur.ID.Value(), "err", err.Error())
	}
	return connect.NewResponse(&accessv1.DeleteServiceAccountResponse{}), nil
}

// CreateServiceAccountToken returns the secret once; only its hash is stored.
func (s saService) CreateServiceAccountToken(ctx context.Context, req *connect.Request[accessv1.CreateServiceAccountTokenRequest]) (*connect.Response[accessv1.CreateServiceAccountTokenResponse], error) {
	m := req.Msg
	if _, err := s.get(ctx, m.GetServiceAccountId()); err != nil {
		return nil, err
	}
	name := m.GetName()
	if name == "" || len(name) > 100 {
		return nil, api.InvalidArgument("name", "a token name of 1 to 100 bytes is required")
	}
	scope := "download"
	switch m.GetScope() {
	case accessv1.TokenScope_TOKEN_SCOPE_API:
		scope = "api"
	case accessv1.TokenScope_TOKEN_SCOPE_DOWNLOAD, accessv1.TokenScope_TOKEN_SCOPE_UNSPECIFIED:
	default:
		return nil, api.InvalidArgument("scope", "unknown token scope")
	}
	now := s.id.now()
	var expires time.Time
	switch {
	case m.GetNoExpiry():
		if m.GetTtlDays() != 0 {
			return nil, api.InvalidArgument("ttlDays", "ttl_days and no_expiry exclude each other")
		}
	case m.GetTtlDays() < 0 || m.GetTtlDays() > maxSATokenD:
		return nil, api.InvalidArgument("ttlDays", fmt.Sprintf("ttl_days must be between 1 and %d", maxSATokenD))
	default:
		days := int(m.GetTtlDays())
		if days == 0 {
			days = defaultSATokenD
		}
		expires = now.Add(time.Duration(days) * 24 * time.Hour)
	}
	secret := newToken(PrefixSA)
	rec := store.TokenKey(secret.reveal())
	entry := store.SATokenEntry{ID: "t" + randomString(crockford, 12), Name: name, Scope: scope, Hash: rec.Hash.Value(), Expires: expires, Created: now}
	rec.Owner, rec.Kind, rec.Scope, rec.TokenID = store.Set("sa:"+m.GetServiceAccountId()), store.Set[string]("sa"), store.Set(scope), store.Set(entry.ID)
	rec.Expires, rec.Created = store.Set(expires), store.Set(now)
	// The SA record lists the token first: a token record without its list entry would be a
	// credential nobody can see or revoke.
	next, err := store.CAS(ctx, s.id.control, store.ServiceAccountKey(m.GetServiceAccountId()), func(sa *store.ServiceAccountRecord) (*store.ServiceAccountRecord, error) {
		if sa == nil {
			return nil, api.NotFound("service account")
		}
		live := sa.Tokens.Value()[:0:0]
		for _, t := range sa.Tokens.Value() {
			if t.Expires.IsZero() || now.Before(t.Expires) {
				live = append(live, t)
			}
		}
		if len(live) >= maxSATokens {
			return nil, api.FailedPrecondition(fmt.Sprintf("a service account has at most %d tokens", maxSATokens))
		}
		sa.Tokens = store.Set(append(sa.Tokens.Value(), entry))
		return sa, nil
	})
	if err != nil {
		return nil, storageError(err)
	}
	s.id.sas.put(next.ID.Value(), next)
	if err := store.Create(ctx, s.id.control, rec); err != nil {
		return nil, storageError(err)
	}
	s.id.tokens.drop(rec.Hash.Value())
	return connect.NewResponse(&accessv1.CreateServiceAccountTokenResponse{Token: saTokenMsg(entry), Secret: secret.reveal()}), nil
}

func (s saService) ListServiceAccountTokens(ctx context.Context, req *connect.Request[accessv1.ListServiceAccountTokensRequest]) (*connect.Response[accessv1.ListServiceAccountTokensResponse], error) {
	sa, err := s.get(ctx, req.Msg.GetServiceAccountId())
	if err != nil {
		return nil, err
	}
	out := &accessv1.ListServiceAccountTokensResponse{}
	for _, t := range sa.Tokens.Value() {
		out.Tokens = append(out.Tokens, saTokenMsg(t))
	}
	return connect.NewResponse(out), nil
}

// RevokeServiceAccountToken deletes the token; it is refused everywhere within 10 s (at once
// on this orchestrator).
func (s saService) RevokeServiceAccountToken(ctx context.Context, req *connect.Request[accessv1.RevokeServiceAccountTokenRequest]) (*connect.Response[accessv1.RevokeServiceAccountTokenResponse], error) {
	m := req.Msg
	if _, err := s.get(ctx, m.GetServiceAccountId()); err != nil {
		return nil, err
	}
	var hash string
	next, err := store.CAS(ctx, s.id.control, store.ServiceAccountKey(m.GetServiceAccountId()), func(sa *store.ServiceAccountRecord) (*store.ServiceAccountRecord, error) {
		if sa == nil {
			return nil, api.NotFound("service account")
		}
		i := slices.IndexFunc(sa.Tokens.Value(), func(t store.SATokenEntry) bool { return t.ID == m.GetTokenId() })
		if i < 0 {
			return nil, api.NotFound("token")
		}
		hash = sa.Tokens.Value()[i].Hash
		sa.Tokens = store.Set(slices.Delete(sa.Tokens.Value(), i, i+1))
		return sa, nil
	})
	if err != nil {
		return nil, storageError(err)
	}
	s.id.sas.put(next.ID.Value(), next)
	s.id.deleteToken(ctx, hash)
	return connect.NewResponse(&accessv1.RevokeServiceAccountTokenResponse{}), nil
}
