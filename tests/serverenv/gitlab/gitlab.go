// Package gitlab starts a local GitLab (nixpkgs pin, no NixOS) for the forge-gitlab-live suite
// (HARNESS.md §8.2, slow, A13). It ports spike S16's gitlab/up.sh: postgres, redis, gitaly, puma,
// sidekiq and workhorse inside one private mount namespace, TLS on a loopback port. It needs
// about 3.8 GiB of RAM and several minutes for the first start (database setup).
package gitlab

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	_ "embed"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/megakuul/bob/tests/serverenv"
)

//go:embed up.sh
var upScript string

// Options configure Start.
type Options struct {
	Dir  string // state directory; required
	Port int    // 0 picks a free port in the harness range
}

// Instance is a running GitLab.
type Instance struct {
	URL       string // https://127.0.0.1:<port>
	CA        string // PEM file of the instance's certificate
	Dir       string
	RootToken string // personal access token of root (api, admin_mode)
	Client    *http.Client
	proc      *serverenv.Process
}

// pinned maps the script's variables to pins.nix entries and the sub path inside each.
var pinned = []struct{ env, pin, sub string }{
	{"GL", "gitlab", "share/gitlab"}, {"RUBYENV", "gitlab-rubyenv", ""}, {"GITALY", "gitaly", ""},
	{"WORKHORSE", "gitlab-workhorse", ""}, {"SHELL_PKG", "gitlab-shell", ""}, {"PG", "postgresql", ""},
	{"REDIS", "redis", ""}, {"GIT", "gitaly-git", ""}, {"NODE", "nodejs", ""}, {"RUBY", "ruby", ""},
	{"REMARSHAL", "remarshal", ""}, {"OPENSSL", "openssl-bin", ""},
}

// Start realises the pinned tools, starts GitLab and waits until the API answers and root has a
// token. On error everything started is stopped.
func Start(ctx context.Context, o Options) (*Instance, error) {
	if o.Dir == "" {
		return nil, errors.New("gitlab: Options.Dir is required")
	}
	dir, err := filepath.Abs(o.Dir)
	if err != nil {
		return nil, fmt.Errorf("gitlab: %w", err)
	}
	port := o.Port
	pgPort := 0
	for _, p := range []*int{&port, &pgPort} {
		if *p == 0 {
			if *p, err = serverenv.FreePort(); err != nil {
				return nil, err
			}
		}
	}
	env := append(os.Environ(), "ROOT="+dir, fmt.Sprintf("PORT=%d", port), fmt.Sprintf("PGPORT=%d", pgPort))
	for _, p := range pinned {
		out, err := serverenv.NixTool(ctx, p.pin)
		if err != nil {
			return nil, err
		}
		env = append(env, p.env+"="+filepath.Join(out, p.sub))
	}
	for _, d := range []string{filepath.Join(dir, "state"), filepath.Join(dir, "tls")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return nil, fmt.Errorf("gitlab: %w", err)
		}
	}
	if err := writeCert(filepath.Join(dir, "tls")); err != nil {
		return nil, fmt.Errorf("gitlab: %w", err)
	}
	script := filepath.Join(dir, "up.sh")
	if err := os.WriteFile(script, []byte(upScript), 0o755); err != nil {
		return nil, fmt.Errorf("gitlab: %w", err)
	}
	cmd := exec.Command("unshare", "--mount", "--propagation", "private", "--", "bash", script)
	cmd.Dir, cmd.Env = dir, env
	proc, err := serverenv.StartProcess(cmd, filepath.Join(dir, "gitlab.log"))
	if err != nil {
		return nil, err
	}
	pool := x509.NewCertPool()
	pem, err := os.ReadFile(filepath.Join(dir, "tls", "cert.pem"))
	if err != nil {
		return nil, errors.Join(err, proc.Stop(10*time.Second))
	}
	pool.AppendCertsFromPEM(pem)
	in := &Instance{
		URL: fmt.Sprintf("https://127.0.0.1:%d", port), CA: filepath.Join(dir, "tls", "cert.pem"), Dir: dir, proc: proc,
		Client: &http.Client{Timeout: 60 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool}}},
	}
	if err := in.waitReady(ctx); err != nil {
		return nil, errors.Join(err, in.Stop())
	}
	if err := in.createRootToken(ctx); err != nil {
		return nil, errors.Join(err, in.Stop())
	}
	// webhooks to the harness' loopback receivers
	if err := in.API(ctx, http.MethodPut, "/api/v4/application/settings?allow_local_requests_from_web_hooks_and_services=true", nil, nil); err != nil {
		return nil, errors.Join(err, in.Stop())
	}
	return in, nil
}

func (in *Instance) waitReady(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Minute)
	defer cancel()
	for {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, in.URL+"/api/v4/version", nil)
		if r, err := in.Client.Do(req); err == nil {
			r.Body.Close()
			if r.StatusCode == http.StatusUnauthorized || r.StatusCode == http.StatusOK {
				return nil
			}
		}
		select {
		case <-in.proc.Exited():
			return fmt.Errorf("gitlab: exited during start-up (%v), see %s: %w", in.proc.Err(), in.proc.Log, serverenv.ErrHarness)
		case <-ctx.Done():
			return fmt.Errorf("gitlab: not ready on %s: %w", in.URL, serverenv.ErrHarness)
		case <-time.After(2 * time.Second):
		}
	}
}

// createRootToken creates root's token with rails runner inside the script's mount namespace
// (S16 gitlab/bootstrap.sh).
func (in *Instance) createRootToken(ctx context.Context) error {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return err
	}
	tok := fmt.Sprintf("glpat-harness%x", b)
	rb := fmt.Sprintf("u=User.find_by_username('root'); t=u.personal_access_tokens.build(name: 'harness-root', scopes: [:api, :admin_mode], expires_at: 30.days.from_now, organization: u.organizations.first || Organizations::Organization.first); t.set_token('%s'); t.save!; puts 'ok'", tok)
	sh := fmt.Sprintf(". %s/state/../gitlab-env.sh; cd $GL; $RUBYENV/bin/rails runner %q", in.Dir, rb)
	cmd := exec.CommandContext(ctx, "nsenter", "-t", fmt.Sprint(in.proc.Pid()), "-m", "--", "bash", "-c", sh)
	// the script's children live in the namespace of the unshare process
	if out, err := cmd.CombinedOutput(); err != nil || !strings.Contains(string(out), "ok") {
		return fmt.Errorf("gitlab: create root token: %v: %s: %w", err, out, serverenv.ErrHarness)
	}
	in.RootToken = tok
	return nil
}

// API calls the GitLab REST API as root. in and out are JSON (either may be nil).
func (in *Instance) API(ctx context.Context, method, path string, body, out any) error {
	return in.APIAs(ctx, in.RootToken, method, path, body, out)
}

// APIAs is API with another PRIVATE-TOKEN.
func (in *Instance) APIAs(ctx context.Context, token, method, path string, body, out any) error {
	var rd *strings.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("gitlab: %w", err)
		}
		rd = strings.NewReader(string(b))
	} else {
		rd = strings.NewReader("")
	}
	req, err := http.NewRequestWithContext(ctx, method, in.URL+path, rd)
	if err != nil {
		return fmt.Errorf("gitlab: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("PRIVATE-TOKEN", token)
	r, err := in.Client.Do(req)
	if err != nil {
		return fmt.Errorf("gitlab: %s %s: %w", method, path, err)
	}
	defer r.Body.Close()
	var raw json.RawMessage
	_ = json.NewDecoder(r.Body).Decode(&raw)
	if r.StatusCode/100 != 2 {
		return &StatusError{Code: r.StatusCode, Method: method, Path: path, Body: string(raw)}
	}
	if out != nil && len(raw) > 0 {
		if err := json.Unmarshal(raw, out); err != nil {
			return fmt.Errorf("gitlab: %s %s: decode: %w", method, path, err)
		}
	}
	return nil
}

// StatusError is a non-2xx API answer.
type StatusError struct {
	Code         int
	Method, Path string
	Body         string
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("gitlab: %s %s: HTTP %d: %s", e.Method, e.Path, e.Code, e.Body)
}

// Stop ends GitLab (the script's trap stops its children) and waits.
func (in *Instance) Stop() error { return in.proc.Stop(60 * time.Second) }

// PrepareTLS writes the instance certificate below dir/tls when it is missing and returns the
// certificate path. The forge adapters have no CA option, so a test process trusts it through
// SSL_CERT_FILE before it starts GitLab with Options.Dir = dir.
func PrepareTLS(dir string) (string, error) {
	d := filepath.Join(dir, "tls")
	if err := os.MkdirAll(d, 0o755); err != nil {
		return "", err
	}
	return filepath.Join(d, "cert.pem"), writeCert(d)
}

func writeCert(dir string) error {
	if _, err := os.Stat(filepath.Join(dir, "cert.pem")); err == nil {
		return nil
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return err
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "127.0.0.1"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(7 * 24 * time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses: []net.IP{net.IPv4(127, 0, 0, 1)}, DNSNames: []string{"localhost"}, IsCA: true, BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return err
	}
	kb, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "key.pem"), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: kb}), 0o644); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "cert.pem"), pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o644)
}
