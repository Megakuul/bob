package oci

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/megakuul/bob/tests/harness"
	"github.com/megakuul/bob/tests/parity"
	"github.com/megakuul/bob/tests/serverenv/clients"
	"github.com/megakuul/bob/tests/serverenv/regenv"
)

// registryHost is host:port of the orchestrator (the registry is served at /v2/ of the API port).
func registryHost(e *regenv.Env) string {
	return strings.TrimPrefix(strings.TrimPrefix(e.API, "https://"), "http://")
}

// authfile writes a docker config giving token to the registry and returns its path.
func authfile(t *testing.T, p *clients.Client, host, token string) string {
	t.Helper()
	cfg, _ := json.Marshal(map[string]any{"auths": map[string]any{
		host: map[string]string{"auth": base64.StdEncoding.EncodeToString([]byte("bob:" + token))}}})
	f := filepath.Join(p.Dir("auth-"+strings.ReplaceAll(token[len(token)-6:], "/", "_")), "auth.json")
	if err := os.WriteFile(f, cfg, 0o600); err != nil {
		t.Fatal(err)
	}
	p.Own(f)
	return f
}

// pull runs podman pull of ref anonymously (authfile "") or with the docker config.
func pull(p *clients.Client, ref, authfile string) harness.Result {
	args := []string{"pull", "--tls-verify=false"}
	if authfile != "" {
		args = append(args, "--authfile", authfile)
	}
	return p.Run(append(args, ref)...)
}

func TestRREG003PullRegistry(t *testing.T) {
	parity.Covers(t, "R-REG-003")
	e := regenv.Up(t, 1)
	if e == nil {
		return
	}
	// project "pub" publishes a public image, project "priv" a private one (same fixture)
	e.Project("pub", "demo")
	e.Project("priv", "other")
	pubRel, _ := e.Publish("pub", "demo", "polyglot", "polyglot", "1.0.0")
	privRel, _ := e.Publish("priv", "other", "polyglot", "polyglot", "1.0.0")
	e.SetVisibility(pubRel.ID, "PUBLIC")
	e.SetVisibility(privRel.ID, "PRIVATE")
	if len(pubRel.Images) == 0 || len(privRel.Images) == 0 {
		t.Fatalf("release polyglot has no OCI image")
	}
	host := registryHost(e)
	pubRef := host + "/" + pubRel.Images[0].Repository + ":" + pubRel.Images[0].Tag
	privRef := host + "/" + privRel.Images[0].Repository + ":" + privRel.Images[0].Tag
	missingRef := host + "/" + privRel.Images[0].Repository + ":no-such-tag"

	// GET /v2/ challenge
	code, hdr, _ := e.Get(e.API+"/v2/", "", nil)
	if c := hdr.Get("WWW-Authenticate"); code != http.StatusUnauthorized || !strings.HasPrefix(c, `Bearer realm="`) || !strings.Contains(c, `service="bob"`) {
		t.Errorf("GET /v2/ anonymous: HTTP %d, WWW-Authenticate %q; want 401 with Bearer realm=\"<base>/oci/token\",service=\"bob\"", code, c)
	}

	p := clients.Podman(t)
	p.InsecureRegistry(host)

	// anonymous pull of a public tag works
	if r := pull(p, pubRef, ""); r.Exit != 0 {
		t.Fatalf("anonymous pull of the public tag: exit %d: %s", r.Exit, r.Combined())
	}
	// private tag anonymous: UNAUTHORIZED
	anon := pull(p, privRef, "")
	if anon.Exit == 0 || !strings.Contains(strings.ToLower(anon.Combined()), "unauthorized") {
		t.Errorf("anonymous pull of a private tag: exit %d: %s; want a failure naming UNAUTHORIZED", anon.Exit, anon.Combined())
	}
	// with an SA download token (docker config) it works
	sa := e.Principal("priv-reader", "priv", "DOWNLOAD")
	if r := pull(p, privRef, authfile(t, p, host, sa)); r.Exit != 0 {
		t.Errorf("pull of the private tag with an SA download token: exit %d: %s", r.Exit, r.Combined())
	}
	// a principal of another project gets the same error as for a missing tag
	other := e.Principal("pub-reader", "pub", "DOWNLOAD")
	af := authfile(t, p, host, other)
	denied, missing := pull(p, privRef, af), pull(p, missingRef, af)
	norm := func(r harness.Result, ref string) string {
		return strings.ReplaceAll(strings.ReplaceAll(r.Combined(), ref, "REF"), host, "HOST")
	}
	if denied.Exit == 0 || missing.Exit == 0 || norm(denied, privRef) != norm(missing, missingRef) {
		t.Errorf("other-project pull must fail like a missing tag:\n denied (exit %d): %s\n missing (exit %d): %s", denied.Exit, denied.Combined(), missing.Exit, missing.Combined())
	}

	// push is refused with valid credentials, over podman and plain HTTP
	pushAuth := authfile(t, p, host, e.Admin)
	if r := p.Run("tag", pubRef, host+"/"+pubRel.Images[0].Repository+":pushed"); r.Exit != 0 {
		t.Fatalf("harness error: podman tag: %s", r.Combined())
	}
	if r := p.Run("push", "--tls-verify=false", "--authfile", pushAuth, host+"/"+pubRel.Images[0].Repository+":pushed"); r.Exit == 0 {
		t.Errorf("podman push with valid credentials succeeded; the registry is pull-only")
	}
	req, _ := http.NewRequest(http.MethodPut, e.API+"/v2/x/manifests/y", strings.NewReader("{}"))
	req.SetBasicAuth("bob", e.Admin)
	resp, err := e.HTTP.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var body struct {
		Errors []struct{ Code, Message string }
	}
	_ = json.NewDecoder(resp.Body).Decode(&body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed || len(body.Errors) == 0 || body.Errors[0].Code != "UNSUPPORTED" {
		t.Errorf("PUT /v2/x/manifests/y: HTTP %d %+v, want 405 UNSUPPORTED", resp.StatusCode, body)
	}
	e.AssertNoList()
}
