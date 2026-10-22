package authenv

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

// Tokens is the answer of /oauth/token (SPEC §6.3.2).
type Tokens struct {
	Access, Refresh, Download, User string
	ExpiresIn                       int
	Raw                             map[string]any
}

// Form is the answer of an OAuth endpoint.
type Form struct {
	Status int
	Header http.Header
	Body   map[string]any
	Raw    string
}

// Error returns the OAuth error code ("invalid_grant"…), or "".
func (f *Form) Error() string { s, _ := f.Body["error"].(string); return s }

// PostForm posts a form to path (no credentials: the OAuth endpoints are public-client).
func (s *Server) PostForm(t testing.TB, path string, form url.Values, hdr map[string]string) *Form {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.URL+path, strings.NewReader(form.Encode()))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := HTTPClient().Do(req)
	if err != nil {
		t.Fatalf("harness error: POST %s: %v", path, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	f := &Form{Status: resp.StatusCode, Header: resp.Header, Raw: string(b)}
	_ = json.Unmarshal(b, &f.Body)
	return f
}

// PKCE returns a verifier and its S256 challenge (RFC 7636).
func PKCE() (verifier, challenge string) {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	verifier = base64.RawURLEncoding.EncodeToString(b)
	sum := sha256.Sum256([]byte(verifier))
	return verifier, base64.RawURLEncoding.EncodeToString(sum[:])
}

// Redirect is the loopback redirect URI the CLI uses; nothing listens there, the harness reads
// the Location header instead of following it.
const Redirect = "http://127.0.0.1:38917/callback"

// AuthorizeQuery is the query of GET /oauth/authorize for client bob-cli.
func AuthorizeQuery(redirect, challenge, state string) url.Values {
	return url.Values{
		"response_type": {"code"}, "client_id": {"bob-cli"}, "redirect_uri": {redirect},
		"code_challenge": {challenge}, "code_challenge_method": {"S256"}, "state": {state},
	}
}

// Authorize runs the browser side of the CLI login for a logged-in session: GET /oauth/authorize,
// the consent page (the harness allows) and returns the loopback Location (redirect_uri?code&state)
// as a parsed URL. The consent step follows SPEC §6.3.2: the server redirects an authenticated
// browser to /oauth/consent, which the user answers with an allow decision (a POST of the form
// field decision=allow together with the authorize parameters).
func (s *Server) Authorize(t testing.TB, session *Client, q url.Values) (*url.URL, *Result) {
	t.Helper()
	b := NewBrowser()
	b.Header.Set("User-Agent", "bob-harness-cli/1 (host cli-box)")
	if session != nil && session.Cookie != "" {
		b.cookies[s.Host()] = map[string]*http.Cookie{"bob_session": {Name: "bob_session", Value: session.Cookie}}
	}
	loopback := func(loc string) bool {
		return strings.HasPrefix(loc, "http://127.0.0.1:38917/") || !strings.HasPrefix(loc, s.URL)
	}
	res := b.Do(t, http.MethodGet, s.URL+"/oauth/authorize?"+q.Encode(), nil, loopback)
	if strings.Contains(res.Location, "/oauth/consent") || (res.Status == 200 && strings.Contains(res.URL, "/oauth/consent")) {
		form := url.Values{"decision": {"allow"}}
		for k, v := range q {
			form[k] = v
		}
		res = b.Do(t, http.MethodPost, s.URL+"/oauth/consent?"+q.Encode(), form, loopback)
	}
	if res.Location == "" {
		return nil, res
	}
	u, err := url.Parse(res.Location)
	if err != nil {
		t.Fatalf("authorize: bad Location %q", res.Location)
	}
	return u, res
}

// Exchange trades an authorization code for tokens (POST /oauth/token).
func (s *Server) Exchange(t testing.TB, code, verifier, redirect string) *Form {
	t.Helper()
	return s.PostForm(t, "/oauth/token", url.Values{
		"grant_type": {"authorization_code"}, "code": {code}, "code_verifier": {verifier},
		"redirect_uri": {redirect}, "client_id": {"bob-cli"},
	}, nil)
}

// TokensOf extracts the token fields of a successful /oauth/token answer.
func TokensOf(f *Form) Tokens {
	tk := Tokens{Raw: f.Body}
	tk.Access, _ = f.Body["access_token"].(string)
	tk.Refresh, _ = f.Body["refresh_token"].(string)
	tk.Download, _ = f.Body["bob_download_token"].(string)
	tk.User, _ = f.Body["bob_user"].(string)
	Secret(tk.Access)
	Secret(tk.Refresh)
	Secret(tk.Download)
	if n, ok := f.Body["expires_in"].(float64); ok {
		tk.ExpiresIn = int(n)
	}
	return tk
}

// CLILogin performs the full authorization-code + PKCE login of a local user and returns its tokens.
func (s *Server) CLILogin(t testing.TB, login, password string) Tokens {
	t.Helper()
	session := s.MustLogin(t, login, password)
	verifier, challenge := PKCE()
	loc, res := s.Authorize(t, session, AuthorizeQuery(Redirect, challenge, "st-"+login))
	if loc == nil || loc.Query().Get("code") == "" {
		t.Fatalf("authorize did not redirect with a code: status %d location %q body %.200q", res.Status, res.Location, res.Body)
	}
	f := s.Exchange(t, loc.Query().Get("code"), verifier, Redirect)
	if f.Status != 200 {
		t.Fatalf("token exchange: %d %s", f.Status, f.Raw)
	}
	return TokensOf(f)
}

// Refresh trades a refresh token for a new access token.
func (s *Server) Refresh(t testing.TB, refresh string) *Form {
	t.Helper()
	return s.PostForm(t, "/oauth/token", url.Values{
		"grant_type": {"refresh_token"}, "refresh_token": {refresh}, "client_id": {"bob-cli"},
	}, nil)
}

// ApproveDevice answers the user_code on /device for a logged-in session: the form field
// decision is allow or deny (SPEC §6.3.2 only says the user enters the code on /device).
func (s *Server) ApproveDevice(t testing.TB, session *Client, userCode string, allow bool) {
	t.Helper()
	b := NewBrowser()
	b.cookies[s.Host()] = map[string]*http.Cookie{"bob_session": {Name: "bob_session", Value: session.Cookie}}
	decision := "deny"
	if allow {
		decision = "allow"
	}
	res := b.Do(t, http.MethodPost, s.URL+"/device", url.Values{"user_code": {userCode}, "decision": {decision}}, nil)
	if res.Status != 200 {
		t.Fatalf("device approval (%s) answered %d: %.200q", decision, res.Status, res.Body)
	}
}
