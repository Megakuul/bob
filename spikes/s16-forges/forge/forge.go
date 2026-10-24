// Package forge is the small adapter interface between bob's server and a source forge
// (R-PIPE-001, R-PIPE-007). Each forge adapter lives in its own sub-package and uses the
// forge's Go SDK. Everything in Event is derived from verified, size-capped input.
package forge

import (
	"context"
	"errors"
	"io"
	"net/http"
	"time"
)

// Kind names a forge implementation.
type Kind string

const (
	GitHub  Kind = "github"
	Forgejo Kind = "forgejo"
	GitLab  Kind = "gitlab"
)

// MaxWebhookBody caps the request body the server reads before verifying a signature.
// GitHub documents 25 MB as its payload limit; the other forges send far less.
const MaxWebhookBody = 25 << 20

// Sentinel errors. Adapters wrap them with %w; callers branch with errors.Is.
var (
	ErrSignature   = errors.New("forge: webhook signature missing or invalid") // HTTP 401, no retry
	ErrPayload     = errors.New("forge: webhook payload malformed")            // HTTP 400
	ErrTooLarge    = errors.New("forge: webhook payload too large")            // HTTP 413
	ErrStale       = errors.New("forge: webhook timestamp outside tolerance")  // HTTP 401 (GitLab only)
	ErrUnsupported = errors.New("forge: operation not supported by this forge")
	ErrAuth        = errors.New("forge: forge rejected bob's credentials") // integration broken, surface in UI
	ErrNotFound    = errors.New("forge: repository or object not found")
)

// EventType is the normalized webhook event type.
type EventType string

const (
	EventPush        EventType = "push"         // branch updated, created or deleted
	EventTag         EventType = "tag"          // tag created or deleted
	EventPullRequest EventType = "pull_request" // PR (GitHub/Forgejo) or MR (GitLab)
	EventPing        EventType = "ping"         // hook configured / test delivery
	EventIgnored     EventType = "ignored"      // valid, signed, but bob does not act on it
)

// Repo identifies a repository on a forge. ID is the forge's stable numeric id; names can
// change (rename, transfer), ids do not. bob links a project to Repo.ID (R-PIPE-002).
type Repo struct {
	Forge         Kind   `json:"forge"`
	ID            int64  `json:"id"`
	FullName      string `json:"fullName"` // "owner/name"; GitLab: path_with_namespace
	CloneURL      string `json:"cloneURL"` // HTTPS, without credentials
	WebURL        string `json:"webURL"`
	DefaultBranch string `json:"defaultBranch"`
	Private       bool   `json:"private"`
}

// Event is the normalized result of a verified webhook delivery.
type Event struct {
	Forge      Kind      `json:"forge"`
	DeliveryID string    `json:"deliveryID"` // forge delivery id, used for de-duplication
	Type       EventType `json:"type"`
	Repo       Repo      `json:"repo"`
	Sender     string    `json:"sender"` // forge login of the actor
	// Exactly one of Push, Tag, PullRequest is set for the matching Type.
	Push        *Push        `json:"push,omitempty"`
	Tag         *Tag         `json:"tag,omitempty"`
	PullRequest *PullRequest `json:"pullRequest,omitempty"`
	// IgnoredReason says why Type is EventIgnored (e.g. "forgejo event create").
	IgnoredReason string `json:"ignoredReason,omitempty"`
}

// Push is a branch update.
type Push struct {
	Branch  string `json:"branch"` // without refs/heads/
	Before  string `json:"before"` // all zeros when the branch was created
	After   string `json:"after"`  // commit to build; all zeros when Deleted
	Created bool   `json:"created"`
	Deleted bool   `json:"deleted"`
}

// Tag is a tag creation or deletion.
type Tag struct {
	Name    string `json:"name"`    // without refs/tags/
	Commit  string `json:"commit"`  // peeled commit SHA (annotated tags point to a tag object)
	Deleted bool   `json:"deleted"` // Commit is empty when Deleted
}

// PRAction is the normalized pull/merge request action.
type PRAction string

const (
	PROpened       PRAction = "opened"
	PRSynchronized PRAction = "synchronized" // new head commit
	PRReopened     PRAction = "reopened"
	PRClosed       PRAction = "closed" // Merged tells merged vs. abandoned
	PROther        PRAction = "other"  // label, review, edit, ...: Event.Type is EventIgnored
)

// PullRequest is a pull request (GitHub, Forgejo) or merge request (GitLab).
type PullRequest struct {
	Number     int64    `json:"number"` // GitLab: iid
	Action     PRAction `json:"action"`
	Title      string   `json:"title"`
	HeadSHA    string   `json:"headSHA"`
	HeadBranch string   `json:"headBranch"`
	HeadRepo   string   `json:"headRepo"` // full name of the source repository
	FromFork   bool     `json:"fromFork"` // head repo id differs from base repo id
	BaseBranch string   `json:"baseBranch"`
	Merged     bool     `json:"merged"`
	// FetchRef is the ref in the *base* repository that holds HeadSHA, so a worker only ever
	// needs a credential for the linked repository, also for fork PRs.
	FetchRef string `json:"fetchRef"` // refs/pull/N/head or refs/merge-requests/N/head
}

// State is bob's build state as reported to a forge.
type State string

const (
	StatePending   State = "pending"
	StateRunning   State = "running"
	StateSuccess   State = "success"
	StateFailure   State = "failure"   // the build or a check failed
	StateError     State = "error"     // bob or infrastructure failed
	StateCancelled State = "cancelled" // cancelled or superseded
)

// Status is one commit status report. Context is stable per pipeline ("bob/<pipeline>"),
// so later reports for the same SHA and Context replace earlier ones on the forge.
type Status struct {
	SHA         string
	Context     string
	State       State
	Description string // truncated by the adapter to the forge limit (GitHub: 140 chars)
	TargetURL   string // link to the build in bob's UI (R-PIPE-003)
}

// Secret holds a credential. It never prints and never marshals (R-SEC-005).
type Secret struct{ v string }

func NewSecret(v string) Secret               { return Secret{v: v} }
func (s Secret) Reveal() string               { return s.v }
func (s Secret) String() string               { return "[redacted]" }
func (s Secret) GoString() string             { return "forge.Secret{[redacted]}" }
func (s Secret) MarshalJSON() ([]byte, error) { return []byte(`"[redacted]"`), nil }
func (s Secret) MarshalText() ([]byte, error) { return []byte("[redacted]"), nil }
func (s Secret) IsZero() bool                 { return s.v == "" }

// Checkout is a short-lived, read-only credential for one repository, handed to the one
// worker that holds the job (R-FLEET-006).
type Checkout struct {
	CloneURL  string    // HTTPS, without credentials
	Username  string    // HTTP basic user name the forge expects with Token
	Token     Secret    // HTTP basic password
	ExpiresAt time.Time // enforced by the forge, or by bob's sweeper where the forge has no expiry
	// ForgeExpires is true when the forge itself expires the token (GitHub, GitLab).
	// False for Forgejo: bob must revoke it (RevokeCheckout, SweepCheckouts).
	ForgeExpires bool
	Handle       string // opaque id for RevokeCheckout
}

// CheckoutRequest says what the credential is for.
type CheckoutRequest struct {
	Repo  Repo
	JobID string        // embedded in the token name where the forge has names (sweeping)
	TTL   time.Duration // wanted lifetime; adapters round up to what the forge supports
}

// Adapter is implemented once per forge. One Adapter value serves one forge integration
// (one set of credentials) and is safe for concurrent use. ParseWebhook does no I/O.
type Adapter interface {
	Kind() Kind
	// ParseWebhook verifies the signature over body (before any JSON parsing) and returns the
	// normalized event. The caller has already capped body at MaxWebhookBody.
	ParseWebhook(h http.Header, body []byte, now time.Time) (Event, error)
	// SetStatus reports a commit status. Idempotent per (SHA, Context, State).
	SetStatus(ctx context.Context, repo Repo, s Status) error
	// IssueCheckout mints a read-only credential limited to req.Repo.
	IssueCheckout(ctx context.Context, req CheckoutRequest) (Checkout, error)
	// RevokeCheckout invalidates a credential early. Revoking twice is not an error.
	RevokeCheckout(ctx context.Context, c Checkout) error
	// SweepCheckouts revokes bob-minted credentials created before cutoff that are still live
	// (crash recovery). Forges that expire tokens themselves return 0, nil.
	SweepCheckouts(ctx context.Context, cutoff time.Time) (int, error)
}

// ReadBody reads a webhook body with the size cap. Server handlers use it before ParseWebhook.
func ReadBody(r *http.Request) ([]byte, error) {
	b, err := io.ReadAll(io.LimitReader(r.Body, MaxWebhookBody+1))
	if err != nil {
		return nil, err
	}
	if len(b) > MaxWebhookBody {
		return nil, ErrTooLarge
	}
	return b, nil
}
