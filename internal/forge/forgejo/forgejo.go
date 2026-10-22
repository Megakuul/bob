// Package forgejo is the Forgejo adapter of internal/forge. API calls go through the community
// Forgejo SDK codeberg.org/mvdkleijn/forgejo-sdk (MIT, A12); Forgejo has no SDK of its own.
// The webhook payload structs are local: the SDK has none, and Forgejo's own are GPL-3.0
// server code (S16-10).
//
// Credentials (D62): bob holds the bot account's password. Forgejo accepts token creation and
// deletion only with basic auth and the password (S16 §3.3), so the adapter mints a per-job
// token limited to the one repository with read:repository, and, per repository, a status
// token with write:repository that never leaves the orchestrator.
package forgejo

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	fj "codeberg.org/mvdkleijn/forgejo-sdk/forgejo/v3"

	"github.com/megakuul/bob/internal/forge"
)

// Config configures one forgejo integration.
type Config struct {
	BaseURL, BotLogin          string
	BotPassword, WebhookSecret forge.Secret
}

// serverVersion is what the SDK is told the server runs, so that it does not probe the version
// on every client construction (S16: tested against Forgejo 16).
const serverVersion = "11.0.0"

// Token names carry the creation time: Forgejo tokens have no expiry and the SDK's token type
// no creation time, so the sweeper works from the name alone (D62). Names are unique per user.
const (
	jobPrefix    = "bob-job-"
	statusPrefix = "bob-status-"
)

type adapter struct {
	cfg  Config
	http *http.Client

	mu     sync.Mutex
	status map[int64]statusToken // repo id -> this process' status token
}

type statusToken struct {
	id    int64
	value string
}

// New builds the adapter. It refuses a non-https base URL.
func New(cfg Config) (forge.Adapter, error) {
	base, err := forge.CheckBaseURL(cfg.BaseURL)
	if err != nil {
		return nil, err
	}
	if cfg.BotLogin == "" {
		return nil, errors.New("forge: forgejo needs the bot login")
	}
	cfg.BaseURL = base
	return &adapter{cfg: cfg, http: forge.HTTPClient(), status: map[int64]statusToken{}}, nil
}

func (a *adapter) Kind() forge.Kind { return forge.Forgejo }

// client builds an SDK client for one call: the SDK keeps the context on the client.
func (a *adapter) client(ctx context.Context, auth fj.ClientOption) (*fj.Client, error) {
	c, err := fj.NewClient(a.cfg.BaseURL, fj.SetHTTPClient(a.http), fj.SetContext(ctx),
		fj.SetForgejoVersion(serverVersion), fj.SetUserAgent("bob"), auth)
	if err != nil {
		return nil, fmt.Errorf("forgejo: %w", err)
	}
	return c, nil
}

func (a *adapter) bot(ctx context.Context) (*fj.Client, error) {
	return a.client(ctx, fj.SetBasicAuth(a.cfg.BotLogin, a.cfg.BotPassword.Reveal()))
}

func apiErr(op string, resp *fj.Response, err error) error {
	if err == nil {
		return nil
	}
	code := 0
	if resp != nil && resp.Response != nil {
		code = resp.StatusCode
	}
	return forge.HTTPError("forgejo: "+op, code, err)
}

// ---- webhooks ----

type repoJSON struct {
	ID            int64  `json:"id"`
	FullName      string `json:"full_name"`
	CloneURL      string `json:"clone_url"`
	HTMLURL       string `json:"html_url"`
	DefaultBranch string `json:"default_branch"`
	Private       bool   `json:"private"`
}

type userJSON struct {
	Login string `json:"login"`
}

type pushJSON struct {
	Ref        string `json:"ref"`
	Before     string `json:"before"`
	After      string `json:"after"`
	HeadCommit *struct {
		ID string `json:"id"`
	} `json:"head_commit"`
	Repository repoJSON `json:"repository"`
	Sender     userJSON `json:"sender"`
}

type prJSON struct {
	Action      string `json:"action"`
	Number      int64  `json:"number"`
	PullRequest struct {
		Title  string `json:"title"`
		Merged bool   `json:"merged"`
		Head   struct {
			Ref    string    `json:"ref"`
			SHA    string    `json:"sha"`
			RepoID int64     `json:"repo_id"`
			Repo   *repoJSON `json:"repo"`
		} `json:"head"`
		Base struct {
			Ref    string `json:"ref"`
			RepoID int64  `json:"repo_id"`
		} `json:"base"`
	} `json:"pull_request"`
	Repository repoJSON `json:"repository"`
	Sender     userJSON `json:"sender"`
}

func repoOf(r repoJSON) forge.Repo {
	return forge.Repo{Forge: forge.Forgejo, ID: r.ID, FullName: r.FullName, CloneURL: r.CloneURL,
		WebURL: r.HTMLURL, DefaultBranch: r.DefaultBranch, Private: r.Private}
}

func payload(body []byte, v any) error {
	if err := json.Unmarshal(body, v); err != nil {
		return fmt.Errorf("%w: %v", forge.ErrPayload, err)
	}
	return nil
}

// ParseWebhook verifies X-Forgejo-Signature (hex HMAC-SHA256 of the body) and normalizes.
func (a *adapter) ParseWebhook(h http.Header, body []byte, _ time.Time) (forge.Event, error) {
	if err := forge.CheckSize(body); err != nil {
		return forge.Event{}, err
	}
	if err := forge.VerifyHexHMAC(a.cfg.WebhookSecret, body, h.Get("X-Forgejo-Signature"), ""); err != nil {
		return forge.Event{}, err
	}
	ev := forge.Event{Forge: forge.Forgejo, DeliveryID: h.Get("X-Forgejo-Delivery")}
	kind := h.Get("X-Forgejo-Event")
	switch kind {
	case "push":
		var p pushJSON
		if err := payload(body, &p); err != nil {
			return forge.Event{}, err
		}
		ev.Repo, ev.Sender = repoOf(p.Repository), p.Sender.Login
		refKind, name := forge.SplitRef(p.Ref)
		switch refKind {
		case "branch":
			ev.Type = forge.EventPush
			ev.Push = &forge.Push{Branch: name, Before: p.Before, After: p.After,
				Created: forge.IsZeroSHA(p.Before), Deleted: forge.IsZeroSHA(p.After)}
		case "tag":
			ev.Type = forge.EventTag
			t := &forge.Tag{Name: name, Deleted: forge.IsZeroSHA(p.After)}
			if !t.Deleted {
				// An annotated tag's after is the tag object; head_commit is the commit (S16).
				if p.HeadCommit == nil {
					return forge.Event{}, fmt.Errorf("%w: tag push without head_commit", forge.ErrPayload)
				}
				t.Commit = p.HeadCommit.ID
			}
			ev.Tag = t
		default:
			return forge.Event{}, fmt.Errorf("%w: push to a ref that is neither a branch nor a tag", forge.ErrPayload)
		}
	case "pull_request":
		var p prJSON
		if err := payload(body, &p); err != nil {
			return forge.Event{}, err
		}
		ev.Repo, ev.Sender = repoOf(p.Repository), p.Sender.Login
		pr := &forge.PullRequest{Number: p.Number, Title: p.PullRequest.Title, HeadSHA: p.PullRequest.Head.SHA,
			HeadBranch: p.PullRequest.Head.Ref, BaseBranch: p.PullRequest.Base.Ref, Merged: p.PullRequest.Merged,
			FromFork: p.PullRequest.Head.RepoID != p.PullRequest.Base.RepoID,
			FetchRef: "refs/pull/" + strconv.FormatInt(p.Number, 10) + "/head"}
		if p.PullRequest.Head.Repo != nil {
			pr.HeadRepo = p.PullRequest.Head.Repo.FullName
		}
		switch p.Action {
		case "opened":
			pr.Action = forge.PROpened
		case "synchronized":
			pr.Action = forge.PRSynchronized
		case "reopened":
			pr.Action = forge.PRReopened
		case "closed":
			pr.Action = forge.PRClosed
		default:
			ev.Type, ev.IgnoredReason = forge.EventIgnored, "pull_request action "+truncName(p.Action)
			return ev, forge.CheckEvent(ev)
		}
		ev.Type, ev.PullRequest = forge.EventPullRequest, pr
	default:
		// create and delete repeat what push says; release is no v1 trigger (S16-4).
		ev.Type, ev.IgnoredReason = forge.EventIgnored, "forgejo event "+truncName(kind)
	}
	if err := forge.CheckEvent(ev); err != nil {
		return forge.Event{}, err
	}
	return ev, nil
}

// truncName bounds header or payload text that ends up in a stored reason.
func truncName(s string) string {
	if s == "" {
		return "(none)"
	}
	return forge.Truncate(s, 64)
}

// ---- statuses ----

// mapState: Forgejo has pending, success, error, failure, warning (SPEC §6.9.2).
func mapState(s forge.State) (fj.StatusState, error) {
	switch s {
	case forge.StatePending, forge.StateRunning:
		return fj.StatusPending, nil
	case forge.StateSuccess:
		return fj.StatusSuccess, nil
	case forge.StateFailure:
		return fj.StatusFailure, nil
	case forge.StateError:
		return fj.StatusError, nil
	case forge.StateCancelled:
		return fj.StatusWarning, nil
	}
	return "", fmt.Errorf("forgejo: unknown status state %q", s)
}

func ownerName(full string) (string, string, error) {
	o, n, ok := strings.Cut(full, "/")
	if !ok || o == "" || n == "" || strings.Contains(n, "/") {
		return "", "", fmt.Errorf("forgejo: repository name %q is not owner/name", full)
	}
	return o, n, nil
}

// SetStatus writes the status with this process' status token for the repository; a token
// that stopped working (swept by another orchestrator, deleted by an admin) is replaced once.
func (a *adapter) SetStatus(ctx context.Context, r forge.Repo, s forge.Status) error {
	st, err := mapState(s.State)
	if err != nil {
		return err
	}
	o, n, err := ownerName(r.FullName)
	if err != nil {
		return err
	}
	opt := fj.CreateStatusOption{State: st, TargetURL: s.TargetURL, Description: forge.Description(s, 255), Context: s.Context}
	for attempt := 0; ; attempt++ {
		tok, err := a.statusToken(ctx, r)
		if err != nil {
			return err
		}
		c, err := a.client(ctx, fj.SetToken(tok.value))
		if err != nil {
			return err
		}
		_, resp, err := c.CreateStatus(o, n, s.SHA, opt)
		err = apiErr("set status", resp, err)
		if errors.Is(err, forge.ErrAuth) && attempt == 0 {
			a.dropStatus(r.ID, tok)
			continue
		}
		return err
	}
}

func (a *adapter) dropStatus(repo int64, tok statusToken) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.status[repo] == tok {
		delete(a.status, repo)
	}
}

// statusToken returns the cached status token of repo or mints one (write:repository, limited
// to the repository).
func (a *adapter) statusToken(ctx context.Context, r forge.Repo) (statusToken, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if t, ok := a.status[r.ID]; ok {
		return t, nil
	}
	name := fmt.Sprintf("%s%d-%d", statusPrefix, time.Now().Unix(), r.ID)
	id, value, err := a.mint(ctx, name, "write:repository", r.FullName)
	if err != nil {
		return statusToken{}, err
	}
	t := statusToken{id: id, value: value}
	a.status[r.ID] = t
	return t, nil
}

// mint creates a bot token limited to one repository. The SDK's CreateAccessTokenOption has
// no repositories field (forgejo-sdk issue #149), so this one call is a plain request against
// the documented API (A12).
func (a *adapter) mint(ctx context.Context, name, scope, fullName string) (int64, string, error) {
	o, n, err := ownerName(fullName)
	if err != nil {
		return 0, "", err
	}
	body, err := json.Marshal(map[string]any{"name": name, "scopes": []string{scope},
		"repositories": []map[string]string{{"owner": o, "name": n}}})
	if err != nil {
		return 0, "", err
	}
	u := a.cfg.BaseURL + "/api/v1/users/" + url.PathEscape(a.cfg.BotLogin) + "/tokens"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(body))
	if err != nil {
		return 0, "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "bob")
	req.SetBasicAuth(a.cfg.BotLogin, a.cfg.BotPassword.Reveal())
	resp, err := a.http.Do(req)
	if err != nil {
		return 0, "", fmt.Errorf("forgejo: create token: %w", err)
	}
	defer resp.Body.Close()
	rb, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return 0, "", fmt.Errorf("forgejo: create token: %w", err)
	}
	if resp.StatusCode != http.StatusCreated {
		return 0, "", forge.HTTPError("forgejo: create token", resp.StatusCode, fmt.Errorf("HTTP %d: %.200s", resp.StatusCode, rb))
	}
	var t struct {
		ID    int64  `json:"id"`
		Token string `json:"sha1"`
	}
	if err := json.Unmarshal(rb, &t); err != nil || t.Token == "" || t.ID <= 0 {
		return 0, "", errors.New("forgejo: create token: response without token")
	}
	return t.ID, t.Token, nil
}

// ---- checkout tokens ----

// IssueCheckout mints bob-job-<unix-seconds>-<jobID>, read:repository, limited to the repository
// (D62). Forgejo tokens do not expire: bob revokes at job end and sweeps leftovers.
func (a *adapter) IssueCheckout(ctx context.Context, req forge.CheckoutRequest) (forge.Checkout, error) {
	if req.JobID == "" || strings.ContainsAny(req.JobID, " /") {
		return forge.Checkout{}, fmt.Errorf("forgejo: job id %q", req.JobID)
	}
	now := time.Now()
	id, value, err := a.mint(ctx, fmt.Sprintf("%s%d-%s", jobPrefix, now.Unix(), req.JobID), "read:repository", req.Repo.FullName)
	if err != nil {
		return forge.Checkout{}, err
	}
	ttl := req.TTL
	if ttl <= 0 || ttl > time.Hour {
		ttl = time.Hour
	}
	return forge.Checkout{CloneURL: req.Repo.CloneURL, Username: a.cfg.BotLogin, Token: forge.NewSecret(value),
		ExpiresAt: now.Add(ttl), Handle: strconv.FormatInt(id, 10)}, nil
}

// RevokeCheckout deletes the token; an unknown one is already gone.
func (a *adapter) RevokeCheckout(ctx context.Context, co forge.Checkout) error {
	id, err := strconv.ParseInt(co.Handle, 10, 64)
	if err != nil || id <= 0 {
		return fmt.Errorf("forgejo: checkout handle %q", co.Handle)
	}
	c, err := a.bot(ctx)
	if err != nil {
		return err
	}
	resp, err := c.DeleteAccessToken(a.cfg.BotLogin, id)
	if resp != nil && resp.Response != nil && resp.StatusCode == http.StatusNotFound {
		return nil
	}
	return apiErr("delete token", resp, err)
}

func tokenTime(name string) (prefix string, at time.Time, ok bool) {
	for _, p := range []string{jobPrefix, statusPrefix} {
		rest, found := strings.CutPrefix(name, p)
		if !found {
			continue
		}
		ts, _, found := strings.Cut(rest, "-")
		u, err := strconv.ParseInt(ts, 10, 64)
		if !found || err != nil {
			return "", time.Time{}, false
		}
		return p, time.Unix(u, 0), true
	}
	return "", time.Time{}, false
}

// SweepCheckouts deletes bob's job tokens created before cutoff, and status tokens created
// before cutoff that are not this process' own (an orchestrator whose token is swept mints a
// new one on the next 401).
func (a *adapter) SweepCheckouts(ctx context.Context, cutoff time.Time) (int, error) {
	c, err := a.bot(ctx)
	if err != nil {
		return 0, err
	}
	a.mu.Lock()
	mine := map[int64]bool{}
	for _, t := range a.status {
		mine[t.id] = true
	}
	a.mu.Unlock()
	// Collect first, delete after: deleting while paging would shift the pages.
	var stale []int64
	const pageSize = 50
	for page := 1; page <= 200; page++ {
		toks, resp, err := c.ListAccessTokens(a.cfg.BotLogin, fj.ListAccessTokensOptions{ListOptions: fj.ListOptions{Page: page, PageSize: pageSize}})
		if err != nil {
			return 0, apiErr("list tokens", resp, err)
		}
		for _, t := range toks {
			if p, at, ok := tokenTime(t.Name); ok && at.Before(cutoff) && !(p == statusPrefix && mine[t.ID]) {
				stale = append(stale, t.ID)
			}
		}
		if len(toks) < pageSize {
			break
		}
	}
	n := 0
	for _, id := range stale {
		resp, err := c.DeleteAccessToken(a.cfg.BotLogin, id)
		if err != nil && !(resp != nil && resp.Response != nil && resp.StatusCode == http.StatusNotFound) {
			return n, apiErr("delete token", resp, err)
		}
		n++
	}
	return n, nil
}

// ---- repositories and hooks ----

// LookupRepo resolves owner/name with the bot's credentials.
func (a *adapter) LookupRepo(ctx context.Context, fullName string) (forge.Repo, error) {
	o, n, err := ownerName(fullName)
	if err != nil {
		return forge.Repo{}, fmt.Errorf("%w: %v", forge.ErrNotFound, err)
	}
	c, err := a.bot(ctx)
	if err != nil {
		return forge.Repo{}, err
	}
	r, resp, err := c.GetRepo(o, n)
	if err != nil {
		return forge.Repo{}, apiErr("get repository", resp, err)
	}
	repo := forge.Repo{Forge: forge.Forgejo, ID: r.ID, FullName: r.FullName, CloneURL: r.CloneURL, WebURL: r.HTMLURL,
		DefaultBranch: r.DefaultBranch, Private: r.Private}
	if repo.ID <= 0 || !strings.HasPrefix(repo.CloneURL, "https://") {
		return forge.Repo{}, fmt.Errorf("forgejo: repository %s: id %d, clone URL %q is not https", fullName, repo.ID, repo.CloneURL)
	}
	return repo, nil
}

// hookEvents are the events bob's hook subscribes to; tags arrive as push (S16-4).
var hookEvents = []string{"push", "pull_request", "pull_request_sync"}

// EnsureWebhook creates bob's hook (identified by its URL) or updates it in place.
func (a *adapter) EnsureWebhook(ctx context.Context, r forge.Repo, hookURL string, secret forge.Secret) error {
	if secret.IsZero() {
		return errors.New("forgejo: webhook secret is empty")
	}
	o, n, err := ownerName(r.FullName)
	if err != nil {
		return err
	}
	c, err := a.bot(ctx)
	if err != nil {
		return err
	}
	conf := map[string]string{"url": hookURL, "content_type": "json", "secret": secret.Reveal()}
	for page := 1; page <= 20; page++ {
		hooks, resp, err := c.ListRepoHooks(o, n, fj.ListHooksOptions{ListOptions: fj.ListOptions{Page: page, PageSize: 50}})
		if err != nil {
			return apiErr("list hooks", resp, err)
		}
		for _, h := range hooks {
			if h.Config["url"] == hookURL {
				active := true
				resp, err := c.EditRepoHook(o, n, h.ID, fj.EditHookOption{Config: conf, Events: hookEvents, BranchFilter: "*", Active: &active})
				return apiErr("edit hook", resp, err)
			}
		}
		if len(hooks) < 50 {
			break
		}
	}
	_, resp, err := c.CreateRepoHook(o, n, fj.CreateHookOption{Type: fj.HookTypeForgejo, Config: conf, Events: hookEvents,
		BranchFilter: "*", Active: true})
	return apiErr("create hook", resp, err)
}

var _ forge.Adapter = (*adapter)(nil)
