package rbac

import (
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/megakuul/bob/tests/auth/authenv"
	"github.com/megakuul/bob/tests/parity"
	"github.com/megakuul/bob/tests/rbac/ref"
	"github.com/megakuul/bob/tests/serverenv"
)

// summary is the rule table of SPEC §6.5.19, written out by hand. Entries are full method names
// or "<pkg>.<Service>/*".
var summary = map[ref.Kind][]string{
	ref.KindPublic: {
		"bob.auth.v1.AuthService/GetServerInfo", "bob.auth.v1.AuthService/LoginPassword",
		"bob.cache.v1.CacheService/GetPath", "bob.release.v1.ReleaseService/ListReleases",
		"bob.release.v1.ReleaseService/GetRelease", "bob.release.v1.ReleaseService/ResolveRelease",
		"bob.registry.v1.RegistryService/*", "bob.settings.v1.SettingsService/ListSigningKeys",
	},
	ref.KindAuthenticated: {
		"bob.auth.v1.AuthService/WhoAmI", "bob.auth.v1.AuthService/Logout", "bob.auth.v1.AuthService/ChangePassword",
		"bob.auth.v1.AuthService/ListMySessions", "bob.auth.v1.AuthService/RevokeMySession",
		"bob.access.v1.RoleService/GetRole", "bob.access.v1.RoleService/ListRoles",
		"bob.access.v1.RoleService/ListPermissionCatalog", "bob.analytics.v1.AnalyticsService/GetOverview",
	},
	ref.KindListFiltered: {
		"bob.project.v1.ProjectService/ListProjects", "bob.build.v1.BuildService/ListBuilds",
		"bob.access.v1.RoleService/ListRoleBindings", "bob.cache.v1.CacheService/ListTopPackages",
		"bob.analytics.v1.AnalyticsService/RecentFailures",
	},
	ref.KindProject: {
		"bob.project.v1.ProjectService/GetProject", "bob.project.v1.ProjectService/UpdateProject",
		"bob.project.v1.ProjectService/ListPipelines", "bob.build.v1.BuildService/CreateBuild",
		"bob.build.v1.BuildService/GetBuild", "bob.build.v1.BuildService/CancelBuild",
		"bob.build.v1.BuildService/RetryBuild", "bob.build.v1.BuildService/RerunBuild",
		"bob.build.v1.BuildService/GetBuildGraph", "bob.build.v1.BuildService/WatchBuild",
		"bob.build.v1.JobService/*", "bob.log.v1.LogService/*",
		"bob.release.v1.ReleaseService/UpdateRelease", "bob.release.v1.ReleaseService/DeleteRelease",
		"bob.access.v1.RoleService/CreateRoleBinding", "bob.access.v1.RoleService/DeleteRoleBinding",
		"bob.analytics.v1.AnalyticsService/PackageDurations", "bob.analytics.v1.AnalyticsService/BuildHistory",
	},
	ref.KindGlobal: {
		"bob.access.v1.UserService/*", "bob.access.v1.RoleService/CreateRole", "bob.access.v1.RoleService/UpdateRole",
		"bob.access.v1.RoleService/DeleteRole", "bob.access.v1.ServiceAccountService/*", "bob.access.v1.OidcService/*",
		"bob.project.v1.ProjectService/CreateProject", "bob.project.v1.ProjectService/DeleteProject",
		"bob.fleet.v1.FleetService/*", "bob.cache.v1.CacheService/GetCacheStats",
		"bob.settings.v1.SettingsService/GetSettings", "bob.settings.v1.SettingsService/UpdateSettings",
		"bob.settings.v1.SettingsService/CreateSigningKey", "bob.settings.v1.SettingsService/RetireSigningKey",
		"bob.forge.v1.ForgeService/*", "bob.notify.v1.NotifyService/*",
	},
	ref.KindWorker: {
		"bob.worker.v1.WorkerControlService/Attach", "bob.worker.v1.WorkerControlService/GetCheckoutCredential",
	},
}

func protoRoot(t *testing.T) string {
	t.Helper()
	root, err := serverenv.Root()
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Join(root, "proto")
}

// TestRAUTH006EveryRPCHasRule: every method carries a rule of a real kind, PROJECT rules name a
// string field of the request, the check demonstrably fails for a service without options, the
// rules equal the SPEC summary table, and the server refuses to serve an RPC without a rule.
func TestRAUTH006EveryRPCHasRule(t *testing.T) {
	parity.Covers(t, "R-AUTH-006")
	cat := catalog(t)

	t.Run("proto", func(t *testing.T) {
		for _, p := range cat.Problems() {
			t.Errorf("proto problem: %s", p)
		}
	})
	t.Run("fixture-without-options-fails", func(t *testing.T) {
		fx, err := ref.Load(filepath.Join(protoRoot(t), "..", "tests", "rbac", "ref", "testdata"))
		if err != nil {
			t.Fatal(err)
		}
		if len(fx.Problems()) < 4 {
			t.Fatalf("the fixture service without options passes the rule check: %v", fx.Problems())
		}
	})
	t.Run("matches-SPEC-summary", func(t *testing.T) {
		want := map[string]ref.Kind{}
		for kind, list := range summary {
			for _, e := range list {
				if prefix, ok := strings.CutSuffix(e, "/*"); ok {
					for _, m := range cat.Methods {
						if m.Package+"."+m.Service == prefix {
							want[m.Full()] = kind
						}
					}
					continue
				}
				want[e] = kind
			}
		}
		var names []string
		for _, m := range cat.Methods {
			names = append(names, m.Full())
			k, ok := want[m.Full()]
			if !ok {
				t.Errorf("%s (%s) is not in the SPEC §6.5.19 table", m.Full(), m.Kind)
			} else if k != m.Kind {
				t.Errorf("%s: proto says %s, SPEC §6.5.19 says %s", m.Full(), m.Kind, k)
			}
		}
		sort.Strings(names)
		for e, k := range want {
			if i := sort.SearchStrings(names, e); i == len(names) || names[i] != e {
				t.Errorf("SPEC §6.5.19 lists %s (%s) but no proto method exists", e, k)
			}
		}
	})
	t.Run("server-permission-catalog", func(t *testing.T) {
		s := authenv.Shared(t)
		r := s.Admin().Call(t, authenv.RoleSvc, "ListPermissionCatalog", map[string]any{}).Must(t, "ListPermissionCatalog")
		got := map[string]string{}
		for _, e := range authenv.List(r.Body, "entries") {
			m, _ := e.(map[string]any)
			got[authenv.Str(m, "method")] = authenv.Str(m, "kind")
		}
		for _, m := range cat.Methods {
			if m.Kind == ref.KindWorker {
				if _, ok := got[m.Full()]; ok {
					t.Errorf("catalog lists the WORKER method %s", m.Full())
				}
				continue
			}
			if got[m.Full()] != string(m.Kind) {
				t.Errorf("catalog entry %s = %q, proto rule is %s", m.Full(), got[m.Full()], m.Kind)
			}
		}
	})
	t.Run("principal-without-bindings-never-ok", func(t *testing.T) {
		s := authenv.Shared(t)
		s.NewUser(t, "r6-nobody", authenv.Password)
		c := s.MustLogin(t, "r6-nobody", authenv.Password)
		for _, m := range cat.Methods {
			if m.Kind != ref.KindProject && m.Kind != ref.KindGlobal {
				continue
			}
			body := map[string]any{}
			if m.Kind == ref.KindProject && (m.Resolve == "" || m.Resolve == "UNSPECIFIED") && m.ProjectField != "" {
				body[m.ProjectField] = "p1"
			}
			got := call(t, c, m, body)
			if got.OK() || (got.Code != "not_found" && got.Code != "permission_denied") {
				t.Errorf("%s without bindings: want NotFound or PermissionDenied, got %s", m.Full(), got)
			}
		}
	})
}
