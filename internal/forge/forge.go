package forge

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"
)

// Kind names a forge.
type Kind string

// The supported forges.
const (
	GitHub  Kind = "github"
	Forgejo Kind = "forgejo"
	GitLab  Kind = "gitlab"
)

// MaxWebhookBody is GitHub's documented payload limit; larger bodies are refused with ErrTooLarge.
const MaxWebhookBody = 25 << 20

// Sentinel errors of the adapter contract (SPEC §6.9.1).
var (
	ErrSignature   = errors.New("forge: webhook signature missing or invalid")
	ErrPayload     = errors.New("forge: webhook payload malformed")
	ErrTooLarge    = errors.New("forge: webhook payload too large")
	ErrStale       = errors.New("forge: webhook timestamp outside tolerance")
	ErrUnsupported = errors.New("forge: operation not supported by this forge")
	ErrAuth        = errors.New("forge: forge rejected bob's credentials")
	ErrNotFound    = errors.New("forge: repository or object not found")
)

// Adapter is bob's only view of one forge integration.
type Adapter interface {
	Kind() Kind
	ParseWebhook(h http.Header, body []byte, now time.Time) (Event, error)
	SetStatus(ctx context.Context, repo Repo, s Status) error
	IssueCheckout(ctx context.Context, req CheckoutRequest) (Checkout, error)
	RevokeCheckout(ctx context.Context, c Checkout) error
	SweepCheckouts(ctx context.Context, cutoff time.Time) (int, error)
	LookupRepo(ctx context.Context, fullName string) (Repo, error)
	EnsureWebhook(ctx context.Context, repo Repo, url string, secret Secret) error
}

// Repo is a repository record of a forge.
type Repo struct {
	Forge         Kind
	ID            int64
	FullName      string
	CloneURL      string
	WebURL        string
	DefaultBranch string
	Private       bool
}

// State is a commit status state.
type State string

// Commit status states.
const (
	StatePending   State = "pending"
	StateRunning   State = "running"
	StateSuccess   State = "success"
	StateFailure   State = "failure"
	StateError     State = "error"
	StateCancelled State = "cancelled"
)

// Status is one commit status.
type Status struct {
	SHA, Context string
	State        State
	Description  string
	TargetURL    string
}

// CheckoutRequest asks for a read-only credential for exactly Repo.
type CheckoutRequest struct {
	Repo  Repo
	JobID string
	TTL   time.Duration
}

// Checkout is a minted read-only credential.
type Checkout struct {
	CloneURL, Username string
	Token              Secret
	ExpiresAt          time.Time
	ForgeExpires       bool
	Handle             string
}

// Secret is a string that never prints or marshals; Reveal returns the value.
type Secret struct{ v string }

// NewSecret wraps v.
func NewSecret(v string) Secret { return Secret{v: v} }

// Reveal returns the value.
func (s Secret) Reveal() string { return s.v }

// String redacts.
func (Secret) String() string { return "[redacted]" }

// GoString redacts.
func (Secret) GoString() string { return "[redacted]" }

// MarshalJSON redacts.
func (Secret) MarshalJSON() ([]byte, error) { return []byte(`"[redacted]"`), nil }

// MarshalText redacts.
func (Secret) MarshalText() ([]byte, error) { return []byte("[redacted]"), nil }

// LogValue redacts.
func (Secret) LogValue() slog.Value { return slog.StringValue("[redacted]") }

// IsZero reports an empty secret.
func (s Secret) IsZero() bool { return s.v == "" }

// EventType is the normalized kind of a webhook event.
type EventType string

// Event types.
const (
	EventPush        EventType = "push"
	EventTag         EventType = "tag"
	EventPullRequest EventType = "pull_request"
	EventPing        EventType = "ping"
	EventIgnored     EventType = "ignored"
)

// PRAction is a normalized pull request action.
type PRAction string

// Pull request actions.
const (
	PROpened       PRAction = "opened"
	PRSynchronized PRAction = "synchronized"
	PRReopened     PRAction = "reopened"
	PRClosed       PRAction = "closed"
)

// Event is a normalized webhook delivery.
type Event struct {
	Forge         Kind
	DeliveryID    string
	Type          EventType
	Repo          Repo
	Sender        string
	Push          *Push
	Tag           *Tag
	PullRequest   *PullRequest
	IgnoredReason string
}

// Push is a branch push.
type Push struct {
	Branch, Before, After string
	Created, Deleted      bool
}

// Tag is a tag push; Commit is the peeled commit.
type Tag struct {
	Name, Commit string
	Deleted      bool
}

// PullRequest is a pull or merge request event.
type PullRequest struct {
	Number                                           int64
	Action                                           PRAction
	Title, HeadSHA, HeadBranch, HeadRepo, BaseBranch string
	FromFork, Merged                                 bool
	FetchRef                                         string
}
