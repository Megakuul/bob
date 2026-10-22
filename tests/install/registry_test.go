package install

import (
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/megakuul/bob/tests/cli/fakeserver"
	"github.com/megakuul/bob/tests/harness"
	"github.com/megakuul/bob/tests/parity"
)

// twoServers returns an environment with registries `default` and `corp` (two fake servers).
func twoServers(t *testing.T) (env, *fakeserver.Server, *fakeserver.Server, harness.Result) {
	t.Helper()
	def := fakeserver.New(t, fakeserver.Options{Name: "default"})
	corp := fakeserver.New(t, fakeserver.Options{Name: "corp"})
	publish(t, def, "acme", "cli", false, []string{"acme"}, "1.0.0", "1.1.0")
	publish(t, corp, "infra", "dt", false, []string{"dt"}, "0.9.0", "0.9.1")
	e := newEnv(t)
	r := e.addRegistry(t, "default", def)
	if r.Exit == 0 {
		r = e.addRegistry(t, "corp", corp)
	}
	return e, def, corp, r
}

// TestRINST004RegistrySelection: `corp:infra/dt` resolves on corp only (the default server sees no
// request for it), no prefix selects the default, and each generation entry records its registry.
func TestRINST004RegistrySelection(t *testing.T) {
	parity.Covers(t, "R-INST-004")
	e, def, corp, r := twoServers(t)
	if harness.Red(t, r) {
		return
	}
	if r.Exit != 0 {
		t.Fatalf("registry add: exit %d\n%s", r.Exit, r.Combined())
	}
	// `bob registry add` pins the server's public keys from AuthService/GetServerInfo (anonymous).
	cfg := readFile(t, e.home+"/.config/bob/config.json")
	if !strings.Contains(cfg, corp.PublicKey()) || !strings.Contains(cfg, def.PublicKey()) {
		t.Errorf("config.json does not pin the registries' public keys:\n%s", cfg)
	}
	for _, rq := range append(def.Requests(), corp.Requests()...) {
		if rq.Path == "/api/bob.auth.v1.AuthService/GetServerInfo" && rq.Authorization != "" {
			t.Errorf("GetServerInfo must be anonymous")
		}
	}
	base := def.Count("/api/bob.release")
	ir := e.bob(t, "", "install", "corp:infra/dt")
	if ir.Exit != 0 {
		t.Fatalf("install corp:infra/dt: exit %d\n%s", ir.Exit, ir.Combined())
	}
	if def.Count("/api/bob.release") != base || def.Count("/nix/") != 0 {
		t.Errorf("the default server saw requests for corp:infra/dt: %v", def.Requests())
	}
	if corp.Count("/api/bob.release.v1.ReleaseService/ResolveRelease") == 0 || corp.Count("/nix/") == 0 {
		t.Errorf("corp saw no resolve/substitute requests")
	}
	if v, rr := e.version(t, "dt"); v != "0.9.1" {
		t.Errorf("dt --version = %q (exit %d), want 0.9.1", v, rr.Exit)
	}
	if ir := e.bob(t, "", "install", "acme/cli@1.0.0"); ir.Exit != 0 {
		t.Fatalf("install acme/cli (default registry): exit %d\n%s", ir.Exit, ir.Combined())
	}
	if corp.Count("/api/bob.release.v1.ReleaseService/ResolveRelease") != 1 {
		t.Errorf("an install without a prefix went to corp")
	}
	g := e.readGeneration(t, e.current(t))
	regs := map[string]string{}
	for _, en := range g.Entries {
		regs[en.Ref] = en.Registry
	}
	if len(g.Entries) != 2 || regs["acme/cli"] != "default" || regs["infra/dt"] != "corp" {
		t.Errorf("generation entries do not record their registries: %+v", regs)
	}
	// `registry list` and `default`.
	l := e.bob(t, "", "registry", "list")
	if !strings.Contains(l.Stdout, "default") || !strings.Contains(l.Stdout, "corp") || !strings.Contains(l.Stdout, corp.URL()) {
		t.Errorf("registry list lacks the registries:\n%s", l.Stdout)
	}
	if d := e.bob(t, "", "registry", "default", "corp"); d.Exit != 0 {
		t.Errorf("registry default corp: exit %d\n%s", d.Exit, d.Combined())
	}
	if rm := e.bob(t, "", "registry", "remove", "corp"); rm.Exit != 0 {
		t.Errorf("registry remove corp: exit %d\n%s", rm.Exit, rm.Combined())
	}
	if l := e.bob(t, "", "registry", "list"); strings.Contains(l.Stdout, "corp") {
		t.Errorf("corp still listed after remove:\n%s", l.Stdout)
	}
}

// TestRINST004KeyChange: a later key change makes installs fail with BOB-E-4004 until
// `bob registry add --refresh-keys corp`.
func TestRINST004KeyChange(t *testing.T) {
	parity.Covers(t, "R-INST-004")
	e, _, corp, r := twoServers(t)
	if harness.Red(t, r) {
		return
	}
	corp.RotateKey(t)
	out := e.bob(t, "", "install", "corp:infra/dt")
	if out.Exit != 4 || !strings.Contains(out.Combined(), "BOB-E-4004") {
		t.Errorf("install after a key change: exit %d, want 4 with BOB-E-4004\n%s", out.Exit, out.Combined())
	}
	noLeak(t, out)
	if e.current(t) != 0 {
		t.Errorf("a generation was written although the signature was untrusted")
	}
	rf := e.bob(t, "", "registry", "add", "--refresh-keys", "corp")
	if rf.Exit != 0 {
		t.Fatalf("registry add --refresh-keys corp: exit %d\n%s", rf.Exit, rf.Combined())
	}
	if !strings.Contains(readFile(t, e.home+"/.config/bob/config.json"), corp.PublicKey()) {
		t.Errorf("--refresh-keys did not pin the new key %s", corp.PublicKey())
	}
	if ok := e.bob(t, "", "install", "corp:infra/dt"); ok.Exit != 0 {
		t.Errorf("install after --refresh-keys: exit %d\n%s", ok.Exit, ok.Combined())
	}
	// an admin can pre-seed keys with --key: no GetServerInfo needed for them.
	e2 := newEnv(t)
	if r := e2.bob(t, "", "registry", "add", "corp", corp.URL(), "--key", corp.PublicKey()); r.Exit != 0 {
		t.Errorf("registry add --key: exit %d\n%s", r.Exit, r.Combined())
	} else if !strings.Contains(readFile(t, e2.home+"/.config/bob/config.json"), corp.PublicKey()) {
		t.Errorf("--key did not pin the key")
	}
}

// TestRINST004ListAndUpgrade: with two harness servers `bob list` shows the registry column and
// `bob upgrade` updates each entry from its own registry.
func TestRINST004ListAndUpgrade(t *testing.T) {
	parity.Covers(t, "R-INST-004")
	e, def, corp, r := twoServers(t)
	if harness.Red(t, r) {
		return
	}
	for _, ref := range []string{"acme/cli@1.0.0", "corp:infra/dt@0.9.0"} {
		if ir := e.bob(t, "", "install", ref); ir.Exit != 0 {
			t.Fatalf("install %s: exit %d\n%s", ref, ir.Exit, ir.Combined())
		}
	}
	row := func(ref, ver, reg, bins string) *regexp.Regexp {
		return regexp.MustCompile(`(?m)^` + regexp.QuoteMeta(ref) + ` +` + regexp.QuoteMeta(ver) + ` +` + reg + ` +` + bins + `$`)
	}
	l := e.bob(t, "", "list")
	if !row("acme/cli", "1.0.0", "default", "acme").MatchString(l.Stdout) || !row("infra/dt", "0.9.0", "corp", "dt").MatchString(l.Stdout) {
		t.Errorf("bob list lacks the rows `acme/cli 1.0.0 default acme` / `infra/dt 0.9.0 corp dt`:\n%s", l.Stdout)
	}
	defBefore, corpBefore := bodies(def, "infra"), bodies(corp, "acme")
	u := e.bob(t, "", "upgrade")
	if u.Exit != 0 || !strings.Contains(u.Stdout, "acme/cli  1.0.0 → 1.1.0") || !strings.Contains(u.Stdout, "infra/dt  0.9.0 → 0.9.1") || !strings.Contains(u.Stdout, "bob: 2 upgraded (generation 3)") {
		t.Errorf("upgrade: exit %d, want both upgrades in one generation (3):\n%s", u.Exit, u.Combined())
	}
	l = e.bob(t, "", "list")
	if !row("acme/cli", "1.1.0", "default", "acme").MatchString(l.Stdout) || !row("infra/dt", "0.9.1", "corp", "dt").MatchString(l.Stdout) {
		t.Errorf("bob list after upgrade:\n%s", l.Stdout)
	}
	if bodies(def, "infra") != defBefore || bodies(corp, "acme") != corpBefore {
		t.Errorf("an upgrade asked a registry about an entry of the other registry")
	}
	if g := e.readGeneration(t, 3); len(g.Entries) != 2 {
		t.Errorf("generation 3 has %d entries, want 2", len(g.Entries))
	}
}

// bodies counts the ResolveRelease requests of srv that name project.
func bodies(srv *fakeserver.Server, project string) int {
	n := 0
	for _, rq := range srv.Requests() {
		if strings.HasSuffix(rq.Path, "/ResolveRelease") && strings.Contains(rq.Body+unescape(rq.Query), `"`+project+`"`) {
			n++
		}
	}
	return n
}

func unescape(q string) string {
	u, err := url.QueryUnescape(q)
	if err != nil {
		return q
	}
	return u
}
