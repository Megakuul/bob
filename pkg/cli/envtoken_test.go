package cli

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/megakuul/bob/pkg/bob/diag"
)

// TestEnvTokenCache: with BOB_TOKEN and BOB_SERVER but no stored login, the server's cache
// (from WhoAmI) is a substituter of the run, authenticated with the token (A295); a stored
// login or a missing token adds nothing; 401 is BOB-E-4008.
func TestEnvTokenCache(t *testing.T) {
	const key = "bob-test-1:" + "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="
	status := http.StatusOK
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/bob.auth.v1.AuthService/WhoAmI") || r.Header.Get("Authorization") != "Bearer tok" {
			http.Error(w, `{"code":"unauthenticated","message":"no"}`, http.StatusUnauthorized)
			return
		}
		if status != http.StatusOK {
			http.Error(w, `{"code":"unauthenticated","message":"no"}`, status)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"principal":{"id":"sa:ci"},"nix":{"substituter":"` + "http://" + r.Host + `/nix","publicKeys":["` + key + `"]}}`))
	}))
	defer srv.Close()
	vars := map[string]string{"BOB_TOKEN": "tok", "BOB_SERVER": srv.URL, "XDG_CONFIG_HOME": t.TempDir()}
	e := &env{ctx: context.Background(), a: &args{vals: map[string][]string{}}, getenv: func(k string) string { return vars[k] }}

	sub, warn, err := e.envTokenCache(e.ctx, nil)
	if err != nil || warn != nil || sub == nil {
		t.Fatalf("envTokenCache = %v, %v, %v; want a substituter", sub, warn, err)
	}
	if sub.URL != srv.URL+"/nix" || sub.PublicKey != key || sub.NetrcPassword != "tok" || sub.NetrcMachine != "127.0.0.1" {
		t.Errorf("substituter = %+v", *sub)
	}
	if sub, _, _ := e.envTokenCache(e.ctx, &credFile{Servers: map[string]*credEntry{srv.URL: {AccessToken: "x"}}}); sub != nil {
		t.Errorf("a stored login got a second substituter: %+v", *sub)
	}
	status = http.StatusUnauthorized
	var d *diag.Error
	if _, _, err := e.envTokenCache(e.ctx, nil); !errors.As(err, &d) || d.Code != 4008 {
		t.Errorf("401: err = %v, want BOB-E-4008", err)
	}
	vars["BOB_TOKEN"] = ""
	if sub, warn, err := e.envTokenCache(e.ctx, nil); sub != nil || warn != nil || err != nil {
		t.Errorf("without BOB_TOKEN: %v, %v, %v; want nothing", sub, warn, err)
	}
}
