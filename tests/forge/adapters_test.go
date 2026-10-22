package forge

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/megakuul/bob/internal/forge"
	"github.com/megakuul/bob/internal/forge/forgejo"
	"github.com/megakuul/bob/internal/forge/github"
	"github.com/megakuul/bob/internal/forge/gitlab"
	"github.com/megakuul/bob/pkg/bob/diag"
	"github.com/megakuul/bob/tests/harness"
	"github.com/megakuul/bob/tests/parity"
	"github.com/megakuul/bob/tests/serverenv/forgefix"
)

const (
	githubSecret = "github-test-webhook-secret"
)

// adapterSet builds the three adapters with https base URLs and the secrets of the recorded
// fixtures. It returns false (the test is red) while the adapters are stubs.
type adapterSet struct {
	forgejo, github, gitlab forge.Adapter
	gitlabToken             string // whsec_… signing token
}

func buildAdapters(t *testing.T) (adapterSet, bool) {
	t.Helper()
	var s adapterSet
	fjSecret, err := forgefix.Secret("forgejo/SECRET.test")
	if err != nil {
		t.Fatalf("harness error: %v", err)
	}
	if s.gitlabToken, err = forgefix.Secret("gitlab/live/SIGNING_TOKEN.test"); err != nil {
		t.Fatalf("harness error: %v", err)
	}
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	der, _ := x509.MarshalPKCS8PrivateKey(key)
	pemKey := string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
	build := func(a forge.Adapter, err error) forge.Adapter {
		t.Helper()
		if err != nil {
			var de *diag.Error
			if errors.As(err, &de) && de.Code == diag.CodeNotImplemented {
				harness.RedFeature(t, "forge adapter constructor: "+err.Error())
				return nil
			}
			t.Fatalf("adapter constructor: %v", err)
		}
		return a
	}
	s.forgejo = build(forgejo.New(forgejo.Config{BaseURL: "https://forgejo.test", BotLogin: "bobbot",
		BotPassword: forge.NewSecret("pw"), WebhookSecret: forge.NewSecret(fjSecret)}))
	s.github = build(github.New(github.Config{BaseURL: "https://api.github.test", AppID: 1, InstallationID: 2,
		PrivateKeyPEM: forge.NewSecret(pemKey), WebhookSecret: forge.NewSecret(githubSecret)}))
	s.gitlab = build(gitlab.New(gitlab.Config{BaseURL: "https://gitlab.test", AccessToken: forge.NewSecret("glpat-x"),
		SigningToken: forge.NewSecret(s.gitlabToken)}))
	return s, s.forgejo != nil && s.github != nil && s.gitlab != nil
}

// delivery is one request an adapter parses.
type delivery struct {
	h    http.Header
	body []byte
	now  time.Time
}

func loadOne(t *testing.T, sub, name string) forgefix.Delivery {
	t.Helper()
	all, err := forgefix.Load(sub)
	if err != nil {
		t.Fatalf("harness error: %v", err)
	}
	for _, d := range all {
		if d.Name == name {
			return d
		}
	}
	t.Fatalf("harness error: fixture %s/%s missing", sub, name)
	return forgefix.Delivery{}
}

func recorded(t *testing.T, sub, name string) delivery {
	d := loadOne(t, sub, name)
	now := time.Now()
	if ts := d.Headers.Get("Webhook-Timestamp"); ts != "" {
		n, _ := strconv.ParseInt(ts, 10, 64)
		now = time.Unix(n, 0)
	}
	return delivery{h: d.Headers, body: d.Body, now: now}
}

// githubSigned signs a body-only fixture like GitHub does.
func githubSigned(t *testing.T, name, event string) delivery {
	// forgefix names body-only deliveries without the .json suffix.
	d := loadOne(t, "github/webhooks", strings.TrimSuffix(name, ".json"))
	mac := hmac.New(sha256.New, []byte(githubSecret))
	mac.Write(d.Body)
	h := http.Header{}
	h.Set("X-GitHub-Event", event)
	h.Set("X-GitHub-Delivery", "00000000-0000-4000-8000-"+fmt.Sprintf("%012d", len(name)))
	h.Set("X-Hub-Signature-256", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	h.Set("Content-Type", "application/json")
	return delivery{h: h, body: d.Body, now: time.Now()}
}

// gitlabSigned signs a body-only fixture with the Standard Webhooks scheme (SPEC 6.9.2).
func gitlabSigned(token string, body []byte, event string, at time.Time) http.Header {
	key, _ := base64.StdEncoding.DecodeString(strings.TrimPrefix(token, "whsec_"))
	id := "msg-" + strconv.FormatInt(at.UnixNano(), 36)
	ts := strconv.FormatInt(at.Unix(), 10)
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(id + "." + ts + "."))
	mac.Write(body)
	h := http.Header{}
	h.Set("Webhook-Id", id)
	h.Set("Webhook-Timestamp", ts)
	h.Set("Webhook-Signature", "v1,"+base64.StdEncoding.EncodeToString(mac.Sum(nil)))
	h.Set("X-Gitlab-Event", event)
	h.Set("Content-Type", "application/json")
	return h
}

func gitlabFile(t *testing.T, token, sub, name, event string) delivery {
	d := loadOne(t, sub, name)
	now := time.Now()
	return delivery{h: gitlabSigned(token, d.Body, event, now), body: d.Body, now: now}
}

func jsonOf(t *testing.T, body []byte) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

// dig reads a nested JSON value by key path.
func dig(m map[string]any, path ...string) any {
	var cur any = m
	for _, k := range path {
		mm, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		cur = mm[k]
	}
	return cur
}

func str(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case float64:
		return strconv.FormatInt(int64(x), 10)
	}
	return ""
}

// want is the normalized event a delivery must produce; zero fields are not compared.
type want struct {
	typ                  forge.EventType
	repoID               int64
	branch, after        string
	created, deleted     bool
	tag, tagCommit       string
	pr                   int64
	action               forge.PRAction
	fromFork, merged     bool
	headSHA, headBranch  string
	baseBranch, fetchRef string
	sender               string
	deliveryID, fullName string
	cloneURL             string
}

func (w want) check(t *testing.T, ev forge.Event) {
	t.Helper()
	if ev.Type != w.typ {
		t.Fatalf("event type %q, want %q (%+v)", ev.Type, w.typ, ev)
	}
	eq := func(name, got, exp string) {
		if exp != "" && got != exp {
			t.Errorf("%s = %q, want %q", name, got, exp)
		}
	}
	eq("DeliveryID", ev.DeliveryID, w.deliveryID)
	eq("Sender", ev.Sender, w.sender)
	eq("Repo.FullName", ev.Repo.FullName, w.fullName)
	eq("Repo.CloneURL", ev.Repo.CloneURL, w.cloneURL)
	if w.repoID != 0 && ev.Repo.ID != w.repoID {
		t.Errorf("Repo.ID = %d, want %d", ev.Repo.ID, w.repoID)
	}
	switch w.typ {
	case forge.EventPush:
		if ev.Push == nil {
			t.Fatalf("push event without Push: %+v", ev)
		}
		eq("Push.Branch", ev.Push.Branch, w.branch)
		eq("Push.After", ev.Push.After, w.after)
		if ev.Push.Created != w.created || ev.Push.Deleted != w.deleted {
			t.Errorf("Push created/deleted = %v/%v, want %v/%v", ev.Push.Created, ev.Push.Deleted, w.created, w.deleted)
		}
	case forge.EventTag:
		if ev.Tag == nil {
			t.Fatalf("tag event without Tag: %+v", ev)
		}
		eq("Tag.Name", ev.Tag.Name, w.tag)
		eq("Tag.Commit", ev.Tag.Commit, w.tagCommit)
		if ev.Tag.Deleted != w.deleted {
			t.Errorf("Tag.Deleted = %v, want %v", ev.Tag.Deleted, w.deleted)
		}
	case forge.EventPullRequest:
		pr := ev.PullRequest
		if pr == nil {
			t.Fatalf("pull_request event without PullRequest: %+v", ev)
		}
		if pr.Number != w.pr || pr.Action != w.action || pr.FromFork != w.fromFork || pr.Merged != w.merged {
			t.Errorf("PullRequest number/action/fork/merged = %d/%s/%v/%v, want %d/%s/%v/%v", pr.Number, pr.Action, pr.FromFork, pr.Merged, w.pr, w.action, w.fromFork, w.merged)
		}
		eq("PullRequest.HeadSHA", pr.HeadSHA, w.headSHA)
		eq("PullRequest.HeadBranch", pr.HeadBranch, w.headBranch)
		eq("PullRequest.BaseBranch", pr.BaseBranch, w.baseBranch)
		eq("PullRequest.FetchRef", pr.FetchRef, w.fetchRef)
	case forge.EventIgnored:
		if ev.IgnoredReason == "" {
			t.Errorf("ignored event without IgnoredReason")
		}
	}
	if w.typ != forge.EventIgnored && w.typ != forge.EventPing && !strings.HasPrefix(ev.Repo.CloneURL, "https://") {
		t.Errorf("Repo.CloneURL %q is not https", ev.Repo.CloneURL)
	}
}

// contractCase is one delivery of the shared suite.
type contractCase struct {
	name string
	adp  func(adapterSet) forge.Adapter
	d    func(*testing.T, adapterSet) delivery
	want func(*testing.T, delivery) want
}

func forgejoCases() []contractCase {
	fj := func(s adapterSet) forge.Adapter { return s.forgejo }
	rec := func(name string) func(*testing.T, adapterSet) delivery {
		return func(t *testing.T, _ adapterSet) delivery { return recorded(t, "forgejo", name) }
	}
	push := func(branch string, created bool) func(*testing.T, delivery) want {
		return func(t *testing.T, d delivery) want {
			m := jsonOf(t, d.body)
			return want{typ: forge.EventPush, repoID: 1, fullName: "alice/demo", branch: branch, created: created,
				after: str(m["after"]), sender: str(dig(m, "sender", "login")), deliveryID: d.h.Get("X-Forgejo-Delivery")}
		}
	}
	tag := func(name string) func(*testing.T, delivery) want {
		return func(t *testing.T, d delivery) want {
			m := jsonOf(t, d.body)
			return want{typ: forge.EventTag, repoID: 1, tag: name, tagCommit: str(dig(m, "head_commit", "id")),
				deliveryID: d.h.Get("X-Forgejo-Delivery")}
		}
	}
	pr := func(action forge.PRAction, fork, merged bool) func(*testing.T, delivery) want {
		return func(t *testing.T, d delivery) want {
			m := jsonOf(t, d.body)
			n := str(m["number"])
			num, _ := strconv.ParseInt(n, 10, 64)
			return want{typ: forge.EventPullRequest, repoID: 1, pr: num, action: action, fromFork: fork, merged: merged,
				headSHA: str(dig(m, "pull_request", "head", "sha")), headBranch: str(dig(m, "pull_request", "head", "ref")),
				baseBranch: str(dig(m, "pull_request", "base", "ref")), fetchRef: "refs/pull/" + n + "/head"}
		}
	}
	ign := func(*testing.T, delivery) want { return want{typ: forge.EventIgnored} }
	return []contractCase{
		{"forgejo push main", fj, rec("push.main"), push("main", false)},
		{"forgejo push main merge", fj, rec("push.main-merge"), push("main", false)},
		{"forgejo push branch created", fj, rec("push.branch-created"), push("feature", true)},
		{"forgejo annotated tag", fj, rec("push.tag-annotated"), tag("v0.1.1")},
		{"forgejo lightweight tag", fj, rec("push.tag-lightweight"), tag("v0.1.0")},
		{"forgejo PR opened", fj, rec("pull_request.opened"), pr(forge.PROpened, false, false)},
		{"forgejo PR synchronized", fj, rec("pull_request.synchronized"), pr(forge.PRSynchronized, false, false)},
		{"forgejo PR from fork", fj, rec("pull_request.opened-fork"), pr(forge.PROpened, true, false)},
		{"forgejo PR closed merged", fj, rec("pull_request.closed-merged"), pr(forge.PRClosed, false, true)},
		{"forgejo create ignored", fj, rec("create.tag"), ign},
		{"forgejo delete ignored", fj, rec("delete.branch"), ign},
		{"forgejo release ignored", fj, rec("release.published"), ign},
	}
}

func githubCases() []contractCase {
	gh := func(s adapterSet) forge.Adapter { return s.github }
	file := func(name, event string) func(*testing.T, adapterSet) delivery {
		return func(t *testing.T, _ adapterSet) delivery { return githubSigned(t, name, event) }
	}
	push := func(branch string, created bool) func(*testing.T, delivery) want {
		return func(t *testing.T, d delivery) want {
			m := jsonOf(t, d.body)
			return want{typ: forge.EventPush, repoID: 186853002, branch: branch, created: created, after: str(m["after"])}
		}
	}
	pr := func(action forge.PRAction, fork, merged bool) func(*testing.T, delivery) want {
		return func(t *testing.T, d delivery) want {
			m := jsonOf(t, d.body)
			n := str(m["number"])
			num, _ := strconv.ParseInt(n, 10, 64)
			return want{typ: forge.EventPullRequest, repoID: 186853002, pr: num, action: action, fromFork: fork, merged: merged,
				headSHA: str(dig(m, "pull_request", "head", "sha")), headBranch: str(dig(m, "pull_request", "head", "ref")),
				baseBranch: str(dig(m, "pull_request", "base", "ref")), fetchRef: "refs/pull/" + n + "/head"}
		}
	}
	return []contractCase{
		// push.branch.json is a branch creation ("created": true, "before": "000…0").
		{"github push", gh, file("push.branch.json", "push"), push("master", true)},
		{"github tag created", gh, file("push.tag-created.derived.json", "push"), func(t *testing.T, d delivery) want {
			m := jsonOf(t, d.body)
			return want{typ: forge.EventTag, repoID: 186853002, tag: "v1.0.0", tagCommit: str(dig(m, "head_commit", "id"))}
		}},
		{"github tag deleted", gh, file("push.tag-deleted.json", "push"), func(*testing.T, delivery) want {
			return want{typ: forge.EventTag, repoID: 186853002, tag: "simple-tag", deleted: true}
		}},
		{"github PR opened", gh, file("pull_request.opened.json", "pull_request"), pr(forge.PROpened, false, false)},
		{"github PR synchronize", gh, file("pull_request.synchronize.json", "pull_request"), pr(forge.PRSynchronized, false, false)},
		{"github PR from fork", gh, file("pull_request.opened-fork.derived.json", "pull_request"), pr(forge.PROpened, true, false)},
		{"github PR closed", gh, file("pull_request.closed.json", "pull_request"), pr(forge.PRClosed, false, false)},
		{"github ping", gh, file("ping.json", "ping"), func(*testing.T, delivery) want { return want{typ: forge.EventPing} }},
		{"github create ignored", gh, file("create.json", "create"), func(*testing.T, delivery) want { return want{typ: forge.EventIgnored} }},
		{"github release ignored", gh, file("release.published.json", "release"), func(*testing.T, delivery) want { return want{typ: forge.EventIgnored} }},
	}
}

func gitlabCases() []contractCase {
	gl := func(s adapterSet) forge.Adapter { return s.gitlab }
	live := func(name string) func(*testing.T, adapterSet) delivery {
		return func(t *testing.T, _ adapterSet) delivery { return recorded(t, "gitlab/live", name) }
	}
	signed := func(sub, name, event string) func(*testing.T, adapterSet) delivery {
		return func(t *testing.T, s adapterSet) delivery { return gitlabFile(t, s.gitlabToken, sub, name, event) }
	}
	push := func(branch string) func(*testing.T, delivery) want {
		return func(t *testing.T, d delivery) want {
			m := jsonOf(t, d.body)
			return want{typ: forge.EventPush, repoID: 1, branch: branch, after: str(m["checkout_sha"]), deliveryID: d.h.Get("Webhook-Id")}
		}
	}
	mr := func(action forge.PRAction, fork bool) func(*testing.T, delivery) want {
		return func(t *testing.T, d delivery) want {
			m := jsonOf(t, d.body)
			oa := dig(m, "object_attributes")
			oam, _ := oa.(map[string]any)
			iid := str(oam["iid"])
			num, _ := strconv.ParseInt(iid, 10, 64)
			return want{typ: forge.EventPullRequest, pr: num, action: action, fromFork: fork,
				headSHA: str(dig(oam, "last_commit", "id")), headBranch: str(oam["source_branch"]),
				baseBranch: str(oam["target_branch"]), fetchRef: "refs/merge-requests/" + iid + "/head"}
		}
	}
	return []contractCase{
		{"gitlab live push main", gl, live("01-push_hook"), push("main")},
		{"gitlab live push feature", gl, live("03-push_hook"), func(t *testing.T, d delivery) want {
			w := push("")(t, d)
			w.branch = strings.TrimPrefix(str(jsonOf(t, d.body)["ref"]), "refs/heads/")
			return w
		}},
		{"gitlab live MR open", gl, live("04-merge_request_hook"), mr(forge.PROpened, false)},
		{"gitlab live MR update with oldrev", gl, live("06-merge_request_hook"), mr(forge.PRSynchronized, false)},
		{"gitlab live tag push", gl, live("07-tag_push_hook"), func(t *testing.T, d delivery) want {
			m := jsonOf(t, d.body)
			return want{typ: forge.EventTag, repoID: 1, tag: strings.TrimPrefix(str(m["ref"]), "refs/tags/"), tagCommit: str(m["checkout_sha"])}
		}},
		{"gitlab live release ignored", gl, live("08-release_hook"), func(*testing.T, delivery) want { return want{typ: forge.EventIgnored} }},
		{"gitlab https push", gl, signed("gitlab/derived", "push.https", "Push Hook"), func(t *testing.T, d delivery) want {
			return want{typ: forge.EventPush, repoID: 15, branch: strings.TrimPrefix(str(jsonOf(t, d.body)["ref"]), "refs/heads/"), after: str(jsonOf(t, d.body)["checkout_sha"])}
		}},
		{"gitlab https tag push", gl, signed("gitlab/derived", "tag_push.https", "Tag Push Hook"), func(t *testing.T, d delivery) want {
			m := jsonOf(t, d.body)
			return want{typ: forge.EventTag, tag: strings.TrimPrefix(str(m["ref"]), "refs/tags/"), tagCommit: str(m["checkout_sha"])}
		}},
		{"gitlab MR from fork", gl, signed("gitlab/derived", "merge_request.fork", "Merge Request Hook"), mr(forge.PROpened, true)},
		{"gitlab MR update with oldrev (derived)", gl, signed("gitlab/derived", "merge_request.update-push", "Merge Request Hook"), mr(forge.PRSynchronized, false)},
	}
}

// TestFixturesContract is the shared adapter suite of R-PIPE-001: the three adapters normalize
// the recorded deliveries (real Forgejo 16 and GitLab 19, documented GitHub payloads).
func TestFixturesContract(t *testing.T) {
	parity.Covers(t, "R-PIPE-001")
	s, ok := buildAdapters(t)
	if !ok {
		return
	}
	var cases []contractCase
	cases = append(append(append(cases, forgejoCases()...), githubCases()...), gitlabCases()...)
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d := c.d(t, s)
			ev, err := c.adp(s).ParseWebhook(d.h, d.body, d.now)
			if err != nil {
				t.Fatalf("ParseWebhook: %v", err)
			}
			c.want(t, d).check(t, ev)
		})
	}
}

// TestFixturesRefused: unsigned, tampered, oversize and stale deliveries, and plain-http clone
// URLs, give the sentinel errors for every adapter.
func TestFixturesRefused(t *testing.T) {
	parity.Covers(t, "R-PIPE-001")
	s, ok := buildAdapters(t)
	if !ok {
		return
	}
	type target struct {
		name string
		a    forge.Adapter
		d    delivery
	}
	targets := []target{
		{"forgejo", s.forgejo, recorded(t, "forgejo", "push.main")},
		{"github", s.github, githubSigned(t, "push.branch.json", "push")},
		{"gitlab", s.gitlab, recorded(t, "gitlab/live", "01-push_hook")},
	}
	for _, g := range targets {
		t.Run(g.name, func(t *testing.T) {
			if _, err := g.a.ParseWebhook(g.d.h, g.d.body, g.d.now); err != nil {
				t.Fatalf("control: the unmodified delivery is refused: %v", err)
			}
			unsigned := http.Header{}
			for k, v := range g.d.h {
				if !strings.Contains(strings.ToLower(k), "signature") {
					unsigned[k] = v
				}
			}
			if _, err := g.a.ParseWebhook(unsigned, g.d.body, g.d.now); !errors.Is(err, forge.ErrSignature) {
				t.Errorf("unsigned: %v, want ErrSignature", err)
			}
			tampered := append([]byte(nil), g.d.body...)
			tampered[len(tampered)/2] ^= 0x01
			if _, err := g.a.ParseWebhook(g.d.h, tampered, g.d.now); !errors.Is(err, forge.ErrSignature) {
				t.Errorf("tampered body: %v, want ErrSignature", err)
			}
			big := make([]byte, forge.MaxWebhookBody+1)
			if _, err := g.a.ParseWebhook(g.d.h, big, g.d.now); !errors.Is(err, forge.ErrTooLarge) {
				t.Errorf("body of MaxWebhookBody+1 bytes: %v, want ErrTooLarge", err)
			}
		})
	}
	// GitLab: +-5 minutes
	d := recorded(t, "gitlab/live", "01-push_hook")
	if _, err := s.gitlab.ParseWebhook(d.h, d.body, d.now.Add(4*time.Minute+50*time.Second)); err != nil {
		t.Errorf("gitlab, 4m50s after the timestamp: %v, want accepted", err)
	}
	for _, off := range []time.Duration{6 * time.Minute, -6 * time.Minute} {
		if _, err := s.gitlab.ParseWebhook(d.h, d.body, d.now.Add(off)); !errors.Is(err, forge.ErrStale) {
			t.Errorf("gitlab, %v from the timestamp: %v, want ErrStale", off, err)
		}
	}
	// plain-http clone URLs (client-go test data) violate CheckEvent
	for _, f := range []struct{ name, event string }{{"push", "Push Hook"}, {"tag_push", "Tag Push Hook"}, {"merge_request", "Merge Request Hook"}} {
		sd := gitlabFile(t, s.gitlabToken, "gitlab/sdk-testdata", f.name, f.event)
		if _, err := s.gitlab.ParseWebhook(sd.h, sd.body, sd.now); !errors.Is(err, forge.ErrPayload) {
			t.Errorf("gitlab sdk %s with an http clone URL: %v, want ErrPayload", f.name, err)
		}
	}
}

// TestFixturesBaseURLMustBeHTTPS: every constructor refuses a non-https base URL.
func TestFixturesBaseURLMustBeHTTPS(t *testing.T) {
	parity.Covers(t, "R-PIPE-001")
	key := forge.NewSecret("x")
	errs := map[string]error{}
	_, errs["forgejo"] = forgejo.New(forgejo.Config{BaseURL: "http://x.test", BotLogin: "b", BotPassword: key, WebhookSecret: key})
	_, errs["github"] = github.New(github.Config{BaseURL: "http://x.test", AppID: 1, InstallationID: 1, PrivateKeyPEM: key, WebhookSecret: key})
	_, errs["gitlab"] = gitlab.New(gitlab.Config{BaseURL: "http://x.test", AccessToken: key, SigningToken: key})
	for k, err := range errs {
		var de *diag.Error
		if errors.As(err, &de) && de.Code == diag.CodeNotImplemented {
			harness.RedFeature(t, k+".New")
			continue
		}
		if err == nil || !strings.Contains(err.Error(), "forge: base URL must be https") {
			t.Errorf("%s.New with an http base URL: %v, want \"forge: base URL must be https\"", k, err)
		}
	}
}

// TestFixturesCheckEvent: the invariants of SPEC 6.9.1 (CheckEvent) and R-PIPE-008's option
// injection: a push whose `after` is an option is refused with ErrPayload (HTTP 400), not
// passed on to git.
func TestFixturesCheckEvent(t *testing.T) {
	parity.Covers(t, "R-PIPE-001")
	parity.Covers(t, "R-PIPE-008")
	s, ok := buildAdapters(t)
	if !ok {
		return
	}
	d := recorded(t, "forgejo", "push.main")
	secret, _ := forgefix.Secret("forgejo/SECRET.test")
	resign := func(mod func(map[string]any)) delivery {
		mm := jsonOf(t, d.body)
		mod(mm)
		body, _ := json.Marshal(mm)
		mac := hmac.New(sha256.New, []byte(secret))
		mac.Write(body)
		h := d.h.Clone()
		h.Set("X-Forgejo-Signature", hex.EncodeToString(mac.Sum(nil)))
		h.Set("X-Hub-Signature-256", "sha256="+hex.EncodeToString(mac.Sum(nil)))
		return delivery{h: h, body: body, now: d.now}
	}
	for name, mod := range map[string]func(map[string]any){
		"after is an option":         func(m map[string]any) { m["after"] = "--upload-pack=x" },
		"after is not a sha":         func(m map[string]any) { m["after"] = "main" },
		"short sha":                  func(m map[string]any) { m["after"] = "c548672" },
		"repository id 0":            func(m map[string]any) { m["repository"].(map[string]any)["id"] = 0 },
		"http clone url":             func(m map[string]any) { m["repository"].(map[string]any)["clone_url"] = "http://x.test/a/b.git" },
		"ref name over 255 bytes":    func(m map[string]any) { m["ref"] = "refs/heads/" + strings.Repeat("a", 256) },
		"ref is not a branch or tag": func(m map[string]any) { m["ref"] = "refs/notes/x" },
	} {
		rd := resign(mod)
		if _, err := s.forgejo.ParseWebhook(rd.h, rd.body, rd.now); !errors.Is(err, forge.ErrPayload) {
			t.Errorf("%s: %v, want ErrPayload", name, err)
		}
	}
}
