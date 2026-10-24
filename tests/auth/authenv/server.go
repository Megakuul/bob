package authenv

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/megakuul/bob/tests/harness"
)

// Server is a running devstack with its API endpoints.
type Server struct {
	URL        string   // first orchestrator, no trailing slash
	URLs       []string // every orchestrator (api_urls, else just URL)
	AdminToken string   // bob_at_/bob_sa_ token of a bootstrap admin (info line admin_token)
	Info       map[string]any
	Dir        string
	cancel     func()
	clock      string // BOB_TEST_CLOCK_FILE of this stack
}

var (
	buildOnce sync.Once
	devstack  string
	buildErr  error

	sharedMu  sync.Mutex
	shared    *Server
	sharedErr string
)

func buildDevstack(t testing.TB) string {
	t.Helper()
	buildOnce.Do(func() {
		root := harness.Root(t)
		dir, err := os.MkdirTemp("", "bob-auth-devstack-")
		if err != nil {
			buildErr = err
			return
		}
		devstack = filepath.Join(dir, "devstack")
		cmd := exec.Command("go", "build", "-o", devstack, "./tools/devstack")
		cmd.Dir = root
		cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
		if out, err := cmd.CombinedOutput(); err != nil {
			buildErr = fmt.Errorf("harness error: go build devstack: %v: %s", err, out)
		}
	})
	if buildErr != nil {
		t.Fatal(buildErr)
	}
	return devstack
}

// Main stops the shared stack after the tests; call it from TestMain.
func Main(m *testing.M) {
	code := m.Run()
	sharedMu.Lock()
	if shared != nil {
		shared.Stop()
	}
	sharedMu.Unlock()
	os.Exit(code)
}

// Shared returns the one-orchestrator stack of this test binary, starting it on first use.
// Tests sharing it use unique names for everything they create.
func Shared(t *testing.T) *Server {
	t.Helper()
	sharedMu.Lock()
	defer sharedMu.Unlock()
	if shared == nil && sharedErr == "" {
		s, why := start(t, 1)
		shared, sharedErr = s, why
	}
	if shared == nil {
		harness.RedFeature(t, sharedErr)
		t.FailNow()
	}
	return shared
}

// Start brings up a private stack with n orchestrators that stops with the test. Without an
// API URL (stub server) the test is red.
func Start(t *testing.T, n int) *Server {
	t.Helper()
	s, why := start(t, n)
	if s == nil {
		harness.RedFeature(t, why)
		t.FailNow()
	}
	t.Cleanup(s.Stop)
	// Private stacks are checked for leaked secrets when their test ends (R-AUTH-151 case 4;
	// the shared stack is checked once, after the whole suite).
	t.Cleanup(func() {
		if leaks, _ := s.LogLeaks(t, Secrets()); len(leaks) > 0 {
			t.Errorf("secret in a server log: %v", leaks)
		}
	})
	if len(s.URLs) < n {
		harness.RedFeature(t, fmt.Sprintf("bob-server orchestrators (the devstack info line lists %d api_urls, want %d)", len(s.URLs), n))
		t.FailNow()
	}
	return s
}

func start(t *testing.T, n int) (*Server, string) {
	bin := buildDevstack(t)
	// The stack's processes inherit the environment: they must trust bobtest-idp's certificate.
	if err := os.Setenv("SSL_CERT_FILE", TheIDP(t).CertFile); err != nil {
		t.Fatal(err)
	}
	dir, err := os.MkdirTemp("", "bob-auth-stack-")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(ctx, bin, "up", "-dir", dir, "-json", "-orchestrators", fmt.Sprint(n))
	cmd.Dir = harness.Root(t)
	out, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		cancel()
		t.Fatalf("harness error: devstack: %v", err)
	}
	s := &Server{Dir: dir, clock: filepath.Join(dir, "clock")}
	// Test hook (compiled into bob-server only with -tags bobtest, like BOB_TEST_NIX_REPLAY, A27):
	// the file holds a Go duration the server adds to its clock. Expiry tests move time with it
	// instead of waiting an hour.
	if err := os.Setenv("BOB_TEST_CLOCK_FILE", s.clock); err != nil {
		t.Fatal(err)
	}
	s.cancel = func() {
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
		_ = os.RemoveAll(dir)
	}
	line, err := bufio.NewReader(out).ReadString('\n')
	if err != nil {
		s.Stop()
		t.Fatalf("harness error: devstack printed no info line: %v", err)
	}
	if err := json.Unmarshal([]byte(line), &s.Info); err != nil {
		s.Stop()
		t.Fatalf("harness error: devstack info line: %v: %q", err, line)
	}
	s.URL, _ = s.Info["api_url"].(string)
	s.AdminToken, _ = s.Info["admin_token"].(string)
	if urls, ok := s.Info["api_urls"].([]any); ok {
		for _, u := range urls {
			if str, ok := u.(string); ok {
				s.URLs = append(s.URLs, str)
			}
		}
	}
	if len(s.URLs) == 0 && s.URL != "" {
		s.URLs = []string{s.URL}
	}
	if s.URL == "" && len(s.URLs) > 0 {
		s.URL = s.URLs[0]
	}
	if s.URL == "" || s.AdminToken == "" {
		s.Stop()
		return nil, "bob-server orchestrator (the devstack info line has no api_url/admin_token)"
	}
	if list, ok := s.Info["orchestrators"].([]any); ok && len(list) > 0 {
		if o, ok := list[0].(map[string]any); ok && o["running"] == false {
			note, _ := o["exit_note"].(string)
			s.Stop()
			return nil, "bob-server orchestrator (exited during start-up: " + note + ")"
		}
	}
	return s, ""
}

// Stop stops the stack. Safe to call twice.
func (s *Server) Stop() {
	if s.cancel != nil {
		s.cancel()
		s.cancel = nil
	}
}

// Host is host:port of the first orchestrator.
func (s *Server) Host() string {
	u, err := url.Parse(s.URL)
	if err != nil {
		return ""
	}
	return u.Host
}

// OrchestratorLogs returns the paths of the orchestrator log files.
func (s *Server) OrchestratorLogs() []string {
	var out []string
	if list, ok := s.Info["orchestrators"].([]any); ok {
		for _, o := range list {
			if m, ok := o.(map[string]any); ok {
				if l, ok := m["log"].(string); ok {
					out = append(out, l)
				}
			}
		}
	}
	return out
}

// HTTPClient is an HTTP client for the stack: loopback only, certificates not verified (the
// stack may serve a self-signed certificate), redirects never followed, timeouts bounded.
func HTTPClient() *http.Client {
	return &http.Client{
		Timeout: 60 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // loopback test stack
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				host, _, _ := net.SplitHostPort(addr)
				if ip := net.ParseIP(host); host != "localhost" && (ip == nil || !ip.IsLoopback()) {
					return nil, fmt.Errorf("authenv: refusing non-loopback address %s", addr)
				}
				return (&net.Dialer{Timeout: 10 * time.Second}).DialContext(ctx, network, addr)
			},
			DisableKeepAlives: true,
		},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

// SetClock moves the stack's clock forward by d (0 resets it). It needs bob-server built with
// -tags bobtest, which tools/devstack must do (see BOB_TEST_CLOCK_FILE in the suite's report).
func (s *Server) SetClock(t testing.TB, d time.Duration) {
	t.Helper()
	tmp := s.clock + ".tmp"
	if err := os.WriteFile(tmp, []byte(d.String()), 0o644); err != nil {
		t.Fatalf("harness error: %v", err)
	}
	if err := os.Rename(tmp, s.clock); err != nil {
		t.Fatalf("harness error: %v", err)
	}
}

// Seed is the fixed data set of the stack: projects and one build per project. The devstack info
// line publishes it as {"seed": {"projects": ["p1","p2"], "builds": {"p1": "<id>", "p2": "<id>"}}}
// (projects need a forge; the harness stack seeds the records itself).
type Seed struct {
	Projects []string
	Builds   map[string]string
}

// Seed returns the stack's seed. Without one the test is red: the server side that creates
// projects and builds does not exist yet.
func (s *Server) Seed(t testing.TB) Seed {
	t.Helper()
	var out Seed
	m, _ := s.Info["seed"].(map[string]any)
	if ps, ok := m["projects"].([]any); ok {
		for _, p := range ps {
			if id, ok := p.(string); ok {
				out.Projects = append(out.Projects, id)
			}
		}
	}
	out.Builds = map[string]string{}
	if bs, ok := m["builds"].(map[string]any); ok {
		for p, id := range bs {
			if str, ok := id.(string); ok {
				out.Builds[p] = str
			}
		}
	}
	if len(out.Projects) < 2 || out.Builds["p1"] == "" || out.Builds["p2"] == "" {
		harness.RedFeature(t, "bob-server seeding (the devstack info line has no seed.projects p1,p2 / seed.builds)")
		t.FailNow()
	}
	return out
}

// ProxyURL is the counting S3 proxy the servers use (info s3_proxy_url).
func (s *Server) ProxyURL() string { u, _ := s.Info["s3_proxy_url"].(string); return u }
