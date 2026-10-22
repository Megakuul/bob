package rbac

import (
	"fmt"
	"strings"
	"testing"

	"github.com/megakuul/bob/tests/auth/authenv"
	"github.com/megakuul/bob/tests/parity"
	"github.com/megakuul/bob/tests/rbac/ref"
)

// connectStatus is the HTTP status the Connect protocol assigns to unary error codes.
var connectStatus = map[string]int{
	"unauthenticated": 401, "permission_denied": 403, "not_found": 404, "invalid_argument": 400,
	"failed_precondition": 400, "already_exists": 409, "aborted": 409, "resource_exhausted": 429,
	"unavailable": 503, "internal": 500, "unimplemented": 501,
}

// detailCode is the ErrorDetail.code of each connect code (SPEC §6.1 table).
var detailCode = map[string]string{
	"invalid_argument": "BOB-API-1001", "unauthenticated": "BOB-API-2001", "permission_denied": "BOB-API-2002",
	"not_found": "BOB-API-3001", "already_exists": "BOB-API-3002", "failed_precondition": "BOB-API-3003",
	"aborted": "BOB-API-3004", "resource_exhausted": "BOB-API-4001", "unavailable": "BOB-API-5001",
	"internal": "BOB-API-9001",
}

// sessionKillers end the calling session; the matrix logs the principal in again afterwards.
var sessionKillers = map[string]bool{
	"bob.auth.v1.AuthService/Logout": true, "bob.auth.v1.AuthService/ChangePassword": true,
	"bob.auth.v1.AuthService/RevokeMySession": true,
}

// principal is one row dimension of the matrix.
type principal struct {
	name   string
	client func() *authenv.Client
	cred   ref.Credential
	binds  func(a, b ref.Permission) []ref.Binding // bindings given the per-method roles
	login  string                                  // cookie principals: login name for re-login
	cl     *authenv.Client
}

// check compares one answer with the reference outcome.
func check(t *testing.T, who string, m ref.Method, project string, got *authenv.Reply, want ref.Outcome, tolerateNotFound bool, seeded ...bool) {
	t.Helper()
	desc := fmt.Sprintf("%s calls %s on %s: want %v, got %s", who, m.Full(), project, want, got)
	switch want {
	case ref.Allow:
		if got.Code == "unauthenticated" || got.Code == "permission_denied" {
			t.Errorf("%s", desc)
		}
		if len(seeded) > 0 && seeded[0] && got.Code == "not_found" {
			t.Errorf("%s: an authorized principal got NotFound for an existing resource", desc) // existence leak in reverse
		}
		return
	case ref.Refused:
		if got.OK() {
			t.Errorf("%s", desc)
		}
		return
	}
	code := map[ref.Outcome]string{ref.Unauthenticated: "unauthenticated", ref.PermissionDenied: "permission_denied", ref.NotFound: "not_found"}[want]
	if tolerateNotFound && got.Code == "not_found" {
		code = "not_found" // R-AUTH-006: "NotFound or PermissionDenied (never OK)" for a principal without bindings
	}
	if got.Code != code {
		t.Errorf("%s", desc)
		return
	}
	if !m.Stream && got.HTTP != connectStatus[code] {
		t.Errorf("%s: HTTP status %d, connect says %d", desc, got.HTTP, connectStatus[code])
	}
	if got.Detail != detailCode[code] {
		t.Errorf("%s: ErrorDetail code %q, want %s", desc, got.Detail, detailCode[code])
	}
	if want == ref.PermissionDenied && m.Kind == ref.KindProject {
		if w := "permission denied: " + m.Full() + " on project " + project; got.Message != w {
			t.Errorf("%s: message %q, want %q", desc, got.Message, w)
		}
	}
	if want == ref.NotFound && strings.Contains(strings.ToLower(got.Message), "permission") {
		t.Errorf("%s: NotFound leaks the reason: %q", desc, got.Message)
	}
}

func call(t *testing.T, c *authenv.Client, m ref.Method, body map[string]any) *authenv.Reply {
	t.Helper()
	if m.Stream {
		return c.CallStream(t, m.Package+"."+m.Service, m.Name, body)
	}
	return c.Call(t, m.Package+"."+m.Service, m.Name, body)
}

// TestRBACMatrix is the generated matrix of HARNESS §8.3: every method × every principal ×
// call project p1/p2, expected outcomes from ref.Expect (SPEC §6.4.1), never from server code.
func TestRBACMatrix(t *testing.T) {
	parity.Covers(t, "R-AUTH-006")
	cat := catalog(t)
	s := authenv.Shared(t)

	login := func(name string) *authenv.Client { return s.MustLogin(t, name, authenv.Password) }
	for _, u := range []string{"mx-none", "mx-p1", "mx-p2", "mx-star", "mx-exact", "mx-other"} {
		s.NewUser(t, u, authenv.Password)
	}
	s.NewAdmin(t, "mx-admin", authenv.Password)
	s.NewSA(t, "mx-sa")
	_, saSecret := s.NewSAToken(t, "mx-sa", "matrix", "api")
	s.NewRole(t, "mx-other-role", authenv.Perm{Method: "bob.project.v1.ProjectService/GetProject", Project: "*"})
	s.Bind(t, "user:"+userID(t, s, "mx-other"), "mx-other-role", "p1")
	otherBinding := ref.Binding{Permissions: []ref.Permission{{Method: "bob.project.v1.ProjectService/GetProject", Project: "*"}}, Project: "p1"}

	one := func(a ref.Permission, project string) []ref.Binding {
		return []ref.Binding{{Permissions: []ref.Permission{a}, Project: project}}
	}
	ps := []*principal{
		{name: "anonymous", cred: ref.Anonymous, client: s.Anon},
		{name: "authenticated-no-bindings", login: "mx-none", cred: ref.User,
			binds: func(a, b ref.Permission) []ref.Binding { return nil }},
		{name: "role@p1", login: "mx-p1", cred: ref.User, binds: func(a, b ref.Permission) []ref.Binding { return one(a, "p1") }},
		{name: "role@p2", login: "mx-p2", cred: ref.User, binds: func(a, b ref.Permission) []ref.Binding { return one(a, "p2") }},
		{name: "role@*", login: "mx-star", cred: ref.User, binds: func(a, b ref.Permission) []ref.Binding { return one(a, "*") }},
		{name: "role{M,p1}@*", login: "mx-exact", cred: ref.User, binds: func(a, b ref.Permission) []ref.Binding { return one(b, "*") }},
		{name: "other-role@p1", login: "mx-other", cred: ref.User,
			binds: func(a, b ref.Permission) []ref.Binding { return []ref.Binding{otherBinding} }},
		{name: "admin", login: "mx-admin", cred: ref.User, binds: func(a, b ref.Permission) []ref.Binding {
			return []ref.Binding{{Permissions: []ref.Permission{{Method: "*", Project: "*"}}, Project: "*"}}
		}},
		{name: "service-account@p1", cred: ref.User, client: func() *authenv.Client { return s.WithToken(saSecret) },
			binds: func(a, b ref.Permission) []ref.Binding { return one(a, "p1") }},
		// A worker token (bwt1) is only valid on bob.worker.v1; the harness cannot register a worker
		// yet, so the credential here is well-formed but unknown and must be refused everywhere.
		{name: "worker-token", cred: ref.Worker, client: func() *authenv.Client { return s.WithToken("bwt1.AAAA.BBBB") }},
	}
	for _, p := range ps {
		if p.login != "" {
			p.cl = login(p.login)
		}
	}
	client := func(p *principal) *authenv.Client {
		if p.cl != nil {
			return p.cl
		}
		return p.client()
	}
	seed := s.Seed(t)
	// ids of existing resources per project for the id-resolving methods: the seeded build and one
	// of its jobs (a release or repository id cannot be seeded from here: those rows keep the
	// no-existence-leak expectation)
	resource := map[string]map[string]string{"BUILD": {}, "JOB": {}}
	for _, pr := range []string{"p1", "p2"} {
		resource["BUILD"][pr] = seed.Builds[pr]
		r := s.Admin().Call(t, "bob.build.v1.JobService", "ListJobs", map[string]any{"buildId": seed.Builds[pr]})
		if r.OK() {
			if js := authenv.List(r.Body, "jobs"); len(js) > 0 {
				if m, ok := js[0].(map[string]any); ok {
					resource["JOB"][pr] = authenv.Str(m, "id")
				}
			}
		}
	}
	byName := map[string]string{"role@p1": "mx-p1", "role@p2": "mx-p2", "role@*": "mx-star", "role{M,p1}@*": "mx-exact", "service-account@p1": "sa:mx-sa"}
	ids := map[string]string{}
	for _, name := range []string{"mx-p1", "mx-p2", "mx-star", "mx-exact"} {
		ids[name] = "user:" + userID(t, s, name)
	}
	ids["sa:mx-sa"] = "sa:mx-sa"

	for _, m := range cat.Methods {
		t.Run(m.Full(), func(t *testing.T) {
			a, b := ref.Permission{Method: m.Full(), Project: "*"}, ref.Permission{Method: m.Full(), Project: "p1"}
			var bindings []string
			if m.Kind != ref.KindWorker {
				s.NewRole(t, "mx-a", authenv.Perm{Method: a.Method, Project: a.Project})
				s.NewRole(t, "mx-b", authenv.Perm{Method: b.Method, Project: b.Project})
				for _, p := range ps {
					id, ok := byName[p.name]
					if !ok {
						continue
					}
					role := "mx-a"
					if p.name == "role{M,p1}@*" {
						role = "mx-b"
					}
					proj := map[string]string{"role@p1": "p1", "role@p2": "p2", "role@*": "*", "role{M,p1}@*": "*", "service-account@p1": "p1"}[p.name]
					bid := s.Bind(t, ids[id], role, proj)
					bindings = append(bindings, bid+"@"+proj)
				}
				defer func() {
					for _, bp := range bindings {
						id, proj, _ := strings.Cut(bp, "@")
						s.Unbind(t, id, proj)
					}
					s.DeleteRole(t, "mx-a")
					s.DeleteRole(t, "mx-b")
				}()
			}
			projects := []string{"p1", "p2"}
			resolved := m.Kind == ref.KindProject && m.Resolve != "" && m.Resolve != "UNSPECIFIED"
			if m.Kind != ref.KindProject || resolved {
				projects = []string{"p1"}
			}
			if resolved && resource[m.Resolve] != nil && resource[m.Resolve]["p1"] != "" && resource[m.Resolve]["p2"] != "" {
				projects = []string{"p1", "p2"}
			}
			for _, p := range ps {
				for _, project := range projects {
					body := map[string]any{}
					if m.Kind == ref.KindProject && (m.Resolve == "" || m.Resolve == "UNSPECIFIED") {
						if nested, ok := nestedProject[m.Full()]; ok {
							// proto problem (R-AUTH-006): the rule names "id" but the request only has the
							// nested message; the row still sends the intended project.
							body[nested] = map[string]any{"id": project}
						} else {
							body[m.ProjectField] = project
						}
					}
					want := ref.Expect(m, p.cred, p.binds2(a, b), project)
					tolerate := false
					seeded := false
					if resolved && resource[m.Resolve] != nil && resource[m.Resolve][project] != "" {
						// the id names a real resource of this project: the outcome is the plain
						// SPEC 6.4 evaluation with the resource's project, per principal
						body[m.ProjectField] = resource[m.Resolve][project]
						seeded = true
					}
					switch {
					case seeded:
						// want stays ref.Expect(...)
					case m.Kind == ref.KindProject && m.Resolve != "" && m.Resolve != "UNSPECIFIED" && p.cred == ref.User:
						// The handler resolves the id first; an unknown id is NotFound for everybody
						// (no existence leak) — the harness has no seeded builds/jobs/releases yet.
						want = ref.NotFound
						if p.name == "admin" || p.name == "role@*" {
							want = ref.Allow // may legitimately answer NotFound for the missing id
						}
					case want == ref.PermissionDenied && m.Kind == ref.KindGlobal && p.name == "authenticated-no-bindings":
						tolerate = true
					}
					if m.Kind == ref.KindWorker {
						want = ref.Refused // the harness holds no valid worker token (see the principal list)
					}
					got := call(t, client(p), m, body)
					check(t, p.name, m, project, got, want, tolerate, seeded)
					if sessionKillers[m.Full()] && p.login != "" {
						p.cl = login(p.login)
					}
				}
			}
		})
	}
}

// nestedProject lists PROJECT methods whose rule names a field that is not in the request.
var nestedProject = map[string]string{"bob.project.v1.ProjectService/UpdateProject": "project"}

func (p *principal) binds2(a, b ref.Permission) []ref.Binding {
	if p.binds == nil {
		return nil
	}
	return p.binds(a, b)
}

// userID looks up a user id by login through the admin API.
func userID(t *testing.T, s *authenv.Server, login string) string {
	t.Helper()
	r := s.Admin().Call(t, authenv.UserSvc, "ListUsers", map[string]any{"query": login, "pageSize": 500}).Must(t, "ListUsers")
	for _, u := range authenv.List(r.Body, "users") {
		m, _ := u.(map[string]any)
		if authenv.Str(m, "login") == login {
			return authenv.Str(m, "id")
		}
	}
	t.Fatalf("user %s not found", login)
	return ""
}
