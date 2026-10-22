//go:build live

package forge

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/megakuul/bob/internal/forge"
	"github.com/megakuul/bob/tests/parity"
	"github.com/megakuul/bob/tests/serverenv/regenv"
)

// pushBody returns the recorded Forgejo push.main delivery re-targeted at alice/<repo> (real
// repository id, name and clone URL of the live instance), with mod applied to the JSON.
func pushBody(t *testing.T, e *regenv.Env, repo string, mod func(map[string]any)) []byte {
	t.Helper()
	d := loadOne(t, "forgejo", "push.main")
	m := jsonOf(t, d.Body)
	r := m["repository"].(map[string]any)
	r["id"] = e.RepoID(repo)
	r["full_name"] = "alice/" + repo
	r["clone_url"] = e.Forgejo.URL + "/alice/" + repo + ".git"
	if mod != nil {
		mod(m)
	}
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func post(t *testing.T, e *regenv.Env, h http.Header, body []byte) (int, string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, e.HookURL(), bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range h {
		req.Header[k] = v
	}
	r, err := e.HTTP.Do(req)
	if err != nil {
		t.Fatalf("POST %s: %v", e.HookURL(), err)
	}
	defer r.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	return r.StatusCode, strings.TrimSpace(string(b))
}

func TestPIPE002OneRepositoryPerProject(t *testing.T) {
	parity.Covers(t, "R-PIPE-002")
	e := regenv.Up(t, 1)
	if e == nil {
		return
	}
	e.Project("acme", "demo")

	// a correctly signed delivery for a repository that is not linked: 202, ignored, no build
	code, text := e.ForgejoDelivery("push", "d-unlinked", pushBody(t, e, "other", nil))
	if code != http.StatusAccepted || !strings.Contains(text, "ignored: repository not linked") {
		t.Errorf("delivery for an unlinked repository: HTTP %d %q, want 202 \"ignored: repository not linked\"", code, text)
	}
	time.Sleep(time.Second)
	if n := len(e.Builds("acme")); n != 0 {
		t.Errorf("the unlinked repository's delivery created %d builds", n)
	}
	var ds struct{ Deliveries []map[string]any }
	e.MustCall(e.Admin, "bob.forge.v1.ForgeService/ListForgeDeliveries", map[string]any{"integrationId": e.Integration}, &ds)
	found := false
	for _, d := range ds.Deliveries {
		b, _ := json.Marshal(d)
		found = found || strings.Contains(string(b), "ignored: repository not linked")
	}
	if !found {
		t.Errorf("the delivery ring does not show \"ignored: repository not linked\": %v", ds.Deliveries)
	}
	// control: the linked repository's delivery queues a build
	if code, _ := e.ForgejoDelivery("push", "d-linked", pushBody(t, e, "demo", nil)); code != http.StatusAccepted {
		t.Errorf("delivery for the linked repository: HTTP %d, want 202", code)
	}
	e.WaitBuilds("acme", 1, 10*time.Second)

	// a repository already linked to another project: AlreadyExists
	err := e.Call(e.Admin, "bob.project.v1.ProjectService/CreateProject", map[string]any{"project": map[string]any{
		"id": "acme2", "displayName": "acme2", "forgeIntegrationId": e.Integration, "repoFullName": "alice/demo"}}, nil)
	if err == nil || err.Code != "already_exists" {
		t.Errorf("CreateProject with an already linked repository: %v, want AlreadyExists", err)
	}
	e.AssertNoList()
}

func TestPIPE008WebhookIntake(t *testing.T) {
	parity.Covers(t, "R-PIPE-008")
	e := regenv.Up(t, 1)
	if e == nil {
		return
	}
	e.Project("acme", "demo")
	body := pushBody(t, e, "demo", nil)

	// wrong signature -> 401 "invalid signature", no header values in the body
	h := http.Header{"X-Forgejo-Event": {"push"}, "X-Forgejo-Delivery": {"d-bad"}, "X-Forgejo-Signature": {strings.Repeat("0", 64)}, "Content-Type": {"application/json"}}
	if code, text := post(t, e, h, body); code != http.StatusUnauthorized || text != "invalid signature" {
		t.Errorf("wrong signature: HTTP %d %q, want 401 \"invalid signature\"", code, text)
	}
	// unknown integration -> 404
	req, _ := http.NewRequest(http.MethodPost, e.API+"/hooks/no-such-integration", bytes.NewReader(body))
	if r, err := e.HTTP.Do(req); err != nil || r.StatusCode != http.StatusNotFound {
		t.Errorf("unknown integration: %v %v, want 404", r, err)
	}
	// replayed delivery id -> 200 duplicate, still one build
	if code, _ := e.ForgejoDelivery("push", "d-replay", body); code != http.StatusAccepted {
		t.Fatalf("first delivery: HTTP %d, want 202", code)
	}
	if code, text := e.ForgejoDelivery("push", "d-replay", body); code != http.StatusOK || !strings.Contains(text, "duplicate") {
		t.Errorf("replayed delivery: HTTP %d %q, want 200 duplicate", code, text)
	}
	time.Sleep(2 * time.Second)
	if n := len(e.Builds("acme")); n != 1 {
		t.Errorf("a replayed delivery gave %d builds, want 1", n)
	}
	// 25 MiB + 1 -> 413
	big := bytes.Repeat([]byte{' '}, forge.MaxWebhookBody+1)
	if code, _ := e.ForgejoDelivery("push", "d-big", big); code != http.StatusRequestEntityTooLarge {
		t.Errorf("body of 25 MiB + 1: HTTP %d, want 413", code)
	}
	// a push whose `after` is an option -> 400 and no git process started
	evil := pushBody(t, e, "demo", func(m map[string]any) { m["after"] = "--upload-pack=x" })
	if code, _ := e.ForgejoDelivery("push", "d-evil", evil); code != http.StatusBadRequest {
		t.Errorf("after = --upload-pack=x: HTTP %d, want 400", code)
	}
	procs, _ := filepath.Glob("/proc/[0-9]*/cmdline")
	for _, p := range procs {
		if b, err := os.ReadFile(p); err == nil && bytes.Contains(b, []byte("--upload-pack=x")) {
			t.Errorf("a process was started with the injected option: %s", bytes.ReplaceAll(b, []byte{0}, []byte{' '}))
		}
	}
	time.Sleep(time.Second)
	if n := len(e.Builds("acme")); n != 1 {
		t.Errorf("rejected deliveries changed the build count to %d", n)
	}

	// GitLab: a timestamp 6 minutes old -> 401 (the Standard Webhooks window is +-5 min)
	var gl struct {
		Integration   struct{ ID string }
		WebhookSecret string `json:"webhookSecret"`
	}
	e.MustCall(e.Admin, "bob.forge.v1.ForgeService/CreateForgeIntegration", map[string]any{"integration": map[string]any{
		"id": "gitlab-test", "kind": "FORGE_KIND_GITLAB", "displayName": "GitLab", "baseUrl": "https://gitlab.invalid",
		"gitlab": map[string]any{"accessToken": "glpat-x"}}}, &gl)
	live := loadOne(t, "gitlab/live", "01-push_hook")
	send := func(age time.Duration) int {
		key, _ := base64.StdEncoding.DecodeString(strings.TrimPrefix(gl.WebhookSecret, "whsec_"))
		at := time.Now().Add(-age)
		id, ts := "msg-"+strconv.FormatInt(at.UnixNano(), 36), strconv.FormatInt(at.Unix(), 10)
		mac := hmac.New(sha256.New, key)
		mac.Write([]byte(id + "." + ts + "."))
		mac.Write(live.Body)
		req, _ := http.NewRequest(http.MethodPost, e.API+"/hooks/gitlab-test", bytes.NewReader(live.Body))
		req.Header.Set("Webhook-Id", id)
		req.Header.Set("Webhook-Timestamp", ts)
		req.Header.Set("Webhook-Signature", "v1,"+base64.StdEncoding.EncodeToString(mac.Sum(nil)))
		req.Header.Set("X-Gitlab-Event", "Push Hook")
		req.Header.Set("Content-Type", "application/json")
		r, err := e.HTTP.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		r.Body.Close()
		return r.StatusCode
	}
	if code := send(4 * time.Minute); code != http.StatusAccepted {
		t.Errorf("GitLab delivery 4 minutes old: HTTP %d, want 202", code)
	}
	if code := send(6 * time.Minute); code != http.StatusUnauthorized {
		t.Errorf("GitLab delivery 6 minutes old: HTTP %d, want 401", code)
	}
	e.AssertNoList()
}
