// Package stack starts the all-in-one local dev stack (HARNESS.md §13.2): RustFS,
// the counting proxy in front of it, N orchestrators and M workers. Server suites
// and `go run ./tools/devstack up` start their stacks through this package.
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
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"github.com/megakuul/bob/tests/serverenv"
	"github.com/megakuul/bob/tests/serverenv/rustfs"
	"github.com/megakuul/bob/tests/serverenv/s3count"
)

// Options configure Start.
type Options struct {
	Dir           string // state directory; required
	Orchestrators int    // default 1
	Workers       int    // default 1
	Offline       bool   // never download pinned tools
	Strict        bool   // fail when a server process exits during start-up
	Server        string // bob-server binary; empty builds ./cmd/bob-server into Dir/bin
}

// Proc is one stack process.
type Proc struct {
	Name     string `json:"name"`
	Pid      int    `json:"pid"`
	Log      string `json:"log"`
	Running  bool   `json:"running"`
	ExitNote string `json:"exit_note,omitempty"` // set when it exited during start-up
}

// Info is what `devstack up` prints and writes to <dir>/devstack.json.
type Info struct {
	V             int      `json:"v"`
	Dir           string   `json:"dir"`
	Pid           int      `json:"pid"` // the process that owns the stack; `down` signals it
	S3URL         string   `json:"s3_url"`
	S3ProxyURL    string   `json:"s3_proxy_url"` // what servers use; counts requests, injects faults
	Region        string   `json:"region"`
	AccessKey     string   `json:"access_key"`
	SecretKey     string   `json:"secret_key"`
	Buckets       []string `json:"buckets"`
	APIURL        string   `json:"api_url"`          // first orchestrator's HTTP listener
	AdminToken    string   `json:"admin_token"`      // bootstrap admin credential for tests
	PoliciesReady bool     `json:"policies_applied"` // false until the shipped policies exist (M-server)
	Orchestrators []Proc   `json:"orchestrators"`
	Workers       []Proc   `json:"workers"`
	Rustfs        Proc     `json:"rustfs"`
}

// Stack is a running stack.
type Stack struct {
	Info  Info
	fs    *rustfs.Server
	proxy *s3count.Server
	procs map[string]*serverenv.Process
	order []string
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
	px, err := s3count.New(s3count.Options{Backend: s.fs.URL})
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
	for i := range o.Orchestrators {
		port, err := serverenv.FreePort()
		if err != nil {
			return fail(err)
		}
		listen := "127.0.0.1:" + strconv.Itoa(port)
		if i == 0 {
			s.Info.APIURL = "http://" + listen
		}
		p, err := s.spawn(server, "orchestrator", "orchestrator-"+strconv.Itoa(i), dir, "BOB_LISTEN="+listen, "BOB_ADMIN_TOKEN="+adminToken)
		if err != nil {
			return fail(err)
		}
		s.Info.Orchestrators = append(s.Info.Orchestrators, p)
	}
	for i := range o.Workers {
		p, err := s.spawn(server, "worker", "worker-"+strconv.Itoa(i), dir)
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

func (s *Stack) spawn(server, mode, name, dir string, env ...string) (Proc, error) {
	cmd := exec.Command(server, mode)
	cmd.Env = append(os.Environ(),
		"BOB_STORAGE_ENDPOINT="+s.proxy.URL,
		"BOB_STORAGE_REGION="+s.fs.Root.Region,
		"AWS_ACCESS_KEY_ID="+s.fs.Root.AccessKey,
		"AWS_SECRET_ACCESS_KEY="+s.fs.Root.SecretKey,
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

func buildServer(ctx context.Context, dir string) (string, error) {
	root, err := serverenv.Root()
	if err != nil {
		return "", err
	}
	out := filepath.Join(dir, "bin", "bob-server")
	cmd := exec.CommandContext(ctx, "go", "build", "-o", out, "./cmd/bob-server")
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
