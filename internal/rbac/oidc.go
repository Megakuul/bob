package rbac

import (
	"slices"
	"time"

	"github.com/megakuul/bob/internal/api"
	"github.com/megakuul/bob/internal/store"
)

// OIDC claim → role mappings (SPEC §6.3.2, R-AUTH-004). Identity stores the mappings
// (`oidcmap/all`) and the bindings; RBAC decides what a login's claims grant.

// Binding sources of store.RoleBindingEntry.Source.
const (
	SourceManual = "manual"
	SourceOIDC   = "oidc"
)

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

// Match returns the {role, project} pairs that the mappings of provider grant for the ID
// token's claims: a mapping matches when its claim equals its value (string claim) or contains
// it (array of strings). The result has no duplicates and keeps mapping order.
func Match(mappings []store.OidcMappingEntry, provider string, claims map[string]any) []api.Binding {
	var out []api.Binding
	for _, m := range mappings {
		if m.Provider != provider {
			continue
		}
		if v, ok := claims[m.Claim]; ok && claimMatches(v, m.Value) {
			b := api.Binding{Role: m.Role, Project: m.Project}
			if !slices.Contains(out, b) {
				out = append(out, b)
			}
		}
	}
	return out
}

// ReplaceOIDC returns the bindings of a principal after an OIDC login that matched the pairs
// matched: every oidc-sourced binding is replaced by the matched set, manual bindings are kept
// (a matched pair already held manually stays manual and is not duplicated). Kept oidc
// bindings keep their id and creation time; new ones get newID() and now. The order is the
// manual bindings, then the oidc ones in matched order. It is pure, so identity can run it
// inside the CAS of the principal's record.
func ReplaceOIDC(cur []store.RoleBindingEntry, matched []api.Binding, newID func() string, now time.Time) []store.RoleBindingEntry {
	out := make([]store.RoleBindingEntry, 0, len(cur)+len(matched))
	held := map[api.Binding]store.RoleBindingEntry{}
	for _, b := range cur {
		if b.Source == SourceOIDC {
			held[api.Binding{Role: b.Role, Project: b.Project}] = b
			continue
		}
		out = append(out, b)
	}
	for _, m := range matched {
		if slices.ContainsFunc(out, func(b store.RoleBindingEntry) bool { return b.Role == m.Role && b.Project == m.Project }) {
			continue
		}
		b, ok := held[m]
		if !ok {
			b = store.RoleBindingEntry{ID: newID(), Role: m.Role, Project: m.Project, Source: SourceOIDC, Created: now}
		}
		out = append(out, b)
	}
	return out
}
