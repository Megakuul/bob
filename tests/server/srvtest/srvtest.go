// Package srvtest is the shared driver of the server suites (tests/server,
// tests/chaos, tests/soak): it starts the all-in-one stack (tests/serverenv/stack),
// talks to the orchestrators with the public Connect API (JSON), submits remote
// builds with the bob CLI and reads the S3 counters of the counting proxy.
//
// Everything that needs the server returns "red: not implemented" while the
// bob-server modes are stubs: Start reports it through harness.RedFeature and
// returns nil, and the caller returns.
//
// Contract with the stack (tests/serverenv/stack, driven by tools/devstack; documented in
// HARNESS.md §13.2). The devstack.json written by stack.Start carries, next to stack.Info:
//   - api_url, api_urls, admin_token   (first / all orchestrators, bootstrap admin token)
//   - orchestrators[i].api_url         (one API endpoint per orchestrator)
//   - attach_proxy_url                 (TCP front of the orchestrators, one per orchestrator name:
//     POST /block, /unblock [?orch=i]; GET /bytes; GET /streams; POST /redirect?orch=i&to=j)
//   - policies_applied, worker_access_key, worker_secret_key, orchestrator_access_key,
//     orchestrator_secret_key          (shipped S3 policies applied; processes use the role keys)
//
// Environment the stack sets on the bob-server processes: BOB_LISTEN and BOB_ADMIN_TOKEN
// (orchestrators), BOB_ORCHESTRATOR_ADDRS (workers: the attach proxy fronts, never the
// orchestrators directly), BOB_STORAGE_WORKER_PROBE_CREDENTIAL=<access>:<secret> (orchestrators,
// unless the test sets it), BOB_TEST_CLOCK_OFFSET (orchestrators, only with
// stack.Options.ClockOffset, honoured by -tags bobtest builds). Options.Env entries
// (BOB_FLEET_*, BOB_LOG_*, BOB_WORKER_SLOTS, ...) reach every process through the test
// environment.
package srvtest

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/megakuul/bob/tests/harness"
	"github.com/megakuul/bob/tests/serverenv"
	"github.com/megakuul/bob/tests/serverenv/s3count"
	"github.com/megakuul/bob/tests/serverenv/stack"
)

// Options configure Start.
type Options struct {
	Orchestrators int               // default 1
	Workers       int               // default 1
	Env           map[string]string // BOB_<UPPER_SNAKE> settings for every server process
}

// Env is a running stack plus the clients that talk to it.
type Env struct {
	T     *testing.T
	Stack *stack.Stack
	Dir   string
	Token string

	apis      []string
	attach    string
	home      []string
	hc        *http.Client
	via       int
	server    string
	restarted map[string]*serverenv.Process
}

// Start brings the stack up. It returns nil after marking t red when the server
// is not implemented yet. A server process that dies for any other reason fails t.
func Start(t *testing.T, o Options) *Env {
	t.Helper()
	for k, v := range o.Env {
		t.Setenv(k, v)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	dir := t.TempDir()
	s, err := stack.Start(ctx, stack.Options{Dir: dir, Orchestrators: o.Orchestrators, Workers: o.Workers})
	if err != nil {
		t.Fatalf("harness error: devstack: %v", err)
	}
	t.Cleanup(func() {
		if err := s.Stop(); err != nil {
			t.Logf("devstack stop: %v", err)
		}
	})
	procs := append(append([]stack.Proc{}, s.Info.Orchestrators...), s.Info.Workers...)
	for _, p := range procs {
		if p.Running {
			continue
		}
		b, _ := os.ReadFile(p.Log)
		if strings.Contains(string(b), harness.NotImplemented) {
			harness.RedFeature(t, fmt.Sprintf("bob-server %s exits at start-up with %s", p.Name, harness.NotImplemented))
			return nil
		}
		t.Fatalf("bob-server %s exited during start-up (not a stub answer): %s\n%s", p.Name, p.ExitNote, b)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "devstack.json"))
	if err != nil {
		t.Fatalf("harness error: %v", err)
	}
	var info struct {
		APIURL        string `json:"api_url"`
		AdminToken    string `json:"admin_token"`
		AttachProxy   string `json:"attach_proxy_url"`
		Orchestrators []struct {
			APIURL string `json:"api_url"`
		} `json:"orchestrators"`
	}
	if err := json.Unmarshal(raw, &info); err != nil {
		t.Fatalf("harness error: devstack.json: %v", err)
	}
	if info.APIURL == "" || info.AdminToken == "" {
		harness.RedFeature(t, "bob-server orchestrator (the devstack info line has no api_url/admin_token)")
		return nil
	}
	e := &Env{
		T: t, Stack: s, Dir: dir, Token: info.AdminToken, attach: info.AttachProxy,
		hc: &http.Client{Timeout: 30 * time.Second, Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, // loopback test servers use throwaway certificates
		}},
		server: filepath.Join(dir, "bin", "bob-server"), restarted: map[string]*serverenv.Process{},
	}
	for i := range s.Info.Orchestrators {
		u := info.APIURL
		if i < len(info.Orchestrators) && info.Orchestrators[i].APIURL != "" {
			u = info.Orchestrators[i].APIURL
		} else if i > 0 {
			harness.RedFeature(t, "devstack info: orchestrators[i].api_url missing")
			return nil
		}
		e.apis = append(e.apis, strings.TrimRight(u, "/"))
	}
	h := filepath.Join(t.TempDir(), "home")
	if err := os.MkdirAll(h, 0o755); err != nil {
		t.Fatal(err)
	}
	e.home = []string{"HOME=" + h, "XDG_CONFIG_HOME=" + h + "/.config", "XDG_DATA_HOME=" + h + "/.local/share",
		"XDG_STATE_HOME=" + h + "/.local/state", "XDG_CACHE_HOME=" + h + "/.cache", "TERM=dumb"}
	r := harness.Run(t, "bob", harness.Spec{Env: e.home, Stdin: e.Token + "\n"}, "login", "--token-stdin", e.apis[0])
	if harness.Red(t, r) { // the CLI's login is still a stub: red, not a real failure
		return nil
	}
	if r.Exit != 0 {
		t.Fatalf("bob login: exit %d\n%s", r.Exit, r.Combined())
	}
	return e
}

// Via returns a view of e whose API calls go to orchestrator i.
func (e *Env) Via(i int) *Env {
	c := *e
	c.via = i
	return &c
}

// Proxy returns the URL of the counting S3 proxy.
func (e *Env) Proxy() string { return e.Stack.Info.S3ProxyURL }

// Call makes one unary Connect call (JSON) to the /api/ mount (SPEC §6.2) and decodes the
// response into resp.
func (e *Env) Call(ctx context.Context, service, method string, req, resp any) error {
	b, err := json.Marshal(req)
	if err != nil {
		return err
	}
	hr, err := http.NewRequestWithContext(ctx, http.MethodPost, e.apis[e.via]+"/api/"+service+"/"+method, bytes.NewReader(b))
	if err != nil {
		return err
	}
	hr.Header.Set("Content-Type", "application/json")
	hr.Header.Set("Connect-Protocol-Version", "1")
	hr.Header.Set("Authorization", "Bearer "+e.Token)
	r, err := e.hc.Do(hr)
	if err != nil {
		return err
	}
	defer r.Body.Close()
	body, err := io.ReadAll(io.LimitReader(r.Body, 64<<20))
	if err != nil {
		return err
	}
	if r.StatusCode != http.StatusOK {
		return fmt.Errorf("%s/%s: %s: %s", service, method, r.Status, body)
	}
	if resp == nil {
		return nil
	}
	return json.Unmarshal(body, resp)
}

// I64 is a proto3 JSON int64: a string on the wire, a number in tolerant clients.
type I64 int64

// UnmarshalJSON accepts "123" and 123.
func (i *I64) UnmarshalJSON(b []byte) error {
	n, err := strconv.ParseInt(strings.Trim(string(b), `"`), 10, 64)
	*i = I64(n)
	return err
}

// Line is one bob.log.v1.LogLine.
type Line struct {
	Package string `json:"package"`
	Seq     I64    `json:"seq"`
	Attempt int    `json:"attempt"`
	Text    string `json:"text"`
}

// Job is the part of bob.build.v1.Job the suites read.
type Job struct {
	ID        string `json:"id"`
	BuildID   string `json:"buildId"`
	Kind      string `json:"kind"`
	State     string `json:"state"`
	WorkerID  string `json:"workerId"`
	Attempt   int    `json:"attempt"`
	CreatedAt string `json:"createdAt"`
	ClaimedAt string `json:"claimedAt"`
	Error     struct {
		Code string `json:"code"`
	} `json:"error"`
}

// Build is the part of bob.build.v1.Build the suites read.
type Build struct {
	ID    string `json:"id"`
	State string `json:"state"`
}

// Terminal reports whether a BuildState name is final.
func Terminal(state string) bool {
	switch state {
	case "BUILD_STATE_SUCCEEDED", "BUILD_STATE_FAILED", "BUILD_STATE_CANCELLED", "BUILD_STATE_SUPERSEDED", "BUILD_STATE_SKIPPED":
		return true
	}
	return false
}

// GetBuild reads one build.
func (e *Env) GetBuild(ctx context.Context, id string) (Build, error) {
	var r struct {
		Build Build `json:"build"`
	}
	err := e.Call(ctx, "bob.build.v1.BuildService", "GetBuild", map[string]string{"id": id}, &r)
	return r.Build, err
}

// Jobs lists every job of a build.
func (e *Env) Jobs(ctx context.Context, buildID string) ([]Job, error) {
	var out []Job
	tok := ""
	for {
		var r struct {
			Jobs          []Job  `json:"jobs"`
			NextPageToken string `json:"nextPageToken"`
		}
		req := map[string]any{"buildId": buildID, "pageSize": 100, "pageToken": tok}
		if err := e.Call(ctx, "bob.build.v1.JobService", "ListJobs", req, &r); err != nil {
			return nil, err
		}
		out = append(out, r.Jobs...)
		if r.NextPageToken == "" {
			return out, nil
		}
		tok = r.NextPageToken
	}
}

// Log returns the persisted lines of one package of a build (GetLog), after afterSeq.
func (e *Env) Log(ctx context.Context, buildID, pkg string, afterSeq int64) ([]Line, error) {
	var out []Line
	for {
		var r struct {
			Lines     []Line `json:"lines"`
			Truncated bool   `json:"truncated"`
		}
		req := map[string]any{"buildId": buildID, "package": pkg, "afterSeq": strconv.FormatInt(afterSeq, 10), "limit": 100000}
		if err := e.Call(ctx, "bob.log.v1.LogService", "GetLog", req, &r); err != nil {
			return nil, err
		}
		out = append(out, r.Lines...)
		if !r.Truncated || len(r.Lines) == 0 {
			return out, nil
		}
		afterSeq = int64(r.Lines[len(r.Lines)-1].Seq)
	}
}

// Orchestrator is the part of bob.fleet.v1.Orchestrator the suites read.
type Orchestrator struct {
	ID            string   `json:"id"`
	Endpoint      string   `json:"endpoint"`
	StartedAt     string   `json:"startedAt"`
	LastHeartbeat string   `json:"lastHeartbeat"`
	LeaderOf      []string `json:"leaderOf"`
}

// Orchestrators lists the members of fleet/cfg.m as the API shows them.
func (e *Env) Orchestrators(ctx context.Context) ([]Orchestrator, error) {
	var r struct {
		Orchestrators []Orchestrator `json:"orchestrators"`
	}
	err := e.Call(ctx, "bob.fleet.v1.FleetService", "ListOrchestrators", map[string]any{}, &r)
	return r.Orchestrators, err
}

// Worker is the part of bob.fleet.v1.Worker the suites read.
type Worker struct {
	ID    string `json:"id"`
	State string `json:"state"`
}

// Workers lists all workers.
func (e *Env) Workers(ctx context.Context) ([]Worker, error) {
	var out []Worker
	tok := ""
	for {
		var r struct {
			Workers       []Worker `json:"workers"`
			NextPageToken string   `json:"nextPageToken"`
		}
		if err := e.Call(ctx, "bob.fleet.v1.FleetService", "ListWorkers", map[string]any{"pageSize": 200, "pageToken": tok}, &r); err != nil {
			return nil, err
		}
		out = append(out, r.Workers...)
		if r.NextPageToken == "" {
			return out, nil
		}
		tok = r.NextPageToken
	}
}

// Eventually polls fn every 200 ms until it returns "" (done) or the deadline passes;
// it returns the last non-empty reason.
func Eventually(timeout time.Duration, fn func() string) string {
	deadline := time.Now().Add(timeout)
	for {
		why := fn()
		if why == "" || time.Now().After(deadline) {
			return why
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// Bob runs the bob CLI as the logged-in admin.
func (e *Env) Bob(dir string, timeout time.Duration, args ...string) harness.Result {
	return harness.Run(e.T, "bob", harness.Spec{Dir: dir, Env: e.home, Timeout: timeout}, args...)
}

// Pkg is one package of a generated repository: label //<Name>:t runs Cmd with bash.
type Pkg struct{ Name, Cmd string }

// Repo writes a git repository with the given packages and returns its directory.
func Repo(t testing.TB, pkgs []Pkg) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "repo")
	write := func(rel, body string) {
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("go.mod", "module example.com/srv\n\ngo 1.26.0\n")
	write("bob.lock", "{\n  \"lockVersion\": 1\n}\n")
	write("bob.go", "//go:build bob\n\npackage srv\n\nimport \"github.com/megakuul/bob/pkg/bob\"\n\nvar _ = bob.Repo(bob.RepoOpts{Nixpkgs: \"nixos-26.05\"})\n")
	for _, p := range pkgs {
		write(p.Name+"/bob.go", fmt.Sprintf(`//go:build bob

package %s

import (
	"github.com/megakuul/bob/pkg/bob"
	"github.com/megakuul/bob/pkg/nixpkgs"
)

// T runs one shell command (label //%s:t).
var T = bob.Define(func(p *bob.P) *bob.Step {
	return bob.Run(p, "t", bob.RunOpts{
		Tools:   []bob.Tool{nixpkgs.Tool(p, "bash"), nixpkgs.Tool(p, "coreutils")},
		Cmd:     []string{"bash", "-c", %q},
		Outputs: []string{"done"},
	})
})
`, p.Name, p.Name, p.Cmd+"; echo ok > $BOB_OUT/done"))
	}
	for _, args := range [][]string{{"init", "-q"}, {"add", "-A"}, {"-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "-q", "-m", "x"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if b, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("harness error: git %v: %v: %s", args, err, b)
		}
	}
	return dir
}

// Submit sends `bob build --remote label` and returns the build id as soon as the
// remote.submitted event arrives; the CLI is then detached with SIGINT (the build keeps
// running on the server, R-DEV-005).
func (e *Env) Submit(dir, label string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, harness.Bin(e.T, "bob"), "build", "--remote", "--json", label)
	cmd.Dir = dir
	cmd.Env = append([]string{"PATH=/usr/bin:/bin", "TZ=UTC", "LC_ALL=C"}, e.home...)
	out, err := cmd.StdoutPipe()
	if err != nil {
		return "", err
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return "", err
	}
	id := ""
	sc := bufio.NewScanner(out)
	sc.Buffer(make([]byte, 1<<20), 16<<20)
	for sc.Scan() {
		var ev map[string]any
		if json.Unmarshal(sc.Bytes(), &ev) == nil && ev["event"] == "remote.submitted" {
			id, _ = ev["buildId"].(string)
			break
		}
	}
	if id != "" {
		_ = cmd.Process.Signal(syscall.SIGINT)
	}
	go func() { _, _ = io.Copy(io.Discard, out) }()
	werr := cmd.Wait()
	if id == "" {
		return "", fmt.Errorf("bob build --remote %s: no remote.submitted event (%v): %s", label, werr, stderr.String())
	}
	return id, nil
}

// WaitBuild waits until the build is terminal and returns its state.
func (e *Env) WaitBuild(id string, timeout time.Duration) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	state := ""
	why := Eventually(timeout, func() string {
		b, err := e.GetBuild(ctx, id)
		if err != nil {
			return err.Error()
		}
		state = b.State
		if !Terminal(b.State) {
			return b.State
		}
		return ""
	})
	if why != "" {
		return state, fmt.Errorf("build %s not terminal after %s: %s", id, timeout, why)
	}
	return state, nil
}

// Counts reads the counters of the S3 proxy.
func (e *Env) Counts() (s3count.Counts, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return s3count.Fetch(ctx, e.Proxy())
}

// ResetCounts zeroes the proxy counters.
func (e *Env) ResetCounts() {
	req, err := http.NewRequest(http.MethodDelete, e.Proxy()+"/_s3count", nil)
	if err != nil {
		e.T.Fatal(err)
	}
	r, err := e.hc.Do(req)
	if err != nil {
		e.T.Fatalf("harness error: reset counters: %v", err)
	}
	r.Body.Close()
}

// SetFaults installs a seeded fault script in the S3 proxy.
func (e *Env) SetFaults(s s3count.Script) {
	b, err := json.Marshal(s)
	if err != nil {
		e.T.Fatal(err)
	}
	req, err := http.NewRequest(http.MethodPut, e.Proxy()+"/_s3count/faults", bytes.NewReader(b))
	if err != nil {
		e.T.Fatal(err)
	}
	r, err := e.hc.Do(req)
	if err != nil || r.StatusCode != http.StatusNoContent {
		e.T.Fatalf("harness error: install fault script: %v", err)
	}
	r.Body.Close()
}

// AssertNoList ends every server e2e test (D60): no LIST on control or fleet except sweeps.
func (e *Env) AssertNoList() {
	e.T.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	s3count.AssertNoList(ctx, e.T, e.Proxy(), "bob-control", "bob-fleet")
}

// Requests sums the proxy rows of the control and fleet buckets, without heartbeats
// (`whb/`, shared per worker, R-FLEET-009) and without LISTs.
func Requests(c s3count.Counts) int {
	n := 0
	for _, r := range c.Rows {
		if (r.Bucket == "bob-control" || r.Bucket == "bob-fleet") && r.Prefix != "whb" && r.Op != s3count.OpList {
			n += r.Count
		}
	}
	return n
}

// AttachProxy blocks or unblocks the orchestrators' TCP front (devstack contract,
// attach_proxy_url). orch < 0 means all orchestrators. While blocked, new connections
// to the orchestrators are refused and open ones are reset: workers keep S3 access.
func (e *Env) AttachProxy(block bool, orch int) bool {
	e.T.Helper()
	if e.attach == "" {
		harness.RedFeature(e.T, "devstack: no attach_proxy_url (TCP front of the orchestrators)")
		return false
	}
	verb := "unblock"
	if block {
		verb = "block"
	}
	u := e.attach + "/" + verb
	if orch >= 0 {
		u += "?orch=" + strconv.Itoa(orch)
	}
	r, err := e.hc.Post(u, "text/plain", nil)
	if err != nil || r.StatusCode != http.StatusNoContent {
		e.T.Fatalf("harness error: attach proxy %s: %v", verb, err)
	}
	r.Body.Close()
	return true
}

// AttachBytes returns the bytes workers sent to orchestrators since the proxy started
// (devstack contract: GET attach_proxy_url/bytes → {"to_orchestrator": n}).
func (e *Env) AttachBytes() int64 {
	e.T.Helper()
	r, err := e.hc.Get(e.attach + "/bytes")
	if err != nil {
		e.T.Fatalf("harness error: attach bytes: %v", err)
	}
	defer r.Body.Close()
	var v struct {
		ToOrchestrator int64 `json:"to_orchestrator"`
	}
	if err := json.NewDecoder(r.Body).Decode(&v); err != nil {
		e.T.Fatalf("harness error: attach bytes: %v", err)
	}
	return v.ToOrchestrator
}

// Pid returns the current pid of the stack process name (orchestrator-N, worker-N);
// a process restarted with Restart has a new pid.
func (e *Env) Pid(name string) int {
	if p, ok := e.restarted[name]; ok {
		return p.Pid()
	}
	for _, p := range append(append([]stack.Proc{}, e.Stack.Info.Orchestrators...), e.Stack.Info.Workers...) {
		if p.Name == name {
			return p.Pid
		}
	}
	return 0
}

// Kill sends SIGKILL to the process group of name.
func (e *Env) Kill(name string) error {
	pid := e.Pid(name)
	if pid == 0 {
		return fmt.Errorf("srvtest: no process %q", name)
	}
	return syscall.Kill(-pid, syscall.SIGKILL)
}

// Restart starts a killed stack process again with the environment the stack gave it.
func (e *Env) Restart(name string) error {
	mode := "orchestrator"
	if strings.HasPrefix(name, "worker") {
		mode = "worker"
	}
	info := e.Stack.Info
	if err := os.MkdirAll(filepath.Join(info.Dir, name), 0o755); err != nil {
		return err
	}
	cmd := exec.Command(e.server, mode)
	ak, sk := e.roleKeys(mode)
	cmd.Env = append(os.Environ(),
		"BOB_STORAGE_ENDPOINT="+info.S3ProxyURL, "BOB_STORAGE_REGION="+info.Region,
		"AWS_ACCESS_KEY_ID="+ak, "AWS_SECRET_ACCESS_KEY="+sk,
		"BOB_SERVER_NAME="+name, "BOB_STATE_DIR="+filepath.Join(info.Dir, name))
	if mode == "orchestrator" {
		cmd.Env = append(cmd.Env, "BOB_LISTEN="+strings.TrimPrefix(e.orchURL(name), "http://"), "BOB_ADMIN_TOKEN="+e.Token)
		if info.PoliciesReady && os.Getenv("BOB_STORAGE_WORKER_PROBE_CREDENTIAL") == "" {
			cmd.Env = append(cmd.Env, "BOB_STORAGE_WORKER_PROBE_CREDENTIAL="+info.WorkerAccess+":"+info.WorkerSecret)
		}
	} else if len(info.AttachFronts) > 0 {
		cmd.Env = append(cmd.Env, "BOB_ORCHESTRATOR_ADDRS="+strings.Join(info.AttachFronts, ","))
	}
	p, err := serverenv.StartProcess(cmd, filepath.Join(info.Dir, "logs", name+".log"))
	if err != nil {
		return err
	}
	e.restarted[name] = p
	e.T.Cleanup(func() { _ = p.Stop(5 * time.Second) })
	return nil
}

// roleKeys returns the S3 key pair of a server mode: the role keys when the stack applied the
// shipped policies, else the root key.
func (e *Env) roleKeys(mode string) (string, string) {
	i := e.Stack.Info
	switch {
	case !i.PoliciesReady:
		return i.AccessKey, i.SecretKey
	case mode == "worker":
		return i.WorkerAccess, i.WorkerSecret
	}
	return i.OrchAccess, i.OrchSecret
}

// orchURL is the listener of the orchestrator process name.
func (e *Env) orchURL(name string) string {
	for _, p := range e.Stack.Info.Orchestrators {
		if p.Name == name {
			return p.APIURL
		}
	}
	return ""
}

// Percentile returns the p-quantile (0..1) of d (nearest rank).
func Percentile(d []time.Duration, p float64) time.Duration {
	if len(d) == 0 {
		return 0
	}
	s := slices.Clone(d)
	slices.Sort(s)
	i := int(float64(len(s))*p+0.9999999) - 1
	return s[max(0, min(len(s)-1, i))]
}

// Follower is one FollowLog stream (Connect server streaming, JSON envelopes).
type Follower struct {
	Msgs   chan FollowMsg
	cancel context.CancelFunc
}

// FollowMsg is one FollowLogResponse with its arrival time.
type FollowMsg struct {
	At       time.Time
	Lines    []Line `json:"lines"`
	Restarts []struct {
		Package string `json:"package"`
		Attempt int    `json:"attempt"`
		FromSeq I64    `json:"fromSeq"`
	} `json:"restarts"`
	End []struct {
		Package string `json:"package"`
		LastSeq I64    `json:"lastSeq"`
	} `json:"end"`
	Done    bool  `json:"done"`
	Delayed bool  `json:"delayed"`
	Err     error `json:"-"`
}

// Follow opens FollowLog on the orchestrator of e (pkg "" = all packages). The channel is
// closed when the stream ends; a transport error arrives as a message with Err set.
func (e *Env) Follow(buildID, pkg string) *Follower { return e.FollowFrom(buildID, pkg, 0, 0) }

// FollowFrom is Follow with a cursor: the client already has the lines of pkg up to seq of attempt.
func (e *Env) FollowFrom(buildID, pkg string, seq int64, attempt int) *Follower {
	ctx, cancel := context.WithCancel(context.Background())
	f := &Follower{Msgs: make(chan FollowMsg, 4096), cancel: cancel}
	e.T.Cleanup(f.Close)
	go func() {
		defer close(f.Msgs)
		req0 := map[string]any{"buildId": buildID, "package": pkg}
		if seq > 0 {
			req0["cursor"] = map[string]any{pkg: map[string]any{"seq": strconv.FormatInt(seq, 10), "attempt": attempt}}
		}
		body, _ := json.Marshal(req0)
		env := make([]byte, 5, 5+len(body))
		binary.BigEndian.PutUint32(env[1:], uint32(len(body)))
		env = append(env, body...)
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.apis[e.via]+"/api/bob.log.v1.LogService/FollowLog", bytes.NewReader(env))
		if err != nil {
			f.Msgs <- FollowMsg{Err: err}
			return
		}
		req.Header.Set("Content-Type", "application/connect+json")
		req.Header.Set("Connect-Protocol-Version", "1")
		req.Header.Set("Authorization", "Bearer "+e.Token)
		hc := &http.Client{Transport: e.hc.Transport} // no overall timeout: the stream lives as long as the build
		r, err := hc.Do(req)
		if err != nil {
			f.Msgs <- FollowMsg{Err: err}
			return
		}
		defer r.Body.Close()
		if r.StatusCode != http.StatusOK {
			b, _ := io.ReadAll(io.LimitReader(r.Body, 4096))
			f.Msgs <- FollowMsg{Err: fmt.Errorf("FollowLog: %s: %s", r.Status, b)}
			return
		}
		br := bufio.NewReader(r.Body)
		for {
			var hdr [5]byte
			if _, err := io.ReadFull(br, hdr[:]); err != nil {
				if ctx.Err() == nil && err != io.EOF {
					f.Msgs <- FollowMsg{Err: err}
				}
				return
			}
			n := binary.BigEndian.Uint32(hdr[1:])
			if n > 64<<20 {
				f.Msgs <- FollowMsg{Err: fmt.Errorf("FollowLog: envelope of %d bytes", n)}
				return
			}
			buf := make([]byte, n)
			if _, err := io.ReadFull(br, buf); err != nil {
				f.Msgs <- FollowMsg{Err: err}
				return
			}
			if hdr[0]&2 != 0 { // end-of-stream envelope: {"error":{...}} or {}
				var end struct {
					Error *struct{ Code, Message string } `json:"error"`
				}
				if json.Unmarshal(buf, &end) == nil && end.Error != nil {
					f.Msgs <- FollowMsg{Err: fmt.Errorf("FollowLog: %s: %s", end.Error.Code, end.Error.Message)}
				}
				return
			}
			var m FollowMsg
			if err := json.Unmarshal(buf, &m); err != nil {
				f.Msgs <- FollowMsg{Err: err}
				return
			}
			m.At = time.Now()
			f.Msgs <- m
		}
	}()
	return f
}

// Close ends the stream.
func (f *Follower) Close() { f.cancel() }

// Stream folds follower messages into the final per-package line sets, applying the
// Restart rule (drop own lines of that package with seq >= fromSeq) and checking that
// no line is delivered twice or out of order.
type Stream struct {
	Lines map[string][]Line // per package, in delivery order
	Errs  []string          // gaps, duplicates, order violations
	Last  map[string]int64
}

// NewStream returns an empty fold.
func NewStream() *Stream { return &Stream{Lines: map[string][]Line{}, Last: map[string]int64{}} }

// Add folds one message in.
func (s *Stream) Add(m FollowMsg) {
	for _, r := range m.Restarts {
		keep := s.Lines[r.Package][:0]
		for _, l := range s.Lines[r.Package] {
			if int64(l.Seq) < int64(r.FromSeq) {
				keep = append(keep, l)
			}
		}
		s.Lines[r.Package] = keep
		s.Last[r.Package] = int64(r.FromSeq) - 1
	}
	for _, l := range m.Lines {
		if want := s.Last[l.Package] + 1; int64(l.Seq) != want {
			s.Errs = append(s.Errs, fmt.Sprintf("%s: got seq %d, want %d (gap or duplicate)", l.Package, l.Seq, want))
		}
		s.Last[l.Package] = int64(l.Seq)
		s.Lines[l.Package] = append(s.Lines[l.Package], l)
	}
}

// Cursor returns the last delivered (seq, attempt) of pkg, for a reconnect.
func (s *Stream) Cursor(pkg string) (int64, int) {
	ls := s.Lines[pkg]
	if len(ls) == 0 {
		return 0, 0
	}
	return int64(ls[len(ls)-1].Seq), ls[len(ls)-1].Attempt
}

// AttachStreams returns the Attach streams the orchestrators' fronts carry now, by process name
// (devstack contract: GET attach_proxy_url/streams → {"orchestrator-0": n, ...}).
func (e *Env) AttachStreams() map[string]int {
	e.T.Helper()
	r, err := e.hc.Get(e.attach + "/streams")
	if err != nil {
		e.T.Fatalf("harness error: attach streams: %v", err)
	}
	defer r.Body.Close()
	m := map[string]int{}
	if err := json.NewDecoder(r.Body).Decode(&m); err != nil {
		e.T.Fatalf("harness error: attach streams: %v", err)
	}
	return m
}

// AttachRedirect makes the front of orchestrator from forward to the backend of orchestrator to
// (whose TLS key differs from the one pinned in fleet/cfg.m for from); to < 0 restores it.
func (e *Env) AttachRedirect(from, to int) {
	e.T.Helper()
	u := e.attach + "/redirect?orch=" + strconv.Itoa(from) + "&to=" + strconv.Itoa(to)
	r, err := e.hc.Post(u, "text/plain", nil)
	if err != nil || r.StatusCode != http.StatusNoContent {
		e.T.Fatalf("harness error: attach redirect: %v", err)
	}
	r.Body.Close()
}

// OrchestratorName maps a member of fleet/cfg.m to its stack process name (orchestrator-N) by
// matching the endpoint it publishes with the API address of each stack orchestrator; "" when none matches.
func (e *Env) OrchestratorName(o Orchestrator) string {
	for i, a := range e.apis {
		host := a
		if _, h, ok := strings.Cut(a, "://"); ok {
			host = h
		}
		if strings.Contains(o.Endpoint, host) {
			return "orchestrator-" + strconv.Itoa(i)
		}
	}
	return ""
}

// Submitting submits label like Submit but retries while the orchestrator the CLI talks to is
// down (chaos runs kill orchestrators): up to 40 tries, 2 s apart.
func (e *Env) Submitting(dir, label string) (string, error) {
	var err error
	for range 40 {
		var id string
		if id, err = e.Submit(dir, label); err == nil {
			return id, nil
		}
		time.Sleep(2 * time.Second)
	}
	return "", err
}

// ViaAny runs fn against each orchestrator in turn and returns the first success.
func (e *Env) ViaAny(fn func(v *Env) error) error {
	var err error
	for i := range e.apis {
		if err = fn(e.Via(i)); err == nil {
			return nil
		}
	}
	return err
}

// RunServer runs `bob-server <args>` against the stack's S3 (an admin command such as `store fsck`).
func (e *Env) RunServer(timeout time.Duration, args ...string) harness.Result {
	info := e.Stack.Info
	ak, sk := e.roleKeys("orchestrator")
	return harness.Run(e.T, "bob-server", harness.Spec{Timeout: timeout, Env: []string{
		"BOB_STORAGE_ENDPOINT=" + info.S3ProxyURL, "BOB_STORAGE_REGION=" + info.Region,
		"AWS_ACCESS_KEY_ID=" + ak, "AWS_SECRET_ACCESS_KEY=" + sk,
	}}, args...)
}
