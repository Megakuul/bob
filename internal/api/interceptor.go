package api

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"connectrpc.com/connect"

	authzv1 "github.com/megakuul/bob/internal/gen/bob/authz/v1"
	"github.com/megakuul/bob/internal/safe"
)

// guard is the one interceptor of every service: it recovers panics, authenticates,
// enforces the method's rule and normalizes errors (SPEC §6.1, §6.5).
type guard struct{ s *Server }

var _ connect.Interceptor = guard{}

func (g guard) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		proc := req.Spec().Procedure
		var res connect.AnyResponse
		err := safe.Run(func() error {
			rule, err := g.s.rule(proc)
			if err != nil {
				return err
			}
			ctx, p, err := g.s.authenticate(ctx, rule, req.Header(), req.HTTPMethod())
			if err != nil {
				return err
			}
			if err := g.s.authorize(ctx, rule, p, req.Any()); err != nil {
				return err
			}
			res, err = next(ctx, req)
			return err
		})
		if err != nil {
			return nil, normalize(ctx, g.s.log, proc, err)
		}
		return res, nil
	}
}

func (g guard) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return next
}

func (g guard) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return func(ctx context.Context, conn connect.StreamingHandlerConn) error {
		proc := conn.Spec().Procedure
		err := safe.Run(func() error {
			rule, err := g.s.rule(proc)
			if err != nil {
				return err
			}
			ctx, p, err := g.s.authenticate(ctx, rule, conn.RequestHeader(), http.MethodPost)
			if err != nil {
				return err
			}
			if rule.Kind != authzv1.Kind_KIND_PROJECT {
				if err := g.s.authorize(ctx, rule, p, nil); err != nil {
					return err
				}
				return next(ctx, conn)
			}
			// The project is in the first request message: check it when the handler reads it.
			return next(ctx, &checkedConn{StreamingHandlerConn: conn, check: func(msg any) error {
				return g.s.authorize(ctx, rule, p, msg)
			}})
		})
		return normalize(ctx, g.s.log, proc, err)
	}
}

// checkedConn authorizes the first received message before the handler sees it.
type checkedConn struct {
	connect.StreamingHandlerConn
	check   func(any) error
	checked bool
}

func (c *checkedConn) Receive(msg any) error {
	if err := c.StreamingHandlerConn.Receive(msg); err != nil {
		return err
	}
	if !c.checked {
		if err := c.check(msg); err != nil {
			return err
		}
		c.checked = true
	}
	return nil
}

func (s *Server) rule(procedure string) (*Rule, error) {
	r, ok := s.rules[procedure]
	if !ok {
		// Unreachable: New refuses services with methods without a rule.
		return nil, errors.New("api: no authorization rule for " + procedure)
	}
	return r, nil
}

// authenticate establishes the caller per the rule's kind and returns the context carrying it.
// p is nil for anonymous PUBLIC calls and for workers.
func (s *Server) authenticate(ctx context.Context, rule *Rule, h http.Header, method string) (context.Context, *Principal, error) {
	authz := h.Get("Authorization")
	if rule.Kind == authzv1.Kind_KIND_WORKER {
		scheme, _, _ := strings.Cut(authz, " ")
		if s.workers == nil || scheme != "Bob-Worker" {
			return ctx, nil, Unauthenticated("worker credential required")
		}
		wid, err := s.workers.Authenticate(ctx, authz, h.Get("Bob-PoP"), http.MethodPost, "/api/"+rule.Method)
		if err != nil {
			return ctx, nil, workerError(err)
		}
		return context.WithValue(ctx, workerKey{}, wid), nil, nil
	}

	var c Credential
	scheme, token, _ := strings.Cut(authz, " ")
	switch {
	case authz != "" && strings.EqualFold(scheme, "Bearer") && token != "":
		c.Bearer = token
	case authz != "":
		return ctx, nil, Unauthenticated("unsupported Authorization scheme")
	default:
		if ck, err := (&http.Request{Header: h}).Cookie("bob_session"); err == nil && ck.Value != "" {
			c.Session = ck.Value
		}
	}
	if c.Bearer == "" && c.Session == "" {
		if rule.Kind == authzv1.Kind_KIND_PUBLIC {
			return ctx, nil, nil
		}
		return ctx, nil, Unauthenticated("authentication required")
	}
	if c.Session != "" {
		// A stale cookie must not lock a browser out of the public methods (login page).
		if rule.Kind == authzv1.Kind_KIND_PUBLIC {
			p, err := s.authn.Authenticate(ctx, c)
			if err != nil || p == nil {
				return ctx, nil, nil
			}
			return context.WithValue(ctx, principalKey{}, p), p, nil
		}
		// Cookie-authenticated calls need Bob-Csrf: 1 (forces a CORS preflight); Connect GET
		// with only a cookie is refused (SPEC §6.3.2).
		if h.Get("Bob-Csrf") != "1" {
			return ctx, nil, Unauthenticated("cookie session requires the Bob-Csrf header")
		}
		if method == http.MethodGet {
			return ctx, nil, Unauthenticated("cookie sessions cannot use GET requests")
		}
	}
	p, err := s.authn.Authenticate(ctx, c)
	switch {
	case errors.Is(err, ErrUnknownCredential) || err == nil && p == nil:
		return ctx, nil, Unauthenticated("invalid credential")
	case err != nil:
		return ctx, nil, err
	}
	if p.Scope == "download" && !rule.Download {
		return ctx, nil, Unauthenticated("token not valid for the API")
	}
	return context.WithValue(ctx, principalKey{}, p), p, nil
}

// workerError maps the worker refusal sentinels to the API error model; the client sees the
// reason after the sentinel ("token expired").
func workerError(err error) error {
	var ce *connect.Error
	switch {
	case errors.As(err, &ce):
		return err
	case errors.Is(err, ErrWorkerUnauthenticated):
		return Unauthenticated(workerReason(err, ErrWorkerUnauthenticated))
	case errors.Is(err, ErrWorkerPermissionDenied):
		return WorkerPermissionDenied(err)
	}
	return err
}

// WorkerPermissionDenied is the PermissionDenied answer to an error wrapping
// ErrWorkerPermissionDenied (a worker asking about a job it holds no lease on).
func WorkerPermissionDenied(err error) error {
	return Forbidden(workerReason(err, ErrWorkerPermissionDenied))
}

// resourceName is the "<what> not found" noun of a rule's project resolution.
var resourceName = map[authzv1.Resolve]string{
	authzv1.Resolve_RESOLVE_UNSPECIFIED: "project",
	authzv1.Resolve_RESOLVE_BUILD:       "build",
	authzv1.Resolve_RESOLVE_JOB:         "job",
	authzv1.Resolve_RESOLVE_RELEASE:     "release",
	authzv1.Resolve_RESOLVE_REPOSITORY:  "repository",
}

// authorize enforces GLOBAL and PROJECT rules. PUBLIC, AUTHENTICATED and LIST_FILTERED need no
// check here (handlers apply visibility and filter lists with Allowed); WORKER was settled by
// authenticate.
func (s *Server) authorize(ctx context.Context, rule *Rule, p *Principal, req any) error {
	switch rule.Kind {
	case authzv1.Kind_KIND_GLOBAL:
		a, err := s.authz.Access(ctx, p, rule.Method, "*")
		if err != nil {
			return err
		}
		if a != Allowed {
			return PermissionDenied(rule.Method, "")
		}
	case authzv1.Kind_KIND_PROJECT:
		what := resourceName[rule.Resolve]
		id := rule.projectOf(req)
		if id == "" {
			if rule.Resolve != authzv1.Resolve_RESOLVE_UNSPECIFIED {
				// A107: an empty id names no resource.
				return NotFound(what)
			}
			return InvalidArgument(protoJSONPath(rule), what+" is required")
		}
		project := id
		if rule.Resolve != authzv1.Resolve_RESOLVE_UNSPECIFIED {
			if s.resolve == nil {
				return NotFound(what)
			}
			pr, err := s.resolve.Project(ctx, what, id)
			if err != nil {
				return err
			}
			project = pr
		}
		a, err := s.authz.Access(ctx, p, rule.Method, project)
		if err != nil {
			return err
		}
		switch a {
		case Allowed:
		case Denied:
			return PermissionDenied(rule.Method, project)
		default:
			return NotFound(what)
		}
	}
	return nil
}

// Allowed reports whether the caller may call method on project; LIST_FILTERED handlers use it
// to filter their results.
func (s *Server) Allowed(ctx context.Context, method, project string) (bool, error) {
	p := PrincipalFrom(ctx)
	if p == nil {
		return false, nil
	}
	a, err := s.authz.Access(ctx, p, method, project)
	return a == Allowed, err
}

// protoJSONPath is the request field path of the rule in protojson spelling ("project.id",
// "buildId").
func protoJSONPath(rule *Rule) string {
	parts := make([]string, len(rule.ProjectField))
	for i, n := range rule.ProjectField {
		parts[i] = lowerCamel(string(n))
	}
	return strings.Join(parts, ".")
}

func lowerCamel(s string) string {
	var b strings.Builder
	up := false
	for _, r := range s {
		if r == '_' {
			up = true
			continue
		}
		if up && r >= 'a' && r <= 'z' {
			r -= 'a' - 'A'
		}
		up = false
		b.WriteRune(r)
	}
	return b.String()
}
