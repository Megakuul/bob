package forgejo

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"bobspike/s16/forge"
)

const fix = "../../fixtures/forgejo"

func load(t *testing.T, name string) (http.Header, []byte) {
	t.Helper()
	hb, err := os.ReadFile(filepath.Join(fix, name+".headers.json"))
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]string
	if err := json.Unmarshal(hb, &m); err != nil {
		t.Fatal(err)
	}
	h := http.Header{}
	for k, v := range m {
		h.Set(k, v)
	}
	body, err := os.ReadFile(filepath.Join(fix, name+".body.json"))
	if err != nil {
		t.Fatal(err)
	}
	return h, body
}

func adapter(t *testing.T) *Adapter {
	sec, err := os.ReadFile(filepath.Join(fix, "SECRET.test"))
	if err != nil {
		t.Fatal(err)
	}
	a, err := New(Config{BaseURL: "https://127.0.0.1:21600", WebhookSecret: forge.NewSecret(string(sec))})
	if err != nil {
		t.Fatal(err)
	}
	return a
}

// Every fixture is a real delivery captured from Forgejo 16.0.6.
func TestParseFixtures(t *testing.T) {
	a := adapter(t)
	cases := []struct {
		name string
		want func(t *testing.T, e forge.Event)
	}{
		{"push.main", func(t *testing.T, e forge.Event) {
			must(t, e.Type == forge.EventPush && e.Push.Branch == "main" && !e.Push.Created && !e.Push.Deleted, e)
			must(t, e.Push.After == "c548672eef197a1df05336f0c9f342034bf44eb6", e)
		}},
		{"push.branch-created", func(t *testing.T, e forge.Event) {
			must(t, e.Type == forge.EventPush && e.Push.Branch == "feature" && e.Push.Created, e)
		}},
		{"push.tag-lightweight", func(t *testing.T, e forge.Event) {
			must(t, e.Type == forge.EventTag && e.Tag.Name == "v0.1.0" && e.Tag.Commit == "c548672eef197a1df05336f0c9f342034bf44eb6", e)
		}},
		{"push.tag-annotated", func(t *testing.T, e forge.Event) {
			// after = tag object 9650917b…; the peeled commit is what bob builds.
			must(t, e.Type == forge.EventTag && e.Tag.Name == "v0.1.1" && e.Tag.Commit == "c548672eef197a1df05336f0c9f342034bf44eb6", e)
		}},
		{"pull_request.opened", func(t *testing.T, e forge.Event) {
			p := e.PullRequest
			must(t, e.Type == forge.EventPullRequest && p.Action == forge.PROpened && p.Number == 1 && !p.FromFork, e)
			must(t, p.FetchRef == "refs/pull/1/head" && p.HeadBranch == "feature" && p.BaseBranch == "main", e)
		}},
		{"pull_request.synchronized", func(t *testing.T, e forge.Event) {
			must(t, e.PullRequest.Action == forge.PRSynchronized && e.PullRequest.HeadSHA == "b2c9cbdd78cfce5fe2cf54d3d1d2365dde12d270", e)
		}},
		{"pull_request.opened-fork", func(t *testing.T, e forge.Event) {
			p := e.PullRequest
			must(t, p.FromFork && p.HeadRepo == "mallory/demo" && e.Repo.FullName == "alice/demo" && e.Sender == "mallory", e)
		}},
		{"pull_request.closed-merged", func(t *testing.T, e forge.Event) {
			must(t, e.PullRequest.Action == forge.PRClosed && e.PullRequest.Merged, e)
		}},
		{"push.main-merge", func(t *testing.T, e forge.Event) { must(t, e.Type == forge.EventPush, e) }},
		{"create.tag", func(t *testing.T, e forge.Event) { must(t, e.Type == forge.EventIgnored, e) }},
		{"delete.branch", func(t *testing.T, e forge.Event) { must(t, e.Type == forge.EventIgnored, e) }},
		{"release.published", func(t *testing.T, e forge.Event) { must(t, e.Type == forge.EventIgnored, e) }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h, b := load(t, c.name)
			e, err := a.ParseWebhook(h, b, time.Now())
			if err != nil {
				t.Fatal(err)
			}
			if e.DeliveryID == "" {
				t.Error("no delivery id")
			}
			if e.Type != forge.EventIgnored && (e.Repo.ID != 1 || e.Repo.FullName != "alice/demo" || !e.Repo.Private) {
				t.Errorf("repo: %+v", e.Repo)
			}
			c.want(t, e)
		})
	}
}

func must(t *testing.T, ok bool, e forge.Event) {
	t.Helper()
	if !ok {
		j, _ := json.MarshalIndent(e, "", " ")
		t.Errorf("unexpected event:\n%s", j)
	}
}

func TestSignatureRejected(t *testing.T) {
	a := adapter(t)
	h, b := load(t, "push.main")
	// 1. flipped body byte
	bad := append([]byte(nil), b...)
	bad[len(bad)/2] ^= 1
	if _, err := a.ParseWebhook(h, bad, time.Now()); !errors.Is(err, forge.ErrSignature) {
		t.Errorf("tampered body: %v", err)
	}
	// 2. missing header
	h2 := h.Clone()
	h2.Del("X-Forgejo-Signature")
	if _, err := a.ParseWebhook(h2, b, time.Now()); !errors.Is(err, forge.ErrSignature) {
		t.Errorf("missing sig: %v", err)
	}
	// 3. wrong secret
	a2, _ := New(Config{BaseURL: "https://x", WebhookSecret: forge.NewSecret("other")})
	if _, err := a2.ParseWebhook(h, b, time.Now()); !errors.Is(err, forge.ErrSignature) {
		t.Errorf("wrong secret: %v", err)
	}
	// 4. empty secret configured never verifies
	a3, _ := New(Config{BaseURL: "https://x"})
	if _, err := a3.ParseWebhook(h, b, time.Now()); !errors.Is(err, forge.ErrSignature) {
		t.Errorf("empty secret: %v", err)
	}
	// 5. GitHub-compatible header is equivalent (Forgejo sends X-Hub-Signature-256 too)
	if err := forge.VerifyHexHMACSHA256([]byte(mustRead(t, "SECRET.test")), b, h.Get("X-Hub-Signature-256"), "sha256="); err != nil {
		t.Errorf("X-Hub-Signature-256: %v", err)
	}
}

func TestTokenName(t *testing.T) {
	now := time.Unix(1792531843, 0)
	n := tokenName("01JOB-x", now)
	ts, ok := parseTokenName(n)
	if !ok || !ts.Equal(now) {
		t.Fatalf("%s -> %v %v", n, ts, ok)
	}
	if _, ok := parseTokenName("bob-status"); ok {
		t.Fatal("non-job token parsed")
	}
}

func mustRead(t *testing.T, n string) string {
	b, err := os.ReadFile(filepath.Join(fix, n))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
