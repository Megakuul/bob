// Package stack starts the all-in-one local dev stack (HARNESS.md §13.2): RustFS,
// the counting proxy in front of it, N orchestrators and M workers. Server suites
// and `go run ./tools/devstack up` start their stacks through this package.
//
// Contracts with the bob-server binary (set in the process environment):
//
//   - BOB_LISTEN (orchestrator): the address the orchestrator serves API and Attach on.
//   - BOB_ADMIN_TOKEN (orchestrator): bootstrap admin credential. bob-server honours it only
//     when built with -tags bobtest (a release build refuses to start with it set), so the
//     stack always builds bob-server with -tags bobtest.
//   - BOB_ORCHESTRATOR_ADDRS (worker): comma-separated http://host:port list, entry i is the
//     attach proxy front of orchestrator-i (tests/serverenv/attachproxy). Workers must reach
//     orchestrators only through these addresses so tests can block, count and redirect Attach.
//   - BOB_TEST_CLOCK_OFFSET (orchestrator, only set when Options.ClockOffset != 0): a Go
//     duration (may be negative, e.g. "90s") added to the orchestrator's wall clock. It must
//     be honoured only by servers built with -tags bobtest, which devstack uses whenever it
//     builds bob-server itself. A release build ignores the variable.
//   - BOB_WORKER_SLOTS and the other BOB_* settings of the test process are inherited by every
//     server process (srvtest.Options.Env sets them with t.Setenv).
//   - BOB_STORAGE_WORKER_PROBE_CREDENTIAL (orchestrator, R-SEC-102): "<access>:<secret>" of the
//     worker role, set when the shipped policies are applied and the test did not set it.
//   - S3 credential roles: the shipped policies (deploy/policies/*.json) are applied to RustFS
//     with two identities, access keys "worker-bob" (worker policy) and "orchestrator-bob"
//     (orchestrator policy; the control bucket policy lets only this user in). Orchestrators get
//     the orchestrator key, workers the worker key; the root key stays with the harness. The
//     counting proxy maps an access key to a role by prefix (Options.RolePrefixes, default
//     "worker-" -> "worker", "orchestrator-" -> "orchestrator").
//     Options.RootCredentials gives every process the root key instead (no policies applied).
//
// devstack.json carries, next to the Info fields, attach_proxy_url (control API of the
// attach proxy, see its package comment), api_url and api_urls (all orchestrators),
// orchestrators[i].api_url, policies_applied and the role keys worker_access_key,
// worker_secret_key, orchestrator_access_key, orchestrator_secret_key.
//
// The stack does not start a forge (HARNESS.md §13.2 -forgejo is still to do).
//
// The product has no dev mode (decision A22). While the server modes are stubs
// ("not implemented"), their processes exit at once; Start records that in Info
// instead of failing, unless Options.Strict is set.
package stack

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/megakuul/bob/tests/serverenv"
	"github.com/megakuul/bob/tests/serverenv/attachproxy"
	"github.com/megakuul/bob/tests/serverenv/rustfs"
	"github.com/megakuul/bob/tests/serverenv/s3count"
	"github.com/megakuul/bob/tests/serverenv/s3sig"
)

// Options configure Start.
type Options struct {
	Dir           string // state directory; required
	Orchestrators int    // default 1
	Workers       int    // default 1
	Offline       bool   // never download pinned tools
	Strict        bool   // fail when a server process exits during start-up
	Server        string // bob-server binary (a -tags bobtest build); empty builds ./cmd/bob-server -tags bobtest into Dir/bin
	// ClockOffset is passed to the orchestrators as BOB_TEST_CLOCK_OFFSET.
	ClockOffset time.Duration
	// RolePrefixes maps S3 access-key prefixes to role names for the counting proxy.
	// Nil means the default (worker-, orchestrator-).
	RolePrefixes map[string]string
	// RootCredentials skips the shipped policies and gives every process the RustFS root key.
	RootCredentials bool
}

// Proc is one stack process.
type Proc struct {
	Name     string `json:"name"`
	Pid      int    `json:"pid"`
	Log      string `json:"log"`
	Running  bool   `json:"running"`
	ExitNote string `json:"exit_note,omitempty"` // set when it exited during start-up
	APIURL   string `json:"api_url,omitempty"`   // orchestrators: this one's HTTP listener
}

// Info is what `devstack up` prints and writes to <dir>/devstack.json.
type Info struct {
	V              int      `json:"v"`
	Dir            string   `json:"dir"`
	Pid            int      `json:"pid"` // the process that owns the stack; `down` signals it
	S3URL          string   `json:"s3_url"`
	S3ProxyURL     string   `json:"s3_proxy_url"` // what servers use; counts requests, injects faults
	Region         string   `json:"region"`
	AccessKey      string   `json:"access_key"`
	SecretKey      string   `json:"secret_key"`
	Buckets        []string `json:"buckets"`
	AttachProxyURL string   `json:"attach_proxy_url"` // control API of the orchestrator TCP fronts
	APIURL         string   `json:"api_url"`          // first orchestrator's HTTP listener
	APIURLs        []string `json:"api_urls"`         // every orchestrator's HTTP listener
	AttachFronts   []string `json:"attach_fronts"`    // BOB_ORCHESTRATOR_ADDRS of the workers
	AdminToken     string   `json:"admin_token"`      // bootstrap admin credential for tests
	PoliciesReady  bool     `json:"policies_applied"` // the shipped policies are applied to RustFS
	WorkerAccess   string   `json:"worker_access_key,omitempty"`
	WorkerSecret   string   `json:"worker_secret_key,omitempty"`
	OrchAccess     string   `json:"orchestrator_access_key,omitempty"`
	OrchSecret     string   `json:"orchestrator_secret_key,omitempty"`
	Orchestrators  []Proc   `json:"orchestrators"`
	Workers        []Proc   `json:"workers"`
	Rustfs         Proc     `json:"rustfs"`
	Seed           *Seed    `json:"seed,omitempty"` // set by Stack.Seed (devstack up)
}

// Stack is a running stack.
type Stack struct {
	Info                   Info
	fs                     *rustfs.Server
	proxy                  *s3count.Server
	attach                 *attachproxy.Proxy
	attachSrv              *http.Server
	workerCreds, orchCreds s3sig.Creds
	procs                  map[string]*serverenv.Process
	order                  []string
}

// Start brings the stack up. On error everything already started is stopped.
func Start(ctx context.Context, o Options) (*Stack, error) {
	if o.Dir == "" {
		return nil, errors.New("devstack: Options.Dir is required")
	}
	if o.Orchestrators == 0 {
		o.Orchestrators = 1
	}
	if o.Workers == 0 {
		o.Workers = 1
	}
	dir, err := filepath.Abs(o.Dir)
	if err != nil {
		return nil, fmt.Errorf("devstack: %w", err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "logs"), 0o755); err != nil {
		return nil, fmt.Errorf("devstack: %w", err)
	}
	s := &Stack{procs: map[string]*serverenv.Process{}}
	fail := func(err error) (*Stack, error) { return nil, errors.Join(err, s.Stop()) }

	server := o.Server
	if server == "" {
		if server, err = buildServer(ctx, dir); err != nil {
			return fail(err)
		}
	}
	if s.fs, err = rustfs.Start(ctx, rustfs.Options{Dir: filepath.Join(dir, "rustfs"), Offline: o.Offline}); err != nil {
		return fail(err)
	}
	prefixes := o.RolePrefixes
	if prefixes == nil {
		prefixes = map[string]string{"worker-": "worker", "orchestrator-": "orchestrator"}
	}
	px, err := s3count.New(s3count.Options{Backend: s.fs.URL, RoleOf: func(ak string) string {
		for pre, role := range prefixes {
			if strings.HasPrefix(ak, pre) {
				return role
			}
		}
		return ""
	}})
	if err != nil {
		return fail(err)
	}
	if s.proxy, err = s3count.Listen(px, 0); err != nil {
		return fail(err)
	}
	tok := make([]byte, 16)
	if _, err := rand.Read(tok); err != nil {
		return fail(fmt.Errorf("devstack: %w", err))
	}
	adminToken := hex.EncodeToString(tok)
	s.Info = Info{
		V: 1, Dir: dir, Pid: os.Getpid(),
		S3URL: s.fs.URL, S3ProxyURL: s.proxy.URL, Region: s.fs.Root.Region,
		AccessKey: s.fs.Root.AccessKey, SecretKey: s.fs.Root.SecretKey,
		Buckets:    rustfs.Buckets,
		AdminToken: adminToken,
		Rustfs:     Proc{Name: "rustfs", Pid: s.fs.Pid(), Log: filepath.Join(s.fs.Dir, "rustfs.log"), Running: true},
	}
	worker := s3sig.Creds{AccessKey: s.Info.AccessKey, SecretKey: s.Info.SecretKey, Region: s.Info.Region}
	orch := worker
	if !o.RootCredentials {
		if err := applyPolicies(ctx, s.fs); err != nil {
			return fail(err)
		}
		worker = s3sig.Creds{AccessKey: WorkerAccessKey, SecretKey: WorkerSecretKey, Region: s.Info.Region}
		orch = s3sig.Creds{AccessKey: OrchestratorAccessKey, SecretKey: OrchestratorSecretKey, Region: s.Info.Region}
		s.Info.PoliciesReady = true
		s.Info.WorkerAccess, s.Info.WorkerSecret = worker.AccessKey, worker.SecretKey
		s.Info.OrchAccess, s.Info.OrchSecret = orch.AccessKey, orch.SecretKey
	}
	s.workerCreds, s.orchCreds = worker, orch
	listens := make([]string, o.Orchestrators)
	for i := range listens {
		port, err := serverenv.FreePort()
		if err != nil {
			return fail(err)
		}
		listens[i] = "127.0.0.1:" + strconv.Itoa(port)
	}
	if s.attach, err = attachproxy.New(listens); err != nil {
		return fail(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return fail(fmt.Errorf("devstack: %w", err))
	}
	s.attachSrv = &http.Server{Handler: s.attach.Handler(), ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = s.attachSrv.Serve(ln) }()
	s.Info.AttachProxyURL = "http://" + ln.Addr().String()
	var fronts []string
	for _, a := range s.attach.Addrs() {
		fronts = append(fronts, "http://"+a)
	}
	s.Info.AttachFronts = fronts
	for i, listen := range listens {
		if i == 0 {
			s.Info.APIURL = "http://" + listen
		}
		s.Info.APIURLs = append(s.Info.APIURLs, "http://"+listen)
		env := []string{"BOB_LISTEN=" + listen, "BOB_ADMIN_TOKEN=" + adminToken}
		if o.ClockOffset != 0 {
			env = append(env, "BOB_TEST_CLOCK_OFFSET="+o.ClockOffset.String())
		}
		if s.Info.PoliciesReady && os.Getenv("BOB_STORAGE_WORKER_PROBE_CREDENTIAL") == "" {
			env = append(env, "BOB_STORAGE_WORKER_PROBE_CREDENTIAL="+worker.AccessKey+":"+worker.SecretKey)
		}
		p, err := s.spawn(server, "orchestrator", "orchestrator-"+strconv.Itoa(i), dir, orch, env...)
		if err != nil {
			return fail(err)
		}
		p.APIURL = "http://" + listen
		s.Info.Orchestrators = append(s.Info.Orchestrators, p)
	}
	for i := range o.Workers {
		p, err := s.spawn(server, "worker", "worker-"+strconv.Itoa(i), dir, worker, "BOB_ORCHESTRATOR_ADDRS="+strings.Join(fronts, ","))
		if err != nil {
			return fail(err)
		}
		s.Info.Workers = append(s.Info.Workers, p)
	}
	if o.Strict {
		for _, p := range append(append([]Proc{}, s.Info.Orchestrators...), s.Info.Workers...) {
			if !p.Running {
				return fail(fmt.Errorf("devstack: %s exited during start-up: %s (log %s)", p.Name, p.ExitNote, p.Log))
			}
		}
	}
	if err := s.writeInfo(); err != nil {
		return fail(err)
	}
	return s, nil
}

func (s *Stack) spawn(server, mode, name, dir string, creds s3sig.Creds, env ...string) (Proc, error) {
	cmd := exec.Command(server, mode)
	cmd.Env = append(os.Environ(),
		"BOB_STORAGE_ENDPOINT="+s.proxy.URL,
		"BOB_STORAGE_REGION="+s.fs.Root.Region,
		"AWS_ACCESS_KEY_ID="+creds.AccessKey,
		"AWS_SECRET_ACCESS_KEY="+creds.SecretKey,
		"BOB_SERVER_NAME="+name,
		"BOB_STATE_DIR="+filepath.Join(dir, name),
	)
	cmd.Env = append(cmd.Env, env...)
	if err := os.MkdirAll(filepath.Join(dir, name), 0o755); err != nil {
		return Proc{}, fmt.Errorf("devstack: %w", err)
	}
	log := filepath.Join(dir, "logs", name+".log")
	p, err := serverenv.StartProcess(cmd, log)
	if err != nil {
		return Proc{}, err
	}
	s.procs[name] = p
	s.order = append(s.order, name)
	pr := Proc{Name: name, Pid: p.Pid(), Log: log, Running: true}
	select {
	case <-p.Exited(): // a stub exits at once
		pr.Running = false
		pr.ExitNote = fmt.Sprint(p.Err())
	case <-time.After(300 * time.Millisecond):
	}
	return pr, nil
}

// buildServer builds bob-server with -tags bobtest: the stack's bootstrap admin token and the
// test clock exist only in that build.
func buildServer(ctx context.Context, dir string) (string, error) {
	root, err := serverenv.Root()
	if err != nil {
		return "", err
	}
	out := filepath.Join(dir, "bin", "bob-server")
	cmd := exec.CommandContext(ctx, "go", "build", "-tags", "bobtest", "-o", out, "./cmd/bob-server")
	cmd.Dir = root
	if b, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("devstack: build bob-server: %w\n%s: %w", err, b, serverenv.ErrHarness)
	}
	return out, nil
}

func (s *Stack) writeInfo() error {
	b, err := json.MarshalIndent(s.Info, "", "  ")
	if err != nil {
		return fmt.Errorf("devstack: %w", err)
	}
	tmp := filepath.Join(s.Info.Dir, "devstack.json.tmp")
	if err := os.WriteFile(tmp, append(b, '\n'), 0o644); err != nil {
		return fmt.Errorf("devstack: %w", err)
	}
	return os.Rename(tmp, filepath.Join(s.Info.Dir, "devstack.json"))
}

// Kill sends SIGKILL to the named process (manual chaos).
func (s *Stack) Kill(name string) error {
	p, ok := s.procs[name]
	if !ok {
		if name == "rustfs" && s.fs != nil {
			return syscall.Kill(-s.fs.Pid(), syscall.SIGKILL)
		}
		return fmt.Errorf("devstack: no process %q", name)
	}
	return syscall.Kill(-p.Pid(), syscall.SIGKILL)
}

// Stop stops everything, servers first, S3 last. It is safe to call twice.
func (s *Stack) Stop() error {
	var errs []error
	for i := len(s.order) - 1; i >= 0; i-- {
		if err := s.procs[s.order[i]].Stop(5 * time.Second); err != nil {
			errs = append(errs, err)
		}
	}
	s.order = nil
	if s.attachSrv != nil {
		_ = s.attachSrv.Close()
		s.attachSrv = nil
	}
	if s.attach != nil {
		errs = append(errs, s.attach.Close())
		s.attach = nil
	}
	if s.proxy != nil {
		errs = append(errs, s.proxy.Close())
		s.proxy = nil
	}
	if s.fs != nil {
		errs = append(errs, s.fs.Stop())
		s.fs = nil
	}
	if s.Info.Dir != "" {
		_ = os.Remove(filepath.Join(s.Info.Dir, "devstack.json"))
	}
	return errors.Join(errs...)
}

// ReadInfo reads <dir>/devstack.json.
func ReadInfo(dir string) (*Info, error) {
	b, err := os.ReadFile(filepath.Join(dir, "devstack.json"))
	if err != nil {
		return nil, fmt.Errorf("devstack: %w (is the stack up?)", err)
	}
	var i Info
	if err := json.Unmarshal(b, &i); err != nil {
		return nil, fmt.Errorf("devstack: %w", err)
	}
	return &i, nil
}
