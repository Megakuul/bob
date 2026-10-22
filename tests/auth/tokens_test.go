package auth

import (
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/megakuul/bob/tests/auth/authenv"
	"github.com/megakuul/bob/tests/parity"
)

// TestRAUTH007ServerIssuedTokens: after a CLI login no object of the control bucket contains a
// plaintext token; an expired access token gives 'token expired' and a refresh then succeeds.
func TestRAUTH007ServerIssuedTokens(t *testing.T) {
	parity.Covers(t, "R-AUTH-007")
	s := authenv.Start(t, 1)
	s.NewUser(t, "cli007", authenv.Password)
	tk := s.CLILogin(t, "cli007", authenv.Password)

	for kind, v := range map[string]struct{ val, prefix string }{
		"access": {tk.Access, "bob_at_"}, "refresh": {tk.Refresh, "bob_rt_"}, "download": {tk.Download, "bob_dt_"},
	} {
		if !strings.HasPrefix(v.val, v.prefix) || len(v.val) < len(v.prefix)+52 { // base32 of 32 bytes = 52 chars
			t.Errorf("%s token %q: want %s + base32(32 bytes)", kind, v.val, v.prefix)
		}
	}
	if tk.ExpiresIn != 3600 || !strings.HasPrefix(tk.User, "u") {
		t.Errorf("token answer: expires_in %d bob_user %q, want 3600 and a u… id", tk.ExpiresIn, tk.User)
	}
	if tt, _ := tk.Raw["token_type"].(string); tt != "Bearer" {
		t.Errorf("token_type = %q, want Bearer", tt)
	}
	api := s.WithToken(tk.Access)
	api.Call(t, authenv.AuthSvc, "WhoAmI", map[string]any{}).Must(t, "WhoAmI with the access token")
	if code, _, _ := s.Anon().BasicGet(t, "/nix/nix-cache-info", "bob", tk.Download); code != 200 {
		t.Errorf("the download token is refused on /nix/: HTTP %d", code)
	}

	hits, scanned := s.ObjectsContaining(t, "bob-control", tk.Access, tk.Refresh, tk.Download)
	if scanned == 0 {
		t.Fatalf("the control bucket holds no object after a login: the check would prove nothing")
	}
	if len(hits) > 0 {
		t.Errorf("control bucket objects contain a plaintext token: %v", hits)
	}

	// expiry: one hour and a minute later the access token is expired, the refresh token is not
	s.SetClock(t, 61*time.Minute)
	defer s.SetClock(t, 0)
	got := api.Call(t, authenv.AuthSvc, "WhoAmI", map[string]any{})
	if got.Code != "unauthenticated" || got.Message != "token expired" || got.Detail != "BOB-API-2001" {
		t.Errorf("expired access token: want Unauthenticated 'token expired', got %s", got)
	}
	f := s.Refresh(t, tk.Refresh)
	if f.Status != 200 {
		t.Fatalf("refresh after the access token expired: HTTP %d %s", f.Status, f.Raw)
	}
	fresh := authenv.TokensOf(f)
	if !strings.HasPrefix(fresh.Access, "bob_at_") || fresh.Access == tk.Access {
		t.Errorf("refresh returned access token %q", fresh.Access)
	}
	if fresh.Refresh != "" && fresh.Refresh != tk.Refresh {
		t.Errorf("the refresh token must not be rotated (SPEC §6.3.2), got a new one")
	}
	s.WithToken(fresh.Access).Call(t, authenv.AuthSvc, "WhoAmI", map[string]any{}).Must(t, "WhoAmI with the refreshed token")
	if f := s.Refresh(t, tk.Refresh); f.Status != 200 {
		t.Errorf("the same refresh token a second time (not rotated): HTTP %d %s", f.Status, f.Raw)
	}
	if hits, _ := s.ObjectsContaining(t, "bob-control", fresh.Access); len(hits) > 0 {
		t.Errorf("control bucket objects contain the refreshed access token: %v", hits)
	}
}

var userCode = regexp.MustCompile(`^[BCDFGHJKLMNPQRSTVWXZ]{4}-[BCDFGHJKLMNPQRSTVWXZ]{4}$`)

// TestRAUTH150OAuthFlows: authorization code + PKCE with loopback redirect, and the device flow,
// for client bob-cli (SPEC §6.3.2).
func TestRAUTH150OAuthFlows(t *testing.T) {
	parity.Covers(t, "R-AUTH-150")
	s := authenv.Start(t, 1)
	s.NewUser(t, "oauth150", authenv.Password)
	session := s.MustLogin(t, "oauth150", authenv.Password)

	t.Run("pkce", func(t *testing.T) {
		verifier, challenge := authenv.PKCE()
		loc, res := s.Authorize(t, session, authenv.AuthorizeQuery(authenv.Redirect, challenge, "state-1"))
		if loc == nil || loc.Query().Get("code") == "" || loc.Query().Get("state") != "state-1" {
			t.Fatalf("authorize: want a redirect to the loopback URI with code and the same state, got status %d location %q", res.Status, res.Location)
		}
		if loc.Host != "127.0.0.1:38917" || loc.Path != "/callback" {
			t.Errorf("redirected to %s, want %s", loc, authenv.Redirect)
		}
		code := loc.Query().Get("code")

		bad := s.Exchange(t, code, verifier+"x", authenv.Redirect)
		if bad.Status != 400 || bad.Error() != "invalid_grant" {
			t.Errorf("wrong code_verifier: want 400 invalid_grant, got %d %s", bad.Status, bad.Raw)
		}
		// the failed attempt may burn the code; a fresh code is used for the success and reuse checks
		loc, _ = s.Authorize(t, session, authenv.AuthorizeQuery(authenv.Redirect, challenge, "state-2"))
		code = loc.Query().Get("code")
		ok := s.Exchange(t, code, verifier, authenv.Redirect)
		if ok.Status != 200 || authenv.TokensOf(ok).Access == "" {
			t.Fatalf("valid exchange: %d %s", ok.Status, ok.Raw)
		}
		reuse := s.Exchange(t, code, verifier, authenv.Redirect)
		if reuse.Status != 400 || reuse.Error() != "invalid_grant" {
			t.Errorf("reused code: want 400 invalid_grant, got %d %s", reuse.Status, reuse.Raw)
		}
		// codes live 60 s
		loc, _ = s.Authorize(t, session, authenv.AuthorizeQuery(authenv.Redirect, challenge, "state-3"))
		s.SetClock(t, 61*time.Second)
		late := s.Exchange(t, loc.Query().Get("code"), verifier, authenv.Redirect)
		s.SetClock(t, 0)
		if late.Status != 400 || late.Error() != "invalid_grant" {
			t.Errorf("code older than 60 s: want 400 invalid_grant, got %d %s", late.Status, late.Raw)
		}
		// a code is bound to its redirect_uri
		loc, _ = s.Authorize(t, session, authenv.AuthorizeQuery(authenv.Redirect, challenge, "state-4"))
		other := s.Exchange(t, loc.Query().Get("code"), verifier, "http://127.0.0.1:1/callback")
		if other.Status != 400 || other.Error() != "invalid_grant" {
			t.Errorf("exchange with another redirect_uri: want 400 invalid_grant, got %d %s", other.Status, other.Raw)
		}
	})

	t.Run("redirect-uri", func(t *testing.T) {
		_, challenge := authenv.PKCE()
		for _, bad := range []string{
			"http://evil.example/callback", "http://localhost:4000/callback", "http://127.0.0.1:4000/other",
			"https://127.0.0.1:4000/callback", "http://127.0.0.1.evil.example/callback", "http://user@evil.example:80/callback",
		} {
			loc, res := s.Authorize(t, session, authenv.AuthorizeQuery(bad, challenge, "s"))
			if res.Status != 400 || loc != nil || res.Header.Get("Location") != "" {
				t.Errorf("redirect_uri=%s: want 400 without a redirect, got %d Location %q", bad, res.Status, res.Header.Get("Location"))
			}
			if strings.Contains(res.Body, "evil.example") && strings.Contains(res.Body, "href") {
				t.Errorf("redirect_uri=%s: the error page links to the rejected URI", bad)
			}
		}
		for _, good := range []string{"http://127.0.0.1:4000/callback", "http://[::1]:4000/callback", "http://127.0.0.1/callback"} {
			loc, res := s.Authorize(t, session, authenv.AuthorizeQuery(good, challenge, "s"))
			if loc == nil || loc.Query().Get("code") == "" || !strings.HasPrefix(loc.String(), good) {
				t.Errorf("redirect_uri=%s: want a redirect with code, got %d %q", good, res.Status, res.Location)
			}
		}
		// no browser session: login first, then back
		b := authenv.NewBrowser()
		res := b.Do(t, "GET", s.URL+"/oauth/authorize?"+authenv.AuthorizeQuery(authenv.Redirect, challenge, "s").Encode(), nil, func(string) bool { return true })
		if res.Status < 300 || res.Status > 399 {
			t.Fatalf("authorize without a session: want a redirect to /login, got %d", res.Status)
		}
		lu, err := url.Parse(res.Location)
		if err != nil || lu.Path != "/login" || !strings.Contains(lu.Query().Get("next"), "/oauth/authorize") {
			t.Errorf("authorize without a session redirected to %q, want /login?next=<authorize URL>", res.Location)
		}
		if res := s.PostForm(t, "/oauth/token", url.Values{"grant_type": {"authorization_code"}, "code": {"nope"}, "client_id": {"other-client"}}, nil); res.Status != 400 && res.Status != 401 {
			t.Errorf("unknown client_id: HTTP %d, want 400/401", res.Status)
		}
	})

	t.Run("device", func(t *testing.T) {
		dc := s.PostForm(t, "/oauth/device/code", url.Values{"client_id": {"bob-cli"}}, nil)
		if dc.Status != 200 {
			t.Fatalf("device/code: %d %s", dc.Status, dc.Raw)
		}
		code, _ := dc.Body["device_code"].(string)
		uc, _ := dc.Body["user_code"].(string)
		vu, _ := dc.Body["verification_uri"].(string)
		if code == "" || !userCode.MatchString(uc) || vu != s.URL+"/device" ||
			dc.Body["expires_in"] != float64(600) || dc.Body["interval"] != float64(5) {
			t.Fatalf("device/code answer %s: want device_code, user_code ABCD-EFGH from BCDFGHJKLMNPQRSTVWXZ, verification_uri %s/device, expires_in 600, interval 5", dc.Raw, s.URL)
		}
		poll := func(dev string) *authenv.Form {
			return s.PostForm(t, "/oauth/token", url.Values{
				"grant_type": {"urn:ietf:params:oauth:grant-type:device_code"}, "device_code": {dev}, "client_id": {"bob-cli"}}, nil)
		}
		if f := poll(code); f.Status != 400 || f.Error() != "authorization_pending" {
			t.Errorf("poll before approval: want authorization_pending, got %d %s", f.Status, f.Raw)
		}
		if f := poll(code); f.Status != 400 || f.Error() != "slow_down" {
			t.Errorf("poll again at once: want slow_down, got %d %s", f.Status, f.Raw)
		}
		if f := poll("not-a-device-code"); f.Status != 400 || f.Error() != "invalid_grant" {
			t.Errorf("unknown device_code: want invalid_grant, got %d %s", f.Status, f.Raw)
		}
		// the user approves on /device (needs a browser session); anonymous approval is refused
		anon := authenv.NewBrowser()
		if res := anon.Do(t, "POST", s.URL+"/device", url.Values{"user_code": {uc}, "decision": {"allow"}}, func(string) bool { return true }); res.Status == 200 && !strings.Contains(res.Body, "login") {
			t.Errorf("approving a device code without a browser session: HTTP %d", res.Status)
		}
		s.ApproveDevice(t, session, uc, true)
		time.Sleep(11 * time.Second) // slow_down raised the interval to 10 s
		f := poll(code)
		if f.Status != 200 {
			t.Fatalf("poll after approval: %d %s", f.Status, f.Raw)
		}
		tk := authenv.TokensOf(f)
		if !strings.HasPrefix(tk.Access, "bob_at_") || !strings.HasPrefix(tk.Refresh, "bob_rt_") || !strings.HasPrefix(tk.Download, "bob_dt_") {
			t.Errorf("device flow tokens: %v", f.Raw)
		}
		s.WithToken(tk.Access).Call(t, authenv.AuthSvc, "WhoAmI", map[string]any{}).Must(t, "WhoAmI with the device-flow token")
		if again := poll(code); again.Status == 200 {
			t.Errorf("a device code was redeemed twice")
		}

		// denial and expiry
		dc = s.PostForm(t, "/oauth/device/code", url.Values{"client_id": {"bob-cli"}}, nil)
		denyCode, denyUser := dc.Body["device_code"].(string), dc.Body["user_code"].(string)
		s.ApproveDevice(t, session, denyUser, false)
		time.Sleep(6 * time.Second)
		if f := poll(denyCode); f.Status != 400 || f.Error() != "access_denied" {
			t.Errorf("poll after denial: want access_denied, got %d %s", f.Status, f.Raw)
		}
		dc = s.PostForm(t, "/oauth/device/code", url.Values{"client_id": {"bob-cli"}}, nil)
		expCode := dc.Body["device_code"].(string)
		s.SetClock(t, 601*time.Second)
		defer s.SetClock(t, 0)
		if f := poll(expCode); f.Status != 400 || f.Error() != "expired_token" {
			t.Errorf("poll after 600 s: want expired_token, got %d %s", f.Status, f.Raw)
		}
	})
}
