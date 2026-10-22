package auth

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"html/template"
	"net/http"
	"net/url"
	"strings"
	"time"

	"connectrpc.com/connect"

	"github.com/megakuul/bob/internal/api"
	"github.com/megakuul/bob/internal/store"
)

// The OAuth 2.0 endpoints for the bob CLI (SPEC §6.3.2, R-AUTH-150): authorization code with
// PKCE and a loopback redirect (RFC 7636, RFC 8252), the device flow (RFC 8628), refresh and
// revocation (RFC 7009), for the one public client bob-cli; plus the browser approval
// endpoints POST /oauth/consent and POST /device (the UI renders the pages, D-UI-7) and the
// OIDC relying party.

const (
	clientID       = "bob-cli"
	codeTTL        = 60 * time.Second
	deviceTTL      = 600 * time.Second
	deviceInterval = 5
	slowDownStep   = 5
	maxForm        = 64 << 10

	grantCode    = "authorization_code"
	grantRefresh = "refresh_token"
	grantDevice  = "urn:ietf:params:oauth:grant-type:device_code"

	userCodeAlphabet = "BCDFGHJKLMNPQRSTVWXZ"
)

// Handler serves the OAuth and OIDC endpoints of §6.2: GET /oauth/authorize, POST
// /oauth/consent, POST /oauth/token, POST /oauth/device/code, POST /oauth/revoke, POST
// /device, GET /auth/oidc/start, GET /auth/oidc/callback.
func (id *Identity) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /oauth/authorize", id.authorize)
	mux.HandleFunc("POST /oauth/consent", id.consent)
	mux.HandleFunc("POST /oauth/token", id.token)
	mux.HandleFunc("POST /oauth/device/code", id.deviceCode)
	mux.HandleFunc("POST /oauth/revoke", id.revoke)
	mux.HandleFunc("POST /device", id.deviceApprove)
	mux.HandleFunc("GET /auth/oidc/start", id.oidcStart)
	mux.HandleFunc("GET /auth/oidc/callback", id.oidcCallback)
	return noStore(mux)
}

// Patterns are the mux patterns Handler serves, for mounting it next to other routes.
func Patterns() []string {
	return []string{
		"GET /oauth/authorize", "POST /oauth/consent", "POST /oauth/token", "POST /oauth/device/code",
		"POST /oauth/revoke", "POST /device", "GET /auth/oidc/start", "GET /auth/oidc/callback",
	}
}

func noStore(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Referrer-Policy", "no-referrer")
		h.ServeHTTP(w, r)
	})
}

// plainError answers a short text page. Nothing from the request is echoed (no reflected
// URLs, R-AUTH-150 "400 without redirect").
func plainError(w http.ResponseWriter, msg string, code int) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(code)
	_, _ = w.Write([]byte(msg + "\n"))
}

// httpFail answers 503 for unavailable storage and 500 with a logged reference otherwise.
func (id *Identity) httpFail(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		plainError(w, "storage unavailable, try again", http.StatusServiceUnavailable)
		return
	}
	ref := randomString(crockford, 12)
	id.log.Error("internal error", "ref", ref, "path", r.URL.Path, "err", err.Error())
	plainError(w, "internal error (ref "+ref+")", http.StatusInternalServerError)
}

// validRedirect accepts only http://127.0.0.1[:port]/callback and http://[::1][:port]/callback
// (RFC 8252 loopback), any port.
func validRedirect(raw string) bool {
	if len(raw) > 200 {
		return false
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "http" || u.User != nil || u.Path != "/callback" || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" {
		return false
	}
	h := u.Hostname()
	if h != "127.0.0.1" && h != "::1" {
		return false
	}
	if h == "::1" && !strings.HasPrefix(u.Host, "[::1]") {
		return false
	}
	return u.Host == h || u.Host == "[::1]" || strings.HasPrefix(u.Host, h+":") || strings.HasPrefix(u.Host, "[::1]:")
}

// authRequest is the validated query of /oauth/authorize (repeated on /oauth/consent).
type authRequest struct {
	redirect, challenge, state string
}

// parseAuthorize validates an authorization request. bad != "" with redirect == false means
// "answer 400 and never redirect"; with redirect == true the error goes to the client's
// redirect URI (RFC 6749 §4.1.2.1).
func parseAuthorize(q url.Values) (req authRequest, bad string, redirect bool) {
	if q.Get("client_id") != clientID {
		return req, "unknown client_id", false
	}
	req.redirect = q.Get("redirect_uri")
	if !validRedirect(req.redirect) {
		return req, "invalid redirect_uri: only http://127.0.0.1:<port>/callback and http://[::1]:<port>/callback are allowed", false
	}
	req.state, req.challenge = q.Get("state"), q.Get("code_challenge")
	switch {
	case len(req.state) > 512:
		return req, "invalid_request", true
	case q.Get("response_type") != "code":
		return req, "unsupported_response_type", true
	case q.Get("code_challenge_method") != "S256" || len(req.challenge) != 43:
		return req, "invalid_request", true
	}
	return req, "", false
}

func redirectWith(w http.ResponseWriter, r *http.Request, target string, params url.Values) {
	u, _ := url.Parse(target)
	q := u.Query()
	for k, v := range params {
		q[k] = v
	}
	u.RawQuery = q.Encode()
	http.Redirect(w, r, u.String(), http.StatusFound)
}

// browserUser returns the user of the request's bob_session cookie, or "".
func (id *Identity) browserUser(r *http.Request) (string, error) {
	ck, err := r.Cookie(CookieName)
	if err != nil || ck.Value == "" || !strings.HasPrefix(ck.Value, PrefixSession) {
		return "", nil
	}
	p, _, err := id.sessionPrincipal(r.Context(), Secret(ck.Value), "browser")
	if err != nil {
		if errors.Is(err, errInvalidToken) || isUnauthenticated(err) {
			return "", nil
		}
		return "", err
	}
	return strings.TrimPrefix(p.ID, "user:"), nil
}

func isUnauthenticated(err error) bool {
	var ce *connect.Error
	return errors.As(err, &ce) && ce.Code() == connect.CodeUnauthenticated
}

func toLogin(w http.ResponseWriter, r *http.Request, next string) {
	http.Redirect(w, r, "/login?"+url.Values{"next": {next}}.Encode(), http.StatusFound)
}

// sameOrigin refuses a cross-site form POST: a present Origin must be this server. (The
// SameSite=Strict cookie already keeps cross-site POSTs anonymous; this is the second line.)
func (id *Identity) sameOrigin(r *http.Request) bool {
	o := r.Header.Get("Origin")
	return o == "" || strings.EqualFold(o, id.baseURL(r))
}

// authorize: GET /oauth/authorize. A browser without a session goes to /login first; with
// one, to the consent page /oauth/consent (rendered by the UI), which posts the decision back.
func (id *Identity) authorize(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	req, bad, redirect := parseAuthorize(q)
	if bad != "" && !redirect {
		plainError(w, bad, http.StatusBadRequest)
		return
	}
	if bad != "" {
		redirectWith(w, r, req.redirect, url.Values{"error": {bad}, "state": {req.state}})
		return
	}
	uid, err := id.browserUser(r)
	if err != nil {
		id.httpFail(w, r, err)
		return
	}
	if uid == "" {
		toLogin(w, r, r.URL.RequestURI())
		return
	}
	http.Redirect(w, r, "/oauth/consent?"+q.Encode(), http.StatusFound)
}

func decisionAllow(v string) (allow, ok bool) {
	switch v {
	case "allow", "approve":
		return true, true
	case "deny":
		return false, true
	}
	return false, false
}

// formOf parses a bounded urlencoded form; the query fills fields the body does not have.
func formOf(w http.ResponseWriter, r *http.Request) (url.Values, bool) {
	r.Body = http.MaxBytesReader(w, r.Body, maxForm)
	if err := r.ParseForm(); err != nil {
		plainError(w, "invalid form", http.StatusBadRequest)
		return nil, false
	}
	return r.Form, true
}

// consent: POST /oauth/consent with the authorize parameters and decision=allow|deny.
func (id *Identity) consent(w http.ResponseWriter, r *http.Request) {
	form, ok := formOf(w, r)
	if !ok {
		return
	}
	if !id.sameOrigin(r) {
		plainError(w, "cross-site request refused", http.StatusForbidden)
		return
	}
	req, bad, redirect := parseAuthorize(form)
	if bad != "" && !redirect {
		plainError(w, bad, http.StatusBadRequest)
		return
	}
	if bad != "" {
		redirectWith(w, r, req.redirect, url.Values{"error": {bad}, "state": {req.state}})
		return
	}
	uid, err := id.browserUser(r)
	if err != nil {
		id.httpFail(w, r, err)
		return
	}
	if uid == "" {
		q := url.Values{}
		for _, k := range []string{"response_type", "client_id", "redirect_uri", "code_challenge", "code_challenge_method", "state"} {
			q.Set(k, form.Get(k))
		}
		toLogin(w, r, "/oauth/authorize?"+q.Encode())
		return
	}
	allow, known := decisionAllow(form.Get("decision"))
	if !known {
		plainError(w, "decision must be allow or deny", http.StatusBadRequest)
		return
	}
	if !allow {
		redirectWith(w, r, req.redirect, url.Values{"error": {"access_denied"}, "state": {req.state}})
		return
	}
	code := newToken("")
	rec := store.OAuthCodeKey(code.reveal())
	rec.User, rec.Client, rec.Redirect, rec.Challenge = uid, clientID, req.redirect, req.challenge
	rec.UserAgent, rec.Expires = truncate(r.UserAgent(), 200), id.now().Add(codeTTL)
	if err := store.Create(r.Context(), id.control, rec); err != nil {
		id.httpFail(w, r, err)
		return
	}
	redirectWith(w, r, req.redirect, url.Values{"code": {code.reveal()}, "state": {req.state}})
}

// oauthError answers an RFC 6749 §5.2 error.
func oauthError(w http.ResponseWriter, code int, kind, desc string) {
	writeJSON(w, code, map[string]any{"error": kind, "error_description": desc})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

// token: POST /oauth/token.
func (id *Identity) token(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxForm)
	if err := r.ParseForm(); err != nil {
		oauthError(w, http.StatusBadRequest, "invalid_request", "invalid form")
		return
	}
	f := r.PostForm
	if f.Get("client_id") != clientID {
		oauthError(w, http.StatusUnauthorized, "invalid_client", "unknown client")
		return
	}
	switch f.Get("grant_type") {
	case grantCode:
		id.tokenFromCode(w, r, f)
	case grantRefresh:
		id.tokenFromRefresh(w, r, f)
	case grantDevice:
		id.tokenFromDevice(w, r, f)
	default:
		oauthError(w, http.StatusBadRequest, "unsupported_grant_type", "grant types: authorization_code, refresh_token, device_code")
	}
}

func pkceOK(verifier, challenge string) bool {
	if len(verifier) < 43 || len(verifier) > 128 {
		return false
	}
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:]) == challenge
}

func (id *Identity) tokenFromCode(w http.ResponseWriter, r *http.Request, f url.Values) {
	ctx := r.Context()
	code := f.Get("code")
	invalid := func() { oauthError(w, http.StatusBadRequest, "invalid_grant", "invalid, expired or used authorization code") }
	if code == "" || len(code) > 200 {
		invalid()
		return
	}
	rec, err := store.Get(ctx, id.control, store.OAuthCodeKey(code))
	if errors.Is(err, store.ErrNotFound) {
		invalid()
		return
	} else if err != nil {
		id.httpFail(w, r, err)
		return
	}
	// Single use: the first exchange attempt, right or wrong, consumes the code.
	if err := store.Delete(ctx, id.control, rec); err != nil {
		invalid()
		return
	}
	if !id.now().Before(rec.Expires) || rec.Client != clientID || rec.Redirect != f.Get("redirect_uri") || !pkceOK(f.Get("code_verifier"), rec.Challenge) {
		invalid()
		return
	}
	id.issueCLI(w, r, rec.User, rec.UserAgent)
}

// issueCLI starts a CLI session for uid and answers the token response of §6.3.2.
func (id *Identity) issueCLI(w http.ResponseWriter, r *http.Request, uid, userAgent string) {
	ctx := r.Context()
	if userAgent == "" {
		userAgent = r.UserAgent()
	}
	iss, err := id.createSession(ctx, uid, "cli", userAgent)
	if errors.Is(err, errInvalidToken) {
		oauthError(w, http.StatusBadRequest, "invalid_grant", "the user no longer exists or is disabled")
		return
	} else if err != nil {
		id.httpFail(w, r, err)
		return
	}
	at, err := id.issueAccess(ctx, iss.rec)
	if err != nil {
		id.httpFail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"access_token": at.reveal(), "token_type": "Bearer", "expires_in": int(accessTTL.Seconds()),
		"refresh_token": iss.session.reveal(), "bob_download_token": iss.download.reveal(), "bob_user": uid,
	})
}

// tokenFromRefresh issues a new access token; the refresh token is not rotated (no CAS race
// between orchestrators) and the session's idle timer is extended.
func (id *Identity) tokenFromRefresh(w http.ResponseWriter, r *http.Request, f url.Values) {
	rt := Secret(f.Get("refresh_token"))
	if !rt.hasPrefix(PrefixRefresh) {
		oauthError(w, http.StatusBadRequest, "invalid_grant", "invalid refresh token")
		return
	}
	p, s, err := id.sessionPrincipal(r.Context(), rt, "cli")
	if err != nil {
		if errors.Is(err, errInvalidToken) || isUnauthenticated(err) {
			oauthError(w, http.StatusBadRequest, "invalid_grant", "invalid, expired or revoked refresh token")
			return
		}
		id.httpFail(w, r, err)
		return
	}
	at, err := id.issueAccess(r.Context(), s)
	if errors.Is(err, errInvalidToken) {
		oauthError(w, http.StatusBadRequest, "invalid_grant", "invalid, expired or revoked refresh token")
		return
	} else if err != nil {
		id.httpFail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"access_token": at.reveal(), "token_type": "Bearer", "expires_in": int(accessTTL.Seconds()),
		"bob_user": strings.TrimPrefix(p.ID, "user:"),
	})
}

// newUserCode returns "XXXX-XXXX" from the 20 consonants of §6.3.2.
func newUserCode() string {
	c := randomString(userCodeAlphabet, 8)
	return c[:4] + "-" + c[4:]
}

// normalizeUserCode accepts lower case and a missing dash.
func normalizeUserCode(s string) string {
	s = strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(s), " ", ""))
	if len(s) == 8 && !strings.Contains(s, "-") {
		s = s[:4] + "-" + s[4:]
	}
	if len(s) != 9 || s[4] != '-' {
		return ""
	}
	for i, c := range s {
		if i != 4 && !strings.ContainsRune(userCodeAlphabet, c) {
			return ""
		}
	}
	return s
}

// deviceCode: POST /oauth/device/code (RFC 8628 §3.1).
func (id *Identity) deviceCode(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxForm)
	if err := r.ParseForm(); err != nil {
		oauthError(w, http.StatusBadRequest, "invalid_request", "invalid form")
		return
	}
	if r.PostForm.Get("client_id") != clientID {
		oauthError(w, http.StatusUnauthorized, "invalid_client", "unknown client")
		return
	}
	ctx := r.Context()
	now := id.now()
	dev := newToken("")
	rec := store.DeviceCodeKey(dev.reveal())
	rec.State, rec.Client, rec.Interval = "pending", clientID, deviceInterval
	rec.UserAgent, rec.Expires = truncate(r.UserAgent(), 200), now.Add(deviceTTL)
	var userCode string
	for range 5 {
		userCode = newUserCode()
		u := store.DeviceUserKey(userCode)
		u.Hash, u.Expires = rec.Hash, rec.Expires
		err := store.Create(ctx, id.control, u)
		if errors.Is(err, store.ErrExists) {
			// A user code still in use (or left behind): take it over only when expired.
			old, gerr := store.Get(ctx, id.control, u)
			if gerr == nil && !now.Before(old.Expires) {
				if store.Delete(ctx, id.control, old) == nil && store.Create(ctx, id.control, u) == nil {
					err = nil
				}
			}
		}
		if err == nil {
			break
		}
		if !errors.Is(err, store.ErrExists) {
			id.httpFail(w, r, err)
			return
		}
		userCode = ""
	}
	if userCode == "" {
		id.httpFail(w, r, errors.New("no free device user code"))
		return
	}
	rec.UserCode = userCode
	if err := store.Create(ctx, id.control, rec); err != nil {
		id.httpFail(w, r, err)
		return
	}
	base := id.baseURL(r)
	writeJSON(w, http.StatusOK, map[string]any{
		"device_code": dev.reveal(), "user_code": userCode,
		"verification_uri":          base + "/device",
		"verification_uri_complete": base + "/device?" + url.Values{"user_code": {userCode}}.Encode(),
		"expires_in":                int(deviceTTL.Seconds()), "interval": deviceInterval,
	})
}

// tokenFromDevice is the device-flow poll (RFC 8628 §3.4/§3.5).
func (id *Identity) tokenFromDevice(w http.ResponseWriter, r *http.Request, f url.Values) {
	ctx := r.Context()
	dev := f.Get("device_code")
	if dev == "" || len(dev) > 200 {
		oauthError(w, http.StatusBadRequest, "invalid_grant", "unknown device code")
		return
	}
	now := id.now()
	var answer string // "" = issue tokens
	var uid, ua string
	_, err := store.CAS(ctx, id.control, store.DeviceCodeKey(dev), func(cur *store.DeviceCodeRecord) (*store.DeviceCodeRecord, error) {
		uid, ua = "", ""
		switch {
		case cur == nil || cur.Client != clientID || cur.State == "used":
			answer = "invalid_grant"
			return nil, store.ErrNoChange
		case !now.Before(cur.Expires):
			answer = "expired_token"
			return nil, store.ErrNoChange
		}
		interval := time.Duration(max(cur.Interval, deviceInterval)) * time.Second
		tooFast := !cur.LastPoll.IsZero() && now.Sub(cur.LastPoll) < interval
		cur.LastPoll = now
		switch {
		case tooFast:
			cur.Interval = max(cur.Interval, deviceInterval) + slowDownStep
			answer = "slow_down"
		case cur.State == "pending":
			answer = "authorization_pending"
		case cur.State == "denied":
			answer = "access_denied"
		case cur.State == "approved":
			answer, uid, ua = "", cur.User, cur.UserAgent
			cur.State = "used"
		default:
			answer = "invalid_grant"
		}
		return cur, nil
	})
	if err != nil {
		id.httpFail(w, r, err)
		return
	}
	if answer != "" {
		oauthError(w, http.StatusBadRequest, answer, deviceErrorText[answer])
		return
	}
	id.issueCLI(w, r, uid, ua)
}

var deviceErrorText = map[string]string{
	"invalid_grant":         "unknown or already used device code",
	"expired_token":         "the device code expired, start the login again",
	"slow_down":             "polling too fast",
	"authorization_pending": "waiting for the user to approve the device",
	"access_denied":         "the user denied the device",
}

// deviceApprove: POST /device with user_code and decision=allow|deny, from a logged-in browser.
func (id *Identity) deviceApprove(w http.ResponseWriter, r *http.Request) {
	form, ok := formOf(w, r)
	if !ok {
		return
	}
	if !id.sameOrigin(r) {
		plainError(w, "cross-site request refused", http.StatusForbidden)
		return
	}
	uid, err := id.browserUser(r)
	if err != nil {
		id.httpFail(w, r, err)
		return
	}
	if uid == "" {
		toLogin(w, r, "/device")
		return
	}
	allow, known := decisionAllow(form.Get("decision"))
	if !known {
		plainError(w, "decision must be allow or deny", http.StatusBadRequest)
		return
	}
	code := normalizeUserCode(form.Get("user_code"))
	if code == "" {
		plainError(w, "unknown or expired code", http.StatusBadRequest)
		return
	}
	ctx := r.Context()
	now := id.now()
	du, err := store.Get(ctx, id.control, store.DeviceUserKey(code))
	if errors.Is(err, store.ErrNotFound) || err == nil && !now.Before(du.Expires) {
		plainError(w, "unknown or expired code", http.StatusBadRequest)
		return
	} else if err != nil {
		id.httpFail(w, r, err)
		return
	}
	var msg string
	_, err = store.CAS(ctx, id.control, &store.DeviceCodeRecord{Hash: du.Hash}, func(cur *store.DeviceCodeRecord) (*store.DeviceCodeRecord, error) {
		switch {
		case cur == nil || !now.Before(cur.Expires) || cur.UserCode != code:
			msg = "unknown or expired code"
			return nil, store.ErrNoChange
		case cur.State != "pending":
			msg = "this code was already answered"
			return nil, store.ErrNoChange
		}
		msg = ""
		cur.State, cur.User = "denied", uid
		if allow {
			cur.State = "approved"
		}
		return cur, nil
	})
	if err != nil {
		id.httpFail(w, r, err)
		return
	}
	if msg != "" {
		plainError(w, msg, http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	if allow {
		_, _ = w.Write([]byte("Device approved. You can return to the terminal.\n"))
	} else {
		_, _ = w.Write([]byte("Device denied.\n"))
	}
}

// revoke: POST /oauth/revoke (RFC 7009). A refresh token ends its whole session; an access or
// download token is deleted. Unknown tokens are answered 200 like known ones.
func (id *Identity) revoke(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxForm)
	if err := r.ParseForm(); err != nil {
		oauthError(w, http.StatusBadRequest, "invalid_request", "invalid form")
		return
	}
	if r.PostForm.Get("client_id") != clientID {
		oauthError(w, http.StatusUnauthorized, "invalid_client", "unknown client")
		return
	}
	ctx := r.Context()
	tok := Secret(r.PostForm.Get("token"))
	switch {
	case tok.hasPrefix(PrefixRefresh):
		key := store.SessionKey(tok.reveal())
		s, err := store.Get(ctx, id.control, key)
		if err == nil && s.Kind == "cli" {
			if _, err := id.revokeSessions(ctx, s.User, func(ref store.SessionRef) bool { return ref.Hash != key.Hash }); err != nil {
				id.httpFail(w, r, err)
				return
			}
			id.deleteSession(ctx, key.Hash)
		} else if err != nil && !errors.Is(err, store.ErrNotFound) {
			id.httpFail(w, r, err)
			return
		}
	case tok.hasPrefix(PrefixAccess) || tok.hasPrefix(PrefixDownload):
		key := store.TokenKey(tok.reveal())
		if rec, err := store.Get(ctx, id.control, key); err == nil && rec.Kind != "sa" {
			id.deleteToken(ctx, key.Hash)
		} else if err != nil && !errors.Is(err, store.ErrNotFound) {
			id.httpFail(w, r, err)
			return
		}
	}
	w.WriteHeader(http.StatusOK)
}

// Fallback pages: when bob-server runs without the embedded UI, GET /oauth/consent and GET
// /device still need a page that posts the decision. The UI's own pages replace them.

var consentPage = template.Must(template.New("consent").Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><title>Allow the bob CLI</title></head>
<body><main><h1>Allow the bob CLI</h1><p>Allow the bob CLI to act as you on this server?</p>
<form method="post" action="/oauth/consent">{{range $k, $v := .}}<input type="hidden" name="{{$k}}" value="{{$v}}">{{end}}
<button name="decision" value="allow">Allow</button> <button name="decision" value="deny">Deny</button></form></main></body></html>
`))

var devicePage = template.Must(template.New("device").Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><title>Connect a device</title></head>
<body><main><h1>Connect a device</h1><form method="post" action="/device">
<label>Code <input name="user_code" value="{{.}}" autocomplete="off"></label>
<button name="decision" value="allow">Allow</button> <button name="decision" value="deny">Deny</button></form></main></body></html>
`))

// FallbackPages serves GET /oauth/consent and GET /device for a server without the UI.
func (id *Identity) FallbackPages() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /oauth/consent", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if _, bad, redirect := parseAuthorize(q); bad != "" && !redirect {
			plainError(w, bad, http.StatusBadRequest)
			return
		}
		if uid, err := id.browserUser(r); err != nil || uid == "" {
			toLogin(w, r, "/oauth/authorize?"+q.Encode())
			return
		}
		fields := map[string]string{}
		for _, k := range []string{"response_type", "client_id", "redirect_uri", "code_challenge", "code_challenge_method", "state"} {
			fields[k] = q.Get(k)
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_ = consentPage.Execute(w, fields)
	})
	mux.HandleFunc("GET /device", func(w http.ResponseWriter, r *http.Request) {
		if uid, err := id.browserUser(r); err != nil || uid == "" {
			toLogin(w, r, "/device")
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_ = devicePage.Execute(w, normalizeUserCode(r.URL.Query().Get("user_code")))
	})
	return noStore(mux)
}

// FallbackPatterns are the patterns of FallbackPages.
func FallbackPatterns() []string { return []string{"GET /oauth/consent", "GET /device"} }

// Services are the identity's API services, to set in api.Services.
func (id *Identity) Services(s *api.Services) {
	s.Auth = authService{id}
	s.User = userService{id}
	s.Role = roleService{id}
	s.ServiceAccount = saService{id}
	s.Oidc = oidcService{id}
}
