package rbac

import (
	"context"
	"crypto/rand"
	"encoding/base32"
	"errors"
	"fmt"
	"slices"
	"strings"

	"connectrpc.com/connect"

	"github.com/megakuul/bob/internal/api"
	accessv1 "github.com/megakuul/bob/internal/gen/bob/access/v1"
	"github.com/megakuul/bob/internal/store"
)

// OIDC claim → role mappings (SPEC §6.3.2, R-AUTH-004). The mapping CRUD methods have the
// OidcService handler signatures: identity's OidcService embeds or forwards to them.

// MaxMappingsPerProvider bounds the mappings of one provider.
const MaxMappingsPerProvider = 500

const maxClaim, maxValue = 100, 256

func mappingMsg(m store.OidcMapping) *accessv1.OidcMapping {
	return &accessv1.OidcMapping{Id: m.ID, ProviderId: m.Provider, Claim: m.Claim, Value: m.Value, Role: m.Role, Project: m.Project}
}

func newMappingID() (string, error) {
	var b [10]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return "m" + strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b[:])), nil
}

// CreateOidcMapping creates a mapping {provider, claim, value, role, project}. The role must
// exist; the provider is identity's and is not checked here.
func (s *Service) CreateOidcMapping(ctx context.Context, req *connect.Request[accessv1.CreateOidcMappingRequest]) (*connect.Response[accessv1.CreateOidcMappingResponse], error) {
	in := req.Msg.GetMapping()
	switch {
	case !ValidRoleID(in.GetProviderId()):
		return nil, api.InvalidArgument("mapping.providerId", "provider id must be a provider's slug")
	case in.GetClaim() == "" || len(in.GetClaim()) > maxClaim:
		return nil, api.InvalidArgument("mapping.claim", fmt.Sprintf("claim must be 1-%d bytes", maxClaim))
	case in.GetValue() == "" || len(in.GetValue()) > maxValue:
		return nil, api.InvalidArgument("mapping.value", fmt.Sprintf("value must be 1-%d bytes", maxValue))
	case !ValidProject(in.GetProject()):
		return nil, api.InvalidArgument("mapping.project", `project must be a project id or "*"`)
	}
	if _, err := s.roleExists(ctx, in.GetRole()); err != nil {
		return nil, err
	}
	id, err := newMappingID()
	if err != nil {
		return nil, err
	}
	m := store.OidcMapping{ID: id, Provider: in.GetProviderId(), Claim: in.GetClaim(), Value: in.GetValue(), Role: in.GetRole(), Project: in.GetProject()}
	_, err = store.CAS(ctx, s.control, store.OidcMappingsKey(), func(cur *store.OidcMappings) (*store.OidcMappings, error) {
		if cur == nil {
			cur = store.OidcMappingsKey()
		}
		n := 0
		for _, x := range cur.Mappings {
			if x.Provider == m.Provider {
				n++
			}
			if x.Provider == m.Provider && x.Claim == m.Claim && x.Value == m.Value && x.Role == m.Role && x.Project == m.Project {
				return nil, api.AlreadyExists("an identical mapping exists (" + x.ID + ")")
			}
		}
		if n >= MaxMappingsPerProvider {
			return nil, api.ResourceExhausted(fmt.Sprintf("a provider has at most %d mappings", MaxMappingsPerProvider))
		}
		cur.Mappings = append(cur.Mappings, m)
		return cur, nil
	})
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&accessv1.CreateOidcMappingResponse{Mapping: mappingMsg(m)}), nil
}

// mappingsOf returns the mappings of provider ("" = all).
func (s *Service) mappingsOf(ctx context.Context, provider string) ([]store.OidcMapping, error) {
	doc, _, err := store.GetCached(ctx, s.control, &s.mappings, store.OidcMappingsKey())
	switch {
	case errors.Is(err, store.ErrNotFound):
		return nil, nil
	case err != nil:
		return nil, err
	}
	var out []store.OidcMapping
	for _, m := range doc.Mappings {
		if provider == "" || m.Provider == provider {
			out = append(out, m)
		}
	}
	return out, nil
}

// ListOidcMappings lists the mappings of a provider (all when provider_id is empty).
func (s *Service) ListOidcMappings(ctx context.Context, req *connect.Request[accessv1.ListOidcMappingsRequest]) (*connect.Response[accessv1.ListOidcMappingsResponse], error) {
	ms, err := s.mappingsOf(ctx, req.Msg.GetProviderId())
	if err != nil {
		return nil, err
	}
	out := &accessv1.ListOidcMappingsResponse{}
	for _, m := range ms {
		out.Mappings = append(out.Mappings, mappingMsg(m))
	}
	return connect.NewResponse(out), nil
}

// DeleteOidcMapping deletes a mapping. Bindings it granted stay until the user's next login.
func (s *Service) DeleteOidcMapping(ctx context.Context, req *connect.Request[accessv1.DeleteOidcMappingRequest]) (*connect.Response[accessv1.DeleteOidcMappingResponse], error) {
	id := req.Msg.GetId()
	_, err := store.CAS(ctx, s.control, store.OidcMappingsKey(), func(cur *store.OidcMappings) (*store.OidcMappings, error) {
		if cur == nil {
			return nil, api.NotFound("mapping")
		}
		i := slices.IndexFunc(cur.Mappings, func(m store.OidcMapping) bool { return m.ID == id })
		if i < 0 {
			return nil, api.NotFound("mapping")
		}
		cur.Mappings = slices.Delete(cur.Mappings, i, i+1)
		return cur, nil
	})
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&accessv1.DeleteOidcMappingResponse{}), nil
}

// claimMatches reports whether claim value v equals want (a string) or contains it (an array
// of strings).
func claimMatches(v any, want string) bool {
	switch x := v.(type) {
	case string:
		return x == want
	case []string:
		return slices.Contains(x, want)
	case []any:
		for _, e := range x {
			if s, ok := e.(string); ok && s == want {
				return true
			}
		}
	}
	return false
}

// Match returns the {role, project} pairs the mappings grant for claims (deduplicated, in
// mapping order).
func Match(mappings []store.OidcMapping, claims map[string]any) []api.Binding {
	var out []api.Binding
	for _, m := range mappings {
		if v, ok := claims[m.Claim]; ok && claimMatches(v, m.Value) {
			b := api.Binding{Role: m.Role, Project: m.Project}
			if !slices.Contains(out, b) {
				out = append(out, b)
			}
		}
	}
	return out
}

// ApplyOIDC re-evaluates the mappings of provider for an OIDC login of principal (SPEC
// §6.3.2): the principal's oidc-sourced bindings are replaced by the matched set; manual
// bindings are kept (a matched pair the principal holds manually stays manual). It returns
// the number of matched pairs; identity refuses the login when it is 0 and the provider has
// require_mapping. Mappings naming a role that no longer exists are skipped. Removing the
// oidc admin@* binding of the last admin is refused by the admin invariant: that binding
// stays and the login proceeds.
func (s *Service) ApplyOIDC(ctx context.Context, principal, provider string, claims map[string]any) (int, error) {
	if s.principals == nil {
		return 0, errNoIdentity()
	}
	ms, err := s.mappingsOf(ctx, provider)
	if err != nil {
		return 0, err
	}
	want := Match(ms, claims)
	cur, _, err := s.principals.Bindings(ctx, principal)
	if err != nil {
		return 0, err
	}
	var errs []error
	for _, b := range cur {
		if b.Source != store.BindingOIDC || slices.Contains(want, api.Binding{Role: b.Role, Project: b.Project}) {
			continue
		}
		err := s.Revoke(ctx, b.ID)
		var ce *connect.Error
		switch {
		case err == nil, errors.As(err, &ce) && ce.Code() == connect.CodeNotFound:
		case errors.As(err, &ce) && ce.Code() == connect.CodeFailedPrecondition:
			s.log.WarnContext(ctx, "rbac: OIDC login keeps the admin binding of the last admin", "principal", principal)
		default:
			errs = append(errs, err)
		}
	}
	for _, w := range want {
		_, err := s.Grant(ctx, principal, w.Role, w.Project, store.BindingOIDC)
		var ce *connect.Error
		switch {
		case err == nil, errors.Is(err, ErrBindingExists):
		case errors.As(err, &ce) && ce.Code() == connect.CodeNotFound: // role deleted
		default:
			errs = append(errs, err)
		}
	}
	return len(want), errors.Join(errs...)
}
