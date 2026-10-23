package worker

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"connectrpc.com/connect"

	authv1 "github.com/megakuul/bob/internal/gen/bob/auth/v1"
	"github.com/megakuul/bob/internal/gen/bob/auth/v1/authv1connect"
	workerv1 "github.com/megakuul/bob/internal/gen/bob/worker/v1"
	"github.com/megakuul/bob/internal/isolation"
)

type serverInfo struct {
	authv1connect.UnimplementedAuthServiceHandler
	keys []string
}

func (s serverInfo) GetServerInfo(context.Context, *connect.Request[authv1.GetServerInfoRequest]) (*connect.Response[authv1.GetServerInfoResponse], error) {
	out := &authv1.GetServerInfoResponse{ServerName: "o1"}
	if s.keys != nil {
		out.Nix = &authv1.NixInfo{PublicKeys: s.keys}
	}
	return connect.NewResponse(out), nil
}

// orchestrator serves AuthService under /api over h2c, as the worker's attach client speaks it.
func orchestrator(t *testing.T, keys []string) string {
	t.Helper()
	mux := http.NewServeMux()
	path, h := authv1connect.NewAuthServiceHandler(serverInfo{keys: keys})
	mux.Handle("/api"+path, http.StripPrefix("/api", h))
	srv := httptest.NewUnstartedServer(mux)
	srv.Config.Protocols = new(http.Protocols)
	srv.Config.Protocols.SetHTTP1(true)
	srv.Config.Protocols.SetUnencryptedHTTP2(true)
	srv.Start()
	t.Cleanup(srv.Close)
	return srv.URL
}

// TestJobNixConfig: a job's container lists the orchestrator that answered its
// GetCheckoutCredential as its first substituter, trusts that orchestrator's cache keys, and
// gets the job's download token in its netrc (0600) for that host (SPEC §05.8, §6.6.1). An
// orchestrator without cache keys, or no credential at all, leaves the worker's own nix.conf
// and an empty netrc.
func TestJobNixConfig(t *testing.T) {
	dir := t.TempDir()
	p := &Process{nixConf: filepath.Join(dir, "worker-nix.conf"), cfg: ProcessConfig{Log: slog.New(slog.NewTextHandler(io.Discard, nil))}}
	base := orchestrator(t, []string{"bob-1:AAAA", "bob-2:BBBB"})
	cred := &jobCredential{base: base, res: &workerv1.GetCheckoutCredentialResponse{DownloadToken: "bob_dt_jobtoken"}}

	jobDir := filepath.Join(dir, "j1")
	if err := os.MkdirAll(jobDir, 0o755); err != nil {
		t.Fatal(err)
	}
	conf, netrc, err := p.jobNixConfig(context.Background(), Claim{JobID: "j1"}, jobDir, cred)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(conf)
	for _, want := range []string{
		"substituters = " + base + "/nix https://cache.nixos.org\n",
		"trusted-public-keys = bob-1:AAAA bob-2:BBBB cache.nixos.org-1:",
		"netrc-file = " + isolation.NetrcPath + "\n",
		"require-sigs = true\n",
	} {
		if !strings.Contains(string(b), want) {
			t.Errorf("job nix.conf lacks %q:\n%s", want, b)
		}
	}
	rc, _ := os.ReadFile(netrc)
	if string(rc) != "machine 127.0.0.1 login bob password bob_dt_jobtoken\n" {
		t.Errorf("netrc = %q", rc)
	}
	if fi, err := os.Stat(netrc); err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("netrc mode: %v %v, want 0600", fi.Mode(), err)
	}

	for name, c := range map[string]*jobCredential{
		"no cache keys": {base: orchestrator(t, nil), res: &workerv1.GetCheckoutCredentialResponse{DownloadToken: "bob_dt_jobtoken"}},
		"no credential": nil,
		"unreachable":   {base: "http://127.0.0.1:1", res: &workerv1.GetCheckoutCredentialResponse{DownloadToken: "bob_dt_jobtoken"}},
	} {
		d := filepath.Join(dir, strings.ReplaceAll(name, " ", "-"))
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
		conf, netrc, err := p.jobNixConfig(context.Background(), Claim{JobID: "j2"}, d, c)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if conf != p.nixConf {
			t.Errorf("%s: nix.conf %s, want the worker's own", name, conf)
		}
		if rc, err := os.ReadFile(netrc); err != nil || len(rc) != 0 {
			t.Errorf("%s: netrc %q (%v), want empty", name, rc, err)
		}
	}
}

// TestJobCaches: the engine inside the container gets bob's caches from the container's
// nix.conf (not cache.nixos.org) with the job's netrc credential: bob derivations are never
// substituted by Nix itself (A71), so without them a job rebuilds what the cache has.
func TestJobCaches(t *testing.T) {
	dir := t.TempDir()
	conf, netrc := filepath.Join(dir, "nix.conf"), filepath.Join(dir, "netrc")
	if err := os.WriteFile(conf, []byte(isolation.NixConf([]string{"http://127.0.0.1:4000/nix"}, []string{"bob-1:AAAA"})), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(netrc, []byte("machine 127.0.0.1 login bob password bob_dt_x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got := jobCaches(conf, netrc)
	if len(got) != 1 || got[0].URL != "http://127.0.0.1:4000/nix" || got[0].NetrcPassword != "bob_dt_x" {
		t.Errorf("jobCaches = %+v, want bob's cache with the job token", got)
	}
	if err := os.WriteFile(conf, []byte(isolation.NixConf(nil, nil)), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := jobCaches(conf, netrc); len(got) != 0 {
		t.Errorf("without a bob cache: %+v, want none", got)
	}
}
