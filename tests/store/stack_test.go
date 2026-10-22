package store

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/megakuul/bob/tests/harness"
	"github.com/megakuul/bob/tests/parity"
	"github.com/megakuul/bob/tests/serverenv/s3count"
	"github.com/megakuul/bob/tests/store/env"
	"github.com/megakuul/bob/tools/devstack/stack"
)

// startE2E starts a stack with one orchestrator and one worker and waits until the worker is
// active. It returns nil after failing t as red while the server modes are stubs.
func startE2E(t *testing.T) (*stack.Stack, env.Extra) {
	t.Helper()
	s := env.StartStack(t, stack.Options{Orchestrators: 1, Workers: 1})
	if s == nil {
		return nil, env.Extra{}
	}
	x := env.MustExtra(t, s, "api", "admin")
	ok := env.Wait(30*time.Second, func() bool {
		c := env.RPC(t, x, "bob.fleet.v1.FleetService/ListWorkers", "", map[string]any{"page_size": 10})
		return c.Status == 200 && strings.Contains(string(c.Body), "WORKER_STATE_ACTIVE")
	})
	if !ok {
		t.Fatalf("the worker did not become active within 30 s (R-FLEET-001)")
	}
	return s, x
}

// TestRSTORE100NoListOnControlAndFleet: with the stack running its controllers (registration
// included), the counting proxy sees 0 LIST on bob-control and bob-fleet. (The full e2e suite
// of tests/server ends every test the same way.)
func TestRSTORE100NoListOnControlAndFleet(t *testing.T) {
	parity.Covers(t, "R-STORE-100")
	s, x := startE2E(t)
	if s == nil {
		return
	}
	// Touch every list page the UI has (R-API-151) so the read paths are covered too.
	for _, m := range []string{
		"bob.analytics.v1.AnalyticsService/GetOverview", "bob.project.v1.ProjectService/ListProjects",
		"bob.build.v1.BuildService/ListBuilds", "bob.fleet.v1.FleetService/ListWorkers",
		"bob.fleet.v1.FleetService/ListOrchestrators", "bob.access.v1.UserService/ListUsers",
		"bob.access.v1.RoleService/ListRoles", "bob.access.v1.ServiceAccountService/ListServiceAccounts",
		"bob.access.v1.OidcService/ListOidcMappings", "bob.release.v1.ReleaseService/ListReleases",
		"bob.registry.v1.RegistryService/ListRepositories",
	} {
		if c := env.RPC(t, x, m, "", map[string]any{"page_size": 50}); c.Status != 200 {
			t.Errorf("%s = %d %s", m, c.Status, c.Body)
		}
	}
	time.Sleep(15 * time.Second) // controllers run: membership, registrar, reconciler, retention
	c := env.ProxyCounts(t, s)
	for _, b := range []string{"bob-control", "bob-fleet"} {
		if n := c.Lists(b); n != 0 {
			t.Errorf("%d LIST requests on %s, want 0", n, b)
		}
		for _, r := range c.Rows {
			if r.Bucket == b && r.Op == s3count.OpList {
				t.Errorf("LIST on %s prefix %q by %q (sweep %q) x%d: LIST is allowed only on analytics and artifacts", b, r.Prefix, r.Cred, r.Sweep, r.Count)
			}
		}
	}
}

// TestRSTORE101ListsAreSweepsOnly: every LIST the servers make is on the analytics or artifacts
// bucket and carries the User-Agent suffix bob-sweep/<controller>.
func TestRSTORE101ListsAreSweepsOnly(t *testing.T) {
	parity.Covers(t, "R-STORE-101")
	s, _ := startE2E(t)
	if s == nil {
		return
	}
	time.Sleep(20 * time.Second)
	for _, r := range env.ProxyCounts(t, s).Rows {
		if r.Op != s3count.OpList {
			continue
		}
		if r.Bucket != "bob-analytics" && r.Bucket != "bob-artifacts" {
			t.Errorf("LIST on %s (prefix %q) x%d: only analytics and artifacts may be listed", r.Bucket, r.Prefix, r.Count)
		}
		if r.Sweep == "" {
			t.Errorf("LIST on %s (prefix %q) x%d has no bob-sweep/<controller> User-Agent suffix", r.Bucket, r.Prefix, r.Count)
		}
	}
}

// TestRSTORE004ReadPlan: the access plan of §05.5.2, page 1, counted through the proxy on a
// warm cache (the second call; auth caches live 10 s). Each row's budget is the table's
// "requests (page 1)" column on control+fleet.
func TestRSTORE004ReadPlan(t *testing.T) {
	parity.Covers(t, "R-STORE-004")
	s, x := startE2E(t)
	if s == nil {
		return
	}
	// A project and a build exist for the project-scoped rows.
	dir := harness.Fixture(t, "acme")
	if r := env.RemoteBuild(t, x, dir, "//..."); r.Exit != 0 {
		t.Fatalf("remote build of acme: exit %d\n%s", r.Exit, r.Combined())
	}
	var projects struct{ Projects []struct{ ID string } }
	if c := env.RPC(t, x, "bob.project.v1.ProjectService/ListProjects", "", map[string]any{"page_size": 50}); c.Status != 200 || json.Unmarshal(c.Body, &projects) != nil || len(projects.Projects) == 0 {
		t.Fatalf("no project after a remote build: %d %s", c.Status, c.Body)
	}
	pid := projects.Projects[0].ID
	var builds struct{ Builds []struct{ ID string } }
	if c := env.RPC(t, x, "bob.build.v1.BuildService/ListBuilds", "", map[string]any{"project_id": pid, "page_size": 50}); c.Status != 200 || json.Unmarshal(c.Body, &builds) != nil || len(builds.Builds) == 0 {
		t.Fatalf("no build listed for %s: %d %s", pid, c.Status, c.Body)
	}
	bid := builds.Builds[0].ID

	rows := []struct {
		page   string
		method string
		req    map[string]any
		max    int
	}{
		{"Overview (3 + classes; one class on this stack)", "bob.analytics.v1.AnalyticsService/GetOverview", map[string]any{}, 3 + 1},
		{"Projects (1 + pages)", "bob.project.v1.ProjectService/ListProjects", map[string]any{"page_size": 50}, 2},
		{"Builds of a project (2-3)", "bob.build.v1.BuildService/ListBuilds", map[string]any{"project_id": pid, "page_size": 50}, 3},
		{"Build detail (2 + live jobs; terminal build)", "bob.build.v1.BuildService/GetBuild", map[string]any{"id": bid}, 2},
		{"Releases of a project (2-3)", "bob.release.v1.ReleaseService/ListReleases", map[string]any{"project_id": pid, "page_size": 50}, 3},
		{"Registry (2)", "bob.registry.v1.RegistryService/ListRepositories", map[string]any{"page_size": 50}, 2},
		{"Fleet: workers (N_widx = 4)", "bob.fleet.v1.FleetService/ListWorkers", map[string]any{"page_size": 50}, 4},
		{"Fleet: orchestrators (1 + members)", "bob.fleet.v1.FleetService/ListOrchestrators", map[string]any{}, 1 + 1},
		{"Access: users (2)", "bob.access.v1.UserService/ListUsers", map[string]any{"page_size": 50}, 2},
		{"Access: roles (1)", "bob.access.v1.RoleService/ListRoles", map[string]any{}, 1},
		{"Access: service accounts (1)", "bob.access.v1.ServiceAccountService/ListServiceAccounts", map[string]any{}, 1},
		{"Access: OIDC mappings (1)", "bob.access.v1.OidcService/ListOidcMappings", map[string]any{}, 1},
	}
	for _, r := range rows {
		env.RPC(t, x, r.method, "", r.req) // warm the caches
		env.ResetProxy(t, s)
		if c := env.RPC(t, x, r.method, "", r.req); c.Status != 200 {
			t.Errorf("%s: %s = %d %s", r.page, r.method, c.Status, c.Body)
			continue
		}
		c := env.ProxyCounts(t, s)
		n := env.Total(c, "bob-control") + env.Total(c, "bob-fleet")
		if n > r.max {
			t.Errorf("%s: %d requests on control+fleet, the read plan allows %d", r.page, n, r.max)
		}
		if l := c.Lists("bob-control") + c.Lists("bob-fleet"); l != 0 {
			t.Errorf("%s: %d LIST requests", r.page, l)
		}
	}
}
