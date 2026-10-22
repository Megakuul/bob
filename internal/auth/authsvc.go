package auth

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/megakuul/bob/internal/api"
	authv1 "github.com/megakuul/bob/internal/gen/bob/auth/v1"
	commonv1 "github.com/megakuul/bob/internal/gen/bob/common/v1"
	"github.com/megakuul/bob/internal/store"
)

// authService implements bob.auth.v1.AuthService.
type authService struct{ id *Identity }

// SetNixKeys sets the source of the cache's public keys (promote.Keys.PublicKeys: ACTIVE and
// VERIFY_ONLY keys) that WhoAmI and GetServerInfo announce. It is called while the server is
// wired, before it serves.
func (id *Identity) SetNixKeys(keys func(context.Context) ([]string, error)) { id.nixKeys = keys }

// nix is the server's binary cache for clients (SPEC §6.5 NixInfo): the substituter (empty
// without server.base_url: the client then uses <server>/nix) and the public keys. Without
// keys (no cache, or the key index unreadable) it is nil: a client must not trust a cache
// whose keys it does not know.
func (id *Identity) nix(ctx context.Context) *authv1.NixInfo {
	if id.nixKeys == nil {
		return nil
	}
	keys, err := id.nixKeys(ctx)
	if err != nil {
		id.log.Warn("cache public keys not available", "err", err.Error())
		return nil
	}
	if len(keys) == 0 {
		return nil
	}
	out := &authv1.NixInfo{PublicKeys: keys}
	if id.info.BaseURL != "" {
		out.Substituter = strings.TrimSuffix(id.info.BaseURL, "/") + "/nix"
	}
	return out
}

func (s authService) GetServerInfo(ctx context.Context, _ *connect.Request[authv1.GetServerInfoRequest]) (*connect.Response[authv1.GetServerInfoResponse], error) {
	out := &authv1.GetServerInfoResponse{
		ServerName: s.id.info.Name, BaseUrl: s.id.info.BaseURL, Version: s.id.info.Version, Nix: s.id.nix(ctx),
		LoginOptions: []*authv1.LoginOption{{DisplayName: "Password"}},
	}
	provs, err := s.id.listProviders(ctx)
	if err != nil {
		s.id.log.Warn("login options without OIDC providers", "err", err.Error())
	}
	for _, p := range provs {
		name := p.DisplayName.Value()
		if name == "" {
			name = p.ID.Value()
		}
		out.LoginOptions = append(out.LoginOptions, &authv1.LoginOption{
			ProviderId: p.ID.Value(), DisplayName: name, StartUrl: "/auth/oidc/start?provider=" + url.QueryEscape(p.ID.Value()),
		})
	}
	return connect.NewResponse(out), nil
}

func (s authService) WhoAmI(ctx context.Context, _ *connect.Request[authv1.WhoAmIRequest]) (*connect.Response[authv1.WhoAmIResponse], error) {
	p := api.PrincipalFrom(ctx)
	out := api.WhoAmI(p, s.id.nix(ctx))
	if p != nil {
		// Projects come from the effective permissions ("*" if any permission is on every
		// project), not from the bindings' projects.
		eff, err := s.id.rbac.Effective(ctx, p.Bindings)
		if err != nil {
			return nil, err
		}
		out.Projects = nil
		for _, e := range eff {
			if e.Project == "*" {
				out.Projects = []string{"*"}
				break
			}
			if !slices.Contains(out.Projects, e.Project) {
				out.Projects = append(out.Projects, e.Project)
			}
		}
		slices.Sort(out.Projects)
	}
	return connect.NewResponse(out), nil
}

var errBadLogin = api.Unauthenticated("invalid login or password")

// clientIP is the address of the caller (the connection's peer; trusted proxies are not
// consulted yet).
func clientIP(addr string) string {
	if h, _, err := net.SplitHostPort(addr); err == nil {
		return h
	}
	return addr
}

// LoginPassword checks a local password and starts a browser session (cookie bob_session).
// Every failure answers "invalid login or password"; throttled callers get ResourceExhausted.
func (s authService) LoginPassword(ctx context.Context, req *connect.Request[authv1.LoginPasswordRequest]) (*connect.Response[authv1.LoginPasswordResponse], error) {
	login, pw := strings.TrimSpace(req.Msg.GetLogin()), Secret(req.Msg.GetPassword())
	if login == "" || len(pw.reveal()) == 0 {
		return nil, api.InvalidArgument("login", "login and password are required")
	}
	ip := clientIP(req.Peer().Addr)
	if len(login) > 256 {
		login = login[:256]
	}
	if err := s.id.throttle.check(login, ip, s.id.now()); err != nil {
		return nil, err
	}
	u, err := s.id.lookupLogin(ctx, login)
	if err != nil {
		return nil, err
	}
	phc := ""
	if u != nil {
		phc = u.PasswordHash.Value()
	}
	ok, err := s.id.hasher.verify(ctx, phc, pw)
	if err != nil && !errors.Is(err, errBadHash) {
		return nil, err
	}
	if !ok || u == nil || u.Disabled.Value() || len(pw.reveal()) > maxPassword {
		s.id.throttle.fail(login, ip, s.id.now())
		return nil, errBadLogin
	}
	iss, err := s.id.createSession(ctx, u.ID.Value(), "browser", req.Header().Get("User-Agent"))
	if err != nil {
		return nil, err
	}
	res := connect.NewResponse(&authv1.LoginPasswordResponse{
		Principal:          &commonv1.Principal{Id: "user:" + u.ID.Value(), DisplayName: userPrincipal(u).DisplayName},
		MustChangePassword: u.MustChange.Value(),
	})
	res.Header().Add("Set-Cookie", sessionCookie(iss.session, browserMax))
	return res, nil
}

// callerSession finds the session behind the request's credential: the bob_session cookie,
// or the session of a bob_at_ token. "" when there is none (e.g. a service account token).
func (id *Identity) callerSession(ctx context.Context, h http.Header) string {
	if scheme, tok, ok := strings.Cut(h.Get("Authorization"), " "); ok && strings.EqualFold(scheme, "Bearer") {
		key := store.TokenKey(tok)
		rec, err := load(ctx, id.control, &id.tokens, key.Hash.Value(), key)
		if err != nil {
			return ""
		}
		return rec.Session.Value()
	}
	if ck, err := (&http.Request{Header: h}).Cookie(CookieName); err == nil && ck.Value != "" {
		return store.SessionKey(ck.Value).Hash.Value()
	}
	return ""
}

func userOf(ctx context.Context) (string, error) {
	p := api.PrincipalFrom(ctx)
	if p == nil {
		return "", api.Unauthenticated("authentication required")
	}
	uid, ok := strings.CutPrefix(p.ID, "user:")
	if !ok {
		return "", api.FailedPrecondition("only users have sessions and passwords")
	}
	return uid, nil
}

// Logout ends the calling session (cookie, or the CLI session of the access token) and
// clears the cookie.
func (s authService) Logout(ctx context.Context, req *connect.Request[authv1.LogoutRequest]) (*connect.Response[authv1.LogoutResponse], error) {
	res := connect.NewResponse(&authv1.LogoutResponse{})
	res.Header().Add("Set-Cookie", sessionCookie("", 0))
	uid, err := userOf(ctx)
	if err != nil {
		return res, nil //nolint:nilerr // a service account has no session to end
	}
	if h := s.id.callerSession(ctx, req.Header()); h != "" {
		if _, err := s.id.revokeSessions(ctx, uid, func(r store.SessionRef) bool { return r.Hash != h }); err != nil {
			return nil, err
		}
		s.id.deleteSession(ctx, h)
	}
	return res, nil
}

// ChangePassword checks the old password, sets the new one and revokes every other session.
func (s authService) ChangePassword(ctx context.Context, req *connect.Request[authv1.ChangePasswordRequest]) (*connect.Response[authv1.ChangePasswordResponse], error) {
	uid, err := userOf(ctx)
	if err != nil {
		return nil, err
	}
	newPW := Secret(req.Msg.GetNewPassword())
	if err := checkPasswordPolicy("newPassword", newPW); err != nil {
		return nil, err
	}
	u, err := s.id.getUser(ctx, uid)
	if err != nil {
		return nil, err
	}
	if u.Source.Value() != sourceLocal {
		return nil, api.FailedPrecondition("an OIDC user has no password")
	}
	ip := clientIP(req.Peer().Addr)
	if err := s.id.throttle.check(u.Login.Value(), ip, s.id.now()); err != nil {
		return nil, err
	}
	ok, err := s.id.hasher.verify(ctx, u.PasswordHash.Value(), Secret(req.Msg.GetOldPassword()))
	if err != nil && !errors.Is(err, errBadHash) {
		return nil, err
	}
	if !ok {
		s.id.throttle.fail(u.Login.Value(), ip, s.id.now())
		return nil, api.InvalidArgument("oldPassword", "the current password is wrong")
	}
	if err := s.id.setPassword(ctx, uid, newPW, false); err != nil {
		return nil, err
	}
	keep := s.id.callerSession(ctx, req.Header())
	if _, err := s.id.revokeSessions(ctx, uid, func(r store.SessionRef) bool { return r.Hash == keep }); err != nil {
		return nil, err
	}
	return connect.NewResponse(&authv1.ChangePasswordResponse{}), nil
}

func (s authService) ListMySessions(ctx context.Context, req *connect.Request[authv1.ListMySessionsRequest]) (*connect.Response[authv1.ListMySessionsResponse], error) {
	uid, err := userOf(ctx)
	if err != nil {
		return connect.NewResponse(&authv1.ListMySessionsResponse{}), nil //nolint:nilerr // service accounts have no sessions
	}
	u, err := s.id.getUser(ctx, uid)
	if err != nil {
		return nil, err
	}
	ss, err := s.id.sessionsOf(ctx, u)
	if err != nil {
		return nil, err
	}
	cur := s.id.callerSession(ctx, req.Header())
	out := &authv1.ListMySessionsResponse{}
	for _, x := range ss {
		out.Sessions = append(out.Sessions, &authv1.Session{
			Id: x.ID.Value(), Kind: x.Kind.Value(), UserAgent: x.UserAgent.Value(),
			CreatedAt:  timestamppb.New(x.Created.Value()),
			LastUsedAt: timestamppb.New(x.LastUsed.Value().UTC().Truncate(24 * time.Hour)),
			Current:    x.Hash.Value() == cur,
		})
	}
	return connect.NewResponse(out), nil
}

func (s authService) RevokeMySession(ctx context.Context, req *connect.Request[authv1.RevokeMySessionRequest]) (*connect.Response[authv1.RevokeMySessionResponse], error) {
	uid, err := userOf(ctx)
	if err != nil {
		return nil, err
	}
	n, err := s.id.revokeSessions(ctx, uid, func(r store.SessionRef) bool { return r.ID != req.Msg.GetSessionId() })
	if err != nil {
		return nil, err
	}
	if n == 0 {
		return nil, api.NotFound("session")
	}
	return connect.NewResponse(&authv1.RevokeMySessionResponse{}), nil
}
