package authenv

import (
	"net/url"
	"testing"
)

// OIDCLogin runs the browser flow /auth/oidc/start → bobtest-idp → /auth/oidc/callback for
// provider and returns the final result, plus a client holding the bob_session cookie (empty
// Cookie when the flow did not set one).
func (s *Server) OIDCLogin(t testing.TB, provider, next string) (*Client, *Result) {
	t.Helper()
	b := NewBrowser()
	res := b.Get(t, s.URL+"/auth/oidc/start?provider="+url.QueryEscape(provider)+"&next="+url.QueryEscape(next))
	c := &Client{Server: s}
	if ck := b.Cookie(s.URL, "bob_session"); ck != nil {
		c.Cookie = Secret(ck.Value)
	}
	return c, res
}

// FindUser returns the id of the user with the given login (display login for OIDC users).
func (s *Server) FindUser(t testing.TB, login string) string {
	t.Helper()
	r := s.Admin().Call(t, UserSvc, "ListUsers", map[string]any{"query": login, "pageSize": 500}).Must(t, "ListUsers")
	for _, u := range List(r.Body, "users") {
		m, _ := u.(map[string]any)
		if Str(m, "login") == login {
			return Str(m, "id")
		}
	}
	return ""
}

// BindingSources returns "role@project/SOURCE" strings of a principal as the admin sees them.
func (s *Server) BindingSources(t testing.TB, principal string) []string {
	t.Helper()
	r := s.Admin().Call(t, RoleSvc, "ListRoleBindings", map[string]any{"principalId": principal, "pageSize": 500}).Must(t, "ListRoleBindings")
	var out []string
	for _, b := range List(r.Body, "bindings") {
		m, _ := b.(map[string]any)
		out = append(out, Str(m, "role")+"@"+Str(m, "project")+"/"+Str(m, "source"))
	}
	return out
}
