// Package gitlab is the GitLab adapter of internal/forge, built on GitLab's official Go client
// gitlab.com/gitlab-org/api/client-go. Webhooks are verified with the signing token of
// GitLab ≥ 19.0 (Standard Webhooks HMAC over "id.timestamp.body", ±5 minutes); the plaintext
// X-Gitlab-Token only when the integration opts in for older GitLab (S16-6). Checkout
// credentials are project deploy tokens with read_repository that GitLab expires itself.
package gitlab

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	gl "gitlab.com/gitlab-org/api/client-go"

	"github.com/megakuul/bob/internal/forge"
)

// Config configures one gitlab integration.
type Config struct {
	BaseURL                   string
	AccessToken, SigningToken forge.Secret
	AllowPlaintextSecret      bool
	HTTP                      *http.Client // nil: forge.HTTPClient()
}

// tolerance is the replay window of the webhook-timestamp header (SPEC §6.9.2).
const tolerance = 5 * time.Minute

type adapter struct {
	cfg Config
	key []byte // decoded signing key, nil when the token is not a whsec_ token
	c   *gl.Client
}

// New builds the adapter. It refuses a non-https base URL.
func New(cfg Config) (forge.Adapter, error) {
	base, err := forge.CheckBaseURL(cfg.BaseURL)
	if err != nil {
		return nil, err
	}
	a := &adapter{cfg: cfg}
	if raw, ok := strings.CutPrefix(cfg.SigningToken.Reveal(), "whsec_"); ok {
		k, err := base64.StdEncoding.DecodeString(raw)
		if err != nil || len(k) == 0 {
			return nil, errors.New("forge: gitlab signing token is not whsec_<base64>")
		}
		a.key = k
	}
	c, err := gl.NewClient(cfg.AccessToken.Reveal(), gl.WithBaseURL(base+"/api/v4"), gl.WithHTTPClient(forge.ClientOr(cfg.HTTP)),
		gl.WithCustomRetryMax(2), gl.WithUserAgent("bob"))
	if err != nil {
		return nil, fmt.Errorf("gitlab: %w", err)
	}
	a.c = c
	return a, nil
}

func (a *adapter) Kind() forge.Kind { return forge.GitLab }

func apiErr(op string, resp *gl.Response, err error) error {
	if err == nil {
		return nil
	}
	return forge.HTTPError("gitlab: "+op, statusOf(resp), err)
}

func statusOf(resp *gl.Response) int {
	if resp == nil || resp.Response == nil {
		return 0
	}
	return resp.StatusCode
}

// ---- webhooks ----

// verify checks webhook-signature (any "v1,<b64>" entry of the space-separated list) and then
// the timestamp, so a forger learns nothing from the order of the checks.
func (a *adapter) verify(h http.Header, body []byte, now time.Time) error {
	sigs := h.Get("Webhook-Signature")
	if sigs == "" && a.cfg.AllowPlaintextSecret {
		got, want := h.Get("X-Gitlab-Token"), a.cfg.SigningToken.Reveal()
		if got == "" || want == "" || subtle.ConstantTimeCompare([]byte(got), []byte(want)) != 1 {
			return fmt.Errorf("%w: X-Gitlab-Token mismatch", forge.ErrSignature)
		}
		return nil
	}
	if a.key == nil {
		return fmt.Errorf("%w: no signing token configured", forge.ErrSignature)
	}
	id, ts := h.Get("Webhook-Id"), h.Get("Webhook-Timestamp")
	if id == "" || ts == "" || sigs == "" {
		return fmt.Errorf("%w: webhook-id, -timestamp or -signature missing", forge.ErrSignature)
	}
	sec, err := strconv.ParseInt(ts, 10, 64)
	if err != nil {
		return fmt.Errorf("%w: webhook-timestamp is not a number", forge.ErrSignature)
	}
	m := hmac.New(sha256.New, a.key)
	m.Write([]byte(id + "." + ts + "."))
	m.Write(body)
	want := m.Sum(nil)
	ok := false
	for _, s := range strings.Fields(sigs) {
		v, b64, found := strings.Cut(s, ",")
		if !found || v != "v1" {
			continue
		}
		if got, err := base64.StdEncoding.DecodeString(b64); err == nil && hmac.Equal(got, want) {
			ok = true
		}
	}
	if !ok {
		return fmt.Errorf("%w: webhook-signature mismatch", forge.ErrSignature)
	}
	if d := now.Sub(time.Unix(sec, 0)); d > tolerance || d < -tolerance {
		return fmt.Errorf("%w: %v", forge.ErrStale, d.Round(time.Second))
	}
	return nil
}

func repoOf(id int64, path, cloneURL, webURL, defaultBranch string, vis gl.VisibilityValue) forge.Repo {
	return forge.Repo{Forge: forge.GitLab, ID: id, FullName: path, CloneURL: cloneURL, WebURL: webURL,
		DefaultBranch: defaultBranch, Private: vis != gl.PublicVisibility}
}

// ParseWebhook verifies the delivery and normalizes push, tag push and merge request hooks.
func (a *adapter) ParseWebhook(h http.Header, body []byte, now time.Time) (forge.Event, error) {
	if err := forge.CheckSize(body); err != nil {
		return forge.Event{}, err
	}
	if err := a.verify(h, body, now); err != nil {
		return forge.Event{}, err
	}
	ev := forge.Event{Forge: forge.GitLab, DeliveryID: h.Get("Webhook-Id")}
	if ev.DeliveryID == "" {
		ev.DeliveryID = h.Get("Idempotency-Key")
	}
	et := gl.EventType(h.Get("X-Gitlab-Event"))
	switch et {
	case gl.EventTypePush, gl.EventTypeTagPush, gl.EventTypeMergeRequest:
	default:
		ev.Type, ev.IgnoredReason = forge.EventIgnored, "gitlab event "+truncName(string(et))
		return ev, forge.CheckEvent(ev)
	}
	raw, err := gl.ParseWebhook(et, body)
	if err != nil {
		return forge.Event{}, fmt.Errorf("%w: %v", forge.ErrPayload, err)
	}
	switch p := raw.(type) {
	case *gl.PushEvent:
		pr := p.Project
		ev.Repo = repoOf(p.ProjectID, pr.PathWithNamespace, pr.GitHTTPURL, pr.WebURL, pr.DefaultBranch, pr.Visibility)
		ev.Sender = p.UserUsername
		kind, name := forge.SplitRef(p.Ref)
		if kind != "branch" {
			return forge.Event{}, fmt.Errorf("%w: push hook for a ref that is not a branch", forge.ErrPayload)
		}
		ev.Type = forge.EventPush
		ev.Push = &forge.Push{Branch: name, Before: p.Before, After: p.After, Created: forge.IsZeroSHA(p.Before), Deleted: forge.IsZeroSHA(p.After)}
	case *gl.TagEvent:
		pr := p.Project
		ev.Repo = repoOf(p.ProjectID, pr.PathWithNamespace, pr.GitHTTPURL, pr.WebURL, pr.DefaultBranch, pr.Visibility)
		ev.Sender = p.UserUsername
		kind, name := forge.SplitRef(p.Ref)
		if kind != "tag" {
			return forge.Event{}, fmt.Errorf("%w: tag push hook for a ref that is not a tag", forge.ErrPayload)
		}
		ev.Type = forge.EventTag
		// checkout_sha is the peeled commit; after is the tag object of an annotated tag.
		ev.Tag = &forge.Tag{Name: name, Deleted: forge.IsZeroSHA(p.After)}
		if !ev.Tag.Deleted {
			ev.Tag.Commit = p.CheckoutSHA
		}
	case *gl.MergeEvent:
		pr := p.Project
		ev.Repo = repoOf(pr.ID, pr.PathWithNamespace, pr.GitHTTPURL, pr.WebURL, pr.DefaultBranch, pr.Visibility)
		if p.User != nil {
			ev.Sender = p.User.Username
		}
		oa := p.ObjectAttributes
		out := &forge.PullRequest{Number: oa.IID, Title: oa.Title, HeadSHA: oa.LastCommit.ID, HeadBranch: oa.SourceBranch,
			BaseBranch: oa.TargetBranch, FromFork: oa.SourceProjectID != oa.TargetProjectID, Merged: oa.State == "merged",
			FetchRef: "refs/merge-requests/" + strconv.FormatInt(oa.IID, 10) + "/head"}
		if oa.Source != nil {
			out.HeadRepo = oa.Source.PathWithNamespace
		}
		switch {
		case oa.Action == "open":
			out.Action = forge.PROpened
		case oa.Action == "update" && oa.OldRev != "":
			out.Action = forge.PRSynchronized // without oldrev an update is a metadata edit
		case oa.Action == "reopen":
			out.Action = forge.PRReopened
		case oa.Action == "close" || oa.Action == "merge":
			out.Action = forge.PRClosed
		default:
			ev.Type, ev.IgnoredReason = forge.EventIgnored, "merge_request action "+truncName(oa.Action)
			return ev, forge.CheckEvent(ev)
		}
		ev.Type, ev.PullRequest = forge.EventPullRequest, out
	default:
		return forge.Event{}, fmt.Errorf("%w: unexpected %s payload", forge.ErrPayload, et)
	}
	if err := forge.CheckEvent(ev); err != nil {
		return forge.Event{}, err
	}
	return ev, nil
}

func truncName(s string) string {
	if s == "" {
		return "(none)"
	}
	return forge.Truncate(s, 64)
}

// ---- statuses ----

// mapState: GitLab has pending, running, success, failed, canceled (SPEC §6.9.2).
func mapState(s forge.State) (gl.BuildStateValue, error) {
	switch s {
	case forge.StatePending:
		return gl.Pending, nil
	case forge.StateRunning:
		return gl.Running, nil
	case forge.StateSuccess:
		return gl.Success, nil
	case forge.StateFailure, forge.StateError:
		return gl.Failed, nil
	case forge.StateCancelled:
		return gl.Canceled, nil
	}
	return "", fmt.Errorf("gitlab: unknown status state %q", s)
}

// SetStatus writes a commit status named s.Context.
func (a *adapter) SetStatus(ctx context.Context, r forge.Repo, s forge.Status) error {
	st, err := mapState(s.State)
	if err != nil {
		return err
	}
	_, resp, err := a.c.Commits.SetCommitStatus(r.ID, s.SHA, &gl.SetCommitStatusOptions{State: st, Name: gl.Ptr(s.Context),
		TargetURL: gl.Ptr(s.TargetURL), Description: gl.Ptr(forge.Description(s, 255))}, gl.WithContext(ctx))
	// GitLab refuses a transition to the state the status already has (HTTP 400 "Cannot
	// transition status via :x from :x", S16); for bob a report is idempotent.
	if err != nil && statusOf(resp) == http.StatusBadRequest && strings.Contains(err.Error(), "Cannot transition status") {
		return nil
	}
	return apiErr("set status", resp, err)
}

// ---- checkout ----

// IssueCheckout creates a project deploy token bob-job-<jobID> with read_repository that
// GitLab expires at now + TTL (deploy token expiry is a timestamp, enforced to the second).
func (a *adapter) IssueCheckout(ctx context.Context, req forge.CheckoutRequest) (forge.Checkout, error) {
	ttl := req.TTL
	if ttl <= 0 || ttl > time.Hour {
		ttl = time.Hour
	}
	exp := time.Now().Add(ttl).UTC().Truncate(time.Second)
	t, resp, err := a.c.DeployTokens.CreateProjectDeployToken(req.Repo.ID, &gl.CreateProjectDeployTokenOptions{
		Name: gl.Ptr("bob-job-" + req.JobID), ExpiresAt: &exp, Scopes: &[]string{"read_repository"},
	}, gl.WithContext(ctx))
	if err != nil {
		return forge.Checkout{}, apiErr("create deploy token", resp, err)
	}
	if t.Token == "" || t.ID <= 0 {
		return forge.Checkout{}, errors.New("gitlab: deploy token response without token")
	}
	if t.ExpiresAt != nil && !t.ExpiresAt.IsZero() {
		exp = *t.ExpiresAt
	}
	return forge.Checkout{CloneURL: req.Repo.CloneURL, Username: t.Username, Token: forge.NewSecret(t.Token),
		ExpiresAt: exp, ForgeExpires: true, Handle: strconv.FormatInt(req.Repo.ID, 10) + ":" + strconv.FormatInt(t.ID, 10)}, nil
}

// RevokeCheckout deletes the deploy token; an unknown one is already gone.
func (a *adapter) RevokeCheckout(ctx context.Context, co forge.Checkout) error {
	p, id, ok := strings.Cut(co.Handle, ":")
	pid, err1 := strconv.ParseInt(p, 10, 64)
	tid, err2 := strconv.ParseInt(id, 10, 64)
	if !ok || err1 != nil || err2 != nil {
		return fmt.Errorf("gitlab: checkout handle %q", co.Handle)
	}
	resp, err := a.c.DeployTokens.DeleteProjectDeployToken(pid, tid, gl.WithContext(ctx))
	if statusOf(resp) == http.StatusNotFound {
		return nil
	}
	return apiErr("delete deploy token", resp, err)
}

// SweepCheckouts: GitLab expires deploy tokens itself.
func (a *adapter) SweepCheckouts(context.Context, time.Time) (int, error) { return 0, nil }

// ---- repositories and hooks ----

// LookupRepo resolves a project by its full path.
func (a *adapter) LookupRepo(ctx context.Context, fullName string) (forge.Repo, error) {
	if fullName == "" || strings.HasPrefix(fullName, "/") {
		return forge.Repo{}, fmt.Errorf("gitlab: project path %q: %w", fullName, forge.ErrNotFound)
	}
	p, resp, err := a.c.Projects.GetProject(fullName, nil, gl.WithContext(ctx))
	if err != nil {
		return forge.Repo{}, apiErr("get project", resp, err)
	}
	repo := repoOf(p.ID, p.PathWithNamespace, p.HTTPURLToRepo, p.WebURL, p.DefaultBranch, p.Visibility)
	if repo.ID <= 0 || !strings.HasPrefix(repo.CloneURL, "https://") {
		return forge.Repo{}, fmt.Errorf("gitlab: project %s: id %d, clone URL %q is not https", fullName, repo.ID, repo.CloneURL)
	}
	return repo, nil
}

// EnsureWebhook creates bob's hook (identified by its URL) or updates it. client-go has no
// signing_token option, so the call goes through its NewRequest/Do with the documented field
// (S16-9).
func (a *adapter) EnsureWebhook(ctx context.Context, r forge.Repo, hookURL string, secret forge.Secret) error {
	if secret.IsZero() {
		return errors.New("gitlab: webhook signing token is empty")
	}
	body := map[string]any{"url": hookURL, "push_events": true, "tag_push_events": true, "merge_requests_events": true,
		"enable_ssl_verification": strings.HasPrefix(hookURL, "https://")}
	if strings.HasPrefix(secret.Reveal(), "whsec_") {
		body["signing_token"] = secret.Reveal()
	} else {
		body["token"] = secret.Reveal()
	}
	method, path := http.MethodPost, fmt.Sprintf("projects/%d/hooks", r.ID)
	for page := int64(1); page <= 20; page++ {
		hooks, resp, err := a.c.Projects.ListProjectHooks(r.ID, &gl.ListProjectHooksOptions{ListOptions: gl.ListOptions{Page: page, PerPage: 100}}, gl.WithContext(ctx))
		if err != nil {
			return apiErr("list hooks", resp, err)
		}
		found := false
		for _, h := range hooks {
			if h.URL == hookURL {
				method, path, found = http.MethodPut, fmt.Sprintf("projects/%d/hooks/%d", r.ID, h.ID), true
				break
			}
		}
		if found || len(hooks) < 100 {
			break
		}
	}
	req, err := a.c.NewRequest(method, path, body, []gl.RequestOptionFunc{gl.WithContext(ctx)})
	if err != nil {
		return fmt.Errorf("gitlab: hook request: %w", err)
	}
	resp, err := a.c.Do(req, nil)
	return apiErr("ensure hook", resp, err)
}

var _ forge.Adapter = (*adapter)(nil)
