// Package regenv is the shared fixture of the registry and pipeline suites (tests/cache,
// tests/oci, tests/forge; R-REG, R-PIPE). It starts the all-in-one stack (tools/devstack)
// next to a live Forgejo (the only forge that starts in every harness run, HARNESS.md §8.2),
// registers the Forgejo integration, links projects to repositories, pushes fixture
// repositories and tags, and offers a Connect JSON client for the bob API.
//
// Assumptions about the stack's info line (the contract of HARNESS.md §13.2, same as the
// other server suites): `api_url` (first orchestrator, base URL), optional `api_urls` (all
// orchestrators), `admin_token` (an API token with the admin role), `s3_proxy_url`. While the
// server is a stub the line has no api_url and Up marks the calling test red.
package regenv

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"encoding/xml"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/megakuul/bob/tests/harness"
	"github.com/megakuul/bob/tests/serverenv/clients"
	"github.com/megakuul/bob/tests/serverenv/forgejo"
	"github.com/megakuul/bob/tests/serverenv/s3count"
	"github.com/megakuul/bob/tests/serverenv/s3sig"
)

// Env is a running stack with a Forgejo and one registered integration.
type Env struct {
	T           testing.TB
	API         string   // base URL of the first orchestrator
	APIs        []string // base URLs of all orchestrators
	Admin       string   // admin API token
	Proxy       string   // counting S3 proxy URL
	Forgejo     *forgejo.Instance
	Integration string // id of the Forgejo integration
	HookSecret  string // its webhook secret (returned once by CreateForgeIntegration)
	HTTP        *http.Client
	Info        map[string]any
	dir         string
}

// RPCError is a non-2xx Connect answer.
type RPCError struct {
	HTTP    int
	Code    string // Connect code, e.g. "unauthenticated", "already_exists", "failed_precondition"
	Message string
}

func (e *RPCError) Error() string {
	return fmt.Sprintf("rpc: HTTP %d %s: %s", e.HTTP, e.Code, e.Message)
}

// Up starts the stack with n orchestrators and the Forgejo. When the server is still a stub it
// fails t as red ("not implemented") and returns nil; the caller returns.
func Up(t *testing.T, orchestrators int) *Env { return UpCA(t, orchestrators, nil) }

// UpCA is Up with extraCA (PEM) added to the certificate pool of the server processes, for
// tests whose servers call a test receiver over https (outgoing webhooks).
func UpCA(t *testing.T, orchestrators int, extraCA []byte) *Env {
	t.Helper()
	if r := harness.RunPath(t, harness.Bin(t, "bob-server"), harness.Spec{Timeout: time.Minute}, "orchestrator"); r.IsNotImplemented() {
		harness.RedFeature(t, "bob-server orchestrator: "+strings.TrimSpace(strings.SplitN(r.Combined(), "\n", 2)[0]))
		return nil
	}
	e := &Env{T: t, dir: t.TempDir(), Info: map[string]any{}}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	fj, err := forgejo.Start(ctx, forgejo.Options{Dir: filepath.Join(e.dir, "forgejo"), Seed: true})
	if err != nil {
		t.Fatalf("harness error: forgejo: %v", err)
	}
	t.Cleanup(func() { _ = fj.Stop() })
	e.Forgejo = fj
	e.startStack(orchestrators, extraCA)
	if e.API == "" {
		harness.RedFeature(t, "bob-server orchestrator (the devstack info line has no api_url/admin_token)")
		return nil
	}
	e.HTTP = &http.Client{Timeout: 5 * time.Minute}
	var resp struct {
		Integration   struct{ ID string }
		WebhookSecret string `json:"webhookSecret"`
	}
	e.MustCall(e.Admin, "bob.forge.v1.ForgeService/CreateForgeIntegration", map[string]any{"integration": map[string]any{
		"id": "forgejo-test", "kind": "FORGE_KIND_FORGEJO", "displayName": "Forgejo harness", "baseUrl": fj.URL,
		"forgejo": map[string]any{"botLogin": "bobbot", "botPassword": forgejo.UserPass("bobbot")},
	}}, &resp)
	e.Integration = resp.Integration.ID
	e.HookSecret = resp.WebhookSecret
	return e
}

func (e *Env) startStack(orchestrators int, extraCA []byte) {
	t := e.T
	root := harness.Root(t)
	bin := filepath.Join(e.dir, "devstack")
	build := exec.Command("go", "build", "-o", bin, "./tools/devstack")
	build.Dir = root
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("harness error: go build devstack: %v: %s", err, out)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Minute)
	cmd := exec.CommandContext(ctx, bin, "up", "-dir", filepath.Join(e.dir, "stack"), "-json", "-orchestrators", fmt.Sprint(orchestrators), "-workers", "2")
	cmd.Dir = root
	ca, err := os.ReadFile(e.Forgejo.CA)
	if err != nil {
		t.Fatalf("harness error: %v", err)
	}
	bundle := filepath.Join(e.dir, "ca.pem")
	if err := os.WriteFile(bundle, append(append(ca, '\n'), extraCA...), 0o644); err != nil {
		t.Fatalf("harness error: %v", err)
	}
	cmd.Env = append(os.Environ(), "SSL_CERT_FILE="+bundle) // the servers talk to Forgejo's self-signed TLS
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("harness error: devstack: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Signal(syscall.SIGTERM)
		done := make(chan struct{})
		go func() { _ = cmd.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(30 * time.Second):
			cancel()
			<-done
		}
		cancel()
	})
	line, err := bufio.NewReader(out).ReadString('\n')
	if err != nil {
		t.Fatalf("harness error: devstack printed no info line: %v", err)
	}
	if err := json.Unmarshal([]byte(line), &e.Info); err != nil {
		t.Fatalf("harness error: devstack info line: %v: %q", err, line)
	}
	e.API, _ = e.Info["api_url"].(string)
	e.Admin, _ = e.Info["admin_token"].(string)
	e.Proxy, _ = e.Info["s3_proxy_url"].(string)
	if e.Admin == "" {
		e.API = ""
		return
	}
	e.APIs = []string{e.API}
	if l, ok := e.Info["api_urls"].([]any); ok && len(l) > 0 {
		e.APIs = e.APIs[:0]
		for _, u := range l {
			s, _ := u.(string)
			e.APIs = append(e.APIs, s)
		}
	}
}

// Call posts a Connect JSON request (path /api/<method>). token may be empty (anonymous).
func (e *Env) Call(token, method string, req, resp any) *RPCError {
	return e.CallAt(e.API, token, method, req, resp)
}

// CallAt is Call against the orchestrator at base.
func (e *Env) CallAt(base, token, method string, req, resp any) *RPCError {
	e.T.Helper()
	if req == nil {
		req = map[string]any{}
	}
	b, err := json.Marshal(req)
	if err != nil {
		e.T.Fatal(err)
	}
	hr, err := http.NewRequest(http.MethodPost, base+"/api/"+method, bytes.NewReader(b))
	if err != nil {
		e.T.Fatal(err)
	}
	hr.Header.Set("Content-Type", "application/json")
	hr.Header.Set("Connect-Protocol-Version", "1")
	if token != "" {
		hr.Header.Set("Authorization", "Bearer "+token)
	}
	r, err := e.HTTP.Do(hr)
	if err != nil {
		e.T.Fatalf("rpc %s: %v", method, err)
	}
	defer r.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(r.Body, 64<<20))
	if r.StatusCode/100 != 2 {
		var ce struct{ Code, Message string }
		_ = json.Unmarshal(body, &ce)
		return &RPCError{HTTP: r.StatusCode, Code: ce.Code, Message: ce.Message}
	}
	if resp != nil {
		if err := json.Unmarshal(body, resp); err != nil {
			e.T.Fatalf("rpc %s: decode: %v: %s", method, err, body)
		}
	}
	return nil
}

// MustCall is Call that fails the test on an RPC error.
func (e *Env) MustCall(token, method string, req, resp any) {
	e.T.Helper()
	if err := e.Call(token, method, req, resp); err != nil {
		e.T.Fatalf("%s: %v", method, err)
	}
}

// Get performs an HTTP GET with an optional Basic credential (user "bob", password token) and
// returns status, headers and body.
func (e *Env) Get(url, token string, hdr map[string]string) (int, http.Header, []byte) {
	e.T.Helper()
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		e.T.Fatal(err)
	}
	if token != "" {
		req.SetBasicAuth("bob", token)
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	r, err := e.HTTP.Do(req)
	if err != nil {
		e.T.Fatalf("GET %s: %v", url, err)
	}
	defer r.Body.Close()
	b, _ := io.ReadAll(r.Body)
	return r.StatusCode, r.Header, b
}

// Project creates project id linked to alice/<repo> of the Forgejo.
func (e *Env) Project(id, repo string) {
	e.T.Helper()
	e.MustCall(e.Admin, "bob.project.v1.ProjectService/CreateProject", map[string]any{"project": map[string]any{
		"id": id, "displayName": id, "forgeIntegrationId": e.Integration, "repoFullName": "alice/" + repo,
	}}, nil)
}

// Principal creates a service account bound to project (read access to cache, registry and
// releases of that project only) and returns a token of the given scope ("API" or "DOWNLOAD").
func (e *Env) Principal(name, project, scope string) string {
	e.T.Helper()
	e.MustCall(e.Admin, "bob.access.v1.ServiceAccountService/CreateServiceAccount", map[string]any{"serviceAccount": map[string]any{"id": name}}, nil)
	role := "reader-" + name
	e.MustCall(e.Admin, "bob.access.v1.RoleService/CreateRole", map[string]any{"role": map[string]any{
		"id": role, "permissions": []map[string]string{
			{"method": "bob.cache.v1.CacheService/GetPath", "project": project},
			{"method": "bob.registry.v1.RegistryService/GetRepository", "project": project},
			{"method": "bob.release.v1.ReleaseService/GetRelease", "project": project},
		}}}, nil)
	e.MustCall(e.Admin, "bob.access.v1.RoleService/CreateRoleBinding", map[string]any{
		"principalId": "sa:" + name, "role": role, "project": project}, nil)
	var tok struct{ Secret string }
	e.MustCall(e.Admin, "bob.access.v1.ServiceAccountService/CreateServiceAccountToken", map[string]any{
		"serviceAccountId": name, "name": "t", "scope": "TOKEN_SCOPE_" + scope, "ttlDays": 1}, &tok)
	return tok.Secret
}

// CLIDownloadToken creates the local user login and returns the bob_dt_ download token of a CLI
// login (SPEC §6.3.1: the CLI session's download token, answered as bob_download_token by
// /oauth/token): LoginPassword gives the browser session, the device flow (§6.3.2) is approved
// with it, and the first poll of /oauth/token redeems the device code.
func (e *Env) CLIDownloadToken(login string) string {
	e.T.Helper()
	password := "pw-" + login + "-harness-secret"
	e.MustCall(e.Admin, "bob.access.v1.UserService/CreateUser", map[string]any{
		"user": map[string]any{"login": login}, "initialPassword": password}, nil)
	post := func(path, contentType string, body []byte, cookie *http.Cookie) (*http.Response, []byte) {
		req, err := http.NewRequest(http.MethodPost, e.API+path, bytes.NewReader(body))
		if err != nil {
			e.T.Fatal(err)
		}
		req.Header.Set("Content-Type", contentType)
		if contentType == "application/json" {
			req.Header.Set("Connect-Protocol-Version", "1")
		}
		if cookie != nil {
			req.AddCookie(cookie)
		}
		r, err := e.HTTP.Do(req)
		if err != nil {
			e.T.Fatalf("harness error: POST %s: %v", path, err)
		}
		defer r.Body.Close()
		b, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		return r, b
	}
	b, _ := json.Marshal(map[string]string{"login": login, "password": password})
	r, body := post("/api/bob.auth.v1.AuthService/LoginPassword", "application/json", b, nil)
	var session *http.Cookie
	for _, c := range r.Cookies() {
		if c.Name == "bob_session" {
			session = c
		}
	}
	if r.StatusCode != http.StatusOK || session == nil {
		e.T.Fatalf("harness error: LoginPassword %s: HTTP %d, cookie %v: %s", login, r.StatusCode, session != nil, body)
	}
	form := "application/x-www-form-urlencoded"
	_, body = post("/oauth/device/code", form, []byte(url.Values{"client_id": {"bob-cli"}}.Encode()), nil)
	var dc struct {
		DeviceCode string `json:"device_code"`
		UserCode   string `json:"user_code"`
	}
	if err := json.Unmarshal(body, &dc); err != nil || dc.DeviceCode == "" || dc.UserCode == "" {
		e.T.Fatalf("harness error: /oauth/device/code: %v: %s", err, body)
	}
	if r, body := post("/device", form, []byte(url.Values{"user_code": {dc.UserCode}, "decision": {"allow"}}.Encode()), session); r.StatusCode != http.StatusOK {
		e.T.Fatalf("harness error: approving the device code: HTTP %d: %.200s", r.StatusCode, body)
	}
	_, body = post("/oauth/token", form, []byte(url.Values{"grant_type": {"urn:ietf:params:oauth:grant-type:device_code"},
		"device_code": {dc.DeviceCode}, "client_id": {"bob-cli"}}.Encode()), nil)
	var tok struct {
		Download string `json:"bob_download_token"`
	}
	if err := json.Unmarshal(body, &tok); err != nil || !strings.HasPrefix(tok.Download, "bob_dt_") {
		e.T.Fatalf("harness error: /oauth/token (device grant) gave no bob_dt_ download token: %v", err)
	}
	return tok.Download
}

// PublicKeys returns the server's trusted public keys ("name:base64"), from the public
// ListSigningKeys.
func (e *Env) PublicKeys() []string {
	e.T.Helper()
	var r struct{ Keys []struct{ PublicKey string } }
	e.MustCall("", "bob.settings.v1.SettingsService/ListSigningKeys", nil, &r)
	var out []string
	for _, k := range r.Keys {
		out = append(out, k.PublicKey)
	}
	return out
}

// git runs git with the harness CA trusted.
func (e *Env) git(dir string, args ...string) string {
	e.T.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_SSL_CAINFO="+e.Forgejo.CA, "GIT_TERMINAL_PROMPT=0",
		"GIT_AUTHOR_NAME=alice", "GIT_AUTHOR_EMAIL=alice@harness.local", "GIT_COMMITTER_NAME=alice", "GIT_COMMITTER_EMAIL=alice@harness.local")
	out, err := cmd.CombinedOutput()
	if err != nil {
		e.T.Fatalf("harness error: git %s: %v: %s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

// Push force-pushes the committed fixture checkout dir (harness.Fixture) to alice/<repo>
// as refs/heads/<branch> and returns the commit SHA.
func (e *Env) Push(dir, repo, branch string) string {
	e.T.Helper()
	url := strings.Replace(e.Forgejo.URL, "https://", "https://alice:"+e.Forgejo.Seeded.OwnerToken+"@", 1) + "/alice/" + repo + ".git"
	e.git(dir, "push", "--force", url, "HEAD:refs/heads/"+branch)
	return e.git(dir, "rev-parse", "HEAD")
}

// Tag creates the annotated tag name on HEAD of dir and pushes it to alice/<repo>.
func (e *Env) Tag(dir, repo, name string) {
	e.T.Helper()
	url := strings.Replace(e.Forgejo.URL, "https://", "https://alice:"+e.Forgejo.Seeded.OwnerToken+"@", 1) + "/alice/" + repo + ".git"
	e.git(dir, "tag", "-a", "-m", name, name)
	e.git(dir, "push", url, "refs/tags/"+name)
}

// Release is the subset of release.v1.Release the suites read.
type Release struct {
	ID         string `json:"id"`
	ProjectID  string `json:"projectId"`
	Name       string `json:"name"`
	Version    string `json:"version"`
	Visibility string `json:"visibility"`
	State      string `json:"state"`
	Closures   []struct {
		Platform string
		Roots    []string
	} `json:"closures"`
	Assets []struct {
		File, Sha256, URL string
		Size              any
	} `json:"assets"`
	Images []struct{ Repository, Tag, Digest string } `json:"images"`
}

// WaitRelease polls until release project/name@version is published.
func (e *Env) WaitRelease(project, name, version string, timeout time.Duration) Release {
	e.T.Helper()
	deadline := time.Now().Add(timeout)
	for {
		var r struct{ Releases []Release }
		e.MustCall(e.Admin, "bob.release.v1.ReleaseService/ListReleases", map[string]any{"projectId": project, "name": name}, &r)
		for _, rel := range r.Releases {
			if rel.Version == version && rel.State == "RELEASE_STATE_PUBLISHED" {
				return rel
			}
		}
		if time.Now().After(deadline) {
			e.T.Fatalf("release %s/%s@%s not published within %v", project, name, version, timeout)
		}
		time.Sleep(time.Second)
	}
}

// Publish pushes the fixture to alice/<repo> (link project `project` first with Project) and
// tags `v<version>`; it returns the published release `name`.
func (e *Env) Publish(project, repo, fixture, name, version string) (Release, string) {
	e.T.Helper()
	dir := harness.Fixture(e.T, fixture)
	e.Push(dir, repo, "main")
	e.Tag(dir, repo, "v"+version)
	return e.WaitRelease(project, name, version, 20*time.Minute), dir
}

// SetVisibility changes the visibility of release id ("PUBLIC" or "PRIVATE").
func (e *Env) SetVisibility(id, vis string) {
	e.T.Helper()
	e.MustCall(e.Admin, "bob.release.v1.ReleaseService/UpdateRelease", map[string]any{
		"release":    map[string]any{"id": id, "visibility": "VISIBILITY_" + vis},
		"updateMask": "visibility"}, nil)
}

// StoreHash returns the 32-character hash of /nix/store/<hash>-<name>.
func StoreHash(path string) string {
	return strings.SplitN(strings.TrimPrefix(path, "/nix/store/"), "-", 2)[0]
}

// ResetS3 zeroes the S3 request counters.
func (e *Env) ResetS3() {
	e.T.Helper()
	req, _ := http.NewRequest(http.MethodDelete, e.Proxy+"/_s3count", nil)
	r, err := e.HTTP.Do(req)
	if err != nil {
		e.T.Fatalf("s3count reset: %v", err)
	}
	_ = r.Body.Close()
}

// S3 returns the counters.
func (e *Env) S3() s3count.Counts {
	e.T.Helper()
	c, err := s3count.Fetch(context.Background(), e.Proxy)
	if err != nil {
		e.T.Fatalf("s3count: %v", err)
	}
	return c
}

// AssertNoList ends a server e2e test (HARNESS.md §8.1).
func (e *Env) AssertNoList() {
	e.T.Helper()
	s3count.AssertNoList(context.Background(), e.T, e.Proxy, "bob-control", "bob-fleet")
}

// Netrc writes a netrc for the nix client giving token to the host of url and returns its path.
func Netrc(t testing.TB, c *clients.Client, url, token string) string {
	t.Helper()
	host := strings.SplitN(strings.SplitN(strings.TrimPrefix(strings.TrimPrefix(url, "https://"), "http://"), "/", 2)[0], ":", 2)[0]
	p := filepath.Join(c.Dir("netrc-"+fmt.Sprint(time.Now().UnixNano())), "netrc")
	if err := os.WriteFile(p, []byte("machine "+host+" login bob password "+token+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	c.Own(p)
	return p
}

// S3Creds are the stack's S3 identities: the root credentials (always in the info line) and,
// when the info line publishes them (`worker_access_key`, `worker_secret_key`), the worker's.
func (e *Env) S3Creds(role string) s3sig.Creds {
	e.T.Helper()
	region, _ := e.Info["region"].(string)
	key := func(k string) string { s, _ := e.Info[k].(string); return s }
	switch role {
	case "root":
		return s3sig.Creds{AccessKey: key("access_key"), SecretKey: key("secret_key"), Region: region}
	case "worker":
		if key("worker_access_key") == "" {
			harness.RedFeature(e.T, "devstack info line without worker_access_key/worker_secret_key (shipped worker policy)")
			e.T.FailNow()
		}
		return s3sig.Creds{AccessKey: key("worker_access_key"), SecretKey: key("worker_secret_key"), Region: region}
	}
	e.T.Fatalf("unknown S3 role %q", role)
	return s3sig.Creds{}
}

// S3Do sends one signed S3 request straight to the backend (not through the counting proxy)
// and returns status and body.
func (e *Env) S3Do(c s3sig.Creds, method, bucket, key string, body []byte) (int, []byte) {
	e.T.Helper()
	base, _ := e.Info["s3_url"].(string)
	req, err := http.NewRequest(method, base+"/"+bucket+"/"+key, bytes.NewReader(body))
	if err != nil {
		e.T.Fatal(err)
	}
	sum := sha256.Sum256(body)
	s3sig.Sign(req, c, hex.EncodeToString(sum[:]), time.Now())
	r, err := e.HTTP.Do(req)
	if err != nil {
		e.T.Fatalf("s3 %s %s/%s: %v", method, bucket, key, err)
	}
	defer r.Body.Close()
	b, _ := io.ReadAll(r.Body)
	return r.StatusCode, b
}

// SetFaults installs a fault script in the counting proxy (an empty script clears it).
func (e *Env) SetFaults(s s3count.Script) {
	e.T.Helper()
	b, err := json.Marshal(s)
	if err != nil {
		e.T.Fatal(err)
	}
	req, _ := http.NewRequest(http.MethodPut, e.Proxy+"/_s3count/faults", bytes.NewReader(b))
	r, err := e.HTTP.Do(req)
	if err != nil {
		e.T.Fatalf("s3count faults: %v", err)
	}
	_ = r.Body.Close()
	if r.StatusCode/100 != 2 {
		e.T.Fatalf("s3count faults: HTTP %d", r.StatusCode)
	}
}

// DelayStaging delays every orchestrator read of staging/ by d, which opens a window between
// the worker's upload and the promotion in which a test can tamper with staged objects.
func (e *Env) DelayStaging(d time.Duration) {
	e.T.Helper()
	e.SetFaults(s3count.Script{Seed: 1, Faults: []s3count.Fault{{
		Kind: s3count.FaultLatency, Op: s3count.OpGet, Bucket: "bob-artifacts", KeyPrefix: "staging/", Delay: d}}})
}

// S3List lists the keys below prefix of the artifacts bucket, straight on the backend (not
// counted by the proxy).
func (e *Env) S3List(prefix string) []string {
	e.T.Helper()
	base, _ := e.Info["s3_url"].(string)
	req, err := http.NewRequest(http.MethodGet, base+"/bob-artifacts?list-type=2&prefix="+prefix, nil)
	if err != nil {
		e.T.Fatal(err)
	}
	s3sig.Sign(req, e.S3Creds("root"), s3sig.UnsignedPayload, time.Now())
	r, err := e.HTTP.Do(req)
	if err != nil {
		e.T.Fatalf("s3 list: %v", err)
	}
	defer r.Body.Close()
	var out struct {
		Contents []struct{ Key string }
	}
	if err := xml.NewDecoder(r.Body).Decode(&out); err != nil {
		e.T.Fatalf("s3 list: %v", err)
	}
	keys := make([]string, 0, len(out.Contents))
	for _, c := range out.Contents {
		keys = append(keys, c.Key)
	}
	return keys
}

// Job is the subset of build.v1.Job the suites read.
type Job struct {
	ID              string   `json:"id"`
	BuildID         string   `json:"buildId"`
	State           string   `json:"state"`
	WorkerID        string   `json:"workerId"`
	Promotion       string   `json:"promotion"`
	NonReproducible []string `json:"nonReproducible"`
}

// Build is the subset of build.v1.Build the suites read.
type Build struct {
	ID      string `json:"id"`
	Number  any    `json:"number"`
	State   string `json:"state"`
	Trigger struct {
		Kind         string `json:"kind"`
		SupersededBy string `json:"supersededBy"`
		RetryOf      string `json:"retryOf"`
		RerunOf      string `json:"rerunOf"`
	} `json:"trigger"`
	Pipelines []struct{ Name, State, ReleaseID string } `json:"pipelines"`
	Errors    []struct{ Code, Message string }          `json:"errors"`
	Counts    struct {
		Total, Cached, Built, Failed, Skipped int
	} `json:"counts"`
	Attempt any `json:"attempt"`
}

// Builds lists the builds of project (newest first).
func (e *Env) Builds(project string) []Build {
	e.T.Helper()
	var r struct{ Builds []Build }
	e.MustCall(e.Admin, "bob.build.v1.BuildService/ListBuilds", map[string]any{"projectId": project}, &r)
	return r.Builds
}

// WaitBuilds polls until project has at least n builds and returns them.
func (e *Env) WaitBuilds(project string, n int, timeout time.Duration) []Build {
	e.T.Helper()
	for deadline := time.Now().Add(timeout); ; time.Sleep(500 * time.Millisecond) {
		if b := e.Builds(project); len(b) >= n {
			return b
		}
		if time.Now().After(deadline) {
			e.T.Fatalf("project %s has fewer than %d builds after %v", project, n, timeout)
		}
	}
}

// WaitBuild polls until the build satisfies done and returns it.
func (e *Env) WaitBuild(id string, timeout time.Duration, done func(Build) bool) Build {
	e.T.Helper()
	for deadline := time.Now().Add(timeout); ; time.Sleep(500 * time.Millisecond) {
		var r struct{ Build Build }
		e.MustCall(e.Admin, "bob.build.v1.BuildService/GetBuild", map[string]any{"id": id}, &r)
		if done(r.Build) {
			return r.Build
		}
		if time.Now().After(deadline) {
			e.T.Fatalf("build %s did not reach the awaited state within %v (state %s)", id, timeout, r.Build.State)
		}
	}
}

// Terminal reports whether b is finished.
func Terminal(b Build) bool {
	switch b.State {
	case "BUILD_STATE_SUCCEEDED", "BUILD_STATE_FAILED", "BUILD_STATE_CANCELLED", "BUILD_STATE_SUPERSEDED", "BUILD_STATE_SKIPPED":
		return true
	}
	return false
}

// Jobs lists the jobs of a build.
func (e *Env) Jobs(buildID string) []Job {
	e.T.Helper()
	var r struct{ Jobs []Job }
	e.MustCall(e.Admin, "bob.build.v1.JobService/ListJobs", map[string]any{"buildId": buildID}, &r)
	return r.Jobs
}

// JobAttempts is a job with its attempt number.
type JobAttempts struct {
	ID      string `json:"id"`
	State   string `json:"state"`
	Attempt int    `json:"attempt"`
}

// JobsAttempts lists the jobs of a build with their attempt numbers.
func (e *Env) JobsAttempts(buildID string) []JobAttempts {
	e.T.Helper()
	var r struct{ Jobs []JobAttempts }
	e.MustCall(e.Admin, "bob.build.v1.JobService/ListJobs", map[string]any{"buildId": buildID}, &r)
	return r.Jobs
}

// OrchestratorLogs returns the concatenated log files of all orchestrators (index i: log i).
func (e *Env) OrchestratorLogs() []string {
	e.T.Helper()
	procs, _ := e.Info["orchestrators"].([]any)
	var out []string
	for _, p := range procs {
		m, _ := p.(map[string]any)
		path, _ := m["log"].(string)
		b, err := os.ReadFile(path)
		if err != nil {
			e.T.Fatalf("harness error: orchestrator log: %v", err)
		}
		out = append(out, string(b))
	}
	return out
}

// KillOrchestrator sends SIGKILL to the process group of orchestrator i.
func (e *Env) KillOrchestrator(i int) {
	e.T.Helper()
	procs, _ := e.Info["orchestrators"].([]any)
	m, _ := procs[i].(map[string]any)
	pid, _ := m["pid"].(float64)
	if err := syscall.Kill(-int(pid), syscall.SIGKILL); err != nil {
		e.T.Fatalf("harness error: kill orchestrator %d: %v", i, err)
	}
}

// OrchestratorPID returns the pid of orchestrator i.
func (e *Env) OrchestratorPID(i int) int {
	procs, _ := e.Info["orchestrators"].([]any)
	m, _ := procs[i].(map[string]any)
	pid, _ := m["pid"].(float64)
	return int(pid)
}

// LoopbackCert makes a self-signed https certificate for 127.0.0.1 and returns it with its PEM.
func LoopbackCert() (tls.Certificate, []byte, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "bob-harness-receiver"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses: []net.IP{net.IPv4(127, 0, 0, 1)}, IsCA: true, BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return tls.Certificate{}, nil, err
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), nil
}

// Loopback is LoopbackCert that fails the test on error (pass the PEM to UpCA so the servers
// trust the receiver).
func Loopback(t testing.TB) (tls.Certificate, []byte) {
	t.Helper()
	c, p, err := LoopbackCert()
	if err != nil {
		t.Fatal(err)
	}
	return c, p
}

// Status is one commit status of the Forgejo.
type Status struct {
	Context     string `json:"context"`
	State       string `json:"status"` // pending success failure error warning
	Description string `json:"description"`
	TargetURL   string `json:"target_url"`
}

// Statuses lists the commit statuses of alice/<repo> at sha, oldest first.
func (e *Env) Statuses(repo, sha string) []Status {
	e.T.Helper()
	var out []Status
	err := e.Forgejo.API(context.Background(), http.MethodGet, "/api/v1/repos/alice/"+repo+"/commits/"+sha+"/statuses?sort=oldest&limit=100",
		forgejo.Auth{Token: e.Forgejo.Seeded.OwnerToken}, nil, &out)
	if err != nil {
		e.T.Fatalf("harness error: %v", err)
	}
	return out
}

// HookURL is the webhook intake URL of the registered integration.
func (e *Env) HookURL() string { return e.API + "/hooks/" + e.Integration }

// ForgejoDelivery signs body for the integration (HMAC-SHA256 with HookSecret) and posts it as a
// Forgejo delivery of the given event with delivery id; it returns status and body.
func (e *Env) ForgejoDelivery(event, id string, body []byte) (int, string) {
	e.T.Helper()
	mac := hmac.New(sha256.New, []byte(e.HookSecret))
	mac.Write(body)
	req, err := http.NewRequest(http.MethodPost, e.HookURL(), bytes.NewReader(body))
	if err != nil {
		e.T.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Forgejo-Event", event)
	req.Header.Set("X-Forgejo-Event-Type", event)
	req.Header.Set("X-Forgejo-Delivery", id)
	req.Header.Set("X-Forgejo-Signature", hex.EncodeToString(mac.Sum(nil)))
	r, err := e.HTTP.Do(req)
	if err != nil {
		e.T.Fatalf("POST %s: %v", e.HookURL(), err)
	}
	defer r.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	return r.StatusCode, strings.TrimSpace(string(b))
}

// CommitAppend appends text to file rel in the checkout dir and commits it; it returns the new
// commit SHA (push it with Push).
func (e *Env) CommitAppend(dir, rel, text string) string {
	e.T.Helper()
	f := filepath.Join(dir, rel)
	b, err := os.ReadFile(f)
	if err != nil {
		e.T.Fatalf("harness error: %v", err)
	}
	if err := os.WriteFile(f, append(b, []byte(text)...), 0o644); err != nil {
		e.T.Fatalf("harness error: %v", err)
	}
	e.git(dir, "add", "-A")
	e.git(dir, "-c", "user.name=alice", "-c", "user.email=alice@harness.local", "commit", "-qm", "edit "+rel)
	return e.git(dir, "rev-parse", "HEAD")
}

// RepoID returns the Forgejo repository id of alice/<repo>.
func (e *Env) RepoID(repo string) int64 {
	e.T.Helper()
	var r struct{ ID int64 }
	if err := e.Forgejo.API(context.Background(), http.MethodGet, "/api/v1/repos/alice/"+repo, forgejo.Auth{Token: e.Forgejo.Seeded.OwnerToken}, nil, &r); err != nil {
		e.T.Fatalf("harness error: %v", err)
	}
	return r.ID
}
