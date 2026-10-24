package api

import (
	"context"
	"crypto/subtle"
	"errors"
	"slices"
	"strings"
	"time"
)

// Principal is the authenticated caller of an RPC.
type Principal struct {
	ID                 string // "user:u01j…" | "sa:ci"
	DisplayName        string
	Login              string    // local login or OIDC subject name; "" for service accounts
	Scope              string    // "api" | "download" | "session"
	Expires            time.Time // credential expiry; zero = none
	Bindings           []Binding
	MustChangePassword bool
	// CacheProject is set for a job's download token (GetCheckoutCredentialResponse.
	// download_token): the one project whose private cache paths it may read. Such a principal
	// has no bindings, so it has no permission anywhere else.
	CacheProject string
}

// Binding is one role binding of a principal.
type Binding struct {
	Role    string
	Project string // project id or "*"
}

// Credential is what a request presented. At most one of Bearer and Session is set.
type Credential struct {
	Bearer  string // Authorization: Bearer <token>
	Session string // bob_session cookie
}

// ErrUnknownCredential is returned by an Authenticator for a credential it does not issue;
// the next Authenticator of the chain is asked.
var ErrUnknownCredential = errors.New("unknown credential")

// Authenticator turns a credential into a principal (identity, M15.2). A credential it issued
// but that is invalid (expired, revoked) is an error from Unauthenticated.
type Authenticator interface {
	Authenticate(ctx context.Context, c Credential) (*Principal, error)
}

// Access is the outcome of an authorization check.
type Access int

const (
	// NoAccess: the principal has no permission at all on the project (answer NotFound).
	NoAccess Access = iota
	// Denied: some permission on the project, but not this method (answer PermissionDenied).
	Denied
	// Allowed: the call may proceed.
	Allowed
)

// Authorizer decides a method on a project ("*" for global methods) for a principal (RBAC,
// M15.4).
type Authorizer interface {
	Access(ctx context.Context, p *Principal, method, project string) (Access, error)
}

// Resolver maps the id of a KIND_PROJECT rule with resolve != UNSPECIFIED (a build, job,
// release or repository) to its project. A missing resource is NotFound.
type Resolver interface {
	Project(ctx context.Context, kind string, id string) (string, error)
}

// WorkerAuthenticator checks `Authorization: Bob-Worker <token>` and `Bob-PoP` (ADR 0013) and
// returns the worker id. orchestrator.Registrar.Authenticate has this shape.
type WorkerAuthenticator interface {
	Authenticate(ctx context.Context, authorization, pop, method, path string) (string, error)
}

// Sentinels of worker refusals. The orchestrator wraps them ("unauthenticated: token expired",
// "permission_denied: worker holds no lease on job j1"); the API boundary maps them with
// errors.Is, so a wrapping prefix added on the way does not turn them into Internal.
var (
	ErrWorkerUnauthenticated   = errors.New("unauthenticated")
	ErrWorkerPermissionDenied = errors.New("permission_denied")
)

// workerReason is the text after the sentinel ("token expired" for
// "unauthenticated: token expired"); the whole message if the sentinel text is not in it.
func workerReason(err, sentinel error) string {
	msg := err.Error()
	if _, rest, ok := strings.Cut(msg, sentinel.Error()+": "); ok {
		return rest
	}
	return msg
}

// StaticToken authenticates one fixed bearer token as a fixed principal. bob-server uses it
// for the bootstrap admin credential (BOB_ADMIN_TOKEN).
type StaticToken struct {
	Token     string
	Principal Principal
}

// Authenticate implements Authenticator.
func (s *StaticToken) Authenticate(_ context.Context, c Credential) (*Principal, error) {
	if s.Token == "" || c.Bearer == "" || subtle.ConstantTimeCompare([]byte(c.Bearer), []byte(s.Token)) != 1 {
		return nil, ErrUnknownCredential
	}
	p := s.Principal
	p.Bindings = slices.Clone(s.Principal.Bindings)
	return &p, nil
}

// Chain asks each Authenticator in order until one knows the credential.
type Chain []Authenticator

// Authenticate implements Authenticator.
func (ch Chain) Authenticate(ctx context.Context, c Credential) (*Principal, error) {
	for _, a := range ch {
		p, err := a.Authenticate(ctx, c)
		if errors.Is(err, ErrUnknownCredential) {
			continue
		}
		return p, err
	}
	return nil, ErrUnknownCredential
}

// AdminBindings is the authorizer until RBAC (M15.4) lands: the built-in role `admin` grants
// every method on its project ("*" = all); any other binding grants nothing.
type AdminBindings struct{}

// Access implements Authorizer.
func (AdminBindings) Access(_ context.Context, p *Principal, _ string, project string) (Access, error) {
	for _, b := range p.Bindings {
		if b.Role == "admin" && (b.Project == "*" || b.Project == project) {
			return Allowed, nil
		}
	}
	return NoAccess, nil
}

type principalKey struct{}
type workerKey struct{}

// PrincipalFrom returns the authenticated principal of the call, nil for anonymous calls.
func PrincipalFrom(ctx context.Context) *Principal {
	p, _ := ctx.Value(principalKey{}).(*Principal)
	return p
}

// WorkerFrom returns the authenticated worker id of a KIND_WORKER call.
func WorkerFrom(ctx context.Context) string {
	w, _ := ctx.Value(workerKey{}).(string)
	return w
}
