package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/types/descriptorpb"

	buildv1 "github.com/megakuul/bob/internal/gen/bob/build/v1"
	"github.com/megakuul/bob/internal/gen/bob/build/v1/buildv1connect"
)

const (
	adminTok    = "admin-token-0123456789abcdef0123"
	userTok     = "user-token-0123456789abcdef01234"
	downloadTok = "download-token-0123456789abcdef0"
	sessionCk   = "session-cookie-0123456789abcdef0"
)

// builds is a BuildService with two builds; GetBuild panics for build "boom" and fails with
// a plain error for "oops".
type builds struct {
	buildv1connect.UnimplementedBuildServiceHandler
}

func (builds) GetBuild(_ context.Context, r *connect.Request[buildv1.GetBuildRequest]) (*connect.Response[buildv1.GetBuildResponse], error) {
	switch r.Msg.GetId() {
	case "boom":
		panic("handler bug")
	case "oops":
		return nil, errors.New("s3 said something secret.go:12")
	}
	return connect.NewResponse(&buildv1.GetBuildResponse{Build: &buildv1.Build{Id: r.Msg.GetId()}}), nil
}

func (builds) WatchBuild(_ context.Context, r *connect.Request[buildv1.WatchBuildRequest], s *connect.ServerStream[buildv1.WatchBuildResponse]) error {
	return s.Send(&buildv1.WatchBuildResponse{})
}

// projectsOf maps build ids to projects; "boom"/"oops" are in p1.
type projectsOf map[string]string

func (m projectsOf) Project(_ context.Context, what, id string) (string, error) {
	if p, ok := m[id]; ok {
		return p, nil
	}
	return "", NotFound(what)
}

// rbac: user has GetBuild on p1 only; on p2 nothing.
type rbac struct{}

func (rbac) Access(ctx context.Context, p *Principal, method, project string) (Access, error) {
	if a, _ := (AdminBindings{}).Access(ctx, p, method, project); a == Allowed {
		return Allowed, nil
	}
	if project == "p1" {
		if method == "bob.build.v1.BuildService/GetBuild" || method == "bob.build.v1.BuildService/WatchBuild" {
			return Allowed, nil
		}
		return Denied, nil
	}
	return NoAccess, nil
}

type sessions struct{}

func (sessions) Authenticate(_ context.Context, c Credential) (*Principal, error) {
	switch {
	case c.Session == sessionCk:
		return &Principal{ID: "user:u2", Scope: "session"}, nil
	case c.Bearer == userTok:
		return &Principal{ID: "user:u1", Login: "u1", Scope: "api"}, nil
	case c.Bearer == downloadTok:
		return &Principal{ID: "user:u1", Scope: "download"}, nil
	case strings.HasPrefix(c.Bearer, "bob_at_"):
		return nil, Unauthenticated("token expired")
	}
	return nil, ErrUnknownCredential
}

func newTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	s, err := New(Config{
		Services: Services{Build: builds{}},
		Authn: Chain{sessions{}, &StaticToken{Token: adminTok, Principal: Principal{
			ID: "sa:bootstrap", Scope: "api", Bindings: []Binding{{Role: "admin", Project: "*"}}}}},
		Authz:   rbac{},
		Resolve: projectsOf{"b1": "p1", "b2": "p2", "boom": "p1", "oops": "p1"},
		Info:    Info{Name: "test", BaseURL: "https://bob.example", Version: "v0"},
		Log:     slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(s)
	t.Cleanup(ts.Close)
	return ts
}

type reply struct {
	status  int
	code    string
	message string
	details []map[string]any
	body    map[string]any
}

func (r reply) detail(key string) string {
	if len(r.details) == 0 {
		return ""
	}
	d, _ := r.details[0]["debug"].(map[string]any)
	s, _ := d[key].(string)
	return s
}

func call(t *testing.T, base, method, path string, body any, hdr map[string]string) reply {
	t.Helper()
	var rd io.Reader
	if method == http.MethodPost {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, base+path, rd)
	if err != nil {
		t.Fatal(err)
	}
	if method == http.MethodPost {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	r := reply{status: resp.StatusCode}
	var m map[string]any
	_ = json.Unmarshal(raw, &m)
	if resp.StatusCode == 200 {
		r.body = m
		return r
	}
	r.code, _ = m["code"].(string)
	r.message, _ = m["message"].(string)
	ds, _ := m["details"].([]any)
	for _, d := range ds {
		dm, _ := d.(map[string]any)
		r.details = append(r.details, dm)
	}
	return r
}

func bearer(tok string) map[string]string { return map[string]string{"Authorization": "Bearer " + tok} }

func TestErrorModelAndAuthz(t *testing.T) {
	ts := newTestServer(t)
	const getBuild = "/api/bob.build.v1.BuildService/GetBuild"
	const cancel = "/api/bob.build.v1.BuildService/CancelBuild"
	const whoami = "/api/bob.auth.v1.AuthService/WhoAmI"
	cases := []struct {
		name          string
		path          string
		body          any
		hdr           map[string]string
		code, msg, dc string
		status        int
	}{
		{"no credential", getBuild, map[string]any{"id": "b1"}, nil, "unauthenticated", "authentication required", "BOB-API-2001", 401},
		{"unknown token", whoami, map[string]any{}, bearer("nope"), "unauthenticated", "invalid credential", "BOB-API-2001", 401},
		{"expired token", whoami, map[string]any{}, bearer("bob_at_x"), "unauthenticated", "token expired", "BOB-API-2001", 401},
		{"download token on the API", getBuild, map[string]any{"id": "b1"}, bearer(downloadTok), "unauthenticated", "token not valid for the API", "BOB-API-2001", 401},
		{"no permission on the project", getBuild, map[string]any{"id": "b2"}, bearer(userTok), "not_found", "build not found", "BOB-API-3001", 404},
		{"missing build", getBuild, map[string]any{"id": "b9"}, bearer(userTok), "not_found", "build not found", "BOB-API-3001", 404},
		{"some permission, not this method", cancel, map[string]any{"id": "b1"}, bearer(userTok), "permission_denied", "permission denied: bob.build.v1.BuildService/CancelBuild on project p1", "BOB-API-2002", 403},
		{"project field missing", getBuild, map[string]any{}, bearer(userTok), "invalid_argument", "build is required", "BOB-API-1001", 400},
		{"global method without permission", "/api/bob.access.v1.UserService/ListUsers", map[string]any{}, bearer(userTok), "permission_denied", "permission denied: bob.access.v1.UserService/ListUsers", "BOB-API-2002", 403},
		{"not implemented", cancel, map[string]any{"id": "b1"}, bearer(adminTok), "unimplemented", "not implemented: bob.build.v1.BuildService/CancelBuild", "BOB-API-9001", 501},
		{"cookie without Bob-Csrf", whoami, map[string]any{}, map[string]string{"Cookie": "bob_session=" + sessionCk}, "unauthenticated", "cookie session requires the Bob-Csrf header", "BOB-API-2001", 401},
		{"worker method without worker token", "/api/bob.worker.v1.WorkerControlService/GetCheckoutCredential", map[string]any{}, bearer(adminTok), "unauthenticated", "worker credential required", "BOB-API-2001", 401},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := call(t, ts.URL, http.MethodPost, c.path, c.body, c.hdr)
			if r.status != c.status || r.code != c.code || r.message != c.msg {
				t.Errorf("got %d %s %q, want %d %s %q", r.status, r.code, r.message, c.status, c.code, c.msg)
			}
			if len(r.details) != 1 || r.detail("code") != c.dc {
				t.Errorf("details %v, want exactly one with code %s", r.details, c.dc)
			}
		})
	}
	if r := call(t, ts.URL, http.MethodPost, getBuild, map[string]any{}, bearer(userTok)); r.detail("field") != "id" {
		t.Errorf("InvalidArgument names field %q, want id", r.detail("field"))
	}
}

func TestPanicAndPlainErrorsAreInternal(t *testing.T) {
	ts := newTestServer(t)
	for _, id := range []string{"boom", "oops"} {
		r := call(t, ts.URL, http.MethodPost, "/api/bob.build.v1.BuildService/GetBuild", map[string]any{"id": id}, bearer(adminTok))
		if r.status != 500 || r.code != "internal" || !strings.HasPrefix(r.message, "internal error (ref ") {
			t.Errorf("%s: got %d %s %q", id, r.status, r.code, r.message)
		}
		if len(r.details) != 1 || r.detail("code") != "BOB-API-9001" || strings.Contains(r.message, "secret") {
			t.Errorf("%s: details %v message %q", id, r.details, r.message)
		}
	}
	// the server survives and still answers
	if r := call(t, ts.URL, http.MethodPost, "/api/bob.build.v1.BuildService/GetBuild", map[string]any{"id": "b1"}, bearer(adminTok)); r.status != 200 {
		t.Errorf("after the panic: %+v", r)
	}
}

func TestWhoAmIPostAndGet(t *testing.T) {
	ts := newTestServer(t)
	post := call(t, ts.URL, http.MethodPost, "/api/bob.auth.v1.AuthService/WhoAmI", map[string]any{}, bearer(adminTok))
	q := url.Values{"connect": {"v1"}, "encoding": {"json"}, "message": {"{}"}}
	get := call(t, ts.URL, http.MethodGet, "/api/bob.auth.v1.AuthService/WhoAmI?"+q.Encode(), nil, bearer(adminTok))
	if post.status != 200 || get.status != 200 {
		t.Fatalf("POST %+v GET %+v", post, get)
	}
	pb, _ := json.Marshal(post.body)
	gb, _ := json.Marshal(get.body)
	if !bytes.Equal(pb, gb) {
		t.Errorf("POST %s != GET %s", pb, gb)
	}
	want := `{"bindings":[{"project":"*","role":"admin"}],"nix":{"substituter":"https://bob.example/nix"},"principal":{"id":"sa:bootstrap"},"projects":["*"],"tokenScope":"api"}`
	if string(pb) != want {
		t.Errorf("WhoAmI = %s\nwant      %s", pb, want)
	}
	// download tokens may call WhoAmI (allow_download_scope)
	if r := call(t, ts.URL, http.MethodPost, "/api/bob.auth.v1.AuthService/WhoAmI", map[string]any{}, bearer(downloadTok)); r.status != 200 {
		t.Errorf("WhoAmI with a download token: %+v", r)
	}
	// cookie sessions: POST with Bob-Csrf works, Connect GET is refused
	ck := map[string]string{"Cookie": "bob_session=" + sessionCk, "Bob-Csrf": "1"}
	if r := call(t, ts.URL, http.MethodPost, "/api/bob.auth.v1.AuthService/WhoAmI", map[string]any{}, ck); r.status != 200 {
		t.Errorf("cookie POST: %+v", r)
	}
	if r := call(t, ts.URL, http.MethodGet, "/api/bob.auth.v1.AuthService/WhoAmI?"+q.Encode(), nil, ck); r.code != "unauthenticated" {
		t.Errorf("cookie GET: %+v", r)
	}
	// PUBLIC methods: anonymous works, a stale cookie is ignored
	for _, h := range []map[string]string{nil, {"Cookie": "bob_session=stale"}} {
		if r := call(t, ts.URL, http.MethodPost, "/api/bob.auth.v1.AuthService/GetServerInfo", map[string]any{}, h); r.status != 200 || r.body["serverName"] != "test" {
			t.Errorf("GetServerInfo %v: %+v", h, r)
		}
	}
}

func TestStreamingProjectCheck(t *testing.T) {
	ts := newTestServer(t)
	stream := func(id, tok string) *http.Response {
		b, _ := json.Marshal(map[string]any{"id": id})
		env := append([]byte{0, 0, 0, 0, byte(len(b))}, b...)
		req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/bob.build.v1.BuildService/WatchBuild", bytes.NewReader(env))
		req.Header.Set("Content-Type", "application/connect+json")
		req.Header.Set("Authorization", "Bearer "+tok)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return resp
	}
	read := func(resp *http.Response) string {
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return string(b)
	}
	if out := read(stream("b2", userTok)); !strings.Contains(out, `"code":"not_found"`) || !strings.Contains(out, "build not found") {
		t.Errorf("WatchBuild of a build in a project without access: %s", out)
	}
	if out := read(stream("b1", userTok)); strings.Contains(out, `"error"`) {
		t.Errorf("WatchBuild with access: %s", out)
	}
}

func TestEveryServedMethodHasARule(t *testing.T) {
	s, err := New(Config{Authn: Chain{}})
	if err != nil {
		t.Fatal(err)
	}
	if n := len(s.Rules()); n < 90 { // 98 methods in proto/ (2026-10-22)
		t.Errorf("only %d rules", n)
	}
	for proc, r := range s.Rules() {
		if r.Kind == 0 || "/"+r.Method != proc {
			t.Errorf("%s: %+v", proc, r)
		}
	}
	for _, e := range s.Catalog() {
		if strings.HasPrefix(e.GetMethod(), "bob.worker.v1.") || e.GetKind() == "" {
			t.Errorf("catalog entry %v", e)
		}
	}
}

// TestServiceWithoutRuleIsRefused builds a service whose method has no option: RulesOf (and so
// New) must refuse it, and so must a PROJECT rule naming a missing field.
func TestServiceWithoutRuleIsRefused(t *testing.T) {
	fdp := &descriptorpb.FileDescriptorProto{
		Name:    proto.String("x/v1/x.proto"),
		Package: proto.String("x.v1"),
		Syntax:  proto.String("proto3"),
		MessageType: []*descriptorpb.DescriptorProto{
			{Name: proto.String("Req")}, {Name: proto.String("Res")},
		},
		Service: []*descriptorpb.ServiceDescriptorProto{{
			Name: proto.String("XService"),
			Method: []*descriptorpb.MethodDescriptorProto{{
				Name: proto.String("Get"), InputType: proto.String(".x.v1.Req"), OutputType: proto.String(".x.v1.Res"),
			}},
		}},
	}
	fd, err := protodesc.NewFile(fdp, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := RulesOf(fd.Services().Get(0)); err == nil || !strings.Contains(err.Error(), "x.v1.XService/Get has no bob.authz.v1.rule") {
		t.Errorf("RulesOf without option = %v", err)
	}
}

func TestConventions(t *testing.T) {
	for _, c := range []struct {
		in   int32
		want int
		bad  bool
	}{{0, 50, false}, {1, 1, false}, {500, 500, false}, {501, 0, true}, {-1, 0, true}} {
		got, err := PageSize(c.in)
		if got != c.want || (err != nil) != c.bad {
			t.Errorf("PageSize(%d) = %d, %v", c.in, got, err)
		}
	}
	tok := EncodePageToken("builds/p1", 150)
	if n, err := DecodePageToken("builds/p1", tok); n != 150 || err != nil {
		t.Errorf("round trip = %d, %v", n, err)
	}
	for _, bad := range []string{"x", "p1.!!", EncodePageToken("builds/p2", 3), "p1." + strings.Repeat("A", 600)} {
		if _, err := DecodePageToken("builds/p1", bad); connect.CodeOf(err) != connect.CodeInvalidArgument {
			t.Errorf("DecodePageToken(%.20q) = %v", bad, err)
		}
	}
	if err := CheckETag("etag", "a", "b"); connect.CodeOf(err) != connect.CodeAborted {
		t.Errorf("CheckETag mismatch = %v", err)
	}
	if err := CheckETag("etag", "", "b"); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Errorf("CheckETag empty = %v", err)
	}
}
