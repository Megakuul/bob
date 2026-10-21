package gitlab

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"bobspike/s16/forge"
)

const fix = "../../fixtures/gitlab"

// Standard Webhooks reference vector (standard-webhooks/standard-webhooks libraries' tests),
// the scheme GitLab documents for its signing token.
func TestStandardWebhooksVector(t *testing.T) {
	a, err := New(Config{BaseURL: "https://x", SigningToken: forge.NewSecret("whsec_MfKQ9r8GKYqrTwjUPD8ILPZIo2LaLaSw")})
	if err != nil {
		t.Fatal(err)
	}
	h := http.Header{}
	h.Set("webhook-id", "msg_p5jXN8AQM9LWM0D4loKWxJek")
	h.Set("webhook-timestamp", "1614265330")
	h.Set("webhook-signature", "v1,g0hM9SsE+OTPJTGt/tmIKtSyZlE3uFJELVlNIOLJ1OE=")
	if err := a.verify(h, []byte(`{"test": 2432232314}`), time.Unix(1614265330, 0)); err != nil {
		t.Fatal(err)
	}
	if err := a.verify(h, []byte(`{"test": 2432232314}`), time.Unix(1614265330+3600, 0)); !errors.Is(err, forge.ErrStale) {
		t.Fatalf("stale: %v", err)
	}
}

const testToken = "whsec_c2VjcmV0LXNpZ25pbmcta2V5LWZvci1ib2Itczk2LXRlc3Q="

func signed(t *testing.T, file, event string, now time.Time) (http.Header, []byte) {
	b, err := os.ReadFile(filepath.Join(fix, file))
	if err != nil {
		t.Fatal(err)
	}
	key, _ := base64.StdEncoding.DecodeString(testToken[len("whsec_"):])
	id, ts := "01JDELIVERY", strconv.FormatInt(now.Unix(), 10)
	m := hmac.New(sha256.New, key)
	m.Write([]byte(id + "." + ts + "."))
	m.Write(b)
	h := http.Header{}
	h.Set("X-Gitlab-Event", event)
	h.Set("webhook-id", id)
	h.Set("webhook-timestamp", ts)
	h.Set("webhook-signature", "v1,AAAA v1,"+base64.StdEncoding.EncodeToString(m.Sum(nil)))
	return h, b
}

func TestParseFixtures(t *testing.T) {
	a, err := New(Config{BaseURL: "https://x", SigningToken: forge.NewSecret(testToken)})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	for _, c := range []struct {
		file, event string
		ok          func(e forge.Event) bool
	}{
		{"derived/push.https.json", "Push Hook", func(e forge.Event) bool {
			return e.Type == forge.EventPush && e.Push.Branch == "master" && e.Repo.ID == 15 && e.Repo.FullName == "mike/diaspora"
		}},
		{"derived/tag_push.https.json", "Tag Push Hook", func(e forge.Event) bool {
			return e.Type == forge.EventTag && e.Tag.Name == "v1.0.0" && e.Tag.Commit == "82b3d5ae55f7080f1e6022629cdb57bfae7cccc7"
		}},
		{"derived/merge_request.https.json", "Merge Request Hook", func(e forge.Event) bool {
			p := e.PullRequest
			return p.Action == forge.PROpened && p.Number == 1 && p.FetchRef == "refs/merge-requests/1/head" && !p.FromFork
		}},
		{"derived/merge_request.update-push.json", "Merge Request Hook", func(e forge.Event) bool { return e.PullRequest.Action == forge.PRSynchronized }},
		{"derived/merge_request.fork.json", "Merge Request Hook", func(e forge.Event) bool { return e.PullRequest.FromFork }},
		{"sdk-testdata/release.json", "Release Hook", func(e forge.Event) bool { return e.Type == forge.EventIgnored }},
	} {
		t.Run(c.file, func(t *testing.T) {
			h, b := signed(t, c.file, c.event, now)
			e, err := a.ParseWebhook(h, b, now)
			if err != nil {
				t.Fatal(err)
			}
			if !c.ok(e) {
				j, _ := json.MarshalIndent(e, "", " ")
				t.Fatalf("%s", j)
			}
		})
	}
	// http:// clone URL in a signed payload is refused (bob only clones over https).
	h, b := signed(t, "sdk-testdata/push.json", "Push Hook", now)
	if _, err := a.ParseWebhook(h, b, now); !errors.Is(err, forge.ErrPayload) {
		t.Errorf("http clone url: %v", err)
	}
	// tamper / stale / missing
	h, b = signed(t, "derived/push.https.json", "Push Hook", now)
	b2 := append([]byte(nil), b...)
	b2[5] ^= 1
	if _, err := a.ParseWebhook(h, b2, now); !errors.Is(err, forge.ErrSignature) {
		t.Errorf("tampered: %v", err)
	}
	if _, err := a.ParseWebhook(h, b, now.Add(10*time.Minute)); !errors.Is(err, forge.ErrStale) {
		t.Errorf("stale: %v", err)
	}
	h.Del("webhook-signature")
	if _, err := a.ParseWebhook(h, b, now); !errors.Is(err, forge.ErrSignature) {
		t.Errorf("missing: %v", err)
	}
}

func TestLegacySecretToken(t *testing.T) {
	b, _ := os.ReadFile(filepath.Join(fix, "derived/push.https.json"))
	off, _ := New(Config{BaseURL: "https://x"})
	h := http.Header{}
	h.Set("X-Gitlab-Event", "Push Hook")
	h.Set("X-Gitlab-Token", "plain")
	if _, err := off.ParseWebhook(h, b, time.Now()); !errors.Is(err, forge.ErrSignature) {
		t.Errorf("no secret configured must refuse: %v", err)
	}
	on, _ := New(Config{BaseURL: "https://x", LegacySecretToken: forge.NewSecret("plain")})
	if _, err := on.ParseWebhook(h, b, time.Now()); err != nil {
		t.Errorf("legacy token: %v", err)
	}
	h.Set("X-Gitlab-Token", "plain2")
	if _, err := on.ParseWebhook(h, b, time.Now()); !errors.Is(err, forge.ErrSignature) {
		t.Errorf("legacy wrong: %v", err)
	}
}
