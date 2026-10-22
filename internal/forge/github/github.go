// Package github is the GitHub adapter of internal/forge, built on the official SDK
// google/go-github (D48). bob authenticates as a GitHub App: an RS256 JWT signed with the app
// key (standard library, S16-5) mints installation tokens that GitHub expires after one hour
// and that are limited to one repository id.
package github

import (
	"context"
	"crypto"
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
	"sync"
	"time"

	gh "github.com/google/go-github/v88/github"

	"github.com/megakuul/bob/internal/forge"
)

// Config configures one github integration.
type Config struct {
	BaseURL                      string
	AppID, InstallationID        int64
	PrivateKeyPEM, WebhookSecret forge.Secret
}

// tokenLifetime is GitHub's fixed lifetime of installation tokens.
const tokenLifetime = time.Hour

type adapter struct {
	cfg  Config
	base string // API base URL with a trailing slash
	key  *rsa.PrivateKey
	http *http.Client

	mu       sync.Mutex
	status   map[int64]cachedToken  // repo id -> statuses:write token (never leaves the orchestrator)
	checkout map[string]cachedToken // handle -> checkout token minted by this process
}

type cachedToken struct {
	value   string
	expires time.Time
}

// New builds the adapter. It refuses a non-https base URL.
func New(cfg Config) (forge.Adapter, error) {
	base, err := forge.CheckBaseURL(cfg.BaseURL)
	if err != nil {
		return nil, err
	}
	if cfg.AppID <= 0 || cfg.InstallationID <= 0 {
		return nil, errors.New("forge: github needs the app id and the installation id")
	}
	key, err := parseKey(cfg.PrivateKeyPEM.Reveal())
	if err != nil {
		return nil, err
	}
	return &adapter{cfg: cfg, base: base + "/", key: key, http: forge.HTTPClient(),
		status: map[int64]cachedToken{}, checkout: map[string]cachedToken{}}, nil
}

func parseKey(p string) (*rsa.PrivateKey, error) {
	blk, _ := pem.Decode([]byte(p))
	if blk == nil {
		return nil, errors.New("forge: github private key is not PEM")
	}
	if k, err := x509.ParsePKCS1PrivateKey(blk.Bytes); err == nil {
		return k, nil
	}
	k, err := x509.ParsePKCS8PrivateKey(blk.Bytes)
	if err != nil {
		return nil, fmt.Errorf("forge: github private key: %w", err)
	}
	rk, ok := k.(*rsa.PrivateKey)
	if !ok {
		return nil, errors.New("forge: github private key is not an RSA key")
	}
	return rk, nil
}

func (a *adapter) Kind() forge.Kind { return forge.GitHub }

func (a *adapter) client(token string) (*gh.Client, error) {
	c, err := gh.NewClient(gh.WithHTTPClient(a.http), gh.WithUserAgent("bob"), gh.WithAuthToken(token),
		gh.WithURLs(&a.base, &a.base))
	if err != nil {
		return nil, fmt.Errorf("github: %w", err)
	}
	return c, nil
}

func apiErr(op string, resp *gh.Response, err error) error {
	if err == nil {
		return nil
	}
	code := 0
	if resp != nil && resp.Response != nil {
		code = resp.StatusCode
	}
	if code == http.StatusUnprocessableEntity {
		code = http.StatusNotFound // GitHub answers 422 for a repository outside the installation
	}
	return forge.HTTPError("github: "+op, code, err)
}

// appJWT signs the App JWT (RS256): iat back-dated 60 s against clock drift, exp 9 minutes
// ahead (GitHub allows at most 10).
func (a *adapter) appJWT() (string, error) {
	now := time.Now()
	hdr := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256","typ":"JWT"}`))
	claims, err := json.Marshal(map[string]any{"iat": now.Add(-60 * time.Second).Unix(), "exp": now.Add(9 * time.Minute).Unix(),
		"iss": strconv.FormatInt(a.cfg.AppID, 10)})
	if err != nil {
		return "", err
	}
	in := hdr + "." + base64.RawURLEncoding.EncodeToString(claims)
	sum := sha256.Sum256([]byte(in))
	sig, err := rsa.SignPKCS1v15(rand.Reader, a.key, crypto.SHA256, sum[:])
	if err != nil {
		return "", fmt.Errorf("github: sign app JWT: %w", err)
	}
	return in + "." + base64.RawURLEncoding.EncodeToString(sig), nil
}

func (a *adapter) appClient() (*gh.Client, error) {
	jwt, err := a.appJWT()
	if err != nil {
		return nil, err
	}
	return a.client(jwt)
}

func (a *adapter) installationToken(ctx context.Context, opts *gh.InstallationTokenOptions) (*gh.InstallationToken, error) {
	c, err := a.appClient()
	if err != nil {
		return nil, err
	}
	t, resp, err := c.Apps.CreateInstallationToken(ctx, a.cfg.InstallationID, opts)
	if err != nil {
		return nil, apiErr("create installation token", resp, err)
	}
	if t.GetToken() == "" {
		return nil, errors.New("github: installation token response without token")
	}
	return t, nil
}

func expiry(t *gh.InstallationToken) time.Time {
	if t.ExpiresAt == nil || t.ExpiresAt.IsZero() {
		return time.Now().Add(tokenLifetime)
	}
	return t.GetExpiresAt().Time
}

// ---- webhooks ----

// ParseWebhook verifies X-Hub-Signature-256 (SHA-1 signatures are not accepted) and normalizes.
func (a *adapter) ParseWebhook(h http.Header, body []byte, _ time.Time) (forge.Event, error) {
	if err := forge.CheckSize(body); err != nil {
		return forge.Event{}, err
	}
	sig := h.Get(gh.SHA256SignatureHeader)
	if a.cfg.WebhookSecret.IsZero() || !strings.HasPrefix(sig, "sha256=") {
		return forge.Event{}, fmt.Errorf("%w: %s missing or not sha256", forge.ErrSignature, gh.SHA256SignatureHeader)
	}
	if err := gh.ValidateSignature(sig, body, []byte(a.cfg.WebhookSecret.Reveal())); err != nil {
		return forge.Event{}, fmt.Errorf("%w: %v", forge.ErrSignature, err)
	}
	ev := forge.Event{Forge: forge.GitHub, DeliveryID: h.Get(gh.DeliveryIDHeader)}
	kind := h.Get(gh.EventTypeHeader)
	switch kind {
	case "push", "pull_request", "ping":
	default:
		ev.Type, ev.IgnoredReason = forge.EventIgnored, "github event "+truncName(kind)
		return ev, forge.CheckEvent(ev)
	}
	raw, err := gh.ParseWebHook(kind, body)
	if err != nil {
		return forge.Event{}, fmt.Errorf("%w: %v", forge.ErrPayload, err)
	}
	switch p := raw.(type) {
	case *gh.PingEvent:
		ev.Type = forge.EventPing
		return ev, nil
	case *gh.PushEvent:
		r := p.GetRepo()
		ev.Repo = forge.Repo{Forge: forge.GitHub, ID: r.GetID(), FullName: r.GetFullName(), CloneURL: r.GetCloneURL(),
			WebURL: r.GetHTMLURL(), DefaultBranch: r.GetDefaultBranch(), Private: r.GetPrivate()}
		ev.Sender = p.GetSender().GetLogin()
		refKind, name := forge.SplitRef(p.GetRef())
		switch refKind {
		case "branch":
			ev.Type = forge.EventPush
			ev.Push = &forge.Push{Branch: name, Before: p.GetBefore(), After: p.GetAfter(), Created: p.GetCreated(), Deleted: p.GetDeleted()}
		case "tag":
			ev.Type = forge.EventTag
			t := &forge.Tag{Name: name, Deleted: p.GetDeleted()}
			if !t.Deleted {
				t.Commit = p.GetHeadCommit().GetID() // after is the tag object for annotated tags
			}
			ev.Tag = t
		default:
			return forge.Event{}, fmt.Errorf("%w: push to a ref that is neither a branch nor a tag", forge.ErrPayload)
		}
	case *gh.PullRequestEvent:
		r := p.GetRepo()
		ev.Repo = forge.Repo{Forge: forge.GitHub, ID: r.GetID(), FullName: r.GetFullName(), CloneURL: r.GetCloneURL(),
			WebURL: r.GetHTMLURL(), DefaultBranch: r.GetDefaultBranch(), Private: r.GetPrivate()}
		ev.Sender = p.GetSender().GetLogin()
		pr := p.GetPullRequest()
		n := int64(p.GetNumber())
		out := &forge.PullRequest{Number: n, Title: pr.GetTitle(), HeadSHA: pr.GetHead().GetSHA(),
			HeadBranch: pr.GetHead().GetRef(), HeadRepo: pr.GetHead().GetRepo().GetFullName(),
			FromFork:   pr.GetHead().GetRepo().GetID() != pr.GetBase().GetRepo().GetID(),
			BaseBranch: pr.GetBase().GetRef(), Merged: pr.GetMerged(), FetchRef: "refs/pull/" + strconv.FormatInt(n, 10) + "/head"}
		switch p.GetAction() {
		case "opened":
			out.Action = forge.PROpened
		case "synchronize":
			out.Action = forge.PRSynchronized
		case "reopened":
			out.Action = forge.PRReopened
		case "closed":
			out.Action = forge.PRClosed
		default:
			ev.Type, ev.IgnoredReason = forge.EventIgnored, "pull_request action "+truncName(p.GetAction())
			return ev, forge.CheckEvent(ev)
		}
		ev.Type, ev.PullRequest = forge.EventPullRequest, out
	default:
		return forge.Event{}, fmt.Errorf("%w: unexpected %s payload", forge.ErrPayload, kind)
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

// mapState: GitHub has error, failure, pending, success (SPEC §6.9.2).
func mapState(s forge.State) (string, error) {
	switch s {
	case forge.StatePending, forge.StateRunning:
		return "pending", nil
	case forge.StateSuccess:
		return "success", nil
	case forge.StateFailure:
		return "failure", nil
	case forge.StateError, forge.StateCancelled:
		return "error", nil
	}
	return "", fmt.Errorf("github: unknown status state %q", s)
}

func ownerName(full string) (string, string, error) {
	o, n, ok := strings.Cut(full, "/")
	if !ok || o == "" || n == "" || strings.Contains(n, "/") {
		return "", "", fmt.Errorf("github: repository name %q is not owner/name", full)
	}
	return o, n, nil
}

// statusToken returns the cached statuses:write token of repo (minted for that one repository
// id) while it has more than 5 minutes left.
func (a *adapter) statusToken(ctx context.Context, repo int64) (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if t, ok := a.status[repo]; ok && time.Until(t.expires) > 5*time.Minute {
		return t.value, nil
	}
	t, err := a.installationToken(ctx, &gh.InstallationTokenOptions{RepositoryIDs: []int64{repo},
		Permissions: &gh.InstallationPermissions{Statuses: gh.Ptr("write")}})
	if err != nil {
		return "", err
	}
	a.status[repo] = cachedToken{value: t.GetToken(), expires: expiry(t)}
	return t.GetToken(), nil
}

// SetStatus writes a commit status; descriptions are cut to 140 runes.
func (a *adapter) SetStatus(ctx context.Context, r forge.Repo, s forge.Status) error {
	st, err := mapState(s.State)
	if err != nil {
		return err
	}
	o, n, err := ownerName(r.FullName)
	if err != nil {
		return err
	}
	if r.ID <= 0 {
		return fmt.Errorf("github: repository %s without id", r.FullName)
	}
	tok, err := a.statusToken(ctx, r.ID)
	if err != nil {
		return err
	}
	c, err := a.client(tok)
	if err != nil {
		return err
	}
	_, resp, err := c.Repositories.CreateStatus(ctx, o, n, s.SHA, gh.RepoStatus{State: gh.Ptr(st), Context: gh.Ptr(s.Context),
		Description: gh.Ptr(forge.Description(s, 140)), TargetURL: gh.Ptr(s.TargetURL)})
	if err := apiErr("set status", resp, err); err != nil {
		if errors.Is(err, forge.ErrAuth) {
			a.mu.Lock()
			delete(a.status, r.ID)
			a.mu.Unlock()
		}
		return err
	}
	return nil
}

// ---- checkout ----

// IssueCheckout mints an installation token for the one repository id with contents:read.
// GitHub fixes the lifetime at one hour; RevokeCheckout can only end it earlier.
func (a *adapter) IssueCheckout(ctx context.Context, req forge.CheckoutRequest) (forge.Checkout, error) {
	if req.Repo.ID <= 0 {
		return forge.Checkout{}, fmt.Errorf("github: repository %s without id", req.Repo.FullName)
	}
	t, err := a.installationToken(ctx, &gh.InstallationTokenOptions{RepositoryIDs: []int64{req.Repo.ID},
		Permissions: &gh.InstallationPermissions{Contents: gh.Ptr("read")}})
	if err != nil {
		return forge.Checkout{}, err
	}
	for _, r := range t.Repositories {
		if r.GetID() != req.Repo.ID {
			return forge.Checkout{}, fmt.Errorf("github: checkout token is not limited to repository %d", req.Repo.ID)
		}
	}
	var h [12]byte
	if _, err := rand.Read(h[:]); err != nil {
		return forge.Checkout{}, err
	}
	handle := hex.EncodeToString(h[:])
	exp := expiry(t)
	a.mu.Lock()
	now := time.Now()
	for k, v := range a.checkout {
		if v.expires.Before(now) {
			delete(a.checkout, k)
		}
	}
	a.checkout[handle] = cachedToken{value: t.GetToken(), expires: exp}
	a.mu.Unlock()
	return forge.Checkout{CloneURL: req.Repo.CloneURL, Username: "x-access-token", Token: forge.NewSecret(t.GetToken()),
		ExpiresAt: exp, ForgeExpires: true, Handle: handle}, nil
}

// RevokeCheckout revokes the token with DELETE /installation/token, authenticated with the
// token itself. Records keep only the handle (D62): a handle this process did not mint has no
// token to revoke with, and GitHub expires it within the hour.
func (a *adapter) RevokeCheckout(ctx context.Context, co forge.Checkout) error {
	a.mu.Lock()
	cached, ok := a.checkout[co.Handle]
	delete(a.checkout, co.Handle)
	a.mu.Unlock()
	tok := co.Token.Reveal()
	if tok == "" {
		if !ok {
			return nil
		}
		tok = cached.value
	}
	c, err := a.client(tok)
	if err != nil {
		return err
	}
	resp, err := c.Apps.RevokeInstallationToken(ctx)
	if err != nil && resp != nil && resp.Response != nil && resp.StatusCode == http.StatusUnauthorized {
		return nil // already revoked or expired
	}
	return apiErr("revoke installation token", resp, err)
}

// SweepCheckouts: GitHub expires installation tokens itself.
func (a *adapter) SweepCheckouts(context.Context, time.Time) (int, error) { return 0, nil }

// ---- repositories and hooks ----

// LookupRepo reads owner/name with the App JWT. Where GitHub refuses the JWT on the repository
// endpoint, it mints a metadata:read token limited to that repository name, whose answer
// carries the repository record, and revokes it at once.
func (a *adapter) LookupRepo(ctx context.Context, fullName string) (forge.Repo, error) {
	o, n, err := ownerName(fullName)
	if err != nil {
		return forge.Repo{}, fmt.Errorf("%w: %v", forge.ErrNotFound, err)
	}
	c, err := a.appClient()
	if err != nil {
		return forge.Repo{}, err
	}
	r, resp, err := c.Repositories.Get(ctx, o, n)
	if err != nil {
		if code := statusOf(resp); code != http.StatusUnauthorized && code != http.StatusForbidden && code != http.StatusNotFound {
			return forge.Repo{}, apiErr("get repository", resp, err)
		}
		t, err := a.installationToken(ctx, &gh.InstallationTokenOptions{Repositories: []string{n},
			Permissions: &gh.InstallationPermissions{Metadata: gh.Ptr("read")}})
		if err != nil {
			return forge.Repo{}, err
		}
		_ = a.RevokeCheckout(ctx, forge.Checkout{Token: forge.NewSecret(t.GetToken())})
		r = nil
		for _, cand := range t.Repositories {
			if strings.EqualFold(cand.GetFullName(), fullName) {
				r = cand
			}
		}
		if r == nil {
			return forge.Repo{}, fmt.Errorf("github: repository %s: %w", fullName, forge.ErrNotFound)
		}
	}
	repo := forge.Repo{Forge: forge.GitHub, ID: r.GetID(), FullName: r.GetFullName(), CloneURL: r.GetCloneURL(),
		WebURL: r.GetHTMLURL(), DefaultBranch: r.GetDefaultBranch(), Private: r.GetPrivate()}
	if repo.ID <= 0 || !strings.HasPrefix(repo.CloneURL, "https://") {
		return forge.Repo{}, fmt.Errorf("github: repository %s: id %d, clone URL %q is not https", fullName, repo.ID, repo.CloneURL)
	}
	return repo, nil
}

func statusOf(resp *gh.Response) int {
	if resp == nil || resp.Response == nil {
		return 0
	}
	return resp.StatusCode
}

// EnsureWebhook: the hook of a GitHub App is configured on the app, not per repository.
func (a *adapter) EnsureWebhook(context.Context, forge.Repo, string, forge.Secret) error {
	return fmt.Errorf("github: app webhooks are configured on the GitHub App: %w", forge.ErrUnsupported)
}

var _ forge.Adapter = (*adapter)(nil)
