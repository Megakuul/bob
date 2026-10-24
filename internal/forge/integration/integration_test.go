package integration

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"

	"github.com/megakuul/bob/internal/forge"
	forgev1 "github.com/megakuul/bob/internal/gen/bob/forge/v1"
	"github.com/megakuul/bob/internal/store"
	"github.com/megakuul/bob/tests/serverenv/forgefix"
	"github.com/megakuul/bob/tests/store/env"
)

type fakeBuilds struct {
	mu    sync.Mutex
	calls []string // project/key
}

func (f *fakeBuilds) CreateFromEvent(_ context.Context, project, key string, ev forge.Event) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, project+"/"+key)
	return "01build" + ev.Push.After[:8], nil
}

func (f *fakeBuilds) n() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

// pushMain is the recorded Forgejo push.main delivery, with mod applied and signed with secret.
func pushMain(t *testing.T, secret, delivery string, mod func(map[string]any)) (http.Header, []byte) {
	t.Helper()
	all, err := forgefix.Load("forgejo")
	if err != nil {
		t.Fatal(err)
	}
	var d forgefix.Delivery
	for _, x := range all {
		if x.Name == "push.main" {
			d = x
		}
	}
	var m map[string]any
	if err := json.Unmarshal(d.Body, &m); err != nil {
		t.Fatal(err)
	}
	if mod != nil {
		mod(m)
	}
	body, _ := json.Marshal(m)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	h := http.Header{}
	h.Set("X-Forgejo-Event", "push")
	h.Set("X-Forgejo-Delivery", delivery)
	h.Set("X-Forgejo-Signature", hex.EncodeToString(mac.Sum(nil)))
	return h, body
}

// TestIntakeAndService runs §6.9.3 against RustFS: unknown integration, size cap, signature,
// hostile payload, unlinked repository, de-duplication, the build hook and the delivery ring.
func TestIntakeAndService(t *testing.T) {
	if testing.Short() {
		t.Skip("starts RustFS")
	}
	be := env.Start(t)
	control := be.Open(t, "control")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	reg := &Registry{Control: control}
	svc := &Service{Registry: reg, BaseURL: "https://bob.example"}

	created, err := svc.CreateForgeIntegration(ctx, connect.NewRequest(&forgev1.CreateForgeIntegrationRequest{Integration: &forgev1.ForgeIntegration{
		Id: "fjx", Kind: forgev1.ForgeKind_FORGE_KIND_FORGEJO, BaseUrl: "https://forgejo.test",
		Credentials: &forgev1.ForgeIntegration_Forgejo{Forgejo: &forgev1.ForgejoCredentials{BotLogin: "bobbot", BotPassword: "pw"}}}}))
	if err != nil {
		t.Fatal(err)
	}
	secret := created.Msg.GetWebhookSecret()
	if len(secret) != 64 || created.Msg.GetIntegration().GetWebhookUrl() != "https://bob.example/hooks/fjx" || !created.Msg.GetIntegration().GetHasCredentials() {
		t.Fatalf("CreateForgeIntegration = %+v", created.Msg)
	}
	if _, err := svc.CreateForgeIntegration(ctx, connect.NewRequest(&forgev1.CreateForgeIntegrationRequest{Integration: &forgev1.ForgeIntegration{
		Id: "plain", Kind: forgev1.ForgeKind_FORGE_KIND_FORGEJO, BaseUrl: "http://forgejo.test"}})); err == nil ||
		!strings.Contains(err.Error(), "base URL must be https") {
		t.Errorf("http base URL: %v", err)
	}

	builds := &fakeBuilds{}
	in := &Intake{Registry: reg}
	mux := http.NewServeMux()
	mux.Handle(HookPattern, in)
	post := func(id string, h http.Header, body []byte) (int, string) {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, "/hooks/"+id, bytes.NewReader(body))
		for k, v := range h {
			req.Header[k] = v
		}
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		b, _ := io.ReadAll(rec.Body)
		return rec.Code, strings.TrimSpace(string(b))
	}

	h, body := pushMain(t, secret, "d1", nil)
	for _, c := range []struct {
		name, id string
		h        http.Header
		body     []byte
		code     int
		text     string
	}{
		{"unknown integration", "nope", h, body, 404, "unknown integration"},
		{"too large", "fjx", h, make([]byte, forge.MaxWebhookBody+1), 413, "payload too large"},
		{"wrong signature", "fjx", func() http.Header { x := h.Clone(); x.Set("X-Forgejo-Signature", strings.Repeat("0", 64)); return x }(), body, 401, "invalid signature"},
		{"unlinked repository", "fjx", h, body, 202, resultNotLinked},
		{"duplicate of the unlinked delivery", "fjx", h, body, 200, "duplicate"},
	} {
		if code, text := post(c.id, c.h, c.body); code != c.code || text != c.text {
			t.Errorf("%s: HTTP %d %q, want %d %q", c.name, code, text, c.code, c.text)
		}
	}
	evil, evilBody := pushMain(t, secret, "d-evil", func(m map[string]any) { m["after"] = "--upload-pack=x" })
	if code, _ := post("fjx", evil, evilBody); code != 400 {
		t.Errorf("after = --upload-pack=x: HTTP %d, want 400", code)
	}

	// link repository 1 (alice/demo) to project acme, as ProjectService does after LookupRepo
	if err := store.Change(ctx, control, ForgeKey("fjx"), func(cur *ForgeRecord) (*ForgeRecord, error) {
		cur.Repos = store.Set(map[string]string{"1": "acme"})
		return cur, nil
	}); err != nil {
		t.Fatal(err)
	}
	link := RepoLinkKey("fjx", 1)
	link.Project, link.FullName, link.CloneURL = store.Set[string]("acme"), store.Set[string]("alice/demo"), store.Set[string]("https://forgejo.test/alice/demo.git")
	if err := store.Create(ctx, control, link); err != nil {
		t.Fatal(err)
	}
	h2, body2 := pushMain(t, secret, "d2", nil)
	if code, _ := post("fjx", h2, body2); code != 503 {
		t.Errorf("linked delivery without builds: HTTP %d, want 503 (the forge retries)", code)
	}
	in.Builds = builds
	if code, text := post("fjx", h2, body2); code != 202 || text != resultAccepted || builds.n() != 1 {
		t.Errorf("retried linked delivery: HTTP %d %q, %d builds; want 202 accepted, 1 build", code, text, builds.n())
	}
	if code, text := post("fjx", h2, body2); code != 200 || text != "duplicate" || builds.n() != 1 {
		t.Errorf("replayed delivery: HTTP %d %q, %d builds; want 200 duplicate, still 1 build", code, text, builds.n())
	}

	ds, err := svc.ListForgeDeliveries(ctx, connect.NewRequest(&forgev1.ListForgeDeliveriesRequest{IntegrationId: "fjx"}))
	if err != nil {
		t.Fatal(err)
	}
	var results []string
	for _, d := range ds.Msg.GetDeliveries() {
		results = append(results, d.GetResult())
	}
	if got := strings.Join(results, ","); got != "accepted,rejected: payload,"+resultNotLinked {
		t.Errorf("delivery ring (newest first) = %s", got)
	}

	if _, err := svc.DeleteForgeIntegration(ctx, connect.NewRequest(&forgev1.DeleteForgeIntegrationRequest{Id: "fjx"})); err == nil {
		t.Errorf("DeleteForgeIntegration with a linked repository succeeded")
	}
	// rotation: the previous secret keeps working for 24 h
	rot, err := svc.RotateWebhookSecret(ctx, connect.NewRequest(&forgev1.RotateWebhookSecretRequest{Id: "fjx"}))
	if err != nil {
		t.Logf("RotateWebhookSecret: %v (re-ensuring hooks needs the forge)", err)
	} else if rot.Msg.GetWebhookSecret() == secret {
		t.Errorf("rotation kept the secret")
	}
	h3, body3 := pushMain(t, secret, "d3", nil)
	if code, _ := post("fjx", h3, body3); code != 202 {
		t.Errorf("delivery signed with the previous secret after a rotation: HTTP %d, want 202", code)
	}
}

func TestSplitRepoAndHandles(t *testing.T) {
	if _, _, err := splitRepo("fjx/12"); err != nil {
		t.Error(err)
	}
	for _, bad := range []string{"", "fjx", "fj/", "/12", "fj/-1", "fj/x"} {
		if _, _, err := splitRepo(bad); err == nil {
			t.Errorf("splitRepo(%q) accepted", bad)
		}
	}
	m := &Minter{Registry: &Registry{}}
	if err := m.Revoke(context.Background(), "nohandle"); err == nil {
		t.Errorf("Revoke of a handle without an integration accepted")
	}
}
