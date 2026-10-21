// Package github is the GitHub adapter, built on the official SDK google/go-github.
// bob authenticates as a GitHub App: a JWT signed with the app key mints installation
// tokens that GitHub expires after one hour and that can be limited to one repository.
package github

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
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

	"bobspike/s16/forge"
)

// Config is one GitHub App integration, stored in the control bucket (orchestrators only).
type Config struct {
	AppID          int64
	PrivateKeyPEM  forge.Secret // app private key (PKCS#1 or PKCS#8)
	InstallationID int64        // installation that covers the linked repositories
	WebhookSecret  forge.Secret // app webhook secret
	APIURL         string       // "" = https://api.github.com/; GHES or test server otherwise
	HTTPClient     *http.Client
	Now            func() time.Time
}

type Adapter struct {
	cfg Config
	key *rsa.PrivateKey

	mu        sync.Mutex
	statusTok string
	statusExp time.Time
}

func New(cfg Config) (*Adapter, error) {
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = &http.Client{Timeout: 30 * time.Second}
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	blk, _ := pem.Decode([]byte(cfg.PrivateKeyPEM.Reveal()))
	if blk == nil {
		return nil, errors.New("github: private key is not PEM")
	}
	var key *rsa.PrivateKey
	if k, err := x509.ParsePKCS1PrivateKey(blk.Bytes); err == nil {
		key = k
	} else if k8, err8 := x509.ParsePKCS8PrivateKey(blk.Bytes); err8 == nil {
		rk, ok := k8.(*rsa.PrivateKey)
		if !ok {
			return nil, errors.New("github: private key is not RSA")
		}
		key = rk
	} else {
		return nil, fmt.Errorf("github: parse private key: %w", err)
	}
	return &Adapter{cfg: cfg, key: key}, nil
}

func (a *Adapter) Kind() forge.Kind { return forge.GitHub }

func (a *Adapter) client(token string) (*gh.Client, error) {
	opts := []gh.ClientOptionsFunc{gh.WithHTTPClient(a.cfg.HTTPClient), gh.WithUserAgent("bob"), gh.WithAuthToken(token)}
	if a.cfg.APIURL != "" {
		opts = append(opts, gh.WithEnterpriseURLs(a.cfg.APIURL, a.cfg.APIURL))
	}
	return gh.NewClient(opts...)
}

// appJWT signs the GitHub App JWT (RS256). GitHub allows at most 10 minutes; iat is
// back-dated 60 s against clock drift, as GitHub's docs recommend.
func (a *Adapter) appJWT() (string, error) {
	now := a.cfg.Now()
	hdr := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256","typ":"JWT"}`))
	cl, _ := json.Marshal(map[string]any{"iat": now.Add(-60 * time.Second).Unix(), "exp": now.Add(9 * time.Minute).Unix(), "iss": strconv.FormatInt(a.cfg.AppID, 10)})
	in := hdr + "." + base64.RawURLEncoding.EncodeToString(cl)
	sum := sha256.Sum256([]byte(in))
	sig, err := rsa.SignPKCS1v15(rand.Reader, a.key, crypto.SHA256, sum[:])
	if err != nil {
		return "", err
	}
	return in + "." + base64.RawURLEncoding.EncodeToString(sig), nil
}

func (a *Adapter) installationToken(ctx context.Context, opts *gh.InstallationTokenOptions) (*gh.InstallationToken, error) {
	jwt, err := a.appJWT()
	if err != nil {
		return nil, err
	}
	c, err := a.client(jwt)
	if err != nil {
		return nil, err
	}
	t, resp, err := c.Apps.CreateInstallationToken(ctx, a.cfg.InstallationID, opts)
	if err != nil {
		return nil, wrapResp(resp, err)
	}
	if t.Token == nil || t.ExpiresAt == nil {
		return nil, errors.New("github: installation token response incomplete")
	}
	return t, nil
}

func wrapResp(resp *gh.Response, err error) error {
	if resp != nil {
		switch resp.StatusCode {
		case 401, 403:
			return fmt.Errorf("%w: %v", forge.ErrAuth, err)
		case 404, 422:
			return fmt.Errorf("%w: %v", forge.ErrNotFound, err)
		}
	}
	return fmt.Errorf("github: %w", err)
}

// ---- webhooks ----

func (a *Adapter) ParseWebhook(h http.Header, body []byte, _ time.Time) (forge.Event, error) {
	if len(body) > forge.MaxWebhookBody {
		return forge.Event{}, forge.ErrTooLarge
	}
	if a.cfg.WebhookSecret.IsZero() {
		return forge.Event{}, fmt.Errorf("%w: no secret configured", forge.ErrSignature)
	}
	sig := h.Get(gh.SHA256SignatureHeader)
	if sig == "" {
		// SHA-1 (X-Hub-Signature) is accepted by the SDK but bob requires SHA-256.
		return forge.Event{}, fmt.Errorf("%w: %s missing", forge.ErrSignature, gh.SHA256SignatureHeader)
	}
	if err := gh.ValidateSignature(sig, body, []byte(a.cfg.WebhookSecret.Reveal())); err != nil {
		return forge.Event{}, fmt.Errorf("%w: %v", forge.ErrSignature, err)
	}
	ev := forge.Event{Forge: forge.GitHub, DeliveryID: h.Get(gh.DeliveryIDHeader)}
	kind := h.Get(gh.EventTypeHeader)
	switch kind {
	case "push", "pull_request", "ping":
	default:
		ev.Type, ev.IgnoredReason = forge.EventIgnored, "github event "+kind
		return ev, nil
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
		ev.Repo = pushRepo(p.GetRepo())
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
				// after is the tag object for annotated tags; head_commit is the commit.
				t.Commit = p.GetHeadCommit().GetID()
			}
			ev.Tag = t
		default:
			ev.Type, ev.IgnoredReason = forge.EventIgnored, "push to "+p.GetRef()
			return ev, nil
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
		ev.Type = forge.EventPullRequest
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
			out.Action = forge.PROther
			ev.Type, ev.IgnoredReason = forge.EventIgnored, "pull_request action "+p.GetAction()
		}
		ev.PullRequest = out
	}
	if err := forge.CheckEvent(ev); err != nil {
		return forge.Event{}, err
	}
	return ev, nil
}

func pushRepo(r *gh.PushEventRepository) forge.Repo {
	return forge.Repo{Forge: forge.GitHub, ID: r.GetID(), FullName: r.GetFullName(), CloneURL: r.GetCloneURL(),
		WebURL: r.GetHTMLURL(), DefaultBranch: r.GetDefaultBranch(), Private: r.GetPrivate()}
}

// ---- statuses ----

// GitHub commit status states: error, failure, pending, success.
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
	return "", fmt.Errorf("github: unknown state %q", s)
}

// statusToken returns a cached installation token with statuses:write only.
func (a *Adapter) statusToken(ctx context.Context) (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.statusTok != "" && a.cfg.Now().Before(a.statusExp.Add(-5*time.Minute)) {
		return a.statusTok, nil
	}
	t, err := a.installationToken(ctx, &gh.InstallationTokenOptions{Permissions: &gh.InstallationPermissions{Statuses: gh.Ptr("write")}})
	if err != nil {
		return "", err
	}
	a.statusTok, a.statusExp = t.GetToken(), t.GetExpiresAt().Time
	return a.statusTok, nil
}

func (a *Adapter) SetStatus(ctx context.Context, r forge.Repo, s forge.Status) error {
	st, err := mapState(s.State)
	if err != nil {
		return err
	}
	o, n, ok := strings.Cut(r.FullName, "/")
	if !ok {
		return fmt.Errorf("github: bad repository name %q", r.FullName)
	}
	tok, err := a.statusToken(ctx)
	if err != nil {
		return err
	}
	c, err := a.client(tok)
	if err != nil {
		return err
	}
	desc := s.Description
	if s.State == forge.StateCancelled && desc == "" {
		desc = "cancelled"
	}
	_, resp, err := c.Repositories.CreateStatus(ctx, o, n, s.SHA, gh.RepoStatus{State: gh.Ptr(st),
		Context: gh.Ptr(s.Context), Description: gh.Ptr(forge.Truncate(desc, 140)), TargetURL: gh.Ptr(s.TargetURL)})
	if err != nil {
		return wrapResp(resp, err)
	}
	return nil
}

// ---- checkout ----

// IssueCheckout mints an installation token limited to one repository id with contents:read.
// GitHub fixes the lifetime at one hour; req.TTL can only be shortened by RevokeCheckout.
func (a *Adapter) IssueCheckout(ctx context.Context, req forge.CheckoutRequest) (forge.Checkout, error) {
	t, err := a.installationToken(ctx, &gh.InstallationTokenOptions{
		RepositoryIDs: []int64{req.Repo.ID},
		Permissions:   &gh.InstallationPermissions{Contents: gh.Ptr("read")},
	})
	if err != nil {
		return forge.Checkout{}, err
	}
	if len(t.Repositories) != 1 || t.Repositories[0].GetID() != req.Repo.ID {
		return forge.Checkout{}, fmt.Errorf("github: token not limited to repository %d", req.Repo.ID)
	}
	return forge.Checkout{CloneURL: req.Repo.CloneURL, Username: "x-access-token", Token: forge.NewSecret(t.GetToken()),
		ExpiresAt: t.GetExpiresAt().Time, ForgeExpires: true, Handle: t.GetToken()[len(t.GetToken())-4:]}, nil
}

// RevokeCheckout calls DELETE /installation/token authenticated with the token itself.
func (a *Adapter) RevokeCheckout(ctx context.Context, co forge.Checkout) error {
	c, err := a.client(co.Token.Reveal())
	if err != nil {
		return err
	}
	resp, err := c.Apps.RevokeInstallationToken(ctx)
	if err != nil && resp != nil && resp.StatusCode == 401 {
		return nil // already expired or revoked
	}
	if err != nil {
		return wrapResp(resp, err)
	}
	return nil
}

// SweepCheckouts: GitHub expires installation tokens itself after one hour.
func (a *Adapter) SweepCheckouts(context.Context, time.Time) (int, error) { return 0, nil }

var _ forge.Adapter = (*Adapter)(nil)
