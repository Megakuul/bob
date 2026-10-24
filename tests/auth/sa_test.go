package auth

import (
	"strings"
	"testing"
	"time"

	"github.com/megakuul/bob/tests/auth/authenv"
	"github.com/megakuul/bob/tests/parity"
)

// TestRAUTH005ServiceAccounts: a download-scope SA token works for OCI Basic and Nix netrc, is
// refused on /api/, and revocation takes effect on all three within 10 s; the secret is shown
// once and the token list shows no secret.
func TestRAUTH005ServiceAccounts(t *testing.T) {
	parity.Covers(t, "R-AUTH-005")
	s := authenv.Shared(t)
	s.NewSA(t, "ci")
	tokenID, secret := s.NewSAToken(t, "ci", "netrc", "download")
	if !strings.HasPrefix(secret, "bob_sa_") {
		t.Fatalf("SA token %q does not start with bob_sa_", secret)
	}

	anon := s.Anon()
	status := func() (oci, nix int, api *authenv.Reply) {
		oci, _, _ = anon.BasicGet(t, "/oci/token?service=bob&scope=repository:p1/app:pull", "ci", secret)
		nix, _, _ = anon.BasicGet(t, "/nix/nix-cache-info", "bob", secret)
		return oci, nix, s.WithToken(secret).Call(t, authenv.AuthSvc, "WhoAmI", map[string]any{})
	}
	oci, nix, api := status()
	if oci != 200 {
		t.Errorf("/oci/token with Basic ci:<download token>: HTTP %d, want 200", oci)
	}
	if nix != 200 {
		t.Errorf("/nix/nix-cache-info with netrc bob:<download token>: HTTP %d, want 200", nix)
	}
	if api.Code != "unauthenticated" || api.Message != "token not valid for the API" {
		t.Errorf("download token on /api/: want Unauthenticated 'token not valid for the API', got %s", api)
	}

	// the secret is returned once; the list shows id, name, scope, expiry, last-used day only
	list := s.Admin().Call(t, authenv.SASvc, "ListServiceAccountTokens", map[string]any{"serviceAccountId": "ci"}).Must(t, "ListServiceAccountTokens")
	if strings.Contains(string(list.Raw), secret) || strings.Contains(string(list.Raw), "secret") {
		t.Errorf("ListServiceAccountTokens exposes the secret: %s", list.Raw)
	}
	toks := authenv.List(list.Body, "tokens")
	if len(toks) != 1 {
		t.Fatalf("ListServiceAccountTokens: %d tokens, want 1: %s", len(toks), list.Raw)
	}
	allowed := map[string]bool{"id": true, "name": true, "scope": true, "expiresAt": true, "lastUsedOn": true, "createdAt": true}
	for k := range toks[0].(map[string]any) {
		if !allowed[k] {
			t.Errorf("ListServiceAccountTokens entry has unexpected field %q", k)
		}
	}
	e := toks[0].(map[string]any)
	if e["id"] != tokenID || e["name"] != "netrc" || e["scope"] != "TOKEN_SCOPE_DOWNLOAD" || e["expiresAt"] == nil || e["lastUsedOn"] == nil {
		t.Errorf("token entry = %v, want id %s name netrc scope download with expiry and last-used day", e, tokenID)
	}
	if again := s.Admin().Call(t, authenv.SASvc, "GetServiceAccount", map[string]any{"id": "ci"}).Must(t, "GetServiceAccount"); strings.Contains(string(again.Raw), secret) {
		t.Errorf("GetServiceAccount exposes the secret")
	}

	// an api-scope token works on /api/ as sa:ci
	_, apiSecret := s.NewSAToken(t, "ci", "api", "api")
	who := s.WithToken(apiSecret).Call(t, authenv.AuthSvc, "WhoAmI", map[string]any{}).Must(t, "WhoAmI with an api-scope SA token")
	if got := authenv.Str(who.Body, "principal.id"); got != "sa:ci" {
		t.Errorf("WhoAmI.principal.id = %q, want sa:ci", got)
	}

	// revoke → within 10 s all three endpoints refuse
	s.Admin().Call(t, authenv.SASvc, "RevokeServiceAccountToken", map[string]any{"serviceAccountId": "ci", "tokenId": tokenID}).Must(t, "RevokeServiceAccountToken")
	start := time.Now()
	for {
		oci, nix, api = status()
		if oci == 401 && nix == 401 && api.Code == "unauthenticated" {
			break
		}
		if time.Since(start) > 10*time.Second {
			t.Fatalf("10 s after revocation: /oci/token %d, /nix %d, /api %s — want 401, 401, Unauthenticated", oci, nix, api)
		}
		time.Sleep(250 * time.Millisecond)
	}
	if got := s.WithToken(apiSecret).Call(t, authenv.AuthSvc, "WhoAmI", map[string]any{}); !got.OK() {
		t.Errorf("revoking one token revoked the other token of the service account: %s", got)
	}
}
