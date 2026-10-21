// Package forgejo starts a local Forgejo 16 from the nixpkgs pin (sqlite, TLS,
// loopback) for the forge-forgejo-live suite (HARNESS.md §8.2). It ports spike
// S16's setup-forgejo.sh: admin user, the users alice / bobbot / mallory, the
// private repositories alice/demo and alice/other, and the tokens the live
// tests use.
package forgejo

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/megakuul/bob/tests/serverenv"
)

// Fixed test identities. They exist only inside a throwaway instance.
const (
	AdminUser = "bobadmin"
	AdminPass = "Harness-admin-pass!"
)

// UserPass returns the password of a seeded user.
func UserPass(user string) string { return "Harness-" + user + "-pass!" }

// Options configure Start.
type Options struct {
	Dir  string // state directory; required
	Port int    // 0 picks a free port in the harness range
	Seed bool   // create users, repositories and tokens (Instance.Seeded)
}

// Seeded is what Seed creates.
type Seeded struct {
	OwnerToken  string // alice: write:repository, write:user, write:issue
	StatusToken string // bobbot: write:repository, scoped to alice/demo
	HookSecret  string
}

// Instance is a running Forgejo.
type Instance struct {
	URL    string // https://127.0.0.1:<port>
	CA     string // PEM file of the instance's self-signed CA
	Dir    string
	Client *http.Client // trusts CA
	Seeded Seeded
	proc   *serverenv.Process
}

// Start realises Forgejo, configures and migrates it, and waits until the API
// answers.
func Start(ctx context.Context, o Options) (*Instance, error) {
	if o.Dir == "" {
		return nil, errors.New("forgejo: Options.Dir is required")
	}
	pkg, err := serverenv.NixTool(ctx, "forgejo")
	if err != nil {
		return nil, err
	}
	git, err := serverenv.NixTool(ctx, "git")
	if err != nil {
		return nil, err
	}
	port := o.Port
	if port == 0 {
		if port, err = serverenv.FreePort(); err != nil {
			return nil, err
		}
	}
	dir, err := filepath.Abs(o.Dir)
	if err != nil {
		return nil, fmt.Errorf("forgejo: %w", err)
	}
	bin := filepath.Join(pkg, "bin", "forgejo")
	env := append(os.Environ(), "PATH="+filepath.Join(git, "bin")+":/usr/bin:/bin", "HOME="+dir, "GITEA_WORK_DIR="+dir)
	run := func(args ...string) ([]byte, error) {
		cmd := exec.CommandContext(ctx, bin, args...)
		cmd.Dir, cmd.Env = dir, env
		out, err := cmd.CombinedOutput()
		if err != nil {
			return out, fmt.Errorf("forgejo %s: %w: %s", args[0], err, out)
		}
		return out, nil
	}
	tlsDir := filepath.Join(dir, "tls")
	conf := filepath.Join(dir, "custom", "conf", "app.ini")
	for _, d := range []string{tlsDir, filepath.Dir(conf), filepath.Join(dir, "data")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return nil, fmt.Errorf("forgejo: %w", err)
		}
	}
	cert := filepath.Join(tlsDir, "cert.pem")
	if _, err := os.Stat(cert); err != nil {
		cmd := exec.CommandContext(ctx, bin, "cert", "--host", "127.0.0.1,localhost", "--ca")
		cmd.Dir, cmd.Env = tlsDir, env
		if out, err := cmd.CombinedOutput(); err != nil {
			return nil, fmt.Errorf("forgejo cert: %w: %s: %w", err, out, serverenv.ErrHarness)
		}
	}
	secrets := map[string]string{}
	for _, k := range []string{"SECRET_KEY", "INTERNAL_TOKEN", "JWT_SECRET"} {
		out, err := run("generate", "secret", k)
		if err != nil {
			return nil, fmt.Errorf("%w: %w", err, serverenv.ErrHarness)
		}
		secrets[k] = strings.TrimSpace(string(out))
	}
	if err := os.WriteFile(conf, []byte(appINI(dir, port, secrets)), 0o600); err != nil {
		return nil, fmt.Errorf("forgejo: %w", err)
	}
	cfg := []string{"--config", conf, "--work-path", dir}
	if _, err := run(append([]string{"migrate"}, cfg...)...); err != nil {
		return nil, fmt.Errorf("%w: %w", err, serverenv.ErrHarness)
	}
	// "admin user create" fails when the user exists (a reused Dir); that is fine.
	_, _ = run(append([]string{"admin", "user", "create"}, append(cfg, "--admin", "--username", AdminUser,
		"--password", AdminPass, "--email", "admin@harness.local", "--must-change-password=false")...)...)

	cmd := exec.Command(bin, append([]string{"web"}, cfg...)...)
	cmd.Dir, cmd.Env = dir, env
	proc, err := serverenv.StartProcess(cmd, filepath.Join(dir, "forgejo.log"))
	if err != nil {
		return nil, err
	}
	pem, err := os.ReadFile(cert)
	if err != nil {
		return nil, errors.Join(fmt.Errorf("forgejo: %w", err), proc.Stop(5*time.Second))
	}
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(pem)
	in := &Instance{
		URL: "https://127.0.0.1:" + strconv.Itoa(port), CA: cert, Dir: dir, proc: proc,
		Client: &http.Client{Timeout: 30 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool}}},
	}
	if err := in.waitReady(ctx); err != nil {
		return nil, errors.Join(err, in.Stop())
	}
	if o.Seed {
		if err := in.seed(ctx); err != nil {
			return nil, errors.Join(err, in.Stop())
		}
	}
	return in, nil
}

func appINI(dir string, port int, s map[string]string) string {
	return fmt.Sprintf(`I_AM_BEING_UNSAFE_RUNNING_AS_ROOT = true
APP_NAME = bob-harness
RUN_MODE = prod
WORK_PATH = %[1]s
[server]
PROTOCOL = https
CERT_FILE = %[1]s/tls/cert.pem
KEY_FILE = %[1]s/tls/key.pem
HTTP_ADDR = 127.0.0.1
HTTP_PORT = %[2]d
ROOT_URL = https://127.0.0.1:%[2]d/
DISABLE_SSH = true
OFFLINE_MODE = true
LFS_START_SERVER = false
[database]
DB_TYPE = sqlite3
PATH = %[1]s/data/forgejo.db
[repository]
ROOT = %[1]s/repos
[security]
INSTALL_LOCK = true
SECRET_KEY = %[3]s
INTERNAL_TOKEN = %[4]s
[oauth2]
JWT_SECRET = %[5]s
[service]
DISABLE_REGISTRATION = true
[webhook]
ALLOWED_HOST_LIST = loopback
DELIVER_TIMEOUT = 5
[log]
MODE = console
LEVEL = Info
[actions]
ENABLED = false
[queue]
TYPE = level
`, dir, port, s["SECRET_KEY"], s["INTERNAL_TOKEN"], s["JWT_SECRET"])
}

func (in *Instance) waitReady(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	var last error
	for {
		var v struct{ Version string }
		if last = in.API(ctx, http.MethodGet, "/api/v1/version", Auth{}, nil, &v); last == nil {
			return nil
		}
		select {
		case <-in.proc.Exited():
			return fmt.Errorf("forgejo: exited during start-up (%v), see %s: %w", in.proc.Err(), in.proc.Log, serverenv.ErrHarness)
		case <-ctx.Done():
			return fmt.Errorf("forgejo: not ready on %s: %v: %w", in.URL, last, serverenv.ErrHarness)
		case <-time.After(100 * time.Millisecond):
		}
	}
}

// Auth is basic auth (User, Pass) or a token.
type Auth struct{ User, Pass, Token string }

// Admin is the admin's basic auth.
func Admin() Auth { return Auth{User: AdminUser, Pass: AdminPass} }

// API calls the Forgejo API. in and out are JSON (either may be nil). A non-2xx
// status is an error that carries the response body.
func (in *Instance) API(ctx context.Context, method, path string, a Auth, body, out any) error {
	var rd io.Reader = http.NoBody
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("forgejo: %w", err)
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, in.URL+path, rd)
	if err != nil {
		return fmt.Errorf("forgejo: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	switch {
	case a.Token != "":
		req.Header.Set("Authorization", "token "+a.Token)
	case a.User != "":
		req.SetBasicAuth(a.User, a.Pass)
	}
	resp, err := in.Client.Do(req)
	if err != nil {
		return fmt.Errorf("forgejo: %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return fmt.Errorf("forgejo: %s %s: %w", method, path, err)
	}
	if resp.StatusCode/100 != 2 {
		return &StatusError{Code: resp.StatusCode, Method: method, Path: path, Body: string(b)}
	}
	if out != nil && len(b) > 0 {
		if err := json.Unmarshal(b, out); err != nil {
			return fmt.Errorf("forgejo: %s %s: decode: %w", method, path, err)
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
	return fmt.Sprintf("forgejo: %s %s: %d: %s", e.Method, e.Path, e.Code, e.Body)
}

// seed mirrors S16's setup-forgejo.sh.
func (in *Instance) seed(ctx context.Context) error {
	for _, u := range []string{"alice", "bobbot", "mallory"} {
		err := in.API(ctx, http.MethodPost, "/api/v1/admin/users", Admin(), map[string]any{
			"username": u, "email": u + "@harness.local", "password": UserPass(u), "must_change_password": false,
		}, nil)
		if err != nil {
			return err
		}
	}
	alice := Auth{User: "alice", Pass: UserPass("alice")}
	var tok struct {
		SHA1 string `json:"sha1"`
	}
	if err := in.API(ctx, http.MethodPost, "/api/v1/users/alice/tokens", alice, map[string]any{
		"name": "alice-admin", "scopes": []string{"write:repository", "write:user", "write:issue"},
	}, &tok); err != nil {
		return err
	}
	in.Seeded.OwnerToken = tok.SHA1
	owner := Auth{Token: tok.SHA1}
	for _, r := range []string{"demo", "other"} {
		if err := in.API(ctx, http.MethodPost, "/api/v1/user/repos", owner, map[string]any{
			"name": r, "private": true, "auto_init": true, "default_branch": "main",
		}, nil); err != nil {
			return err
		}
	}
	for repo, perm := range map[string]string{"demo": "write", "other": "read"} {
		if err := in.API(ctx, http.MethodPut, "/api/v1/repos/alice/"+repo+"/collaborators/bobbot", owner,
			map[string]any{"permission": perm}, nil); err != nil {
			return err
		}
	}
	if err := in.API(ctx, http.MethodPost, "/api/v1/users/bobbot/tokens", Auth{User: "bobbot", Pass: UserPass("bobbot")}, map[string]any{
		"name": "bob-status", "scopes": []string{"write:repository"},
		"repositories": []map[string]string{{"owner": "alice", "name": "demo"}},
	}, &tok); err != nil {
		return err
	}
	in.Seeded.StatusToken = tok.SHA1
	secret := make([]byte, 18)
	if _, err := rand.Read(secret); err != nil {
		return fmt.Errorf("forgejo: %w", err)
	}
	in.Seeded.HookSecret = hex.EncodeToString(secret)
	return nil
}

// Pid is Forgejo's process id (for the chaos driver).
func (in *Instance) Pid() int { return in.proc.Pid() }

// Stop ends Forgejo and waits for it.
func (in *Instance) Stop() error {
	if err := in.proc.Stop(5 * time.Second); err != nil {
		return fmt.Errorf("forgejo: %w", err)
	}
	return nil
}
