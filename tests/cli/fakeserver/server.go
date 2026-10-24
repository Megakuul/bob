package fakeserver

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

// Request is one logged request.
type Request struct {
	Method, Path, Query string
	Authorization       string
	Form                url.Values
	Body                string // request body of a POST, up to 64 KiB
}

// Token is an access token the server accepts.
type Token struct {
	User    string
	Scope   string // api | download | session
	Expires time.Time
}

// Options configure New.
type Options struct {
	Name         string        // server name (default "fake")
	LoginTTL     time.Duration // lifetime of an access token from a login (default 1 h)
	RefreshedTTL time.Duration // lifetime of an access token from a refresh (default 1 h)
}

// Server is a running fake bob server.
type Server struct {
	srv  *httptest.Server
	opts Options
	mu   sync.Mutex

	reqs    []Request
	tokens  map[string]Token
	refresh map[string]string // refresh token → user
	seq     int

	// Refresh behaviour.
	refreshStatus int // 0: serve; otherwise the HTTP status of POST /oauth/token grant_type=refresh_token
	refreshCalls  int

	// Authorization-code state.
	codes map[string]pkce // code → challenge
	// Device flow state.
	devices map[string]*device // device_code → state

	// Registry state.
	pub       ed25519.PublicKey
	priv      ed25519.PrivateKey
	keyName   string
	releases  []*Release
	store     map[string]*storeEntry // narinfo hash part → entry
	whoamiHit int
	keyOffset int
}

type pkce struct{ challenge, method, redirect string }

type device struct {
	userCode string
	approved bool
	user     string
}

// New starts a fake server; it stops with the test.
func New(t testing.TB, o Options) *Server {
	t.Helper()
	if o.Name == "" {
		o.Name = "fake"
	}
	if o.LoginTTL == 0 {
		o.LoginTTL = time.Hour
	}
	if o.RefreshedTTL == 0 {
		o.RefreshedTTL = time.Hour
	}
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{opts: o, tokens: map[string]Token{}, refresh: map[string]string{}, codes: map[string]pkce{},
		devices: map[string]*device{}, pub: pub, priv: priv, keyName: o.Name + "-1", store: map[string]*storeEntry{}}
	s.srv = httptest.NewServer(http.HandlerFunc(s.handle))
	t.Cleanup(s.srv.Close)
	return s
}

// URL is the base URL (http://127.0.0.1:port).
func (s *Server) URL() string { return s.srv.URL }

// PublicKey is the cache signing key in Nix form ("name:base64").
func (s *Server) PublicKey() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.pubKeyLocked()
}

func (s *Server) pubKeyLocked() string {
	return s.keyName + ":" + base64.StdEncoding.EncodeToString(s.pub)
}

// RotateKey replaces the signing key (a later key change, R-INST-004). Paths served afterwards
// are signed by the new key; GetServerInfo announces it.
func (s *Server) RotateKey(t testing.TB) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.keyOffset++
	s.pub, s.priv = pub, priv
	s.keyName = fmt.Sprintf("%s-%d", s.opts.Name, s.keyOffset+1)
}

// AddToken registers an access token the server accepts (a service-account token, for example).
func (s *Server) AddToken(token, user, scope string, expires time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tokens[token] = Token{User: user, Scope: scope, Expires: expires}
}

// FailRefresh makes POST /oauth/token with grant_type=refresh_token answer status (0 = serve).
func (s *Server) FailRefresh(status int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.refreshStatus = status
}

// RefreshCalls is the number of refresh-token grants received.
func (s *Server) RefreshCalls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.refreshCalls
}

// WhoAmICalls is the number of WhoAmI requests received.
func (s *Server) WhoAmICalls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.whoamiHit
}

// Requests returns a copy of the request log.
func (s *Server) Requests() []Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Request{}, s.reqs...)
}

// Count returns the number of logged requests whose path has the prefix.
func (s *Server) Count(prefix string) int {
	n := 0
	for _, r := range s.Requests() {
		if strings.HasPrefix(r.Path, prefix) {
			n++
		}
	}
	return n
}

// DeviceCodes returns the user codes of the device flows started so far.
func (s *Server) DeviceCodes() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for _, d := range s.devices {
		out = append(out, d.userCode)
	}
	return out
}

// ApproveDevice approves a device code the way the /device page does for a logged-in user.
func (s *Server) ApproveDevice(userCode, user string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, d := range s.devices {
		if d.userCode == userCode {
			d.approved, d.user = true, user
			return true
		}
	}
	return false
}

// AuthorizeRequests are the logged GET /oauth/authorize requests, TokenRequests the logged
// POST /oauth/token forms (authorization_code grants).
func (s *Server) TokenForms(grant string) []url.Values {
	var out []url.Values
	for _, r := range s.Requests() {
		if r.Path == "/oauth/token" && r.Form.Get("grant_type") == grant {
			out = append(out, r.Form)
		}
	}
	return out
}

func (s *Server) newToken(prefix string) string {
	s.seq++
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return fmt.Sprintf("%s%d_%x", prefix, s.seq, b)
}

// authenticate returns the token behind the request's Authorization header: ok=false and
// present=true for an invalid or expired token.
func (s *Server) authenticate(r *http.Request) (tok Token, present, ok bool) {
	h := r.Header.Get("Authorization")
	if h == "" {
		return Token{}, false, false
	}
	raw := strings.TrimPrefix(h, "Bearer ")
	t, found := s.tokens[raw]
	if !found || (!t.Expires.IsZero() && time.Now().After(t.Expires)) {
		return Token{}, true, false
	}
	return t, true, true
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func connectError(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, map[string]string{"code": code, "message": msg})
}

func (s *Server) handle(w http.ResponseWriter, r *http.Request) {
	var body []byte
	if r.Body != nil && strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		body, _ = io.ReadAll(io.LimitReader(r.Body, 64<<10))
		r.Body = io.NopCloser(bytes.NewReader(body))
	}
	_ = r.ParseForm()
	s.mu.Lock()
	s.reqs = append(s.reqs, Request{r.Method, r.URL.Path, r.URL.RawQuery, r.Header.Get("Authorization"), r.Form, string(body)})
	s.mu.Unlock()
	switch {
	case r.URL.Path == "/oauth/authorize":
		s.authorize(w, r)
	case r.URL.Path == "/oauth/token":
		s.token(w, r)
	case r.URL.Path == "/oauth/device/code":
		s.deviceCode(w, r)
	case r.URL.Path == "/oauth/revoke":
		s.mu.Lock()
		delete(s.tokens, strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
		s.mu.Unlock()
		writeJSON(w, 200, map[string]any{})
	case r.URL.Path == "/api/bob.auth.v1.AuthService/GetServerInfo":
		s.serverInfo(w)
	case r.URL.Path == "/api/bob.auth.v1.AuthService/WhoAmI":
		s.whoami(w, r)
	case r.URL.Path == "/api/bob.release.v1.ReleaseService/ResolveRelease":
		s.resolve(w, r)
	case strings.HasPrefix(r.URL.Path, "/nix/"):
		s.cache(w, r)
	default:
		http.NotFound(w, r)
	}
}

func (s *Server) nixInfoLocked() map[string]any {
	return map[string]any{"substituter": s.srv.URL + "/nix", "publicKeys": []string{s.pubKeyLocked()}}
}

func (s *Server) serverInfo(w http.ResponseWriter) {
	s.mu.Lock()
	defer s.mu.Unlock()
	writeJSON(w, 200, map[string]any{"serverName": s.opts.Name, "baseUrl": s.srv.URL, "version": "0.0.0-fake", "nix": s.nixInfoLocked()})
}

func (s *Server) whoami(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.whoamiHit++
	tok, present, ok := s.authenticate(r)
	if !present || !ok {
		connectError(w, 401, "unauthenticated", "missing or invalid credential")
		return
	}
	writeJSON(w, 200, map[string]any{
		"principal": map[string]string{"id": "user:" + tok.User, "displayName": tok.User}, "login": tok.User,
		"tokenScope": tok.Scope, "tokenExpiresAt": tok.Expires.UTC().Format(time.RFC3339), "nix": s.nixInfoLocked(),
	})
}

func (s *Server) authorize(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	redirect := q.Get("redirect_uri")
	u, err := url.Parse(redirect)
	if err != nil || (u.Hostname() != "127.0.0.1" && u.Hostname() != "::1") || u.Path != "/callback" || q.Get("client_id") != "bob-cli" || q.Get("response_type") != "code" {
		http.Error(w, "invalid authorize request", 400)
		return
	}
	s.mu.Lock()
	code := s.newToken("code_")
	s.codes[code] = pkce{q.Get("code_challenge"), q.Get("code_challenge_method"), redirect}
	s.mu.Unlock()
	loc := url.Values{"code": {code}, "state": {q.Get("state")}}
	http.Redirect(w, r, redirect+"?"+loc.Encode(), http.StatusFound)
}

func (s *Server) deviceCode(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	dc := s.newToken("dev_")
	user := fmt.Sprintf("%s-%04d", "WDJB", 1000+len(s.devices))
	s.devices[dc] = &device{userCode: user}
	writeJSON(w, 200, map[string]any{"device_code": dc, "user_code": user, "verification_uri": s.srv.URL + "/device", "expires_in": 600, "interval": 1})
}

func oauthError(w http.ResponseWriter, status int, code string) {
	writeJSON(w, status, map[string]string{"error": code})
}

func (s *Server) issueLocked(user string, ttl time.Duration, withRefresh bool) map[string]any {
	at := s.newToken("bob_at_")
	s.tokens[at] = Token{User: user, Scope: "api", Expires: time.Now().Add(ttl)}
	dt := s.newToken("bob_dt_")
	s.tokens[dt] = Token{User: user, Scope: "download", Expires: time.Now().Add(24 * time.Hour)}
	out := map[string]any{"access_token": at, "token_type": "Bearer", "expires_in": int(ttl.Seconds()), "bob_download_token": dt, "bob_user": user}
	if withRefresh {
		rt := s.newToken("bob_rt_")
		s.refresh[rt] = user
		out["refresh_token"] = rt
	}
	return out
}

func (s *Server) token(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch r.Form.Get("grant_type") {
	case "authorization_code":
		c, ok := s.codes[r.Form.Get("code")]
		if !ok || c.method != "S256" || r.Form.Get("client_id") != "bob-cli" || r.Form.Get("redirect_uri") != c.redirect {
			oauthError(w, 400, "invalid_grant")
			return
		}
		delete(s.codes, r.Form.Get("code")) // single use
		sum := sha256.Sum256([]byte(r.Form.Get("code_verifier")))
		if base64.RawURLEncoding.EncodeToString(sum[:]) != c.challenge {
			oauthError(w, 400, "invalid_grant")
			return
		}
		writeJSON(w, 200, s.issueLocked("alice", s.opts.LoginTTL, true))
	case "urn:ietf:params:oauth:grant-type:device_code":
		d, ok := s.devices[r.Form.Get("device_code")]
		switch {
		case !ok:
			oauthError(w, 400, "invalid_grant")
		case !d.approved:
			oauthError(w, 400, "authorization_pending")
		default:
			delete(s.devices, r.Form.Get("device_code"))
			writeJSON(w, 200, s.issueLocked(d.user, s.opts.LoginTTL, true))
		}
	case "refresh_token":
		s.refreshCalls++
		user, ok := s.refresh[r.Form.Get("refresh_token")]
		if s.refreshStatus != 0 || !ok {
			status := s.refreshStatus
			if status == 0 {
				status = 401
			}
			oauthError(w, status, "invalid_grant")
			return
		}
		out := s.issueLocked(user, s.opts.RefreshedTTL, false) // the refresh token is not rotated
		writeJSON(w, 200, out)
	default:
		oauthError(w, 400, "unsupported_grant_type")
	}
}
