package remote

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// ClientID is the OAuth client of the CLI (SPEC §6.3.2).
const ClientID = "bob-cli"

// Tokens is a successful answer of POST /oauth/token.
type Tokens struct {
	AccessToken   string `json:"access_token"`
	TokenType     string `json:"token_type"`
	ExpiresIn     int64  `json:"expires_in"`
	RefreshToken  string `json:"refresh_token"`
	DownloadToken string `json:"bob_download_token"`
	User          string `json:"bob_user"`
}

// OAuthError is an OAuth error answer ({"error": …}) or an unexpected status.
type OAuthError struct {
	Status      int
	Code        string // invalid_grant, authorization_pending, slow_down, expired_token, access_denied, …
	Description string
}

func (e *OAuthError) Error() string {
	if e.Description != "" {
		return fmt.Sprintf("%s (%s)", e.Code, e.Description)
	}
	if e.Code != "" {
		return e.Code
	}
	return fmt.Sprintf("HTTP %d", e.Status)
}

// form posts an OAuth form and decodes a JSON answer into out.
func (s *Server) form(ctx context.Context, path string, f url.Values, out any) error {
	hr, err := http.NewRequestWithContext(ctx, http.MethodPost, s.URL+path, strings.NewReader(f.Encode()))
	if err != nil {
		return &Error{Method: path, Transport: err}
	}
	hr.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	hr.Header.Set("Accept", "application/json")
	if s.Token != "" {
		hr.Header.Set("Authorization", "Bearer "+s.Token)
	}
	if s.UserAgent != "" {
		hr.Header.Set("User-Agent", s.UserAgent)
	}
	r, err := s.client(false).Do(hr)
	if err != nil {
		return &Error{Method: path, Transport: err}
	}
	defer r.Body.Close()
	b, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		return &Error{Method: path, Transport: err}
	}
	if r.StatusCode >= 500 {
		return &Error{Method: path, Status: r.StatusCode, Code: codeOf(r.StatusCode)}
	}
	if r.StatusCode != http.StatusOK {
		var oe struct {
			Error       string `json:"error"`
			Description string `json:"error_description"`
		}
		_ = json.Unmarshal(b, &oe)
		return &OAuthError{Status: r.StatusCode, Code: oe.Error, Description: oe.Description}
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(b, out); err != nil {
		return &Error{Method: path, Status: r.StatusCode, Code: "internal", Message: "invalid response: " + err.Error()}
	}
	return nil
}

// Refresh exchanges a refresh token for a new access token (the refresh token is not rotated).
func (s *Server) Refresh(ctx context.Context, refreshToken string) (*Tokens, error) {
	var t Tokens
	err := s.form(ctx, "/oauth/token", url.Values{"grant_type": {"refresh_token"}, "refresh_token": {refreshToken}, "client_id": {ClientID}}, &t)
	if err != nil {
		return nil, err
	}
	if t.AccessToken == "" {
		return nil, &OAuthError{Status: http.StatusOK, Code: "invalid_response", Description: "no access token"}
	}
	return &t, nil
}

// Revoke revokes token (RFC 7009); the bearer of s identifies the caller.
func (s *Server) Revoke(ctx context.Context, token string) error {
	return s.form(ctx, "/oauth/revoke", url.Values{"token": {token}, "client_id": {ClientID}}, nil)
}

// DeviceCode is the answer of POST /oauth/device/code (RFC 8628 §3.2).
type DeviceCode struct {
	DeviceCode              string `json:"device_code"`
	UserCode                string `json:"user_code"`
	VerificationURI         string `json:"verification_uri"`
	VerificationURIComplete string `json:"verification_uri_complete"`
	ExpiresIn               int64  `json:"expires_in"`
	Interval                int64  `json:"interval"`
}

// StartDevice starts the device flow.
func (s *Server) StartDevice(ctx context.Context) (*DeviceCode, error) {
	var d DeviceCode
	if err := s.form(ctx, "/oauth/device/code", url.Values{"client_id": {ClientID}}, &d); err != nil {
		return nil, err
	}
	if d.DeviceCode == "" || d.UserCode == "" || d.VerificationURI == "" {
		return nil, &OAuthError{Status: http.StatusOK, Code: "invalid_response", Description: "incomplete device code answer"}
	}
	return &d, nil
}

// PollDevice polls the token endpoint until the code is approved, denied or expired
// (authorization_pending → wait, slow_down → interval + 5 s).
func (s *Server) PollDevice(ctx context.Context, d *DeviceCode) (*Tokens, error) {
	interval := time.Duration(max(d.Interval, 1)) * time.Second
	deadline := time.Now().Add(time.Duration(max(d.ExpiresIn, 1)) * time.Second)
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(interval):
		}
		var t Tokens
		err := s.form(ctx, "/oauth/token", url.Values{"grant_type": {"urn:ietf:params:oauth:grant-type:device_code"},
			"device_code": {d.DeviceCode}, "client_id": {ClientID}}, &t)
		var oe *OAuthError
		switch {
		case err == nil:
			return &t, nil
		case errors.As(err, &oe) && oe.Code == "authorization_pending":
		case errors.As(err, &oe) && oe.Code == "slow_down":
			interval += 5 * time.Second
		default:
			return nil, err
		}
		if time.Now().After(deadline) {
			return nil, &OAuthError{Code: "expired_token", Description: "the code expired before it was approved"}
		}
	}
}

// PKCE runs the authorization code flow with PKCE and a loopback redirect (RFC 7636, RFC
// 8252): it listens on 127.0.0.1:<random>, calls open with the authorize URL (the browser),
// waits for the redirect to /callback and exchanges the code.
func (s *Server) PKCE(ctx context.Context, open func(authorizeURL string) error) (*Tokens, error) {
	verifier, err := randomString(48)
	if err != nil {
		return nil, err
	}
	state, err := randomString(24)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(sum[:])

	var lc net.ListenConfig
	ln, err := lc.Listen(ctx, "tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("login: loopback listener: %w", err)
	}
	redirect := "http://" + ln.Addr().String() + "/callback"
	type result struct {
		code string
		err  error
	}
	got := make(chan result, 1)
	srv := &http.Server{ReadHeaderTimeout: 10 * time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/callback" {
			http.NotFound(w, r)
			return
		}
		q := r.URL.Query()
		res := result{code: q.Get("code")}
		switch {
		case q.Get("state") != state:
			http.Error(w, "unexpected state", http.StatusBadRequest)
			return // a stray request: keep waiting for the real redirect
		case q.Get("error") != "":
			res.err = &OAuthError{Code: q.Get("error"), Description: q.Get("error_description")}
		case res.code == "":
			res.err = &OAuthError{Code: "invalid_response", Description: "the redirect carries no code"}
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		msg := "You are logged in to bob. You can close this window."
		if res.err != nil {
			msg = "bob login failed: " + res.err.Error()
		}
		_, _ = io.WriteString(w, "<!doctype html><title>bob login</title><p>"+html.EscapeString(msg)+"</p>\n")
		select {
		case got <- res:
		default:
		}
	})}
	served := make(chan error, 1)
	go func() {
		defer func() {
			if p := recover(); p != nil {
				served <- fmt.Errorf("login: callback server panicked: %v", p)
			}
		}()
		served <- srv.Serve(ln)
	}()
	defer func() {
		sctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(sctx)
	}()

	q := url.Values{"response_type": {"code"}, "client_id": {ClientID}, "redirect_uri": {redirect},
		"code_challenge": {challenge}, "code_challenge_method": {"S256"}, "state": {state}}
	if err := open(s.URL + "/oauth/authorize?" + q.Encode()); err != nil {
		return nil, err
	}
	var res result
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case err := <-served:
		return nil, fmt.Errorf("login: callback server: %w", err)
	case <-time.After(10 * time.Minute):
		return nil, &OAuthError{Code: "expired_token", Description: "no answer from the browser within 10 minutes"}
	case res = <-got:
	}
	if res.err != nil {
		return nil, res.err
	}
	var t Tokens
	err = s.form(ctx, "/oauth/token", url.Values{"grant_type": {"authorization_code"}, "code": {res.code},
		"code_verifier": {verifier}, "redirect_uri": {redirect}, "client_id": {ClientID}}, &t)
	if err != nil {
		return nil, err
	}
	return &t, nil
}

// randomString returns n random bytes in base64url (the PKCE verifier alphabet).
func randomString(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("login: random: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// NixInfo is the cache configuration a server announces (WhoAmI.nix, §6.6.5).
type NixInfo struct {
	Substituter string   `json:"substituter"`
	PublicKeys  []string `json:"publicKeys"`
}

// WhoAmI is the part of bob.auth.v1.WhoAmIResponse the CLI reads.
type WhoAmI struct {
	Principal struct {
		ID          string `json:"id"`
		DisplayName string `json:"displayName"`
	} `json:"principal"`
	Login          string   `json:"login"`
	Projects       []string `json:"projects"`
	TokenScope     string   `json:"tokenScope"`
	TokenExpiresAt string   `json:"tokenExpiresAt"`
	Nix            NixInfo  `json:"nix"`
}

// WhoAmIMethod is the credential pre-check of R-NIX-029.
const WhoAmIMethod = "bob.auth.v1.AuthService/WhoAmI"

// WhoAmI calls AuthService/WhoAmI with a Connect GET.
func (s *Server) WhoAmI(ctx context.Context) (*WhoAmI, error) {
	var w WhoAmI
	if err := s.Get(ctx, WhoAmIMethod, struct{}{}, &w); err != nil {
		return nil, err
	}
	return &w, nil
}
