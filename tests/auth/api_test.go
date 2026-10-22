package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/megakuul/bob/tests/auth/authenv"
	"github.com/megakuul/bob/tests/harness"
	"github.com/megakuul/bob/tests/parity"
	"github.com/megakuul/bob/tests/rbac/ref"
	"github.com/megakuul/bob/tests/serverenv"
	"github.com/megakuul/bob/tests/serverenv/s3count"
)

func repoRoot(t *testing.T) string {
	t.Helper()
	root, err := serverenv.Root()
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func catalog(t *testing.T) *ref.Catalog {
	t.Helper()
	c, err := ref.Load(filepath.Join(repoRoot(t), "proto"))
	if err != nil {
		t.Fatalf("harness error: %v", err)
	}
	return c
}

// buf runs the pinned buf (go tool, tools module) in dir.
func buf(t *testing.T, dir string, args ...string) (string, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "go", append([]string{"tool", "buf"}, args...)...)
	cmd.Dir = dir
	// The repo's go.work resolves the pinned tools also when dir is a scratch copy outside the module.
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0", "GOWORK="+filepath.Join(repoRoot(t), "go.work"))
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	return out.String(), err
}

func copyTree(t *testing.T, src, dst string) {
	t.Helper()
	err := filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		if d.IsDir() {
			return os.MkdirAll(filepath.Join(dst, rel), 0o755)
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(dst, rel), b, 0o644)
	})
	if err != nil {
		t.Fatalf("harness error: copy %s: %v", src, err)
	}
}

// TestRAPI001ConnectAndBuf: buf lint passes, the generated trees equal the committed ones, and
// WhoAmI answers the same through every client.
func TestRAPI001ConnectAndBuf(t *testing.T) {
	parity.Covers(t, "R-API-001")
	root := repoRoot(t)

	t.Run("buf-lint", func(t *testing.T) {
		if out, err := buf(t, filepath.Join(root, "proto"), "lint"); err != nil {
			t.Errorf("buf lint: %v\n%s", err, out)
		}
	})

	t.Run("generated-trees-committed", func(t *testing.T) {
		goGen := filepath.Join(root, "internal", "gen", "bob", "auth", "v1")
		tsGen := filepath.Join(root, "web", "src", "lib", "gen", "bob", "auth", "v1")
		for _, d := range []string{goGen, tsGen} {
			if _, err := os.Stat(d); err != nil {
				harness.RedFeature(t, "buf generate output is not committed ("+strings.TrimPrefix(d, root+"/")+" missing)")
				return
			}
		}
		// regenerate into a scratch copy: outputs are relative to buf.gen.yaml (../internal/gen, ../web/src/lib/gen)
		tmp := t.TempDir()
		copyTree(t, filepath.Join(root, "proto"), filepath.Join(tmp, "proto"))
		if out, err := buf(t, filepath.Join(tmp, "proto"), "generate"); err != nil {
			t.Fatalf("harness error: buf generate: %v\n%s", err, out)
		}
		for _, rel := range []string{"internal/gen", "web/src/lib/gen"} {
			want, got := map[string][]byte{}, map[string][]byte{}
			for base, m := range map[string]map[string][]byte{root: want, tmp: got} {
				_ = filepath.WalkDir(filepath.Join(base, rel), func(p string, d fs.DirEntry, err error) error {
					if err == nil && !d.IsDir() {
						b, _ := os.ReadFile(p)
						r, _ := filepath.Rel(base, p)
						m[r] = b
					}
					return nil
				})
			}
			if len(got) == 0 {
				t.Errorf("buf generate wrote nothing into %s", rel)
			}
			for k, v := range got {
				if !bytes.Equal(want[k], v) {
					t.Errorf("%s differs from what buf generate writes", k)
				}
			}
			for k := range want {
				if _, ok := got[k]; !ok && !strings.HasSuffix(k, "_test.go") {
					t.Errorf("%s is committed but not generated", k)
				}
			}
		}
	})

	t.Run("whoami-same-everywhere", func(t *testing.T) {
		s := authenv.Shared(t)
		whoami := map[string]any{}
		// Connect over POST with JSON, and Connect GET (WhoAmI is NO_SIDE_EFFECTS)
		post := s.Admin().Call(t, authenv.AuthSvc, "WhoAmI", whoami).Must(t, "WhoAmI (POST)")
		code, _, raw := s.Admin().Get(t, "/api/bob.auth.v1.AuthService/WhoAmI?connect=v1&encoding=json&message=%7B%7D", nil)
		if code != 200 {
			t.Fatalf("Connect GET WhoAmI: HTTP %d %.200s", code, raw)
		}
		var get map[string]any
		if err := json.Unmarshal(raw, &get); err != nil {
			t.Fatalf("Connect GET answer is not JSON: %v: %s", err, raw)
		}
		if !reflect.DeepEqual(post.Body, get) {
			t.Errorf("WhoAmI differs between POST and GET:\n%s\n%s", post.Raw, raw)
		}
		es := connectES(t, root, s)
		if !reflect.DeepEqual(post.Body, es) {
			t.Errorf("WhoAmI differs between the connect-go style client and connect-es:\n%s\n%v", post.Raw, es)
		}
	})
}

// connectES calls AuthService/WhoAmI with the generated TypeScript client (connect-es) under the
// pinned Node and returns the JSON-decoded answer. Until web/src/lib/gen exists the test is red.
func connectES(t *testing.T, root string, s *authenv.Server) map[string]any {
	t.Helper()
	gen := filepath.Join(root, "web", "src", "lib", "gen")
	if _, err := os.Stat(filepath.Join(gen, "bob", "auth", "v1", "auth_pb.ts")); err != nil {
		harness.RedFeature(t, "connect-es client (web/src/lib/gen/bob/auth/v1/auth_pb.ts is not generated)")
		t.FailNow()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	node, err := serverenv.NixTool(ctx, "nodejs")
	if err != nil {
		t.Fatalf("harness error: %v", err)
	}
	dir := t.TempDir()
	for link, target := range map[string]string{"gen": gen, "node_modules": filepath.Join(root, "web", "node_modules")} {
		if err := os.Symlink(target, filepath.Join(dir, link)); err != nil {
			t.Fatalf("harness error: %v", err)
		}
	}
	script := `import { createClient } from "@connectrpc/connect";
import { createConnectTransport } from "@connectrpc/connect-node";
import { toJson } from "@bufbuild/protobuf";
import { AuthService, WhoAmIResponseSchema } from "./gen/bob/auth/v1/auth_pb.ts";
const [, , base, token] = process.argv;
const transport = createConnectTransport({
  baseUrl: base + "/api", httpVersion: "1.1",
  interceptors: [(next) => async (req) => { req.header.set("Authorization", "Bearer " + token); return next(req); }],
});
const res = await createClient(AuthService, transport).whoAmI({});
console.log(JSON.stringify(toJson(WhoAmIResponseSchema, res)));
`
	if err := os.WriteFile(filepath.Join(dir, "whoami.mjs"), []byte(script), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(ctx, filepath.Join(node, "bin", "node"), "--experimental-transform-types", "whoami.mjs", s.URL, s.AdminToken)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("connect-es WhoAmI: %v\n%s", err, out)
	}
	var m map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(out), &m); err != nil {
		t.Fatalf("connect-es answer is not JSON: %v: %s", err, out)
	}
	return m
}

// resources is the R-API-002 resource list with the methods its service must have (SPEC §6.5.19
// coverage table and §6.4 proto text): full CRUD where user-managed, read-only where
// system-produced.
var resources = []struct {
	name    string
	service string
	methods []string
}{
	{"projects", "bob.project.v1.ProjectService", []string{"CreateProject", "GetProject", "ListProjects", "UpdateProject", "DeleteProject", "ListPipelines"}},
	{"builds", "bob.build.v1.BuildService", []string{"CreateBuild", "GetBuild", "ListBuilds", "CancelBuild", "RetryBuild", "RerunBuild", "GetBuildGraph", "WatchBuild"}},
	{"jobs", "bob.build.v1.JobService", []string{"GetJob", "ListJobs"}},
	{"workers", "bob.fleet.v1.FleetService", []string{"ListWorkers", "GetWorker", "DrainWorker", "UndrainWorker", "RevokeWorker", "ListOrchestrators"}},
	{"releases", "bob.release.v1.ReleaseService", []string{"ListReleases", "GetRelease", "ResolveRelease", "UpdateRelease", "DeleteRelease"}},
	{"images", "bob.registry.v1.RegistryService", []string{"ListRepositories", "GetRepository", "ListTags"}},
	{"cache entries", "bob.cache.v1.CacheService", []string{"GetPath", "ListTopPackages", "GetCacheStats"}},
	{"users", "bob.access.v1.UserService", []string{"CreateUser", "GetUser", "ListUsers", "UpdateUser", "DeleteUser", "SetUserPassword"}},
	{"roles", "bob.access.v1.RoleService", []string{"CreateRole", "GetRole", "ListRoles", "UpdateRole", "DeleteRole", "CreateRoleBinding", "DeleteRoleBinding", "ListRoleBindings"}},
	{"permissions", "bob.access.v1.RoleService", []string{"ListPermissionCatalog"}},
	{"service accounts", "bob.access.v1.ServiceAccountService", []string{"CreateServiceAccount", "GetServiceAccount", "ListServiceAccounts", "UpdateServiceAccount", "DeleteServiceAccount", "CreateServiceAccountToken", "ListServiceAccountTokens", "RevokeServiceAccountToken"}},
	{"OIDC mappings", "bob.access.v1.OidcService", []string{"CreateOidcMapping", "ListOidcMappings", "DeleteOidcMapping", "CreateOidcProvider", "GetOidcProvider", "ListOidcProviders", "UpdateOidcProvider", "DeleteOidcProvider"}},
	{"settings", "bob.settings.v1.SettingsService", []string{"GetSettings", "UpdateSettings", "ListSigningKeys", "CreateSigningKey", "RetireSigningKey"}},
}

// TestRAPI002Resources asserts, from the proto descriptors, that every resource has its RPCs.
func TestRAPI002Resources(t *testing.T) {
	parity.Covers(t, "R-API-002")
	cat := catalog(t)
	for _, r := range resources {
		t.Run(r.name, func(t *testing.T) {
			for _, m := range r.methods {
				if _, ok := cat.Find(r.service + "/" + m); !ok {
					t.Errorf("%s: %s/%s is missing from the descriptors", r.name, r.service, m)
				}
			}
		})
	}
	// system-produced resources are read-only: no Create/Update/Delete of workers, jobs, images
	for _, ro := range []string{"bob.fleet.v1.FleetService", "bob.build.v1.JobService", "bob.registry.v1.RegistryService"} {
		for _, m := range cat.Methods {
			if m.Package+"."+m.Service != ro {
				continue
			}
			for _, w := range []string{"Create", "Update", "Delete"} {
				if strings.HasPrefix(m.Name, w) {
					t.Errorf("%s is system-produced but has %s", ro, m.Name)
				}
			}
		}
	}
}

// TestRAPI003BufBreaking: renumbering a field fails `buf breaking`, the unchanged tree passes.
func TestRAPI003BufBreaking(t *testing.T) {
	parity.Covers(t, "R-API-003")
	root := repoRoot(t)
	tmp := t.TempDir()
	base, changed := filepath.Join(tmp, "base"), filepath.Join(tmp, "changed")
	copyTree(t, filepath.Join(root, "proto"), base)
	copyTree(t, filepath.Join(root, "proto"), changed)

	if out, err := buf(t, filepath.Join(root, "proto"), "breaking", changed, "--against", base); err != nil {
		t.Errorf("the unchanged tree fails buf breaking: %v\n%s", err, out)
	}

	file := filepath.Join(changed, "bob", "auth", "v1", "auth.proto")
	b, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	const from, to = "string login = 1;\n  string password = 2;", "string login = 7;\n  string password = 2;"
	if !strings.Contains(string(b), from) {
		t.Fatalf("harness error: auth.proto no longer has the LoginPasswordRequest fields the fixture renumbers")
	}
	if err := os.WriteFile(file, []byte(strings.Replace(string(b), from, to, 1)), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := buf(t, filepath.Join(root, "proto"), "breaking", changed, "--against", base)
	if err == nil {
		t.Errorf("renumbering LoginPasswordRequest.login passed buf breaking")
	} else if !strings.Contains(out, "FIELD_SAME_NUMBER") && !strings.Contains(out, "login") {
		t.Errorf("buf breaking failed for another reason:\n%s", out)
	}
}

// TestRAPI150ErrorModel: connect code + exactly one ErrorDetail per SPEC §6.1, NotFound instead of
// PermissionDenied without any permission on the project.
func TestRAPI150ErrorModel(t *testing.T) {
	parity.Covers(t, "R-API-150")
	s := authenv.Shared(t)
	seed := s.Seed(t)
	existing := seed.Builds["p2"]
	const missing = "01jbq7k2p4m8x0c3v5n7r9t1y6"

	// u1 has no binding on p2: an existing and a non-existent build answer identically
	s.NewUser(t, "u1-api150", authenv.Password)
	u1 := s.MustLogin(t, "u1-api150", authenv.Password)
	a := u1.Call(t, "bob.build.v1.BuildService", "GetBuild", map[string]any{"id": existing})
	b := u1.Call(t, "bob.build.v1.BuildService", "GetBuild", map[string]any{"id": missing})
	for name, r := range map[string]*authenv.Reply{"existing": a, "non-existent": b} {
		if r.Code != "not_found" || r.Message != "build not found" || r.Detail != "BOB-API-3001" || r.HTTP != 404 {
			t.Errorf("GetBuild of the %s build without permission: want NotFound 'build not found' BOB-API-3001, got %s", name, r)
		}
	}
	if a.Message != b.Message || a.Code != b.Code || a.Detail != b.Detail || a.Details != b.Details {
		t.Errorf("the answers differ (existence leak): %s / %s", a, b)
	}

	// u2 has only GetBuild on p2: GetBuild works, CancelBuild is PermissionDenied with the exact text
	s.NewRole(t, "api150-get", authenv.Perm{Method: "bob.build.v1.BuildService/GetBuild", Project: "*"})
	id := s.NewUser(t, "u2-api150", authenv.Password)
	s.Bind(t, "user:"+id, "api150-get", "p2")
	u2 := s.MustLogin(t, "u2-api150", authenv.Password)
	u2.Call(t, "bob.build.v1.BuildService", "GetBuild", map[string]any{"id": existing}).Must(t, "GetBuild with GetBuild on p2")
	r := u2.Call(t, "bob.build.v1.BuildService", "CancelBuild", map[string]any{"id": existing})
	const want = "permission denied: bob.build.v1.BuildService/CancelBuild on project p2"
	if r.Code != "permission_denied" || r.Message != want || r.Detail != "BOB-API-2002" || r.HTTP != 403 {
		t.Errorf("CancelBuild with only GetBuild: want PermissionDenied %q, got %s", want, r)
	}

	// the other codes of the §6.1 table that the API can provoke
	s.NewUser(t, "dup-api150", authenv.Password)
	s.NewSA(t, "sa-api150")
	sa := s.Admin().Call(t, authenv.SASvc, "GetServiceAccount", map[string]any{"id": "sa-api150"}).Must(t, "GetServiceAccount")
	cases := []struct {
		what         string
		reply        *authenv.Reply
		code, detail string
		http         int
	}{
		{"invalid argument", s.Admin().Call(t, authenv.RoleSvc, "CreateRole", map[string]any{"role": map[string]any{"id": "Not_A_Slug"}}), "invalid_argument", "BOB-API-1001", 400},
		{"unauthenticated (no credential)", s.Anon().Call(t, authenv.AuthSvc, "WhoAmI", map[string]any{}), "unauthenticated", "BOB-API-2001", 401},
		{"unauthenticated (bad token)", s.WithToken("bob_at_NOPE").Call(t, authenv.AuthSvc, "WhoAmI", map[string]any{}), "unauthenticated", "BOB-API-2001", 401},
		{"already exists", s.Admin().Call(t, authenv.UserSvc, "CreateUser", map[string]any{"user": map[string]any{"login": "dup-api150"}, "initialPassword": authenv.Password}), "already_exists", "BOB-API-3002", 409},
		{"failed precondition", s.Admin().Call(t, authenv.RoleSvc, "DeleteRole", map[string]any{"id": "admin"}), "failed_precondition", "BOB-API-3003", 400},
		{"aborted (stale etag)", s.Admin().Call(t, authenv.SASvc, "UpdateServiceAccount", map[string]any{
			"serviceAccount": map[string]any{"id": "sa-api150", "description": "changed", "etag": "stale-" + authenv.Str(sa.Body, "serviceAccount.etag")},
			"updateMask":     "description"}), "aborted", "BOB-API-3004", 409},
	}
	for _, c := range cases {
		if c.reply.Code != c.code || c.reply.Detail != c.detail || c.reply.HTTP != c.http {
			t.Errorf("%s: want %s %s (HTTP %d), got %s", c.what, c.code, c.detail, c.http, c.reply)
		}
		if c.reply.Details != 1 {
			t.Errorf("%s: %d error details, want exactly one ErrorDetail", c.what, c.reply.Details)
		}
		if strings.Contains(c.reply.Message, "goroutine") || strings.Contains(c.reply.Message, ".go:") {
			t.Errorf("%s: the message leaks internals: %q", c.what, c.reply.Message)
		}
	}
	if iv := cases[0].reply; iv.Field == "" {
		t.Errorf("InvalidArgument must name the field in ErrorDetail.field, got %s", iv)
	}
	if r.Details != 1 || a.Details != 1 {
		t.Errorf("authorization errors carry %d / %d details, want exactly one each", r.Details, a.Details)
	}
}

// TestRAPI151ListsAreGetOnly calls every list RPC and asserts that the counting S3 proxy saw no
// ListObjects request against the control and fleet buckets (D60). TestRAPI151WholeSuite repeats
// the assertion for everything the other tests of this package did to the shared stack.
func TestRAPI151ListsAreGetOnly(t *testing.T) {
	parity.Covers(t, "R-API-151")
	s := authenv.Start(t, 1)
	s.NewUser(t, "lister", authenv.Password)
	n := 0
	for _, m := range catalog(t).Methods {
		if !strings.HasPrefix(m.Name, "List") && m.Name != "GetOverview" && m.Name != "RecentFailures" || m.Stream {
			continue
		}
		for _, body := range []map[string]any{{}, {"pageSize": 1}, {"pageSize": 500, "pageToken": ""}} {
			r := s.Admin().Call(t, m.Package+"."+m.Service, m.Name, body)
			if r.Code == "internal" || r.Code == "unavailable" || r.Code == "unimplemented" || r.HTTP >= 500 {
				t.Errorf("%s %v: %s", m.Full(), body, r)
			}
		}
		n++
	}
	if n < 20 {
		t.Fatalf("only %d list RPCs found", n)
	}
	assertNoList(t, s)
}

func assertNoList(t *testing.T, s *authenv.Server) {
	t.Helper()
	if s.ProxyURL() == "" {
		t.Fatalf("harness error: the devstack info line has no s3_proxy_url")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	s3count.AssertNoList(ctx, t, s.ProxyURL(), "bob-control", "bob-fleet")
}
