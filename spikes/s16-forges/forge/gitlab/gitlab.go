// Package gitlab is the GitLab adapter, built on GitLab's official Go client
// (gitlab.com/gitlab-org/api/client-go). Checkout credentials are project deploy tokens with
// read_repository and an expiry timestamp; webhooks are verified with the GitLab >= 19.0
// signing token (Standard Webhooks HMAC), the plaintext X-Gitlab-Token only if configured.
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

	"bobspike/s16/forge"
)

type Config struct {
	BaseURL string // https://gitlab.example.com
	// APIToken: project/group access token or bot PAT with "api" scope and Maintainer role on
	// the linked project (deploy tokens need Maintainer; statuses need Developer).
	APIToken forge.Secret
	// SigningToken is the hook's signing token ("whsec_<base64>"), GitLab >= 19.0.
	SigningToken forge.Secret
	// LegacySecretToken, if set, accepts X-Gitlab-Token (plain shared secret) when no
	// signing token is configured. Off by default.
	LegacySecretToken forge.Secret
	// TimestampTolerance bounds webhook-timestamp skew (replay protection). Default 5 min.
	TimestampTolerance time.Duration
	HTTPClient         *http.Client
}

type Adapter struct {
	cfg Config
	key []byte
	c   *gl.Client
}

func New(cfg Config) (*Adapter, error) {
	if !strings.HasPrefix(cfg.BaseURL, "https://") {
		return nil, fmt.Errorf("gitlab: base URL must be https: %q", cfg.BaseURL)
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = &http.Client{Timeout: 30 * time.Second}
	}
	if cfg.TimestampTolerance == 0 {
		cfg.TimestampTolerance = 5 * time.Minute
	}
	a := &Adapter{cfg: cfg}
	if !cfg.SigningToken.IsZero() {
		raw, ok := strings.CutPrefix(cfg.SigningToken.Reveal(), "whsec_")
		if !ok {
			return nil, errors.New("gitlab: signing token must start with whsec_")
		}
		k, err := base64.StdEncoding.DecodeString(raw)
		if err != nil {
			return nil, fmt.Errorf("gitlab: signing token: %w", err)
		}
		a.key = k
	}
	c, err := gl.NewClient(cfg.APIToken.Reveal(), gl.WithBaseURL(strings.TrimSuffix(cfg.BaseURL, "/")+"/api/v4"), gl.WithHTTPClient(cfg.HTTPClient))
	if err != nil {
		return nil, err
	}
	a.c = c
	return a, nil
}

func (a *Adapter) Kind() forge.Kind { return forge.GitLab }

// verify implements GitLab's documented scheme: signed string "{webhook-id}.{webhook-timestamp}.{body}",
// HMAC-SHA256 with the base64-decoded token, header "v1,<base64>" (space-separated list).
func (a *Adapter) verify(h http.Header, body []byte, now time.Time) error {
	if a.key == nil {
		if a.cfg.LegacySecretToken.IsZero() {
			return fmt.Errorf("%w: no signing token configured", forge.ErrSignature)
		}
		got := h.Get("X-Gitlab-Token")
		if subtle.ConstantTimeCompare([]byte(got), []byte(a.cfg.LegacySecretToken.Reveal())) != 1 {
			return fmt.Errorf("%w: X-Gitlab-Token mismatch", forge.ErrSignature)
		}
		return nil
	}
	id, ts, sigs := h.Get("webhook-id"), h.Get("webhook-timestamp"), h.Get("webhook-signature")
	if id == "" || ts == "" || sigs == "" {
		return fmt.Errorf("%w: webhook-id/-timestamp/-signature missing", forge.ErrSignature)
	}
	sec, err := strconv.ParseInt(ts, 10, 64)
	if err != nil {
		return fmt.Errorf("%w: bad webhook-timestamp", forge.ErrSignature)
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
		got, err := base64.StdEncoding.DecodeString(b64)
		if err == nil && hmac.Equal(got, want) {
			ok = true
		}
	}
	if !ok {
		return fmt.Errorf("%w: webhook-signature mismatch", forge.ErrSignature)
	}
	// Check time after the MAC so an attacker learns nothing from the error.
	if d := now.Sub(time.Unix(sec, 0)); d > a.cfg.TimestampTolerance || d < -a.cfg.TimestampTolerance {
		return fmt.Errorf("%w: %v", forge.ErrStale, d)
	}
	return nil
}

func (a *Adapter) ParseWebhook(h http.Header, body []byte, now time.Time) (forge.Event, error) {
	if len(body) > forge.MaxWebhookBody {
		return forge.Event{}, forge.ErrTooLarge
	}
	if err := a.verify(h, body, now); err != nil {
		return forge.Event{}, err
	}
	ev := forge.Event{Forge: forge.GitLab, DeliveryID: h.Get("webhook-id")}
	if ev.DeliveryID == "" {
		ev.DeliveryID = h.Get("Idempotency-Key")
	}
	et := gl.EventType(h.Get("X-Gitlab-Event"))
	switch et {
	case gl.EventTypePush, gl.EventTypeTagPush, gl.EventTypeMergeRequest:
	default:
		ev.Type, ev.IgnoredReason = forge.EventIgnored, "gitlab event "+string(et)
		return ev, nil
	}
	raw, err := gl.ParseWebhook(et, body)
	if err != nil {
		return forge.Event{}, fmt.Errorf("%w: %v", forge.ErrPayload, err)
	}
	switch p := raw.(type) {
	case *gl.PushEvent:
		ev.Repo = forge.Repo{Forge: forge.GitLab, ID: p.Project.ID, FullName: p.Project.PathWithNamespace, CloneURL: p.Project.GitHTTPURL,
			WebURL: p.Project.WebURL, DefaultBranch: p.Project.DefaultBranch, Private: p.Project.Visibility != gl.PublicVisibility}
		ev.Sender = p.UserUsername
		_, name := forge.SplitRef(p.Ref)
		ev.Type = forge.EventPush
		ev.Push = &forge.Push{Branch: name, Before: p.Before, After: p.After, Created: forge.IsZeroSHA(p.Before), Deleted: forge.IsZeroSHA(p.After)}
	case *gl.TagEvent:
		ev.Repo = forge.Repo{Forge: forge.GitLab, ID: p.Project.ID, FullName: p.Project.PathWithNamespace, CloneURL: p.Project.GitHTTPURL,
			WebURL: p.Project.WebURL, DefaultBranch: p.Project.DefaultBranch, Private: p.Project.Visibility != gl.PublicVisibility}
		ev.Sender = p.UserUsername
		_, name := forge.SplitRef(p.Ref)
		ev.Type = forge.EventTag
		// checkout_sha is the peeled commit; after is the tag object for annotated tags.
		ev.Tag = &forge.Tag{Name: name, Commit: p.CheckoutSHA, Deleted: forge.IsZeroSHA(p.After)}
		if ev.Tag.Deleted {
			ev.Tag.Commit = ""
		}
	case *gl.MergeEvent:
		pr := p.Project
		ev.Repo = forge.Repo{Forge: forge.GitLab, ID: pr.ID, FullName: pr.PathWithNamespace, CloneURL: pr.GitHTTPURL,
			WebURL: pr.WebURL, DefaultBranch: pr.DefaultBranch, Private: pr.Visibility != gl.PublicVisibility}
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
		ev.Type = forge.EventPullRequest
		switch {
		case oa.Action == "open":
			out.Action = forge.PROpened
		case oa.Action == "update" && oa.OldRev != "":
			out.Action = forge.PRSynchronized // update with new commits; without oldrev it is a metadata edit
		case oa.Action == "reopen":
			out.Action = forge.PRReopened
		case oa.Action == "close" || oa.Action == "merge":
			out.Action = forge.PRClosed
		default:
			out.Action = forge.PROther
			ev.Type, ev.IgnoredReason = forge.EventIgnored, "merge_request action "+oa.Action
		}
		ev.PullRequest = out
	}
	if err := forge.CheckEvent(ev); err != nil {
		return forge.Event{}, err
	}
	return ev, nil
}

// GitLab commit status states: pending, running, success, failed, canceled, skipped.
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
	return "", fmt.Errorf("gitlab: unknown state %q", s)
}

func wrapResp(resp *gl.Response, err error) error {
	if resp != nil {
		switch resp.StatusCode {
		case 401, 403:
			return fmt.Errorf("%w: %v", forge.ErrAuth, err)
		case 404:
			return fmt.Errorf("%w: %v", forge.ErrNotFound, err)
		}
	}
	return fmt.Errorf("gitlab: %w", err)
}

func (a *Adapter) SetStatus(ctx context.Context, r forge.Repo, s forge.Status) error {
	st, err := mapState(s.State)
	if err != nil {
		return err
	}
	_, resp, err := a.c.Commits.SetCommitStatus(r.ID, s.SHA, &gl.SetCommitStatusOptions{State: st, Name: gl.Ptr(s.Context),
		TargetURL: gl.Ptr(s.TargetURL), Description: gl.Ptr(forge.Truncate(s.Description, 255))}, gl.WithContext(ctx))
	if err != nil {
		// GitLab refuses a transition to the state the status already has (HTTP 400
		// "Cannot transition status via :x from :x"). Reports are idempotent for bob.
		if resp != nil && resp.StatusCode == 400 && strings.Contains(err.Error(), "Cannot transition status") {
			return nil
		}
		return wrapResp(resp, err)
	}
	return nil
}

const tokenPrefix = "bob-job-"

// IssueCheckout creates a project deploy token with read_repository that GitLab expires at
// now+TTL (expires_at is a timestamp for deploy tokens, unlike access tokens' date).
func (a *Adapter) IssueCheckout(ctx context.Context, req forge.CheckoutRequest) (forge.Checkout, error) {
	ttl := req.TTL
	if ttl <= 0 {
		ttl = time.Hour
	}
	exp := time.Now().Add(ttl).UTC().Truncate(time.Second)
	t, resp, err := a.c.DeployTokens.CreateProjectDeployToken(req.Repo.ID, &gl.CreateProjectDeployTokenOptions{
		Name: gl.Ptr(tokenPrefix + req.JobID), ExpiresAt: &exp, Scopes: &[]string{"read_repository"},
	}, gl.WithContext(ctx))
	if err != nil {
		return forge.Checkout{}, wrapResp(resp, err)
	}
	if t.Token == "" {
		return forge.Checkout{}, errors.New("gitlab: deploy token response without token")
	}
	got := exp
	if t.ExpiresAt != nil {
		got = *t.ExpiresAt
	}
	return forge.Checkout{CloneURL: req.Repo.CloneURL, Username: t.Username, Token: forge.NewSecret(t.Token),
		ExpiresAt: got, ForgeExpires: true, Handle: strconv.FormatInt(req.Repo.ID, 10) + ":" + strconv.FormatInt(t.ID, 10)}, nil
}

func (a *Adapter) RevokeCheckout(ctx context.Context, co forge.Checkout) error {
	p, id, ok := strings.Cut(co.Handle, ":")
	pid, err1 := strconv.ParseInt(p, 10, 64)
	tid, err2 := strconv.ParseInt(id, 10, 64)
	if !ok || err1 != nil || err2 != nil {
		return fmt.Errorf("gitlab: bad handle %q", co.Handle)
	}
	resp, err := a.c.DeployTokens.DeleteProjectDeployToken(pid, tid, gl.WithContext(ctx))
	if resp != nil && resp.StatusCode == 404 {
		return nil
	}
	if err != nil {
		return wrapResp(resp, err)
	}
	return nil
}

// SweepCheckouts: GitLab expires deploy tokens itself; expired rows stay listed, so deleting
// them is housekeeping only and is left to a per-project call in the SPEC.
func (a *Adapter) SweepCheckouts(context.Context, time.Time) (int, error) { return 0, nil }

var _ forge.Adapter = (*Adapter)(nil)
