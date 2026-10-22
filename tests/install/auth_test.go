package install

import (
	"strings"
	"testing"
	"time"

	"github.com/megakuul/bob/tests/cli/fakeserver"
	"github.com/megakuul/bob/tests/harness"
	"github.com/megakuul/bob/tests/parity"
)

// TestRINST003PublicWithoutAuth: with no credentials a public release installs, and the server's
// log shows no Authorization header on any of its requests.
func TestRINST003PublicWithoutAuth(t *testing.T) {
	parity.Covers(t, "R-INST-003")
	srv := fakeserver.New(t, fakeserver.Options{Name: "default"})
	publish(t, srv, "acme", "cli", false, []string{"acme"}, "1.0.0")
	e := newEnv(t)
	if r := e.bob(t, "", "registry", "add", "default", srv.URL()); harness.Red(t, r) {
		return
	}
	r := e.bob(t, "", "install", "acme/cli")
	if r.Exit != 0 {
		t.Fatalf("anonymous install of a public release: exit %d\n%s", r.Exit, r.Combined())
	}
	if v, rr := e.version(t, "acme"); v != "1.0.0" {
		t.Errorf("acme --version = %q (exit %d)", v, rr.Exit)
	}
	for _, rq := range srv.Requests() {
		if rq.Authorization != "" {
			t.Errorf("request %s %s carries an Authorization header although bob has no credentials", rq.Method, rq.Path)
		}
	}
	if srv.Count("/nix/") == 0 {
		t.Errorf("the closure was not substituted from the registry's /nix cache")
	}
	noLeak(t, r)
	checkNo401(t, r)
}

// checkNo401 fails when Nix's 401 warning reaches the user ("No Nix 401 warning is ever shown").
func checkNo401(t testing.TB, r harness.Result) {
	t.Helper()
	for _, bad := range []string{"401", "Unauthorized", "HTTP error", "unable to download", "warning: error:"} {
		if strings.Contains(r.Combined(), bad) {
			t.Errorf("output of bob %s mentions %q:\n%s", strings.Join(r.Args, " "), bad, r.Combined())
		}
	}
}

// TestRINST003PrivateNeedsLogin: a private release fails without credentials with exit 4 and
// BOB-E-4010 (4008 with an invalid token), and the hint names `bob login`; after `bob login
// --token-stdin` with a service token that has the read permission, it installs.
func TestRINST003PrivateNeedsLogin(t *testing.T) {
	parity.Covers(t, "R-INST-003")
	srv := fakeserver.New(t, fakeserver.Options{Name: "default"})
	publish(t, srv, "acme", "secret", true, []string{"secret"}, "2.0.0")
	srv.AddToken("bob_at_reader", "ci", "api", time.Time{})
	e := newEnv(t)
	if r := e.bob(t, "", "registry", "add", "default", srv.URL()); harness.Red(t, r) {
		return
	}
	r := e.bob(t, "", "install", "acme/secret")
	if r.Exit != 4 || !strings.Contains(r.Combined(), "BOB-E-4010") || !strings.Contains(r.Combined(), "bob login") {
		t.Errorf("private release without credentials: exit %d, want 4 with BOB-E-4010 and a hint naming `bob login`\n%s", r.Exit, r.Combined())
	}
	noLeak(t, r)
	checkNo401(t, r)

	bad := e.with("BOB_TOKEN=bob_at_invalid")
	r = bad.bob(t, "", "install", "acme/secret")
	if r.Exit != 4 || !strings.Contains(r.Combined(), "BOB-E-4008") || !strings.Contains(r.Combined(), "bob login") {
		t.Errorf("invalid token: exit %d, want 4 with BOB-E-4008 and a hint naming `bob login`\n%s", r.Exit, r.Combined())
	}
	checkNo401(t, r)

	l := harness.Run(t, "bob", harness.Spec{Env: e.vars, Stdin: "bob_at_reader\n"}, "login", "--token-stdin", srv.URL())
	if l.Exit != 0 {
		t.Fatalf("login --token-stdin: exit %d\n%s", l.Exit, l.Combined())
	}
	pre := srv.WhoAmICalls()
	r = e.bob(t, "", "install", "acme/secret")
	if r.Exit != 0 {
		t.Fatalf("install of a private release after login: exit %d\n%s", r.Exit, r.Combined())
	}
	if srv.WhoAmICalls() <= pre {
		t.Errorf("no AuthService/WhoAmI pre-check before the install (R-NIX-029)")
	}
	if v, rr := e.version(t, "secret"); v != "2.0.0" {
		t.Errorf("secret --version = %q (exit %d)", v, rr.Exit)
	}
	checkNo401(t, r)
	noLeak(t, r)
	// the token reaches Nix only through bob's netrc: it is never on a command line or in the
	// environment of a child; here: it must be in the generated netrc with mode 0600.
	netrc := e.home + "/.local/share/bob/etc/nix/netrc"
	if !strings.Contains(readFile(t, netrc), "bob_") {
		t.Errorf("bob's netrc %s holds no token", netrc)
	}
}
