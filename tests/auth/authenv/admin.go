package authenv

import (
	"net/http"
	"strings"
	"testing"
)

// Svc names of the services the helpers call.
const (
	UserSvc = "bob.access.v1.UserService"
	RoleSvc = "bob.access.v1.RoleService"
	SASvc   = "bob.access.v1.ServiceAccountService"
	OidcSvc = "bob.access.v1.OidcService"
	AuthSvc = "bob.auth.v1.AuthService"
)

// Password is a valid test password (12–256 bytes, SPEC §6.3.1).
const Password = "correct-horse-battery-1"

// Cookie returns the Set-Cookie of the reply with the given name, or nil.
func (r *Reply) Cookie(name string) *http.Cookie {
	resp := http.Response{Header: r.Header}
	for _, c := range resp.Cookies() {
		if c.Name == name {
			return c
		}
	}
	return nil
}

// Must fails the test unless the call succeeded, and returns the reply.
func (r *Reply) Must(t testing.TB, what string) *Reply {
	t.Helper()
	if !r.OK() {
		t.Fatalf("%s: %s", what, r)
	}
	return r
}

// NewUser creates a local user through the admin API and returns its id ("u…").
func (s *Server) NewUser(t testing.TB, login, password string) string {
	t.Helper()
	r := s.Admin().Call(t, UserSvc, "CreateUser", map[string]any{
		"user": map[string]any{"login": login}, "initialPassword": Secret(password),
	}).Must(t, "CreateUser "+login)
	return Str(r.Body, "user.id")
}

// NewAdmin creates a local user bound to admin@*.
func (s *Server) NewAdmin(t testing.TB, login, password string) string {
	t.Helper()
	id := s.NewUser(t, login, password)
	s.Bind(t, "user:"+id, "admin", "*")
	return id
}

// Bind creates a role binding as the bootstrap admin and returns its id.
func (s *Server) Bind(t testing.TB, principal, role, project string) string {
	t.Helper()
	r := s.Admin().Call(t, RoleSvc, "CreateRoleBinding", map[string]any{
		"principalId": principal, "role": role, "project": project,
	}).Must(t, "CreateRoleBinding "+role+"@"+project)
	return Str(r.Body, "binding.id")
}

// Unbind deletes a binding as the bootstrap admin.
func (s *Server) Unbind(t testing.TB, id, project string) {
	t.Helper()
	s.Admin().Call(t, RoleSvc, "DeleteRoleBinding", map[string]any{"id": id, "project": project}).Must(t, "DeleteRoleBinding")
}

// Perm is one role permission.
type Perm struct{ Method, Project string }

// NewRole creates a role as the bootstrap admin.
func (s *Server) NewRole(t testing.TB, id string, perms ...Perm) {
	t.Helper()
	var ps []map[string]any
	for _, p := range perms {
		ps = append(ps, map[string]any{"method": p.Method, "project": p.Project})
	}
	s.Admin().Call(t, RoleSvc, "CreateRole", map[string]any{
		"role": map[string]any{"id": id, "permissions": ps},
	}).Must(t, "CreateRole "+id)
}

// DeleteRole deletes a role as the bootstrap admin (errors are returned, not fatal).
func (s *Server) DeleteRole(t testing.TB, id string) *Reply {
	t.Helper()
	return s.Admin().Call(t, RoleSvc, "DeleteRole", map[string]any{"id": id})
}

// NewSA creates a service account.
func (s *Server) NewSA(t testing.TB, id string) {
	t.Helper()
	s.Admin().Call(t, SASvc, "CreateServiceAccount", map[string]any{
		"serviceAccount": map[string]any{"id": id},
	}).Must(t, "CreateServiceAccount "+id)
}

// NewSAToken creates a token for service account sa (scope "api" or "download") and returns
// its id and the secret.
func (s *Server) NewSAToken(t testing.TB, sa, name, scope string) (id, secret string) {
	t.Helper()
	sc := "TOKEN_SCOPE_API"
	if scope == "download" {
		sc = "TOKEN_SCOPE_DOWNLOAD"
	}
	r := s.Admin().Call(t, SASvc, "CreateServiceAccountToken", map[string]any{
		"serviceAccountId": sa, "name": name, "scope": sc, "ttlDays": 30,
	}).Must(t, "CreateServiceAccountToken")
	return Str(r.Body, "token.id"), Secret(Str(r.Body, "secret"))
}

// Login logs in with a local password through AuthService/LoginPassword and returns a client
// holding the bob_session cookie. The reply is returned too for assertions on the cookie.
func (s *Server) Login(t testing.TB, login, password string) (*Client, *Reply) {
	t.Helper()
	r := s.Anon().Call(t, AuthSvc, "LoginPassword", map[string]any{"login": login, "password": Secret(password)})
	c := &Client{Server: s}
	if ck := r.Cookie("bob_session"); ck != nil {
		c.Cookie = Secret(ck.Value)
	}
	return c, r
}

// MustLogin is Login that requires success and a session cookie.
func (s *Server) MustLogin(t testing.TB, login, password string) *Client {
	t.Helper()
	c, r := s.Login(t, login, password)
	r.Must(t, "LoginPassword "+login)
	if c.Cookie == "" {
		t.Fatalf("LoginPassword %s set no bob_session cookie", login)
	}
	return c
}

// Bindings returns the "role@project" strings of WhoAmI for the client.
func (c *Client) Bindings(t testing.TB) []string {
	t.Helper()
	r := c.Call(t, AuthSvc, "WhoAmI", map[string]any{}).Must(t, "WhoAmI")
	var out []string
	for _, b := range List(r.Body, "bindings") {
		m, _ := b.(map[string]any)
		out = append(out, Str(m, "role")+"@"+Str(m, "project"))
	}
	return out
}

// Join renders a list for failure messages.
func Join(l []string) string { return "[" + strings.Join(l, " ") + "]" }
