package auth

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"time"

	"github.com/megakuul/bob/internal/store"
)

// Lifetimes of SPEC §6.3.1 (the session settings' defaults).
const (
	accessTTL   = time.Hour
	browserIdle = 12 * time.Hour
	browserMax  = 7 * 24 * time.Hour
	cliIdle     = 7 * 24 * time.Hour
	cliMax      = 30 * 24 * time.Hour
	maxSessions = 100

	// CookieName is the browser session cookie.
	CookieName = "bob_session"
)

// sessionCookie returns the Set-Cookie value of a browser session (HttpOnly; Secure;
// SameSite=Strict; Path=/). An empty value clears the cookie.
func sessionCookie(value Secret, maxAge time.Duration) string {
	c := &http.Cookie{
		Name: CookieName, Value: value.reveal(), Path: "/", HttpOnly: true, Secure: true,
		SameSite: http.SameSiteStrictMode, MaxAge: int(maxAge.Seconds()),
	}
	if value == "" {
		c.MaxAge = -1
	}
	return c.String()
}

// issued are the credentials of a new session.
type issued struct {
	session  Secret // bob_st_ (browser) or bob_rt_ (cli)
	download Secret // bob_dt_ (cli only)
	rec      *store.SessionRecord
}

// createSession starts a session of kind "browser" or "cli" for uid. A CLI session also gets
// its download token, valid as long as the session.
func (id *Identity) createSession(ctx context.Context, uid, kind, userAgent string) (*issued, error) {
	now := id.now()
	prefix, idle, maxAge := PrefixSession, browserIdle, browserMax
	if kind == "cli" {
		prefix, idle, maxAge = PrefixRefresh, cliIdle, cliMax
	}
	out := &issued{session: newToken(prefix)}
	rec := store.SessionKey(out.session.reveal())
	rec.ID = store.Set(newID(now))
	rec.User, rec.Kind, rec.UserAgent = store.Set(uid), store.Set(kind), store.Set(truncate(userAgent, 200))
	rec.Created, rec.LastUsed = store.Set(now), store.Set(now)
	rec.IdleUntil, rec.MaxUntil = store.Set(now.Add(idle)), store.Set(now.Add(maxAge))
	if kind == "cli" {
		out.download = newToken(PrefixDownload)
		dt := store.TokenKey(out.download.reveal())
		dt.Owner, dt.Kind, dt.Scope, dt.Session = store.Set("user:"+uid), store.Set("dt"), store.Set("download"), store.Set(rec.Hash.Value())
		dt.Expires, dt.Created = store.Set(rec.MaxUntil.Value()), store.Set(now)
		if err := store.Create(ctx, id.control, dt); err != nil {
			return nil, storageError(err)
		}
		rec.Download = store.Set(dt.Hash.Value())
	}
	if err := store.Create(ctx, id.control, rec); err != nil {
		return nil, storageError(err)
	}
	out.rec = rec
	var evicted []string
	u, err := store.CAS(ctx, id.control, store.UserKey(uid), func(cur *store.UserRecord) (*store.UserRecord, error) {
		if cur == nil {
			return nil, store.ErrNoChange
		}
		cur.Sessions = store.Set(append(cur.Sessions.Value(), store.SessionRef{ID: rec.ID.Value(), Hash: rec.Hash.Value()}))
		evicted = nil
		for len(cur.Sessions.Value()) > maxSessions {
			evicted = append(evicted, cur.Sessions.Value()[0].Hash)
			cur.Sessions = store.Set(cur.Sessions.Value()[1:])
		}
		cur.LastLogin = store.Set(now)
		return cur, nil
	})
	if err != nil {
		id.deleteSession(ctx, rec.Hash.Value())
		return nil, storageError(err)
	}
	if u == nil || u.Disabled.Value() {
		id.deleteSession(ctx, rec.Hash.Value())
		return nil, errInvalidToken
	}
	id.users.put(uid, u)
	for _, h := range evicted {
		id.deleteSession(ctx, h)
	}
	return out, nil
}

// issueAccess issues a bob_at_ token (1 h) under a CLI session and forgets the session's
// expired access tokens.
func (id *Identity) issueAccess(ctx context.Context, s *store.SessionRecord) (Secret, error) {
	now := id.now()
	at := newToken(PrefixAccess)
	rec := store.TokenKey(at.reveal())
	rec.Owner, rec.Kind, rec.Scope, rec.Session = store.Set("user:"+s.User.Value()), store.Set("at"), store.Set("api"), store.Set(s.Hash.Value())
	rec.Expires, rec.Created = store.Set(now.Add(accessTTL)), store.Set(now)
	if err := store.Create(ctx, id.control, rec); err != nil {
		return "", storageError(err)
	}
	var expired []string
	next, err := store.CAS(ctx, id.control, &store.SessionRecord{Hash: s.Hash}, func(cur *store.SessionRecord) (*store.SessionRecord, error) {
		if cur == nil {
			return nil, store.ErrNoChange
		}
		expired = nil
		live := cur.Access.Value()[:0:0]
		for _, t := range cur.Access.Value() {
			if now.Before(t.Expires) {
				live = append(live, t)
			} else {
				expired = append(expired, t.Hash)
			}
		}
		cur.Access = store.Set(append(live, store.TokenRef{Hash: rec.Hash.Value(), Expires: rec.Expires.Value()}))
		cur.LastUsed = store.Set(now)
		cur.IdleUntil = store.Set(minTime(now.Add(cliIdle), cur.MaxUntil.Value()))
		return cur, nil
	})
	if err != nil {
		id.deleteToken(ctx, rec.Hash.Value())
		return "", storageError(err)
	}
	if next == nil {
		id.deleteToken(ctx, rec.Hash.Value())
		return "", errInvalidToken
	}
	id.sessions.put(s.Hash.Value(), next)
	for _, h := range expired {
		id.deleteToken(ctx, h)
	}
	return at, nil
}

// deleteToken removes tok/<hash> (absent is fine) and drops it from this orchestrator's cache.
func (id *Identity) deleteToken(ctx context.Context, hash string) {
	id.tokens.drop(hash)
	if _, err := store.CAS(ctx, id.control, &store.TokenRecord{Hash: store.ID(hash)}, func(cur *store.TokenRecord) (*store.TokenRecord, error) {
		if cur == nil {
			return nil, store.ErrNoChange
		}
		return nil, nil
	}); err != nil {
		id.log.Warn("token record not deleted", "err", err.Error())
	}
	id.tokens.drop(hash)
}

// deleteSession removes a session with its access and download tokens. The caller removes
// the user's `ss` entry (or already did).
func (id *Identity) deleteSession(ctx context.Context, hash string) {
	id.sessions.drop(hash)
	var tokens []string
	_, err := store.CAS(ctx, id.control, &store.SessionRecord{Hash: store.ID(hash)}, func(cur *store.SessionRecord) (*store.SessionRecord, error) {
		if cur == nil {
			return nil, store.ErrNoChange
		}
		tokens = nil
		for _, t := range cur.Access.Value() {
			tokens = append(tokens, t.Hash)
		}
		if cur.Download.Value() != "" {
			tokens = append(tokens, cur.Download.Value())
		}
		return nil, nil
	})
	if err != nil {
		id.log.Warn("session record not deleted", "err", err.Error())
	}
	id.sessions.drop(hash)
	for _, t := range tokens {
		id.deleteToken(ctx, t)
	}
}

// revokeSessions ends sessions of uid: those whose hash keep() rejects. It returns the
// number revoked.
func (id *Identity) revokeSessions(ctx context.Context, uid string, keep func(store.SessionRef) bool) (int, error) {
	var gone []string
	u, err := store.CAS(ctx, id.control, store.UserKey(uid), func(cur *store.UserRecord) (*store.UserRecord, error) {
		if cur == nil {
			return nil, store.ErrNoChange
		}
		gone = nil
		kept := cur.Sessions.Value()[:0:0]
		for _, s := range cur.Sessions.Value() {
			if keep(s) {
				kept = append(kept, s)
			} else {
				gone = append(gone, s.Hash)
			}
		}
		if len(gone) == 0 {
			return nil, store.ErrNoChange
		}
		cur.Sessions = store.Set(kept)
		return cur, nil
	})
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return 0, storageError(err)
	}
	if u != nil {
		id.users.put(uid, u)
	}
	for _, h := range gone {
		id.deleteSession(ctx, h)
	}
	return len(gone), nil
}

// sessionsOf returns the live session records of a user (one GET each, ≤ 100).
func (id *Identity) sessionsOf(ctx context.Context, u *store.UserRecord) ([]*store.SessionRecord, error) {
	now := id.now()
	var out []*store.SessionRecord
	for _, ref := range u.Sessions.Value() {
		s, err := store.Get(ctx, id.control, &store.SessionRecord{Hash: store.ID(ref.Hash)})
		if errors.Is(err, store.ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, storageError(err)
		}
		if sessionAlive(s, now) {
			out = append(out, s)
		}
	}
	slices.SortFunc(out, func(a, b *store.SessionRecord) int { return b.Created.Value().Compare(a.Created.Value()) })
	return out, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
