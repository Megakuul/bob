package cli

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/megakuul/bob/tests/cli/fakeserver"
	"github.com/megakuul/bob/tests/harness"
	"github.com/megakuul/bob/tests/parity"
)

// credentials is $XDG_CONFIG_HOME/bob/credentials.json (SPEC §02.8 Files).
type credentials struct {
	V       int `json:"v"`
	Servers map[string]struct {
		Kind         string `json:"kind"`
		AccessToken  string `json:"accessToken"`
		RefreshToken string `json:"refreshToken"`
		Expires      string `json:"expires"`
		User         string `json:"user"`
	} `json:"servers"`
}

func readCredentials(t testing.TB, env []string) (credentials, os.FileInfo) {
	t.Helper()
	p := filepath.Join(homeDir(env), ".config", "bob", "credentials.json")
	st, err := os.Stat(p)
	if err != nil {
		t.Errorf("credentials.json: %v", err)
		return credentials{}, nil
	}
	var c credentials
	if err := json.Unmarshal([]byte(read(t, p)), &c); err != nil {
		t.Errorf("credentials.json is not JSON: %v", err)
	}
	return c, st
}

// approveDevice approves every device code the server hands out, like the /device page does for
// a logged-in user, until the test ends.
func approveDevice(t testing.TB, srv *fakeserver.Server, user string) {
	t.Helper()
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-stop:
				return
			case <-time.After(50 * time.Millisecond):
				for _, c := range srv.DeviceCodes() {
					srv.ApproveDevice(c, user)
				}
			}
		}
	}()
	t.Cleanup(func() { close(stop); <-done })
}

// TestRCLI003DeviceFlow: the device flow completes when the code is approved through the server,
// prints the verification URL and code, and credentials.json has mode 0600.
func TestRCLI003DeviceFlow(t *testing.T) {
	parity.Covers(t, "R-CLI-003")
	srv := fakeserver.New(t, fakeserver.Options{})
	approveDevice(t, srv, "linus")
	env := home(t)
	r := bob(t, t.TempDir(), env, "login", "--device", srv.URL())
	if harness.Red(t, r) {
		return
	}
	if r.Exit != 0 {
		t.Fatalf("bob login --device: exit %d\n%s", r.Exit, r.Combined())
	}
	if !strings.Contains(r.Stdout, "Open "+srv.URL()+"/device and enter code ") {
		t.Errorf("no `Open <url>/device and enter code <code>` line:\n%s", r.Stdout)
	}
	if !strings.Contains(r.Stdout, "logged in to") || !strings.Contains(r.Stdout, "linus") {
		t.Errorf("no `logged in to <server> as linus` line:\n%s", r.Stdout)
	}
	c, st := readCredentials(t, env)
	if st != nil && st.Mode().Perm() != 0o600 {
		t.Errorf("credentials.json mode %o, want 0600", st.Mode().Perm())
	}
	if c.V != 1 || len(c.Servers) != 1 {
		t.Fatalf("credentials.json: %+v", c)
	}
	for _, s := range c.Servers {
		if s.Kind != "user" || s.AccessToken == "" || s.RefreshToken == "" || s.User != "linus" || s.Expires == "" {
			t.Errorf("credentials entry incomplete: %+v", s)
		}
	}
	cfg := read(t, filepath.Join(homeDir(env), ".config", "bob", "config.json"))
	if !strings.Contains(cfg, `"defaultServer"`) || !strings.Contains(cfg, srv.URL()) {
		t.Errorf("config.json has no defaultServer %s:\n%s", srv.URL(), cfg)
	}
	if srv.WhoAmICalls() == 0 {
		t.Errorf("bob login did not call AuthService/WhoAmI afterwards")
	}
	noLeak(t, r)

	// logout revokes best effort and removes the credentials entry.
	lo := bob(t, t.TempDir(), env, "logout", srv.URL())
	if lo.Exit != 0 || srv.Count("/oauth/revoke") != 1 {
		t.Errorf("bob logout: exit %d, %d revoke calls\n%s", lo.Exit, srv.Count("/oauth/revoke"), lo.Combined())
	}
	if c, _ := readCredentials(t, env); len(c.Servers) != 0 {
		t.Errorf("credentials entry still present after logout: %+v", c)
	}
}

// TestRCLI003Refresh: with the access token set to expire in 30 s, `bob build //...` (server
// substituter configured) calls Refresh exactly once, then succeeds; with Refresh answering 401
// the build exits 4 with BOB-E-4008.
func TestRCLI003Refresh(t *testing.T) {
	parity.Covers(t, "R-CLI-003")
	for _, c := range []struct {
		name   string
		status int
		exit   int
	}{{"refresh-ok", 0, 0}, {"refresh-401", 401, 4}} {
		t.Run(c.name, func(t *testing.T) {
			srv := fakeserver.New(t, fakeserver.Options{LoginTTL: 30 * time.Second})
			approveDevice(t, srv, "linus")
			env := home(t)
			dir := harness.Fixture(t, "acme")
			l := bob(t, dir, env, "login", "--device", srv.URL())
			if harness.Red(t, l) {
				return
			}
			if l.Exit != 0 {
				t.Fatalf("login: exit %d\n%s", l.Exit, l.Combined())
			}
			srv.FailRefresh(c.status)
			r := bob(t, dir, env, "build", "//...")
			if got := srv.RefreshCalls(); got != 1 {
				t.Errorf("Refresh called %d times, want exactly once", got)
			}
			if r.Exit != c.exit {
				t.Errorf("bob build: exit %d, want %d\n%s", r.Exit, c.exit, r.Combined())
			}
			if c.status == 401 && !strings.Contains(r.Combined(), "BOB-E-4008") {
				t.Errorf("want BOB-E-4008 after a refresh answered 401:\n%s", r.Combined())
			}
			if c.status == 0 {
				used := false
				for _, rq := range srv.Requests() {
					if rq.Path == "/api/bob.auth.v1.AuthService/WhoAmI" && rq.Authorization != "" {
						used = true
					}
				}
				if !used {
					t.Errorf("no authenticated WhoAmI (R-NIX-029 pre-check) after the refresh")
				}
				c2, _ := readCredentials(t, env)
				for _, s := range c2.Servers {
					if exp, err := time.Parse(time.RFC3339, s.Expires); err == nil && time.Until(exp) < 5*time.Minute {
						t.Errorf("credentials.json still has the old expiry %s after the refresh", s.Expires)
					}
				}
			}
			noLeak(t, r)
		})
	}
}

// TestRCLI003PKCE: the PKCE flow with a fake browser (a $BROWSER script that GETs the URL and
// follows the redirect) completes, and the token request carries a code_verifier that matches the
// S256 challenge.
func TestRCLI003PKCE(t *testing.T) {
	parity.Covers(t, "R-CLI-003")
	srv := fakeserver.New(t, fakeserver.Options{})
	browser := filepath.Join(t.TempDir(), "browser.sh")
	script := "#!/bin/sh\nexec curl -sS -L -o /dev/null \"$1\"\n"
	if err := os.WriteFile(browser, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	env := append(home(t), "BROWSER="+browser)
	r := bob(t, t.TempDir(), env, "login", srv.URL())
	if harness.Red(t, r) {
		return
	}
	if r.Exit != 0 {
		t.Fatalf("bob login (PKCE): exit %d\n%s", r.Exit, r.Combined())
	}
	if srv.Count("/oauth/device") != 0 {
		t.Errorf("the device flow was used although a browser is available")
	}
	var auth *fakeserver.Request
	for _, rq := range srv.Requests() {
		if rq.Path == "/oauth/authorize" {
			rq := rq
			auth = &rq
		}
	}
	if auth == nil {
		t.Fatalf("the fake browser never reached /oauth/authorize:\n%s", r.Combined())
	}
	q := auth.Form
	if q.Get("code_challenge_method") != "S256" || q.Get("client_id") != "bob-cli" || q.Get("response_type") != "code" ||
		!strings.HasPrefix(q.Get("redirect_uri"), "http://127.0.0.1:") || !strings.HasSuffix(q.Get("redirect_uri"), "/callback") || q.Get("state") == "" {
		t.Errorf("authorize request does not follow RFC 7636/8252 as SPEC §06 requires: %v", q)
	}
	forms := srv.TokenForms("authorization_code")
	if len(forms) != 1 {
		t.Fatalf("want one authorization_code token request, got %d", len(forms))
	}
	ver := forms[0].Get("code_verifier")
	sum := sha256.Sum256([]byte(ver))
	if ver == "" || len(ver) < 43 || base64.RawURLEncoding.EncodeToString(sum[:]) != q.Get("code_challenge") {
		t.Errorf("code_verifier %q does not match the S256 challenge %q", ver, q.Get("code_challenge"))
	}
	if c, st := readCredentials(t, env); st == nil || st.Mode().Perm() != 0o600 || len(c.Servers) != 1 {
		t.Errorf("credentials.json missing, wrong mode or entries: %+v", c)
	}
}

// TestRCLI003TokenStdin: `--token-stdin` stores a service-account token (kind service, no
// refresh token), checked with WhoAmI.
func TestRCLI003TokenStdin(t *testing.T) {
	parity.Covers(t, "R-CLI-003")
	srv := fakeserver.New(t, fakeserver.Options{})
	srv.AddToken("bob_at_service", "ci", "api", time.Time{})
	env := home(t)
	r := harness.Run(t, "bob", harness.Spec{Dir: t.TempDir(), Env: env, Stdin: "bob_at_service\n"}, "login", "--token-stdin", srv.URL())
	if harness.Red(t, r) {
		return
	}
	if r.Exit != 0 {
		t.Fatalf("exit %d\n%s", r.Exit, r.Combined())
	}
	c, st := readCredentials(t, env)
	if st != nil && st.Mode().Perm() != 0o600 {
		t.Errorf("mode %o, want 0600", st.Mode().Perm())
	}
	for _, s := range c.Servers {
		if s.Kind != "service" || s.AccessToken != "bob_at_service" || s.RefreshToken != "" {
			t.Errorf("service entry wrong: %+v", s)
		}
	}
	if srv.WhoAmICalls() == 0 {
		t.Errorf("WhoAmI not called")
	}
	bad := harness.Run(t, "bob", harness.Spec{Dir: t.TempDir(), Env: home(t), Stdin: "nope\n"}, "login", "--token-stdin", srv.URL())
	if bad.Exit != 4 || !strings.Contains(bad.Combined(), "BOB-E-4008") {
		t.Errorf("invalid service token: exit %d, want 4 with BOB-E-4008\n%s", bad.Exit, bad.Combined())
	}
}
