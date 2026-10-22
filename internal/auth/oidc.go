package auth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"connectrpc.com/connect"
	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"

	"github.com/megakuul/bob/internal/api"
	accessv1 "github.com/megakuul/bob/internal/gen/bob/access/v1"
	"github.com/megakuul/bob/internal/rbac"
	"github.com/megakuul/bob/internal/store"
)

// OIDC relying party (SPEC §6.3.2): code flow with PKCE, state and nonce kept in a 10-minute
// record keyed by SHA-256(state); users keyed by SHA-256(issuer ‖ 0 ‖ sub); claim → role
// mappings re-evaluated at every login, replacing only OIDC-sourced bindings.

const (
	oidcStateTTL    = 10 * time.Minute
	discoveryTTL    = 10 * time.Minute
	maxProviders    = 50
	maxMappings     = 500
	oidcHTTPTimeout = 15 * time.Second

	msgNoMapping = "your account has no access to this bob server (no role mapping matched)"
)

var defaultScopes = []string{oidc.ScopeOpenID, "profile", "email"}

// providers memoises OIDC discovery per issuer (R-FW-011-style designed memoisation: one
// entry per configured issuer, refreshed after 10 minutes).
type providers struct {
	mu     sync.Mutex
	m      map[string]discovered
	client *http.Client
}

type discovered struct {
	p  *oidc.Provider
	at time.Time
}

func newProviders() *providers {
	return &providers{m: map[string]discovered{}, client: &http.Client{Timeout: oidcHTTPTimeout}}
}

func (ps *providers) ctx(ctx context.Context) context.Context {
	return context.WithValue(oidc.ClientContext(ctx, ps.client), oauth2.HTTPClient, ps.client)
}

func (ps *providers) get(ctx context.Context, issuer string) (*oidc.Provider, error) {
	ps.mu.Lock()
	d, ok := ps.m[issuer]
	ps.mu.Unlock()
	if ok && time.Since(d.at) < discoveryTTL {
		return d.p, nil
	}
	p, err := oidc.NewProvider(ps.ctx(ctx), issuer)
	if err != nil {
		return nil, fmt.Errorf("oidc discovery of %s: %w", issuer, err)
	}
	ps.mu.Lock()
	if len(ps.m) > maxProviders*2 {
		ps.m = map[string]discovered{}
	}
	ps.m[issuer] = discovered{p: p, at: time.Now()}
	ps.mu.Unlock()
	return p, nil
}

func (id *Identity) listProviders(ctx context.Context) ([]*store.OidcProviderRecord, error) {
	idx, err := store.Get(ctx, id.control, store.OidcProviderIndexKey())
	if errors.Is(err, store.ErrNotFound) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	var out []*store.OidcProviderRecord
	for _, pid := range idx.IDs.Value() {
		p, err := store.Get(ctx, id.control, store.OidcProviderKey(pid))
		if errors.Is(err, store.ErrNotFound) {
			continue
		} else if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, nil
}

// baseURL is server.base_url, or the scheme and host of the request when it is not set.
func (id *Identity) baseURL(r *http.Request) string {
	if id.info.BaseURL != "" {
		return strings.TrimSuffix(id.info.BaseURL, "/")
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}

// safeNext keeps a post-login target on this server: a path, never another origin.
func safeNext(next string) string {
	if !strings.HasPrefix(next, "/") || strings.HasPrefix(next, "//") || strings.ContainsAny(next, "\\\r\n") || len(next) > 2048 {
		return "/"
	}
	return next
}

func (id *Identity) oauthConfig(p *store.OidcProviderRecord, disc *oidc.Provider, redirect string) *oauth2.Config {
	scopes := p.Scopes.Value()
	if len(scopes) == 0 {
		scopes = defaultScopes
	}
	return &oauth2.Config{
		ClientID: p.ClientID.Value(), ClientSecret: p.ClientSecret.Value(), Endpoint: disc.Endpoint(),
		RedirectURL: redirect, Scopes: scopes,
	}
}

// oidcStart: GET /auth/oidc/start?provider=<id>&next=<path>.
func (id *Identity) oidcStart(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	pid := r.URL.Query().Get("provider")
	if !slugRE.MatchString(pid) {
		plainError(w, "unknown login provider", http.StatusNotFound)
		return
	}
	p, err := store.Get(ctx, id.control, store.OidcProviderKey(pid))
	if errors.Is(err, store.ErrNotFound) {
		plainError(w, "unknown login provider", http.StatusNotFound)
		return
	} else if err != nil {
		id.httpFail(w, r, err)
		return
	}
	disc, err := id.oidc.get(ctx, p.Issuer.Value())
	if err != nil {
		id.log.Warn("OIDC provider unreachable", "provider", pid, "err", err.Error())
		plainError(w, "the login provider is unreachable, try again later", http.StatusBadGateway)
		return
	}
	state, nonce, verifier := newToken(""), newToken(""), oauth2.GenerateVerifier()
	st := store.OidcStateKey(state.reveal())
	st.Provider, st.Nonce, st.Verifier = store.Set(pid), store.Set(nonce.reveal()), store.Set(verifier)
	st.Next, st.Redirect = store.Set(safeNext(r.URL.Query().Get("next"))), store.Set(id.baseURL(r)+"/auth/oidc/callback")
	st.Expires = store.Set(id.now().Add(oidcStateTTL))
	if err := store.Create(ctx, id.control, st); err != nil {
		id.httpFail(w, r, err)
		return
	}
	cfg := id.oauthConfig(p, disc, st.Redirect.Value())
	http.Redirect(w, r, cfg.AuthCodeURL(state.reveal(), oidc.Nonce(st.Nonce.Value()), oauth2.S256ChallengeOption(verifier)), http.StatusFound)
}

// oidcCallback: GET /auth/oidc/callback?code&state. Every failure answers a 4xx page and
// starts no session.
func (id *Identity) oidcCallback(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	q := r.URL.Query()
	state := q.Get("state")
	if state == "" || len(state) > 512 {
		plainError(w, "invalid login state", http.StatusBadRequest)
		return
	}
	st, err := store.Get(ctx, id.control, store.OidcStateKey(state))
	if errors.Is(err, store.ErrNotFound) {
		plainError(w, "invalid or expired login state, start the login again", http.StatusBadRequest)
		return
	} else if err != nil {
		id.httpFail(w, r, err)
		return
	}
	// Single use: whoever deletes the state owns this callback.
	if err := store.Delete(ctx, id.control, st); err != nil {
		plainError(w, "invalid or expired login state, start the login again", http.StatusBadRequest)
		return
	}
	if !id.now().Before(st.Expires.Value()) {
		plainError(w, "the login took too long, start it again", http.StatusBadRequest)
		return
	}
	if e := q.Get("error"); e != "" {
		plainError(w, "the login provider refused the login", http.StatusBadRequest)
		return
	}
	p, err := store.Get(ctx, id.control, store.OidcProviderKey(st.Provider.Value()))
	if errors.Is(err, store.ErrNotFound) {
		plainError(w, "unknown login provider", http.StatusBadRequest)
		return
	} else if err != nil {
		id.httpFail(w, r, err)
		return
	}
	disc, err := id.oidc.get(ctx, p.Issuer.Value())
	if err != nil {
		id.log.Warn("OIDC provider unreachable", "provider", p.ID.Value(), "err", err.Error())
		plainError(w, "the login provider is unreachable, try again later", http.StatusBadGateway)
		return
	}
	tok, err := id.oauthConfig(p, disc, st.Redirect.Value()).Exchange(id.oidc.ctx(ctx), q.Get("code"), oauth2.VerifierOption(st.Verifier.Value()))
	if err != nil {
		id.log.Warn("OIDC code exchange failed", "provider", p.ID.Value(), "err", oauthErrText(err))
		plainError(w, "the login provider did not confirm the login", http.StatusBadRequest)
		return
	}
	raw, _ := tok.Extra("id_token").(string)
	if raw == "" {
		plainError(w, "the login provider sent no ID token", http.StatusBadRequest)
		return
	}
	idt, err := disc.Verifier(&oidc.Config{ClientID: p.ClientID.Value(), Now: id.now}).Verify(id.oidc.ctx(ctx), raw)
	if err != nil {
		id.log.Warn("OIDC ID token rejected", "provider", p.ID.Value(), "err", err.Error())
		plainError(w, "the ID token of the login provider is not valid", http.StatusBadRequest)
		return
	}
	if idt.Nonce != st.Nonce.Value() {
		id.log.Warn("OIDC ID token with a wrong nonce", "provider", p.ID.Value())
		plainError(w, "the ID token of the login provider is not valid (nonce mismatch)", http.StatusBadRequest)
		return
	}
	var claims map[string]any
	if err := idt.Claims(&claims); err != nil {
		plainError(w, "the ID token of the login provider is not valid", http.StatusBadRequest)
		return
	}
	uid, status, msg, err := id.oidcLogin(ctx, p, idt.Issuer, idt.Subject, claims)
	if err != nil {
		id.httpFail(w, r, err)
		return
	}
	if msg != "" {
		plainError(w, msg, status)
		return
	}
	iss, err := id.createSession(ctx, uid, "browser", r.UserAgent())
	if err != nil {
		id.httpFail(w, r, err)
		return
	}
	w.Header().Add("Set-Cookie", sessionCookie(iss.session, browserMax))
	http.Redirect(w, r, st.Next.Value(), http.StatusFound)
}

// oauthErrText is an oauth2 error without the response body (which could carry tokens).
func oauthErrText(err error) string {
	var re *oauth2.RetrieveError
	if errors.As(err, &re) {
		return fmt.Sprintf("token endpoint answered %d %s", re.Response.StatusCode, re.ErrorCode)
	}
	return err.Error()
}

// oidcLogin finds or creates the user of (issuer, subject) and replaces its OIDC bindings by
// the matched mappings. A refused login returns a status and message for the page.
func (id *Identity) oidcLogin(ctx context.Context, p *store.OidcProviderRecord, issuer, subject string, claims map[string]any) (uid string, status int, msg string, err error) {
	maps, err := store.Get(ctx, id.control, store.OidcMappingsKey())
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return "", 0, "", err
	}
	var matched []api.Binding
	if maps != nil {
		matched = rbac.Match(maps.Mappings.Value(), p.ID.Value(), claims)
	}
	if p.RequireMapping.Value() && len(matched) == 0 {
		return "", http.StatusForbidden, msgNoMapping, nil
	}
	claim := p.UsernameClaim.Value()
	if claim == "" {
		claim = "preferred_username"
	}
	login, _ := claims[claim].(string)
	if login == "" {
		login = subject
	}
	email, _ := claims["email"].(string)
	name, _ := claims["name"].(string)
	login, email, name = truncate(login, 200), truncate(email, 254), truncate(name, 200)

	uid, err = id.oidcUser(ctx, p, issuer, subject, login)
	if err != nil {
		return "", 0, "", err
	}
	u, err := store.CAS(ctx, id.control, store.UserKey(uid), func(cur *store.UserRecord) (*store.UserRecord, error) {
		if cur == nil {
			return nil, store.ErrNotFound
		}
		cur.Login, cur.Email, cur.DisplayName, cur.Provider = store.Set(login), store.Set(email), store.Set(name), store.Set(p.ID.Value())
		return cur, nil
	})
	if err != nil {
		return "", 0, "", err
	}
	id.users.put(uid, u)
	if u.Disabled.Value() {
		return "", http.StatusForbidden, "your account is disabled", nil
	}
	if u.Index.Value() > 0 {
		if err := id.indexUser(ctx, u); err != nil {
			id.log.Warn("users index entry not updated", "user", uid, "err", err.Error())
		}
	}
	now := id.now()
	replace := func(keepAdmin bool) func(ro []store.RoleBindingEntry) ([]store.RoleBindingEntry, error) {
		return func(ro []store.RoleBindingEntry) ([]store.RoleBindingEntry, error) {
			next := rbac.ReplaceOIDC(ro, matched, bindingID, now)
			if keepAdmin && !hasBinding(next, adminRole, "*") {
				for _, b := range ro {
					if b.Role == adminRole && b.Project == "*" {
						next = append(next, b)
						break
					}
				}
			}
			return next, nil
		}
	}
	principal := "user:" + uid
	before, after, err := id.updateBindings(ctx, principal, replace(false))
	if errors.Is(err, errLastAdmin) {
		// The provider dropped the admin mapping of the last admin: keep that one binding
		// rather than leave the server without an admin (§6.4.1).
		id.log.Warn("OIDC login kept the admin binding of the last admin", "user", uid)
		before, after, err = id.updateBindings(ctx, principal, replace(true))
	}
	if err != nil {
		return "", 0, "", err
	}
	for _, b := range after {
		if !slices.ContainsFunc(before, func(x store.RoleBindingEntry) bool { return x.Role == b.Role }) {
			id.addMember(ctx, b.Role, principal)
		}
	}
	for _, b := range before {
		id.removeMember(ctx, b.Role, principal, after)
	}
	return uid, 0, "", nil
}

// oidcUser returns the user id of (issuer, subject), creating the user on first login.
func (id *Identity) oidcUser(ctx context.Context, p *store.OidcProviderRecord, issuer, subject, login string) (string, error) {
	for range 3 {
		n, err := store.Get(ctx, id.control, store.OidcSubKey(issuer, subject))
		if err == nil {
			if _, err := store.Get(ctx, id.control, store.UserKey(n.ID.Value())); err == nil {
				return n.ID.Value(), nil
			} else if !errors.Is(err, store.ErrNotFound) {
				return "", err
			}
			// The mapping points to a deleted user: release it and create a new user.
			_ = store.Delete(ctx, id.control, n)
			continue
		}
		if !errors.Is(err, store.ErrNotFound) {
			return "", err
		}
		now := id.now()
		u := store.UserKey("u" + newID(now))
		u.Login, u.Source, u.Provider, u.Issuer, u.Subject, u.Created = store.Set(login), store.Set[string](sourceOIDC), store.Set(p.ID.Value()), store.Set(issuer), store.Set(subject), store.Set(now)
		err = id.createUser(ctx, u, store.OidcSubKey(issuer, subject))
		if err == nil {
			return u.ID.Value(), nil
		}
		var ce *connect.Error
		if !errors.As(err, &ce) || ce.Code() != connect.CodeAlreadyExists {
			return "", err
		}
		// A concurrent first login created the user: read it again.
	}
	return "", errors.New("oidc: user of the subject could not be created")
}

// oidcService implements bob.access.v1.OidcService.
type oidcService struct{ id *Identity }

func (s oidcService) providerMsg(p *store.OidcProviderRecord, base string) *accessv1.OidcProvider {
	scopes := p.Scopes.Value()
	if len(scopes) == 0 {
		scopes = defaultScopes
	}
	claim := p.UsernameClaim.Value()
	if claim == "" {
		claim = "preferred_username"
	}
	m := &accessv1.OidcProvider{
		Id: p.ID.Value(), DisplayName: p.DisplayName.Value(), IssuerUrl: p.Issuer.Value(), ClientId: p.ClientID.Value(),
		HasClientSecret: p.ClientSecret.Value() != "", Scopes: scopes, UsernameClaim: claim,
		RequireMapping: p.RequireMapping.Value(), Etag: store.ETagOf(p.ETag),
	}
	if base != "" {
		m.RedirectUrl = strings.TrimSuffix(base, "/") + "/auth/oidc/callback"
	}
	return m
}

func validIssuer(s string) bool {
	u, err := url.Parse(s)
	return err == nil && u.Scheme == "https" && u.Host != "" && u.User == nil && u.RawQuery == "" && u.Fragment == "" && len(s) <= 500
}

func (s oidcService) CreateOidcProvider(ctx context.Context, req *connect.Request[accessv1.CreateOidcProviderRequest]) (*connect.Response[accessv1.CreateOidcProviderResponse], error) {
	in := req.Msg.GetProvider()
	switch {
	case !slugRE.MatchString(in.GetId()):
		return nil, api.InvalidArgument("provider.id", "a provider id must match ^[a-z][a-z0-9-]{1,38}[a-z0-9]$")
	case !validIssuer(in.GetIssuerUrl()):
		return nil, api.InvalidArgument("provider.issuerUrl", "the issuer must be an https URL")
	case in.GetClientId() == "" || len(in.GetClientId()) > 500:
		return nil, api.InvalidArgument("provider.clientId", "a client id is required")
	case len(in.GetClientSecret()) > 1000 || len(in.GetDisplayName()) > 200 || len(in.GetUsernameClaim()) > 100 || len(in.GetScopes()) > 20:
		return nil, api.InvalidArgument("provider", "field too long")
	}
	p := store.OidcProviderKey(in.GetId())
	p.DisplayName, p.Issuer, p.ClientID, p.ClientSecret = store.Set(in.GetDisplayName()), store.Set(in.GetIssuerUrl()), store.Set(in.GetClientId()), store.Set(in.GetClientSecret())
	p.Scopes, p.UsernameClaim, p.RequireMapping, p.Created = store.Set(in.GetScopes()), store.Set(in.GetUsernameClaim()), store.Set(in.GetRequireMapping()), store.Set(s.id.now())
	if _, err := store.CAS(ctx, s.id.control, store.OidcProviderIndexKey(), func(cur *store.OidcProviderIndex) (*store.OidcProviderIndex, error) {
		if cur == nil {
			cur = store.OidcProviderIndexKey()
		}
		if slices.Contains(cur.IDs.Value(), p.ID.Value()) {
			return nil, store.ErrNoChange
		}
		if len(cur.IDs.Value()) >= maxProviders {
			return nil, api.FailedPrecondition(fmt.Sprintf("at most %d OIDC providers", maxProviders))
		}
		cur.IDs = store.Set(append(cur.IDs.Value(), p.ID.Value()))
		slices.Sort(cur.IDs.Value())
		return cur, nil
	}); err != nil {
		return nil, storageError(err)
	}
	if err := store.Create(ctx, s.id.control, p); err != nil {
		if errors.Is(err, store.ErrExists) {
			return nil, api.AlreadyExists(fmt.Sprintf("OIDC provider %s already exists", p.ID.Value()))
		}
		return nil, storageError(err)
	}
	return connect.NewResponse(&accessv1.CreateOidcProviderResponse{Provider: s.providerMsg(p, s.id.info.BaseURL)}), nil
}

func (s oidcService) get(ctx context.Context, pid string) (*store.OidcProviderRecord, error) {
	if !slugRE.MatchString(pid) {
		return nil, api.NotFound("OIDC provider")
	}
	p, err := store.Get(ctx, s.id.control, store.OidcProviderKey(pid))
	if errors.Is(err, store.ErrNotFound) {
		return nil, api.NotFound("OIDC provider")
	} else if err != nil {
		return nil, storageError(err)
	}
	return p, nil
}

func (s oidcService) GetOidcProvider(ctx context.Context, req *connect.Request[accessv1.GetOidcProviderRequest]) (*connect.Response[accessv1.GetOidcProviderResponse], error) {
	p, err := s.get(ctx, req.Msg.GetId())
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&accessv1.GetOidcProviderResponse{Provider: s.providerMsg(p, s.id.info.BaseURL)}), nil
}

func (s oidcService) ListOidcProviders(ctx context.Context, _ *connect.Request[accessv1.ListOidcProvidersRequest]) (*connect.Response[accessv1.ListOidcProvidersResponse], error) {
	ps, err := s.id.listProviders(ctx)
	if err != nil {
		return nil, storageError(err)
	}
	out := &accessv1.ListOidcProvidersResponse{}
	for _, p := range ps {
		out.Providers = append(out.Providers, s.providerMsg(p, s.id.info.BaseURL))
	}
	return connect.NewResponse(out), nil
}

func (s oidcService) UpdateOidcProvider(ctx context.Context, req *connect.Request[accessv1.UpdateOidcProviderRequest]) (*connect.Response[accessv1.UpdateOidcProviderResponse], error) {
	in := req.Msg.GetProvider()
	if err := api.CheckMask(req.Msg.GetUpdateMask(), in, "display_name", "issuer_url", "client_id", "client_secret", "scopes", "username_claim", "require_mapping"); err != nil {
		return nil, err
	}
	if _, err := s.get(ctx, in.GetId()); err != nil {
		return nil, err
	}
	paths := map[string]bool{}
	for _, p := range req.Msg.GetUpdateMask().GetPaths() {
		paths[strings.ToLower(strings.ReplaceAll(p, "_", ""))] = true
	}
	if paths["issuerurl"] && !validIssuer(in.GetIssuerUrl()) {
		return nil, api.InvalidArgument("provider.issuerUrl", "the issuer must be an https URL")
	}
	if paths["clientid"] && in.GetClientId() == "" {
		return nil, api.InvalidArgument("provider.clientId", "a client id is required")
	}
	next, err := store.CAS(ctx, s.id.control, store.OidcProviderKey(in.GetId()), func(p *store.OidcProviderRecord) (*store.OidcProviderRecord, error) {
		if p == nil {
			return nil, api.NotFound("OIDC provider")
		}
		if err := checkETag("provider.etag", in.GetEtag(), store.ETagOf(p.ETag)); err != nil {
			return nil, err
		}
		if paths["displayname"] {
			p.DisplayName = store.Set(in.GetDisplayName())
		}
		if paths["issuerurl"] {
			p.Issuer = store.Set(in.GetIssuerUrl())
		}
		if paths["clientid"] {
			p.ClientID = store.Set(in.GetClientId())
		}
		if paths["clientsecret"] {
			p.ClientSecret = store.Set(in.GetClientSecret())
		}
		if paths["scopes"] {
			p.Scopes = store.Set(in.GetScopes())
		}
		if paths["usernameclaim"] {
			p.UsernameClaim = store.Set(in.GetUsernameClaim())
		}
		if paths["requiremapping"] {
			p.RequireMapping = store.Set(in.GetRequireMapping())
		}
		return p, nil
	})
	if err != nil {
		return nil, storageError(err)
	}
	return connect.NewResponse(&accessv1.UpdateOidcProviderResponse{Provider: s.providerMsg(next, s.id.info.BaseURL)}), nil
}

func (s oidcService) DeleteOidcProvider(ctx context.Context, req *connect.Request[accessv1.DeleteOidcProviderRequest]) (*connect.Response[accessv1.DeleteOidcProviderResponse], error) {
	p, err := s.get(ctx, req.Msg.GetId())
	if err != nil {
		return nil, err
	}
	if e := req.Msg.GetEtag(); e != "" && e != store.ETagOf(p.ETag) {
		return nil, api.Aborted("the resource was changed by someone else (etag mismatch); reload and retry")
	}
	if err := store.Delete(ctx, s.id.control, p); err != nil {
		return nil, storageError(err)
	}
	if _, err := store.CAS(ctx, s.id.control, store.OidcProviderIndexKey(), func(cur *store.OidcProviderIndex) (*store.OidcProviderIndex, error) {
		if cur == nil || !slices.Contains(cur.IDs.Value(), p.ID.Value()) {
			return nil, store.ErrNoChange
		}
		cur.IDs = store.Set(slices.DeleteFunc(cur.IDs.Value(), func(x string) bool { return x == p.ID.Value() }))
		return cur, nil
	}); err != nil {
		s.id.log.Warn("OIDC provider index not updated", "provider", p.ID.Value(), "err", err.Error())
	}
	if _, err := store.CAS(ctx, s.id.control, store.OidcMappingsKey(), func(cur *store.OidcMappings) (*store.OidcMappings, error) {
		if cur == nil {
			return nil, store.ErrNoChange
		}
		n := len(cur.Mappings.Value())
		cur.Mappings = store.Set(slices.DeleteFunc(cur.Mappings.Value(), func(m store.OidcMappingEntry) bool { return m.Provider == p.ID.Value() }))
		if n == len(cur.Mappings.Value()) {
			return nil, store.ErrNoChange
		}
		return cur, nil
	}); err != nil {
		s.id.log.Warn("mappings of a deleted OIDC provider not removed", "provider", p.ID.Value(), "err", err.Error())
	}
	return connect.NewResponse(&accessv1.DeleteOidcProviderResponse{}), nil
}

func mappingMsg(m store.OidcMappingEntry) *accessv1.OidcMapping {
	return &accessv1.OidcMapping{Id: m.ID, ProviderId: m.Provider, Claim: m.Claim, Value: m.Value, Role: m.Role, Project: m.Project}
}

func (s oidcService) CreateOidcMapping(ctx context.Context, req *connect.Request[accessv1.CreateOidcMappingRequest]) (*connect.Response[accessv1.CreateOidcMappingResponse], error) {
	in := req.Msg.GetMapping()
	switch {
	case in.GetClaim() == "" || len(in.GetClaim()) > 100:
		return nil, api.InvalidArgument("mapping.claim", "a claim name is required")
	case in.GetValue() == "" || len(in.GetValue()) > 500:
		return nil, api.InvalidArgument("mapping.value", "a claim value is required")
	case !rbac.ValidProject(in.GetProject()):
		return nil, api.InvalidArgument("mapping.project", "project must be a project id or \"*\"")
	}
	if _, err := s.get(ctx, in.GetProviderId()); err != nil {
		return nil, err
	}
	if in.GetRole() != adminRole {
		if _, err := s.id.role(ctx, in.GetRole()); errors.Is(err, store.ErrNotFound) {
			return nil, api.NotFound("role")
		} else if err != nil {
			return nil, storageError(err)
		}
	}
	e := store.OidcMappingEntry{
		ID: "m" + randomString(crockford, 12), Provider: in.GetProviderId(), Claim: in.GetClaim(),
		Value: in.GetValue(), Role: in.GetRole(), Project: in.GetProject(),
	}
	if _, err := store.CAS(ctx, s.id.control, store.OidcMappingsKey(), func(cur *store.OidcMappings) (*store.OidcMappings, error) {
		if cur == nil {
			cur = store.OidcMappingsKey()
		}
		n := 0
		for _, m := range cur.Mappings.Value() {
			if m.Provider == e.Provider {
				n++
			}
		}
		if n >= maxMappings {
			return nil, api.FailedPrecondition(fmt.Sprintf("a provider has at most %d mappings", maxMappings))
		}
		cur.Mappings = store.Set(append(cur.Mappings.Value(), e))
		return cur, nil
	}); err != nil {
		return nil, storageError(err)
	}
	return connect.NewResponse(&accessv1.CreateOidcMappingResponse{Mapping: mappingMsg(e)}), nil
}

func (s oidcService) ListOidcMappings(ctx context.Context, req *connect.Request[accessv1.ListOidcMappingsRequest]) (*connect.Response[accessv1.ListOidcMappingsResponse], error) {
	maps, err := store.Get(ctx, s.id.control, store.OidcMappingsKey())
	if errors.Is(err, store.ErrNotFound) {
		return connect.NewResponse(&accessv1.ListOidcMappingsResponse{}), nil
	} else if err != nil {
		return nil, storageError(err)
	}
	out := &accessv1.ListOidcMappingsResponse{}
	for _, m := range maps.Mappings.Value() {
		if pid := req.Msg.GetProviderId(); pid == "" || m.Provider == pid {
			out.Mappings = append(out.Mappings, mappingMsg(m))
		}
	}
	return connect.NewResponse(out), nil
}

func (s oidcService) DeleteOidcMapping(ctx context.Context, req *connect.Request[accessv1.DeleteOidcMappingRequest]) (*connect.Response[accessv1.DeleteOidcMappingResponse], error) {
	found := false
	if _, err := store.CAS(ctx, s.id.control, store.OidcMappingsKey(), func(cur *store.OidcMappings) (*store.OidcMappings, error) {
		if cur == nil {
			return nil, store.ErrNoChange
		}
		n := len(cur.Mappings.Value())
		cur.Mappings = store.Set(slices.DeleteFunc(cur.Mappings.Value(), func(m store.OidcMappingEntry) bool { return m.ID == req.Msg.GetId() }))
		found = n != len(cur.Mappings.Value())
		if !found {
			return nil, store.ErrNoChange
		}
		return cur, nil
	}); err != nil {
		return nil, storageError(err)
	}
	if !found {
		return nil, api.NotFound("mapping")
	}
	return connect.NewResponse(&accessv1.DeleteOidcMappingResponse{}), nil
}
