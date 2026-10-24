package cache

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/megakuul/bob/tests/parity"
	"github.com/megakuul/bob/tests/serverenv/clients"
	"github.com/megakuul/bob/tests/serverenv/regenv"
)

// publishP1 links p1 ("acme", alice/demo) and p2 ("other", alice/other) and publishes the
// private release acme/api@1.4.2 of fixture acme. It returns the release and its first root.
func publishP1(t *testing.T, e *regenv.Env) (regenv.Release, string) {
	t.Helper()
	e.Project("acme", "demo")
	e.Project("other", "other")
	rel, _ := e.Publish("acme", "demo", "acme", "api", "1.4.2")
	e.SetVisibility(rel.ID, "PRIVATE")
	return rel, roots(t, rel)[0]
}

func narinfoStatus(e *regenv.Env, base, path, token string) int {
	code, _, _ := e.Get(base+"/nix/"+regenv.StoreHash(path)+".narinfo", token, nil)
	return code
}

func TestRREG001NixSubstitution(t *testing.T) {
	parity.Covers(t, "R-REG-001")
	e := regenv.Up(t, 2)
	if e == nil {
		return
	}
	rel, root := publishP1(t, e)
	keys := e.PublicKeys()
	n := clients.Nix(t)
	p1 := e.Principal("p1-reader", "acme", "DOWNLOAD")
	p2 := e.Principal("p2-reader", "other", "DOWNLOAD")
	cache := e.API + "/nix"

	// (1) a download token for p1 substitutes the p1 closure from either orchestrator;
	// the paths verify against bob's key alone.
	for i, base := range e.APIs {
		l := newLocal(n)
		r := l.copyFrom(base+"/nix", regenv.Netrc(t, n, base, p1), keys, root)
		if r.Exit != 0 {
			t.Fatalf("(1) orchestrator %d: nix copy with p1 token: exit %d: %s", i, r.Exit, r.Combined())
		}
		if v := l.verify(keys, l.closure(t, root)...); v.Exit != 0 {
			t.Errorf("(1) orchestrator %d: nix store verify --trusted-public-keys: exit %d: %s", i, v.Exit, v.Combined())
		}
	}

	// (2) without credentials: narinfo 404, and Nix prints no bob path as copied.
	if got := narinfoStatus(e, e.API, root, ""); got != http.StatusNotFound {
		t.Errorf("(2) anonymous narinfo of a private path: HTTP %d, want 404", got)
	}
	l := newLocal(n)
	r := l.copyFrom(cache, "", keys, root)
	if r.Exit == 0 {
		t.Errorf("(2) anonymous nix copy of a private path succeeded")
	}
	if strings.Contains(r.Combined(), "copying path") {
		t.Errorf("(2) Nix printed a copied bob path without credentials: %s", r.Combined())
	}

	// (4) a principal of p2 only gets 404 for a p1 path.
	if got := narinfoStatus(e, e.API, root, p2); got != http.StatusNotFound {
		t.Errorf("(4) p2-only narinfo of a p1 path: HTTP %d, want 404", got)
	}
	l = newLocal(n)
	if r := l.copyFrom(cache, regenv.Netrc(t, n, e.API, p2), keys, root); r.Exit == 0 || strings.Contains(r.Combined(), "copying path") {
		t.Errorf("(4) p2-only nix copy of a p1 path: exit %d: %s", r.Exit, r.Combined())
	}

	// (5) a wrong token: nix-cache-info answers 401.
	code, hdr, _ := e.Get(cache+"/nix-cache-info", "bob_dt_wrong", nil)
	if code != http.StatusUnauthorized {
		t.Errorf("(5) nix-cache-info with a wrong token: HTTP %d, want 401", code)
	}
	if !strings.HasPrefix(hdr.Get("WWW-Authenticate"), "Basic") {
		t.Errorf("(5) WWW-Authenticate = %q, want Basic realm=\"bob\"", hdr.Get("WWW-Authenticate"))
	}

	// (3) after the closure is in a public release, anonymous substitution works (the
	// visibility cache lives 30 s, so allow 35 s).
	e.SetVisibility(rel.ID, "PUBLIC")
	deadline := time.Now().Add(35 * time.Second)
	for narinfoStatus(e, e.API, root, "") != http.StatusOK && time.Now().Before(deadline) {
		time.Sleep(time.Second)
	}
	l = newLocal(n)
	if r := l.copyFrom(cache, "", keys, root); r.Exit != 0 {
		t.Fatalf("(3) anonymous nix copy of a public path: exit %d: %s", r.Exit, r.Combined())
	}
	if v := l.verify(keys, l.closure(t, root)...); v.Exit != 0 {
		t.Errorf("(3) nix store verify: exit %d: %s", v.Exit, v.Combined())
	}
	e.AssertNoList()
}

func TestRREG154DownloadTokenScope(t *testing.T) {
	parity.Covers(t, "R-REG-154")
	e := regenv.Up(t, 1)
	if e == nil {
		return
	}
	e.Project("acme", "demo")
	// R-REG-154 is about the bob_dt_ download token of a CLI login; a service-account token of
	// scope download (bob_sa_) is refused on every /api/ method, WhoAmI included (R-AUTH-005,
	// TestRAUTH005ServiceAccounts).
	tok := e.CLIDownloadToken("dl")

	var who struct {
		TokenScope string `json:"tokenScope"`
	}
	e.MustCall(tok, "bob.auth.v1.AuthService/WhoAmI", nil, &who)
	if who.TokenScope != "download" {
		t.Errorf("WhoAmI with a download token: token_scope %q, want \"download\"", who.TokenScope)
	}
	err := e.Call(tok, "bob.project.v1.ProjectService/ListProjects", nil, nil)
	if err == nil || err.Code != "unauthenticated" || !strings.Contains(err.Message, "token not valid for the API") {
		t.Errorf("ListProjects with a download token: %v, want Unauthenticated \"token not valid for the API\"", err)
	}
	if code, _, _ := e.Get(e.API+"/nix/nix-cache-info", tok, nil); code != http.StatusOK {
		t.Errorf("nix-cache-info with a download token: HTTP %d, want 200", code)
	}
	e.AssertNoList()
}
