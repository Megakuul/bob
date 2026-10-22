package rbac

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"connectrpc.com/connect"

	"github.com/megakuul/bob/internal/api"
	"github.com/megakuul/bob/internal/store"
	"github.com/megakuul/bob/tests/rbac/ref"
	"github.com/megakuul/bob/tests/serverenv"
)

// roleMap is a RoleSource over fixed roles (admin is built in).
type roleMap map[string][]Permission

func (m roleMap) Permissions(_ context.Context, id string) ([]Permission, bool, error) {
	if id == Admin {
		return adminPermissions(), true, nil
	}
	ps, ok := m[id]
	return ps, ok, nil
}

func catalog(t *testing.T) *Catalog {
	t.Helper()
	rules, err := api.AllRules()
	if err != nil {
		t.Fatal(err)
	}
	return NewCatalog(rules)
}

func p(m, pr string) Permission { return Permission{Method: m, Project: pr} }

const (
	cancel   = "bob.build.v1.BuildService/CancelBuild"
	getBuild = "bob.build.v1.BuildService/GetBuild"
	retry    = "bob.build.v1.BuildService/RetryBuild"
	buildSvc = "bob.build.v1.BuildService/*"
	roleSvc  = "bob.access.v1.RoleService/*"
)

// TestAccessTable: the R-AUTH-002 rows of SPEC §6.4.1 plus the NotFound/PermissionDenied split.
func TestAccessTable(t *testing.T) {
	for i, c := range []struct {
		perms   []Permission
		bind    string
		method  string
		project string
		want    api.Access
	}{
		{[]Permission{p(buildSvc, "*")}, "p1", cancel, "p1", api.Allowed},
		{[]Permission{p(buildSvc, "*")}, "p1", cancel, "p2", api.NoAccess},
		{[]Permission{p(getBuild, "p1")}, "p2", getBuild, "p1", api.NoAccess},
		{[]Permission{p(getBuild, "p1")}, "p2", getBuild, "p2", api.NoAccess},
		{[]Permission{p("*", "*")}, "*", cancel, "p1", api.Allowed},
		{[]Permission{p("*", "*")}, "*", getBuild, "anything", api.Allowed},
		{[]Permission{p(getBuild, "*")}, "p1", cancel, "p1", api.Denied},
		{[]Permission{p(getBuild, "p1")}, "*", cancel, "p1", api.Denied},
		{[]Permission{p(getBuild, "p1")}, "*", getBuild, "p1", api.Allowed},
		{[]Permission{p(getBuild, "p1")}, "*", getBuild, "p2", api.NoAccess},
		{[]Permission{p(getBuild, "*")}, "*", getBuild, "*", api.Allowed}, // global
		{[]Permission{p(getBuild, "*")}, "p1", getBuild, "*", api.Denied}, // global: never NotFound
		{[]Permission{p(getBuild, "p1")}, "*", getBuild, "*", api.Denied}, // {M, p1} is not {M, *}
		{[]Permission{p("*", "p1")}, "p1", "bob.fleet.v1.FleetService/ListWorkers", "*", api.Denied},
		{nil, "*", getBuild, "p1", api.NoAccess},
	} {
		a := NewAuthorizer(roleMap{"r": c.perms}, catalog(t))
		got, err := a.Access(context.Background(), &api.Principal{Bindings: []api.Binding{{Role: "r", Project: c.bind}}}, c.method, c.project)
		if err != nil || got != c.want {
			t.Errorf("row %d %v @%s %s on %s: got %v (%v), want %v", i, c.perms, c.bind, c.method, c.project, got, err, c.want)
		}
	}
	a := NewAuthorizer(roleMap{}, catalog(t))
	if got, _ := a.Access(context.Background(), nil, getBuild, "p1"); got != api.NoAccess {
		t.Errorf("anonymous: %v", got)
	}
	if got, _ := a.Access(context.Background(), &api.Principal{Bindings: []api.Binding{{Role: "gone", Project: "*"}}}, getBuild, "p1"); got != api.NoAccess {
		t.Errorf("binding of a missing role: %v", got)
	}
	if got, _ := a.Access(context.Background(), &api.Principal{Bindings: []api.Binding{{Role: Admin, Project: "p1"}}}, cancel, "p1"); got != api.Allowed {
		t.Errorf("admin@p1 on p1: %v", got)
	}
}

// TestAccessAgainstReference compares Access with the independent reference evaluator of the
// RBAC suite (tests/rbac/ref) for every PROJECT and GLOBAL method of proto/ and random roles.
func TestAccessAgainstReference(t *testing.T) {
	root, err := serverenv.Root()
	if err != nil {
		t.Fatal(err)
	}
	cat, err := ref.Load(filepath.Join(root, "proto"))
	if err != nil {
		t.Fatal(err)
	}
	methods := []string{"*", buildSvc, roleSvc, getBuild, cancel}
	projects := []string{"*", "p1", "p2"}
	rng := rand.New(rand.NewPCG(1, 2))
	for _, m := range cat.Methods {
		methods = append(methods, m.Full())
	}
	for range 400 {
		roles := roleMap{}
		var refb []ref.Binding
		var bs []api.Binding
		for r := range 1 + rng.IntN(3) {
			id := fmt.Sprintf("r%d", r)
			var ps []Permission
			var rps []ref.Permission
			for range 1 + rng.IntN(3) {
				x := p(methods[rng.IntN(len(methods))], projects[rng.IntN(len(projects))])
				ps = append(ps, x)
				rps = append(rps, ref.Permission{Method: x.Method, Project: x.Project})
			}
			roles[id] = ps
			bp := projects[rng.IntN(len(projects))]
			bs = append(bs, api.Binding{Role: id, Project: bp})
			refb = append(refb, ref.Binding{Permissions: rps, Project: bp})
		}
		a := NewAuthorizer(roles, catalog(t))
		for _, m := range cat.Methods {
			if m.Kind != ref.KindProject && m.Kind != ref.KindGlobal {
				continue
			}
			for _, pr := range []string{"p1", "p2"} {
				want := ref.Expect(m, ref.User, refb, pr)
				project := pr
				if m.Kind == ref.KindGlobal {
					project = "*" // the interceptor asks GLOBAL rules on "*"
				}
				got, err := a.Access(context.Background(), &api.Principal{Bindings: bs}, m.Full(), project)
				if err != nil {
					t.Fatal(err)
				}
				ok := map[ref.Outcome]api.Access{ref.Allow: api.Allowed, ref.PermissionDenied: api.Denied, ref.NotFound: api.NoAccess}[want] == got
				if !ok {
					t.Fatalf("%v: %s on %s: got %v, reference %v", refb, m.Full(), pr, got, want)
				}
			}
		}
	}
}

// TestCheckGrant: the R-AUTH-152 acceptance cases.
func TestCheckGrant(t *testing.T) {
	roles := roleMap{
		"esc-roles":  {p(roleSvc, "*")},
		"esc-get":    {p(getBuild, "*")},
		"esc-cancel": {p(cancel, "*")},
		"esc-viewer": {p(getBuild, "*")},
		"esc-both":   {p(getBuild, "*"), p(cancel, "*")},
		"esc-more":   {p(getBuild, "*"), p(retry, "*")},
		"svc":        {p(buildSvc, "p1")},
	}
	a := NewAuthorizer(roles, catalog(t))
	caller := &api.Principal{Bindings: []api.Binding{{Role: "esc-roles", Project: "p1"}, {Role: "esc-get", Project: "p1"}}}
	ctx := context.Background()
	denied := func(err error) bool {
		var ce *connect.Error
		return errors.As(err, &ce) && ce.Code() == connect.CodePermissionDenied && ce.Message() == "binding would grant permissions you do not have"
	}
	if err := a.CheckGrant(ctx, caller, "esc-viewer", "p1"); err != nil {
		t.Errorf("viewer@p1: %v", err)
	}
	for _, c := range []struct{ role, project string }{
		{Admin, "p1"}, {"esc-viewer", "*"}, {Admin, "*"}, {"esc-more", "p1"}, {"esc-viewer", "p2"}, {"esc-both", "p1"},
	} {
		if err := a.CheckGrant(ctx, caller, c.role, c.project); !denied(err) {
			t.Errorf("%s@%s: want PermissionDenied, got %v", c.role, c.project, err)
		}
	}
	caller.Bindings = append(caller.Bindings, api.Binding{Role: "esc-cancel", Project: "p1"})
	if err := a.CheckGrant(ctx, caller, "esc-both", "p1"); err != nil {
		t.Errorf("GetBuild+CancelBuild@p1 held through two bindings: %v", err)
	}
	// a service wildcard is covered by holding every method of the service
	if err := a.CheckGrant(ctx, caller, "svc", "p1"); !denied(err) {
		t.Errorf("BuildService/*@p1 with only two of its methods: %v", err)
	}
	all := &api.Principal{Bindings: []api.Binding{{Role: Admin, Project: "*"}}}
	for _, r := range []string{Admin, "svc", "esc-more"} {
		if err := a.CheckGrant(ctx, all, r, "*"); err != nil {
			t.Errorf("admin@* grants %s@*: %v", r, err)
		}
	}
	if err := a.CheckGrant(ctx, all, "nope", "p1"); err == nil {
		t.Errorf("unknown role: no error")
	}
	if err := a.CheckGrant(ctx, nil, "esc-viewer", "p1"); !denied(err) {
		t.Errorf("no caller: %v", err)
	}
	if err := a.CheckRolePermissions(ctx, caller, []Permission{p(getBuild, "*")}); err == nil {
		t.Errorf("adding {GetBuild, *} with GetBuild only on p1 must be refused")
	}
	if err := a.CheckRolePermissions(ctx, all, []Permission{p("*", "*")}); err != nil {
		t.Errorf("admin@* adds {*, *}: %v", err)
	}
}

func TestCatalog(t *testing.T) {
	c := catalog(t)
	srv, err := api.New(api.Config{Authn: api.Chain{}})
	if err != nil {
		t.Fatal(err)
	}
	all, err := api.AllRules()
	if err != nil {
		t.Fatal(err)
	}
	for proc := range srv.Rules() {
		if all[proc] == nil {
			t.Errorf("AllRules misses %s", proc)
		}
	}
	if len(srv.Rules()) != len(all) {
		t.Fatalf("AllRules has %d methods, the server serves %d", len(all), len(srv.Rules()))
	}
	var want []string
	for _, e := range srv.Catalog() {
		want = append(want, e.Method+" "+e.Kind)
	}
	var got []string
	for _, e := range c.Entries() {
		got = append(got, e.Method+" "+e.Kind)
	}
	if !slices.Equal(got, want) {
		t.Errorf("catalogue differs from the server's ListPermissionCatalog")
	}
	for _, m := range c.Methods() {
		if c.Kind(m) == "WORKER" {
			t.Errorf("worker method %s in the catalogue", m)
		}
	}
	if n := len(c.Expand(buildSvc)); n < 5 {
		t.Errorf("BuildService/* expands to %d methods", n)
	}
	for _, bad := range []Permission{p("bob.build.v1.BuildService/Nope", "*"), p("bob.nope.v1.X/*", "*"), p("bob.worker.v1.WorkerControlService/Attach", "*"), p(getBuild, "P1"), p(getBuild, "")} {
		if _, err := c.CheckPermissions([]Permission{bad}); err == nil {
			t.Errorf("%v accepted", bad)
		}
	}
	got2, err := c.CheckPermissions([]Permission{p(getBuild, "p1"), p("*", "*"), p(getBuild, "p1")})
	if err != nil || len(got2) != 2 || got2[0].Method != "*" {
		t.Errorf("CheckPermissions = %v, %v", got2, err)
	}
}

// TestOIDC: R-AUTH-004 — mappings replace only oidc-sourced bindings.
func TestOIDC(t *testing.T) {
	ms := []store.OidcMappingEntry{
		{ID: "m1", Provider: "idp", Claim: "groups", Value: "dev", Role: "dev", Project: "p1"},
		{ID: "m2", Provider: "idp", Claim: "team", Value: "ops", Role: "ops", Project: "*"},
		{ID: "m3", Provider: "other", Claim: "groups", Value: "dev", Role: "x", Project: "*"},
		{ID: "m4", Provider: "idp", Claim: "groups", Value: "viewers", Role: "viewer", Project: "*"},
	}
	got := Match(ms, "idp", map[string]any{"groups": []any{"dev", "qa"}, "team": "ops"})
	if want := []api.Binding{{Role: "dev", Project: "p1"}, {Role: "ops", Project: "*"}}; !slices.Equal(got, want) {
		t.Errorf("Match = %v, want %v", got, want)
	}
	if got := Match(ms, "idp", map[string]any{"groups": "devs", "team": []any{1, "x"}}); len(got) != 0 {
		t.Errorf("no match expected, got %v", got)
	}
	now := time.Date(2026, 10, 22, 0, 0, 0, 0, time.UTC)
	n := 0
	newID := func() string { n++; return fmt.Sprintf("n%d", n) }
	cur := []store.RoleBindingEntry{
		{ID: "a", Role: "dev", Project: "p1", Source: SourceOIDC},
		{ID: "b", Role: "viewer", Project: "*", Source: SourceManual},
	}
	// the IdP drops dev: bindings are exactly viewer@*
	after := ReplaceOIDC(cur, Match(ms, "idp", map[string]any{"groups": []any{"qa"}}), newID, now)
	if len(after) != 1 || after[0].ID != "b" {
		t.Errorf("after dropping dev: %v", after)
	}
	// dev kept (same id), viewer matched but held manually (stays manual), ops added
	after = ReplaceOIDC(cur, Match(ms, "idp", map[string]any{"groups": []any{"dev", "viewers"}, "team": "ops"}), newID, now)
	want := []store.RoleBindingEntry{cur[1], cur[0], {ID: "n1", Role: "ops", Project: "*", Source: SourceOIDC, Created: now}}
	if !slices.Equal(after, want) {
		t.Errorf("after = %v, want %v", after, want)
	}
}

func TestScope(t *testing.T) {
	a := NewAuthorizer(roleMap{"r": {p(getBuild, "p2"), p(buildSvc, "*"), p(roleSvc, "p3")}}, catalog(t))
	s, err := a.Scope(context.Background(), &api.Principal{Bindings: []api.Binding{{Role: "r", Project: "p1"}}}, getBuild)
	if err != nil || s.All || !slices.Equal(s.Projects, []string{"p1"}) || s.Contains("*") || !s.Contains("p1") {
		t.Errorf("scope = %+v, %v", s, err)
	}
	s, _ = a.Scope(context.Background(), &api.Principal{Bindings: []api.Binding{{Role: "r", Project: "*"}}}, getBuild)
	if !s.All || !s.Contains("*") {
		t.Errorf("scope @* = %+v", s)
	}
}
