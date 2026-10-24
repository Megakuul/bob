package auth

import (
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/megakuul/bob/tests/auth/authenv"
	"github.com/megakuul/bob/tests/parity"
)

// TestRAUTH001LocalAndOIDC: local password login, OIDC login with a claim mapping, and a wrong
// nonce (SPEC R-AUTH-001 acceptance, cases 1–3).
func TestRAUTH001LocalAndOIDC(t *testing.T) {
	parity.Covers(t, "R-AUTH-001")
	s := authenv.Shared(t)
	idp := authenv.TheIDP(t)

	t.Run("local-user", func(t *testing.T) {
		s.NewUser(t, "alice", authenv.Password)
		c, r := s.Login(t, "alice", authenv.Password)
		r.Must(t, "LoginPassword alice")
		ck := r.Cookie("bob_session")
		if ck == nil || !strings.HasPrefix(ck.Value, "bob_st_") {
			t.Fatalf("no bob_session cookie with a bob_st_ value: %v", r.Header["Set-Cookie"])
		}
		if !ck.HttpOnly || !ck.Secure || ck.SameSite != http.SameSiteStrictMode || ck.Path != "/" {
			t.Errorf("session cookie must be HttpOnly; Secure; SameSite=Strict; Path=/, got %v", r.Header["Set-Cookie"])
		}
		who := c.Call(t, authenv.AuthSvc, "WhoAmI", map[string]any{}).Must(t, "WhoAmI with the session")
		if got := authenv.Str(who.Body, "login"); got != "alice" {
			t.Errorf("WhoAmI.login = %q, want alice (%s)", got, who.Raw)
		}
		if got := authenv.Str(who.Body, "tokenScope"); got != "session" {
			t.Errorf("WhoAmI.token_scope = %q, want session", got)
		}
		_, bad := s.Login(t, "alice", authenv.Password+"x")
		if bad.Code != "unauthenticated" || bad.Message != "invalid login or password" || bad.Detail != "BOB-API-2001" {
			t.Errorf("wrong password: want Unauthenticated 'invalid login or password', got %s", bad)
		}
		if bad.Cookie("bob_session") != nil {
			t.Errorf("a failed login set a session cookie")
		}
	})

	t.Run("oidc-claim-mapping", func(t *testing.T) {
		s.NewRole(t, "dev", authenv.Perm{Method: "bob.build.v1.BuildService/GetBuild", Project: "*"})
		idp.RegisterProvider(t, s, "idp-r1", true)
		s.Map(t, "idp-r1", "groups", "dev", "dev", "p1")
		idp.WrongNonce(false)
		idp.Login("oidc-dev-1", map[string]any{"groups": []string{"dev"}})
		c, res := s.OIDCLogin(t, "idp-r1", "/")
		if c.Cookie == "" {
			t.Fatalf("OIDC login set no session cookie; final status %d chain %v body %.200q", res.Status, res.URL, res.Body)
		}
		who := c.Call(t, authenv.AuthSvc, "WhoAmI", map[string]any{}).Must(t, "WhoAmI after OIDC login")
		if !slices.Contains(c.Bindings(t), "dev@p1") {
			t.Errorf("WhoAmI.bindings = %v, want dev@p1 (%s)", c.Bindings(t), who.Raw)
		}
		if got := authenv.Str(who.Body, "login"); got != "oidc-dev-1" {
			t.Errorf("WhoAmI.login = %q, want the username claim oidc-dev-1", got)
		}
		if !idp.SawPKCE() {
			t.Errorf("the server did not use PKCE towards the provider (R-AUTH-001, SPEC §6.3.2)")
		}
	})

	t.Run("wrong-nonce", func(t *testing.T) {
		idp.RegisterProvider(t, s, "idp-r1n", true)
		s.Map(t, "idp-r1n", "groups", "dev", "dev", "p1")
		idp.Login("oidc-nonce-1", map[string]any{"groups": []string{"dev"}})
		idp.WrongNonce(true)
		defer idp.WrongNonce(false)
		c, res := s.OIDCLogin(t, "idp-r1n", "/")
		if res.Status != 400 || !strings.Contains(res.URL, "/auth/oidc/callback") {
			t.Errorf("callback with a wrong nonce: want 400 at /auth/oidc/callback, got %d at %s", res.Status, res.URL)
		}
		if c.Cookie != "" {
			t.Errorf("a session cookie was set despite the wrong nonce")
		}
		if id := s.FindUser(t, "oidc-nonce-1"); id != "" {
			if bs := s.Admin().Call(t, authenv.RoleSvc, "ListRoleBindings", map[string]any{"principalId": "user:" + id}).Must(t, "ListRoleBindings"); len(authenv.List(bs.Body, "bindings")) != 0 {
				t.Errorf("the rejected login left bindings: %s", bs.Raw)
			}
		}
		anon := s.Anon().Call(t, authenv.AuthSvc, "WhoAmI", map[string]any{})
		if anon.Code != "unauthenticated" {
			t.Errorf("anonymous WhoAmI after the rejected login: %s", anon)
		}
	})
}

// TestRAUTH004MappingsReevaluated: OIDC-sourced bindings follow the claims at every login,
// manual bindings stay; require_mapping=true without a match refuses the login.
func TestRAUTH004MappingsReevaluated(t *testing.T) {
	parity.Covers(t, "R-AUTH-004")
	s := authenv.Shared(t)
	idp := authenv.TheIDP(t)
	idp.WrongNonce(false)

	s.NewRole(t, "dev4", authenv.Perm{Method: "bob.build.v1.BuildService/GetBuild", Project: "*"})
	s.NewRole(t, "viewer", authenv.Perm{Method: "bob.build.v1.BuildService/GetBuild", Project: "*"})

	// The user keeps a manual binding, so the IdP dropping the group must not lock it out:
	// this provider does not require a mapping (the require_mapping=true case follows below).
	idp.RegisterProvider(t, s, "idp-r4", false)
	s.Map(t, "idp-r4", "groups", "dev", "dev4", "p1")
	idp.Login("oidc-dev-4", map[string]any{"groups": []string{"dev"}})
	c, res := s.OIDCLogin(t, "idp-r4", "/")
	if c.Cookie == "" {
		t.Fatalf("first login failed: %d %.200q", res.Status, res.Body)
	}
	uid := s.FindUser(t, "oidc-dev-4")
	if uid == "" {
		t.Fatalf("the OIDC user was not created")
	}
	if got := s.BindingSources(t, "user:"+uid); !slices.Equal(got, []string{"dev4@p1/BINDING_SOURCE_OIDC"}) {
		t.Fatalf("after the first login: bindings %v, want [dev4@p1/oidc]", got)
	}
	s.Bind(t, "user:"+uid, "viewer", "*")

	idp.Login("oidc-dev-4", map[string]any{"groups": []string{"other"}})
	c, res = s.OIDCLogin(t, "idp-r4", "/")
	if c.Cookie == "" {
		t.Fatalf("second login failed: %d %.200q", res.Status, res.Body)
	}
	if got := c.Bindings(t); !slices.Equal(got, []string{"viewer@*"}) {
		t.Errorf("after the IdP dropped dev: WhoAmI.bindings = %v, want exactly [viewer@*]", got)
	}
	if got := s.BindingSources(t, "user:"+uid); !slices.Equal(got, []string{"viewer@*/BINDING_SOURCE_MANUAL"}) {
		t.Errorf("after the second login the stored bindings are %v, want only the manual viewer@*", got)
	}

	// and back: the mapping matches again
	idp.Login("oidc-dev-4", map[string]any{"groups": []string{"dev", "ops"}})
	c, _ = s.OIDCLogin(t, "idp-r4", "/")
	got := c.Bindings(t)
	slices.Sort(got)
	if !slices.Equal(got, []string{"dev4@p1", "viewer@*"}) {
		t.Errorf("after dev returned (array claim contains the value): bindings %v, want [dev4@p1 viewer@*]", got)
	}

	// require_mapping = true and nothing matches → the login fails with the message of §6.3.2
	idp.RegisterProvider(t, s, "idp-r4m", true)
	s.Map(t, "idp-r4m", "groups", "dev", "dev4", "p1")
	idp.Login("oidc-nomatch-4", map[string]any{"groups": []string{"other"}})
	c, res = s.OIDCLogin(t, "idp-r4m", "/")
	const msg = "your account has no access to this bob server (no role mapping matched)"
	if c.Cookie != "" || res.Status < 400 || res.Status >= 500 || !strings.Contains(res.Body, msg) {
		t.Errorf("require_mapping=true without a match: want a 4xx page containing %q and no session, got %d cookie=%q body %.200q", msg, res.Status, c.Cookie, res.Body)
	}
}
