package rbac

import (
	"fmt"
	"sync"
	"testing"

	"github.com/megakuul/bob/tests/auth/authenv"
	"github.com/megakuul/bob/tests/parity"
	"github.com/megakuul/bob/tests/rbac/ref"
)

// The probe method of the R-AUTH-002 table: a PROJECT method with a direct project field whose
// request is made invalid on purpose (days ≤ 400, SPEC §6.5.16). The interceptor authorizes before
// the handler validates, so an authorized caller gets InvalidArgument while a caller without
// any permission on the project gets NotFound and a caller with other permissions PermissionDenied
// — three different answers although the project does not exist.
const (
	probe     = "bob.analytics.v1.AnalyticsService/PackageDurations"
	probeSvc  = "bob.analytics.v1.AnalyticsService/*"
	otherM    = "bob.analytics.v1.AnalyticsService/BuildHistory"
	otherSvc  = "bob.build.v1.BuildService/*"
	cancelM   = "bob.build.v1.BuildService/CancelBuild"
	getBuildM = "bob.build.v1.BuildService/GetBuild"
)

type outcome string

const (
	allow outcome = "allow" // InvalidArgument from the probe
	denyP outcome = "permission_denied"
	notF  outcome = "not_found"
)

// row is one line of the R-AUTH-002 table: a role (permissions), a binding project, the call.
type row struct {
	perms   []ref.Permission
	bind    string
	method  string
	project string
	want    outcome
}

func p(method, project string) ref.Permission {
	return ref.Permission{Method: method, Project: project}
}

var rows = []row{
	// SPEC examples, with the probe service standing in for BuildService.
	{[]ref.Permission{p(probeSvc, "*")}, "p1", probe, "p1", allow},
	{[]ref.Permission{p(probeSvc, "*")}, "p1", probe, "p2", notF},
	{[]ref.Permission{p(probe, "p1")}, "p2", probe, "p1", notF},
	{[]ref.Permission{p(probe, "p1")}, "p2", probe, "p2", notF},
	{[]ref.Permission{p("*", "*")}, "*", probe, "p1", allow},
	{[]ref.Permission{p("*", "*")}, "*", probe, "p2", allow},
	// service wildcard
	{[]ref.Permission{p(probeSvc, "*")}, "p2", probe, "p1", notF},
	{[]ref.Permission{p(probeSvc, "*")}, "*", probe, "p1", allow},
	{[]ref.Permission{p(probeSvc, "*")}, "*", probe, "p2", allow},
	{[]ref.Permission{p(probeSvc, "p2")}, "p2", probe, "p2", allow},
	{[]ref.Permission{p(probeSvc, "p2")}, "p2", probe, "p1", notF},
	{[]ref.Permission{p(probeSvc, "p2")}, "*", probe, "p2", allow},
	{[]ref.Permission{p(probeSvc, "p2")}, "*", probe, "p1", notF},
	// exact method
	{[]ref.Permission{p(probe, "p1")}, "*", probe, "p1", allow},
	{[]ref.Permission{p(probe, "p1")}, "*", probe, "p2", notF},
	{[]ref.Permission{p(probe, "p1")}, "p1", probe, "p1", allow},
	{[]ref.Permission{p(probe, "p1")}, "p1", probe, "p2", notF},
	{[]ref.Permission{p(probe, "*")}, "*", probe, "p1", allow},
	{[]ref.Permission{p(probe, "*")}, "*", probe, "p2", allow},
	{[]ref.Permission{p(probe, "*")}, "p2", probe, "p2", allow},
	{[]ref.Permission{p(probe, "*")}, "p2", probe, "p1", notF},
	// all methods
	{[]ref.Permission{p("*", "*")}, "p1", probe, "p1", allow},
	{[]ref.Permission{p("*", "*")}, "p1", probe, "p2", notF},
	{[]ref.Permission{p("*", "p1")}, "*", probe, "p1", allow},
	{[]ref.Permission{p("*", "p1")}, "*", probe, "p2", notF},
	{[]ref.Permission{p("*", "p1")}, "p2", probe, "p1", notF},
	// another method of the same service / another service: some permission on the project
	// but not this method → PermissionDenied; none on the project → NotFound
	{[]ref.Permission{p(otherM, "*")}, "p1", probe, "p1", denyP},
	{[]ref.Permission{p(otherM, "*")}, "p1", probe, "p2", notF},
	{[]ref.Permission{p(otherM, "p1")}, "*", probe, "p1", denyP},
	{[]ref.Permission{p(otherM, "p1")}, "*", probe, "p2", notF},
	{[]ref.Permission{p(otherM, "*")}, "*", probe, "p1", denyP},
	{[]ref.Permission{p(otherM, "*")}, "*", probe, "p2", denyP},
	{[]ref.Permission{p(otherSvc, "*")}, "p1", probe, "p1", denyP},
	{[]ref.Permission{p(otherSvc, "*")}, "*", probe, "p2", denyP},
	// a role with several permissions: union per project
	{[]ref.Permission{p(probe, "p1"), p(otherM, "p2")}, "*", probe, "p1", allow},
	{[]ref.Permission{p(probe, "p1"), p(otherM, "p2")}, "*", probe, "p2", denyP},
	{[]ref.Permission{p(probe, "p1"), p(otherM, "p2")}, "p2", probe, "p1", notF},
}

// TestRAUTH002Table runs the table against the server and against the reference evaluator:
// the table is checked against ref.Allowed first, so a typo in a row cannot hide a server bug.
func TestRAUTH002Table(t *testing.T) {
	parity.Covers(t, "R-AUTH-002")
	if len(rows) < 30 {
		t.Fatalf("the table has %d rows, SPEC wants ≥ 30", len(rows))
	}
	// Evaluator rows named by the SPEC for BuildService: authorization is evaluated exactly
	// per §6.4.1 (these need builds to observe on the server; the reference alone is checked here).
	spec := []struct {
		perms   []ref.Permission
		bind    string
		method  string
		project string
		want    bool
	}{
		{[]ref.Permission{p("bob.build.v1.BuildService/*", "*")}, "p1", cancelM, "p1", true},
		{[]ref.Permission{p("bob.build.v1.BuildService/*", "*")}, "p1", cancelM, "p2", false},
		{[]ref.Permission{p(getBuildM, "p1")}, "p2", getBuildM, "p1", false},
		{[]ref.Permission{p(getBuildM, "p1")}, "p2", getBuildM, "p2", false},
		{[]ref.Permission{p("*", "*")}, "*", cancelM, "p1", true},
		{[]ref.Permission{p("*", "*")}, "*", getBuildM, "anything", true},
	}
	for i, r := range spec {
		if got := ref.Allowed([]ref.Binding{{Permissions: r.perms, Project: r.bind}}, r.method, r.project); got != r.want {
			t.Fatalf("spec row %d: evaluator says %v, SPEC says %v", i, got, r.want)
		}
	}
	for i, r := range rows {
		b := []ref.Binding{{Permissions: r.perms, Project: r.bind}}
		var want outcome = notF
		switch {
		case ref.Allowed(b, r.method, r.project):
			want = allow
		case ref.HasAny(b, r.project):
			want = denyP
		}
		if want != r.want {
			t.Fatalf("row %d %+v: the table says %s but SPEC §6.4.1 gives %s", i, r, r.want, want)
		}
	}

	s := authenv.Shared(t)
	for i, r := range rows {
		id := fmt.Sprintf("a2-%02d", i)
		var ps []authenv.Perm
		for _, x := range r.perms {
			ps = append(ps, authenv.Perm{Method: x.Method, Project: x.Project})
		}
		s.NewRole(t, id, ps...)
		uid := s.NewUser(t, id, authenv.Password)
		bid := s.Bind(t, "user:"+uid, id, r.bind)
		c := s.MustLogin(t, id, authenv.Password)
		svc, name, _ := cutMethod(r.method)
		got := c.Call(t, svc, name, map[string]any{"projectId": r.project, "days": 401})
		switch r.want {
		case allow:
			if got.Code != "invalid_argument" || got.Detail != "BOB-API-1001" {
				t.Errorf("row %d (%v bound @%s calls %s on %s): want authorized (InvalidArgument from the handler), got %s", i, r.perms, r.bind, r.method, r.project, got)
			}
		case denyP:
			if got.Code != "permission_denied" || got.Detail != "BOB-API-2002" {
				t.Errorf("row %d (%v bound @%s calls %s on %s): want PermissionDenied, got %s", i, r.perms, r.bind, r.method, r.project, got)
			}
		case notF:
			if got.Code != "not_found" || got.Detail != "BOB-API-3001" {
				t.Errorf("row %d (%v bound @%s calls %s on %s): want NotFound, got %s", i, r.perms, r.bind, r.method, r.project, got)
			}
		}
		s.Unbind(t, bid, r.bind)
		s.DeleteRole(t, id)
	}
}

// TestRAUTH002SpecExamples runs the three BuildService examples of the R-AUTH-002 acceptance
// text against seeded builds of p1 and p2.
func TestRAUTH002SpecExamples(t *testing.T) {
	parity.Covers(t, "R-AUTH-002")
	s := authenv.Shared(t)
	seed := s.Seed(t)
	b1, b2 := seed.Builds["p1"], seed.Builds["p2"]

	user := func(login, role, project string, perms ...authenv.Perm) *authenv.Client {
		s.NewRole(t, role, perms...)
		id := s.NewUser(t, login, authenv.Password)
		s.Bind(t, "user:"+id, role, project)
		return s.MustLogin(t, login, authenv.Password)
	}
	authorized := func(r *authenv.Reply) bool {
		return r.Code != "not_found" && r.Code != "permission_denied" && r.Code != "unauthenticated"
	}

	c := user("ex-build-all", "ex-build-all", "p1", authenv.Perm{Method: "bob.build.v1.BuildService/*", Project: "*"})
	if r := c.Call(t, "bob.build.v1.BuildService", "CancelBuild", map[string]any{"id": b1}); !authorized(r) {
		t.Errorf("{BuildService/*, *} bound @p1: CancelBuild on p1 must be allowed, got %s", r)
	}
	if r := c.Call(t, "bob.build.v1.BuildService", "CancelBuild", map[string]any{"id": b2}); r.Code != "not_found" {
		t.Errorf("{BuildService/*, *} bound @p1: CancelBuild on p2 must be denied (NotFound), got %s", r)
	}

	c = user("ex-get-p1", "ex-get-p1", "p2", authenv.Perm{Method: "bob.build.v1.BuildService/GetBuild", Project: "p1"})
	for name, id := range map[string]string{"p1": b1, "p2": b2} {
		if r := c.Call(t, "bob.build.v1.BuildService", "GetBuild", map[string]any{"id": id}); r.Code != "not_found" {
			t.Errorf("{GetBuild, p1} bound @p2 grants nothing: GetBuild on %s got %s", name, r)
		}
	}

	c = user("ex-all", "ex-all", "*", authenv.Perm{Method: "*", Project: "*"})
	for name, id := range map[string]string{"p1": b1, "p2": b2} {
		if r := c.Call(t, "bob.build.v1.BuildService", "GetBuild", map[string]any{"id": id}); !r.OK() {
			t.Errorf("{*, *} bound @*: GetBuild on %s must succeed, got %s", name, r)
		}
	}
}

func cutMethod(full string) (svc, name string, ok bool) {
	for i := len(full) - 1; i >= 0; i-- {
		if full[i] == '/' {
			return full[:i], full[i+1:], true
		}
	}
	return full, "", false
}

// TestRAUTH003AdminInvariants: a fresh server has exactly the role admin; it is immutable; at
// least one admin remains, also when two orchestrators remove the last two admin bindings at once.
func TestRAUTH003AdminInvariants(t *testing.T) {
	parity.Covers(t, "R-AUTH-003")
	s := authenv.Start(t, 2) // fresh stack, two orchestrators
	admin := s.Admin()

	r := admin.Call(t, authenv.RoleSvc, "ListRoles", map[string]any{}).Must(t, "ListRoles")
	roles := authenv.List(r.Body, "roles")
	if len(roles) != 1 || authenv.Str(roles[0].(map[string]any), "id") != "admin" {
		t.Fatalf("fresh server: ListRoles = %s, want exactly [admin]", r.Raw)
	}
	for _, name := range []string{"UpdateRole", "DeleteRole"} {
		req := map[string]any{"id": "admin"}
		if name == "UpdateRole" {
			req = map[string]any{"role": map[string]any{"id": "admin", "description": "changed"}, "updateMask": "description"}
		}
		got := admin.Call(t, authenv.RoleSvc, name, req)
		if got.Code != "failed_precondition" || got.Detail != "BOB-API-3003" || got.Message != "role admin is built in" {
			t.Errorf("%s(admin): want FailedPrecondition 'role admin is built in', got %s", name, got)
		}
	}

	// exactly one admin binding: the bootstrap admin
	who := admin.Call(t, authenv.AuthSvc, "WhoAmI", map[string]any{}).Must(t, "WhoAmI")
	pid := authenv.Str(who.Body, "principal.id") // "user:u…"
	bl := admin.Call(t, authenv.RoleSvc, "ListRoleBindings", map[string]any{"role": "admin"}).Must(t, "ListRoleBindings")
	bs := authenv.List(bl.Body, "bindings")
	if len(bs) != 1 {
		t.Fatalf("fresh server: %d admin bindings, want 1: %s", len(bs), bl.Raw)
	}
	bid := authenv.Str(bs[0].(map[string]any), "id")
	const remain = "at least one admin must remain"
	userID := pid[len("user:"):]
	for _, step := range []struct {
		name, svc, method string
		req               map[string]any
	}{
		{"RemoveRoleBinding", authenv.RoleSvc, "DeleteRoleBinding", map[string]any{"id": bid, "project": "*"}},
		{"UpdateUser{disabled:true}", authenv.UserSvc, "UpdateUser", map[string]any{
			"user": map[string]any{"id": userID, "disabled": true}, "updateMask": "disabled"}},
		{"DeleteUser", authenv.UserSvc, "DeleteUser", map[string]any{"id": userID}},
	} {
		got := admin.Call(t, step.svc, step.method, step.req)
		if got.Code != "failed_precondition" || got.Message != remain {
			t.Errorf("%s of the last admin: want FailedPrecondition %q, got %s", step.name, remain, got)
		}
	}
	if len(admin.Bindings(t)) == 0 {
		t.Fatalf("the last admin lost its bindings although the calls were refused")
	}

	// two admins, two orchestrators, both bindings removed at the same time → exactly one remains
	s.NewAdmin(t, "adm-two", authenv.Password)
	two := &authenv.Client{Server: s, Base: s.URLs[1], Cookie: s.MustLogin(t, "adm-two", authenv.Password).Cookie}
	one := &authenv.Client{Server: s, Base: s.URLs[0], Token: s.AdminToken}
	twoID := userIDOf(t, two)
	for round := range 5 {
		bindingOf := func(c *authenv.Client, uid string) string {
			l := c.Call(t, authenv.RoleSvc, "ListRoleBindings", map[string]any{"principalId": "user:" + uid, "role": "admin"}).Must(t, "ListRoleBindings")
			for _, b := range authenv.List(l.Body, "bindings") {
				return authenv.Str(b.(map[string]any), "id")
			}
			return ""
		}
		b1, b2 := bindingOf(one, userID), bindingOf(one, twoID)
		if b1 == "" || b2 == "" {
			t.Fatalf("round %d: admin bindings missing (%q, %q)", round, b1, b2)
		}
		var wg sync.WaitGroup
		var r1, r2 *authenv.Reply
		start := make(chan struct{})
		wg.Add(2)
		go func() {
			defer wg.Done()
			<-start
			r1, _ = two.Do(authenv.RoleSvc, "DeleteRoleBinding", map[string]any{"id": b1, "project": "*"}, false)
		}()
		go func() {
			defer wg.Done()
			<-start
			r2, _ = one.Do(authenv.RoleSvc, "DeleteRoleBinding", map[string]any{"id": b2, "project": "*"}, false)
		}()
		close(start)
		wg.Wait()
		if r1 == nil || r2 == nil {
			t.Fatalf("round %d: transport error", round)
		}
		ok := 0
		for _, rr := range []*authenv.Reply{r1, r2} {
			if rr.OK() {
				ok++
			} else if rr.Code != "failed_precondition" || rr.Message != remain {
				t.Errorf("round %d: loser answered %s, want FailedPrecondition %q", round, rr, remain)
			}
		}
		if ok != 1 {
			t.Errorf("round %d: %d removals succeeded, want exactly 1 (%s / %s)", round, ok, r1, r2)
		}
		// whoever survived restores the other binding
		survivor, loserID := one, twoID
		if r2.OK() == false { // the call made as `one` failed: `two`'s removal of b1 won, `one`'s binding is gone
			survivor, loserID = two, userID
		}
		_ = loserID
		l := survivor.Call(t, authenv.RoleSvc, "ListRoleBindings", map[string]any{"role": "admin"}).Must(t, fmt.Sprintf("round %d: survivor lists bindings", round))
		if n := len(authenv.List(l.Body, "bindings")); n != 1 {
			t.Fatalf("round %d: %d admin bindings remain, want exactly 1: %s", round, n, l.Raw)
		}
		survivor.Call(t, authenv.RoleSvc, "CreateRoleBinding", map[string]any{
			"principalId": "user:" + loserID, "role": "admin", "project": "*"}).Must(t, fmt.Sprintf("round %d: restore admin", round))
	}
}

func userIDOf(t *testing.T, c *authenv.Client) string {
	t.Helper()
	r := c.Call(t, authenv.AuthSvc, "WhoAmI", map[string]any{}).Must(t, "WhoAmI")
	id := authenv.Str(r.Body, "principal.id")
	return id[len("user:"):]
}

// TestRAUTH152NoEscalation: CreateRoleBinding succeeds only if every effective permission of the
// new binding is one of the caller's.
func TestRAUTH152NoEscalation(t *testing.T) {
	parity.Covers(t, "R-AUTH-152")
	s := authenv.Shared(t)

	s.NewRole(t, "esc-roles", authenv.Perm{Method: "bob.access.v1.RoleService/*", Project: "*"})
	s.NewRole(t, "esc-get", authenv.Perm{Method: "bob.build.v1.BuildService/GetBuild", Project: "*"})
	s.NewRole(t, "esc-cancel", authenv.Perm{Method: "bob.build.v1.BuildService/CancelBuild", Project: "*"})
	s.NewRole(t, "esc-viewer", authenv.Perm{Method: "bob.build.v1.BuildService/GetBuild", Project: "*"})
	s.NewRole(t, "esc-both",
		authenv.Perm{Method: "bob.build.v1.BuildService/GetBuild", Project: "*"},
		authenv.Perm{Method: "bob.build.v1.BuildService/CancelBuild", Project: "*"})
	s.NewRole(t, "esc-more",
		authenv.Perm{Method: "bob.build.v1.BuildService/GetBuild", Project: "*"},
		authenv.Perm{Method: "bob.build.v1.BuildService/RetryBuild", Project: "*"})

	callerID := s.NewUser(t, "esc-caller", authenv.Password)
	targetID := s.NewUser(t, "esc-target", authenv.Password)
	s.Bind(t, "user:"+callerID, "esc-roles", "p1")
	s.Bind(t, "user:"+callerID, "esc-get", "p1")
	caller := s.MustLogin(t, "esc-caller", authenv.Password)

	bindingsOf := func() []string {
		r := s.Admin().Call(t, authenv.RoleSvc, "ListRoleBindings", map[string]any{"principalId": "user:" + targetID}).Must(t, "ListRoleBindings")
		var out []string
		for _, b := range authenv.List(r.Body, "bindings") {
			m := b.(map[string]any)
			out = append(out, authenv.Str(m, "role")+"@"+authenv.Str(m, "project"))
		}
		return out
	}
	bind := func(role, project string) *authenv.Reply {
		return caller.Call(t, authenv.RoleSvc, "CreateRoleBinding", map[string]any{
			"principalId": "user:" + targetID, "role": role, "project": project})
	}
	const msg = "binding would grant permissions you do not have"
	denied := func(what string, r *authenv.Reply, wantMsg bool) {
		t.Helper()
		if r.Code != "permission_denied" || (wantMsg && r.Message != msg) {
			t.Errorf("%s: want PermissionDenied %q, got %s", what, msg, r)
		}
		if len(bindingsOf()) != 0 {
			t.Errorf("%s: a binding was created: %v", what, bindingsOf())
		}
	}

	if r := bind("esc-viewer", "p1"); !r.OK() {
		t.Fatalf("viewer(GetBuild)@p1: want OK, got %s", r)
	}
	if got := bindingsOf(); len(got) != 1 || got[0] != "esc-viewer@p1" {
		t.Fatalf("after the allowed binding the target has %v", got)
	}
	s.Admin().Call(t, authenv.RoleSvc, "DeleteRoleBinding", map[string]any{
		"id": firstBindingID(t, s, targetID), "project": "p1"}).Must(t, "cleanup")

	denied("admin@p1", bind("admin", "p1"), true)
	denied("viewer@*", bind("esc-viewer", "*"), false)
	denied("admin@*", bind("admin", "*"), false)
	denied("role with an extra permission (GetBuild + RetryBuild)@p1", bind("esc-more", "p1"), true)

	// permissions are checked one by one: the caller's effective set is the union of its bindings
	s.Bind(t, "user:"+callerID, "esc-cancel", "p1")
	if r := bind("esc-both", "p1"); !r.OK() {
		t.Errorf("GetBuild+CancelBuild@p1 with both held through two bindings: want OK, got %s", r)
	}
}

func firstBindingID(t *testing.T, s *authenv.Server, uid string) string {
	t.Helper()
	r := s.Admin().Call(t, authenv.RoleSvc, "ListRoleBindings", map[string]any{"principalId": "user:" + uid}).Must(t, "ListRoleBindings")
	for _, b := range authenv.List(r.Body, "bindings") {
		return authenv.Str(b.(map[string]any), "id")
	}
	t.Fatalf("no binding for %s", uid)
	return ""
}
