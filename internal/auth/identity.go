package auth

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"connectrpc.com/connect"

	"github.com/megakuul/bob/internal/api"
	"github.com/megakuul/bob/internal/rbac"
	"github.com/megakuul/bob/internal/store"
)

// Config configures the identity of one orchestrator.
type Config struct {
	Control *store.Bucket    // required: the control bucket
	Now     func() time.Time // the process clock (bobtest builds can move it); nil = time.Now
	Log     *slog.Logger     // nil = slog.Default()
	Info    api.Info         // GetServerInfo / WhoAmI; Info.BaseURL "" = derived per request
	// RBAC (required) decides grants, role permissions, the catalogue and LIST_FILTERED
	// scopes; it must be the API's authorizer too.
	RBAC *rbac.Authorizer
}

// Identity is the identity service of an orchestrator (SPEC §6.3): it authenticates API and
// download credentials, serves AuthService, UserService, RoleService, ServiceAccountService and
// OidcService, and the OAuth/OIDC HTTP endpoints.
type Identity struct {
	control *store.Bucket
	now     func() time.Time
	log     *slog.Logger
	info    api.Info
	rbac    *rbac.Authorizer

	hasher   *hasher
	throttle *throttle
	oidc     *providers

	tokens   ttlCache[store.TokenRecord]
	sessions ttlCache[store.SessionRecord]
	users    ttlCache[store.UserRecord]
	sas      ttlCache[store.ServiceAccountRecord]
}

// New returns the identity service. It performs no I/O.
func New(cfg Config) (*Identity, error) {
	if cfg.Control == nil || cfg.RBAC == nil {
		return nil, errors.New("auth: Config.Control and Config.RBAC are required")
	}
	id := &Identity{
		control: cfg.Control, now: cfg.Now, log: cfg.Log, info: cfg.Info, rbac: cfg.RBAC,
		hasher: newHasher(), throttle: newThrottle(), oidc: newProviders(),
	}
	if id.now == nil {
		id.now = time.Now
	}
	if id.log == nil {
		id.log = slog.Default()
	}
	return id, nil
}

// Cache lifetimes (SPEC §6.3.1): positive token lookups ≤ 10 s, negative ≤ 2 s. Records of
// principals use the same 10 s, so revocation, disabling and binding changes made on another
// orchestrator take effect within 10 s. Writes on this orchestrator drop the entry at once.
const (
	positiveTTL = 10 * time.Second
	negativeTTL = 2 * time.Second
	cacheMax    = 50_000
)

// ttlCache keeps records (or their absence) for a short time. Wall-clock based: the test
// clock moves expiries, not cache ages.
type ttlCache[T any] struct {
	mu sync.Mutex
	m  map[string]ttlEntry[T]
}

type ttlEntry[T any] struct {
	v  *T // nil = known absent
	at time.Time
}

func (c *ttlCache[T]) get(key string) (*T, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.m[key]
	if !ok {
		return nil, false
	}
	ttl := positiveTTL
	if e.v == nil {
		ttl = negativeTTL
	}
	if time.Since(e.at) > ttl {
		delete(c.m, key)
		return nil, false
	}
	return e.v, true
}

func (c *ttlCache[T]) put(key string, v *T) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.m == nil || len(c.m) >= cacheMax {
		c.m = map[string]ttlEntry[T]{}
	}
	c.m[key] = ttlEntry[T]{v: v, at: time.Now()}
}

func (c *ttlCache[T]) drop(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.m, key)
}

// load reads a record through the cache. key is the record's key model; name its cache key.
func load[T any](ctx context.Context, b *store.Bucket, c *ttlCache[T], name string, key *T) (*T, error) {
	if v, ok := c.get(name); ok {
		if v == nil {
			return nil, store.ErrNotFound
		}
		return v, nil
	}
	v, err := store.Get(ctx, b, key)
	switch {
	case errors.Is(err, store.ErrNotFound):
		c.put(name, nil)
		return nil, store.ErrNotFound
	case err != nil:
		return nil, err
	}
	c.put(name, v)
	return v, nil
}

func (id *Identity) user(ctx context.Context, uid string) (*store.UserRecord, error) {
	return load(ctx, id.control, &id.users, uid, store.UserKey(uid))
}

func (id *Identity) serviceAccount(ctx context.Context, sa string) (*store.ServiceAccountRecord, error) {
	return load(ctx, id.control, &id.sas, sa, store.ServiceAccountKey(sa))
}

// storageError maps a store failure to the API error model: unavailable storage is
// Unavailable, everything else Internal (logged with a reference by the API layer).
func storageError(err error) error {
	var ce *connect.Error
	if errors.As(err, &ce) {
		return err
	}
	if errors.Is(err, store.ErrConflict) {
		return api.Aborted("the resource changed concurrently, try again")
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return api.Unavailable("storage unavailable")
	}
	return err
}

// Authenticate implements api.Authenticator for bob's own credentials: bob_at_, bob_dt_ and
// bob_sa_ bearer tokens and bob_st_ session cookies. Any other credential is
// api.ErrUnknownCredential (the next authenticator, e.g. the bootstrap token, is asked).
func (id *Identity) Authenticate(ctx context.Context, c api.Credential) (*api.Principal, error) {
	switch {
	case c.Bearer != "":
		tok := Secret(c.Bearer)
		if !tok.hasPrefix(PrefixAccess) && !tok.hasPrefix(PrefixDownload) && !tok.hasPrefix(PrefixSA) {
			return nil, api.ErrUnknownCredential
		}
		return id.tokenPrincipal(ctx, tok)
	case c.Session != "":
		st := Secret(c.Session)
		if !st.hasPrefix(PrefixSession) {
			return nil, api.ErrUnknownCredential
		}
		p, _, err := id.sessionPrincipal(ctx, st, "browser")
		return p, err
	}
	return nil, api.ErrUnknownCredential
}

var errInvalidToken = api.Unauthenticated("invalid or revoked credential")

// tokenPrincipal validates a bob_at_/bob_dt_/bob_sa_ token.
func (id *Identity) tokenPrincipal(ctx context.Context, tok Secret) (*api.Principal, error) {
	key := store.TokenKey(tok.reveal())
	rec, err := load(ctx, id.control, &id.tokens, key.Hash, key)
	switch {
	case errors.Is(err, store.ErrNotFound):
		return nil, errInvalidToken
	case err != nil:
		return nil, storageError(err)
	}
	now := id.now()
	if !rec.Expires.IsZero() && !now.Before(rec.Expires) {
		return nil, api.Unauthenticated("token expired")
	}
	var p *api.Principal
	kind, owner, _ := strings.Cut(rec.Owner, ":")
	switch kind {
	case "user":
		u, err := id.user(ctx, owner)
		if errors.Is(err, store.ErrNotFound) || err == nil && u.Disabled {
			return nil, errInvalidToken
		} else if err != nil {
			return nil, storageError(err)
		}
		if rec.Session != "" {
			s, err := load(ctx, id.control, &id.sessions, rec.Session, &store.SessionRecord{Hash: rec.Session})
			if errors.Is(err, store.ErrNotFound) || err == nil && (s.User != owner || !sessionAlive(s, now)) {
				return nil, errInvalidToken
			} else if err != nil {
				return nil, storageError(err)
			}
		}
		p = userPrincipal(u)
	case "sa":
		sa, err := id.serviceAccount(ctx, owner)
		if errors.Is(err, store.ErrNotFound) || err == nil && sa.Disabled {
			return nil, errInvalidToken
		} else if err != nil {
			return nil, storageError(err)
		}
		// The SA record's list is the truth: a token whose record survived a revocation (the
		// delete failed) is still refused.
		if !slices.ContainsFunc(sa.Tokens, func(t store.SATokenEntry) bool { return t.ID == rec.TokenID && t.Hash == rec.Hash }) {
			return nil, errInvalidToken
		}
		p = saPrincipal(sa)
		id.touchSAToken(ctx, sa, rec.TokenID, now)
	default:
		return nil, errInvalidToken
	}
	p.Scope = rec.Scope
	p.Expires = rec.Expires
	return p, nil
}

func sessionAlive(s *store.SessionRecord, now time.Time) bool {
	return now.Before(s.IdleUntil) && now.Before(s.MaxUntil)
}

// sessionPrincipal validates a session token of kind ("browser": bob_st_, "cli": bob_rt_) and
// extends its idle timer.
func (id *Identity) sessionPrincipal(ctx context.Context, tok Secret, kind string) (*api.Principal, *store.SessionRecord, error) {
	key := store.SessionKey(tok.reveal())
	s, err := load(ctx, id.control, &id.sessions, key.Hash, key)
	switch {
	case errors.Is(err, store.ErrNotFound):
		return nil, nil, errInvalidToken
	case err != nil:
		return nil, nil, storageError(err)
	}
	now := id.now()
	if s.Kind != kind {
		return nil, nil, errInvalidToken
	}
	if !sessionAlive(s, now) {
		return nil, nil, api.Unauthenticated("session expired")
	}
	u, err := id.user(ctx, s.User)
	if errors.Is(err, store.ErrNotFound) || err == nil && u.Disabled {
		return nil, nil, errInvalidToken
	} else if err != nil {
		return nil, nil, storageError(err)
	}
	s = id.touchSession(ctx, s, now)
	p := userPrincipal(u)
	p.Scope = "session"
	if kind == "cli" {
		p.Scope = "api"
	}
	p.Expires = s.IdleUntil
	return p, s, nil
}

// touchEvery bounds how often a session's last-used time and idle timer are written.
const touchEvery = 5 * time.Minute

// touchSession extends the idle timer (CAS, at most every 5 min). A failed write only means
// the timer is extended at the next use.
func (id *Identity) touchSession(ctx context.Context, s *store.SessionRecord, now time.Time) *store.SessionRecord {
	if now.Sub(s.LastUsed) < touchEvery {
		return s
	}
	idle := browserIdle
	if s.Kind == "cli" {
		idle = cliIdle
	}
	next, err := store.CAS(ctx, id.control, &store.SessionRecord{Hash: s.Hash}, func(cur *store.SessionRecord) (*store.SessionRecord, error) {
		if cur == nil {
			return nil, store.ErrNoChange
		}
		cur.LastUsed = now
		cur.IdleUntil = minTime(now.Add(idle), cur.MaxUntil)
		return cur, nil
	})
	if err != nil || next == nil {
		if err != nil {
			id.log.Warn("session idle timer not extended", "err", err.Error())
		}
		return s
	}
	id.sessions.put(s.Hash, next)
	return next
}

// touchSAToken records the day a service-account token was last used (one write per token
// and day).
func (id *Identity) touchSAToken(ctx context.Context, sa *store.ServiceAccountRecord, tokenID string, now time.Time) {
	day := now.UTC().Truncate(24 * time.Hour)
	for _, t := range sa.Tokens {
		if t.ID == tokenID && !t.LastUsed.Before(day) {
			return
		}
	}
	next, err := store.CAS(ctx, id.control, store.ServiceAccountKey(sa.ID), func(cur *store.ServiceAccountRecord) (*store.ServiceAccountRecord, error) {
		if cur == nil {
			return nil, store.ErrNoChange
		}
		for i := range cur.Tokens {
			if cur.Tokens[i].ID == tokenID && cur.Tokens[i].LastUsed.Before(day) {
				cur.Tokens[i].LastUsed = day
				return cur, nil
			}
		}
		return nil, store.ErrNoChange
	})
	if err != nil {
		id.log.Warn("service account token last-used day not recorded", "sa", sa.ID, "err", err.Error())
		return
	}
	if next != nil {
		id.sas.put(sa.ID, next)
	}
}

func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}

func bindingsOf(ro []store.RoleBindingEntry) []api.Binding {
	out := make([]api.Binding, 0, len(ro))
	for _, b := range ro {
		out = append(out, api.Binding{Role: b.Role, Project: b.Project})
	}
	return out
}

func userPrincipal(u *store.UserRecord) *api.Principal {
	name := u.DisplayName
	if name == "" {
		name = u.Login
	}
	return &api.Principal{
		ID: "user:" + u.ID, DisplayName: name, Login: u.Login,
		Bindings: bindingsOf(u.Bindings), MustChangePassword: u.MustChange,
	}
}

func saPrincipal(sa *store.ServiceAccountRecord) *api.Principal {
	return &api.Principal{ID: "sa:" + sa.ID, DisplayName: sa.ID, Bindings: bindingsOf(sa.Bindings)}
}

// HTTPPrincipal authenticates a download request (/nix/*, /oci/token, /r/*, SPEC §6.2): HTTP
// Basic with any user name and a bob token as the password (netrc), or a Bearer token. Both
// api and download scopes are accepted. No Authorization header is anonymous (nil, nil); an
// invalid or expired credential is an error.
func (id *Identity) HTTPPrincipal(r *http.Request) (*api.Principal, error) {
	h := r.Header.Get("Authorization")
	if h == "" {
		return nil, nil
	}
	var tok string
	if _, pw, ok := r.BasicAuth(); ok {
		tok = pw
	} else if scheme, t, ok := strings.Cut(h, " "); ok && strings.EqualFold(scheme, "Bearer") {
		tok = strings.TrimSpace(t)
	}
	if tok == "" {
		return nil, errInvalidToken
	}
	p, err := id.Authenticate(r.Context(), api.Credential{Bearer: tok})
	if errors.Is(err, api.ErrUnknownCredential) {
		return nil, errInvalidToken
	}
	return p, err
}
