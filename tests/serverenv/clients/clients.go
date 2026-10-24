// Package clients runs the real Nix and podman clients for the conformance suites
// (HARNESS.md §7.5, §8.2: cache-conformance, oci-conformance, and the OCI run
// tests of R-OUT). Both tools are realised from the nixpkgs pin through
// serverenv.NixTool and run rootless: when the test binary is root, every client
// process is started as the unprivileged user bobtest with its own HOME, so a
// client can never touch the host's Nix or container state.
//
// API:
//
//	n := clients.Nix(t)                       // *Client, fails t with a harness error if unavailable
//	r := n.Run("path-info", "--store", url)   // harness.Result (Stdout, Stderr, Exit)
//
//	p := clients.Podman(t)
//	p.InsecureRegistry("127.0.0.1:23456")     // plain-HTTP / self-signed registry allowed
//	r := p.Run("pull", "127.0.0.1:23456/polyglot:latest")
//	p.Load(ociArchive)                        // podman load -i, returns the image reference
//
//	c.Dir("work")                             // scratch directory, 0755, owned by the client user
//	c.Timeout = time.Minute                   // per-Run limit (default 5 minutes)
//
// Nix runs with experimental features nix-command and flakes enabled, no config
// files, and its own cache dir. Podman uses the vfs storage driver with the
// containers files written below the client's HOME (HARNESS.md §7.5 fallback:
// no overlay or fuse needed), cgroupfs and the file events backend. newuidmap and
// newgidmap must be the setuid host binaries (/usr/bin) and bobtest needs a subuid
// range; a missing prerequisite is a harness error (never a skipped test).
package clients

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/megakuul/bob/tests/harness"
	"github.com/megakuul/bob/tests/serverenv"
)

// Client is one realised tool bound to an unprivileged identity and a private HOME.
type Client struct {
	Bin     string        // absolute path of the tool
	Home    string        // the client's HOME (below Share)
	Share   string        // world-readable scratch root owned by the client user
	Timeout time.Duration // per-Run limit; zero means 5 minutes
	Env     []string      // appended to the base environment

	t    testing.TB
	cred *syscall.Credential
	base []string // arguments put in front of every Run
	seq  atomic.Int32
}

// Result is one finished client process.
type Result = harness.Result

// Nix returns the pinned Nix client (pins.nix["nix"]).
func Nix(t testing.TB) *Client {
	t.Helper()
	c := newClient(t, "nix")
	c.base = []string{"--extra-experimental-features", "nix-command flakes"}
	c.Env = append(c.Env,
		"NIX_CONFIG=connect-timeout = 10\nsubstituters =\nsandbox = false",
		"NIX_STATE_DIR="+c.Dir("nix-state"),
		"NIX_LOG_DIR="+c.Dir("nix-log"),
		"NIX_STORE_DIR="+filepath.Join(c.Dir("nix-store"), "nix", "store"),
	)
	return c
}

// Podman returns the pinned rootless podman client (pins.nix["podman"]).
func Podman(t testing.TB) *Client {
	t.Helper()
	c := newClient(t, "podman")
	cfg := filepath.Join(c.Home, ".config", "containers")
	if err := os.MkdirAll(cfg, 0o755); err != nil {
		t.Fatalf("harness: %v", err)
	}
	graph, run := c.Dir("podman-graph"), c.Dir("podman-run")
	files := map[string]string{
		"storage.conf": fmt.Sprintf("[storage]\ndriver = \"vfs\"\ngraphroot = %q\nrunroot = %q\n[storage.options]\nignore_chown_errors = \"true\"\n", graph, run),
		"policy.json":  `{"default":[{"type":"insecureAcceptAnything"}]}`,
		"containers.conf": "[containers]\nlog_driver = \"k8s-file\"\n" +
			"[engine]\ncgroup_manager = \"cgroupfs\"\nevents_logger = \"file\"\nruntime = \"crun\"\n",
		"registries.conf": "unqualified-search-registries = []\n",
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(cfg, name), []byte(body), 0o644); err != nil {
			t.Fatalf("harness: %v", err)
		}
	}
	c.chown(c.Home)
	for _, tool := range []string{"newuidmap", "newgidmap"} {
		if !privileged("/usr/bin/" + tool) {
			t.Fatalf("harness: rootless podman needs /usr/bin/%s with the setuid bit or file capabilities (HARNESS.md §7.5): %v", tool, serverenv.ErrHarness)
		}
	}
	c.Env = append(c.Env,
		"CONTAINERS_STORAGE_CONF="+filepath.Join(cfg, "storage.conf"),
		"CONTAINERS_CONF="+filepath.Join(cfg, "containers.conf"),
		"CONTAINERS_REGISTRIES_CONF="+filepath.Join(cfg, "registries.conf"),
		"XDG_RUNTIME_DIR="+c.Dir("xdg-run"),
		"PATH=/usr/bin:/bin",
	)
	return c
}

// privileged reports whether path is setuid root or carries file capabilities
// (Fedora ships newuidmap with cap_setuid instead of the setuid bit).
func privileged(path string) bool {
	fi, err := os.Stat(path)
	if err != nil {
		return false
	}
	if fi.Mode()&os.ModeSetuid != 0 {
		return true
	}
	n, err := syscall.Getxattr(path, "security.capability", nil)
	return err == nil && n > 0
}

// toolBin finds bin/<name> in the realised tool. NixTool returns the first
// printed output, which for a multi-output attribute (nix, podman) can be the
// man output, so the out-link without suffix is tried as well.
func toolBin(ctx context.Context, name string) (string, error) {
	out, err := serverenv.NixTool(ctx, name)
	if err != nil {
		return "", err
	}
	bin := filepath.Join(out, "bin", name)
	if _, err := os.Stat(bin); err == nil {
		return bin, nil
	}
	cache, err := serverenv.CacheDir()
	if err != nil {
		return "", err
	}
	if p, err := filepath.EvalSymlinks(filepath.Join(cache, "nix", name)); err == nil {
		bin = filepath.Join(p, "bin", name)
		if _, err := os.Stat(bin); err == nil {
			return bin, nil
		}
	}
	return "", fmt.Errorf("clients: no bin/%s in %s: %w", name, out, serverenv.ErrHarness)
}

func newClient(t testing.TB, name string) *Client {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	bin, err := toolBin(ctx, name)
	if err != nil {
		t.Fatalf("harness: %v", err)
	}
	c := &Client{Bin: bin, t: t}
	share, err := os.MkdirTemp("/var/tmp", "bob-client-"+name+"-")
	if err != nil {
		t.Fatalf("harness: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(share) })
	if err := os.Chmod(share, 0o755); err != nil {
		t.Fatalf("harness: %v", err)
	}
	c.Share = share
	if os.Getuid() == 0 {
		u, err := user.Lookup("bobtest")
		if err != nil {
			t.Fatalf("harness: the clients run as the unprivileged user bobtest (HARNESS.md §7): %v: %v", err, serverenv.ErrHarness)
		}
		uid, _ := strconv.Atoi(u.Uid)
		gid, _ := strconv.Atoi(u.Gid)
		c.cred = &syscall.Credential{Uid: uint32(uid), Gid: uint32(gid)}
	}
	c.Home = c.Dir("home")
	c.Env = []string{"HOME=" + c.Home, "XDG_CACHE_HOME=" + c.Dir("cache"), "TZ=UTC", "LC_ALL=C"}
	return c
}

// Dir makes (or returns) a 0755 directory below Share owned by the client user.
func (c *Client) Dir(name string) string {
	d := filepath.Join(c.Share, name)
	if err := os.MkdirAll(d, 0o755); err != nil {
		c.t.Fatalf("harness: %v", err)
	}
	c.chown(d)
	return d
}

// Own hands path (recursively) to the client user, for files the test created
// and the client must read or write.
func (c *Client) Own(path string) { c.chown(path) }

func (c *Client) chown(path string) {
	if c.cred == nil {
		return
	}
	err := filepath.WalkDir(path, func(p string, _ fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		return os.Lchown(p, int(c.cred.Uid), int(c.cred.Gid))
	})
	if err != nil {
		c.t.Fatalf("harness: chown %s: %v", path, err)
	}
}

// Run runs the tool with args (after the fixed leading arguments) and returns
// its result. A client that cannot start is a harness error.
func (c *Client) Run(args ...string) Result {
	c.t.Helper()
	return c.RunEnv(nil, args...)
}

// RunEnv is Run with extra environment entries for this call only.
func (c *Client) RunEnv(env []string, args ...string) Result {
	c.t.Helper()
	d := c.Timeout
	if d == 0 {
		d = 5 * time.Minute
	}
	ctx, cancel := context.WithTimeout(context.Background(), d)
	defer cancel()
	argv := append(append([]string{}, c.base...), args...)
	cmd := exec.CommandContext(ctx, c.Bin, argv...)
	cmd.Dir = c.Home
	cmd.Env = append(append(append([]string{}, c.Env...), env...), "PATH="+pathOf(c.Env, env))
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Pdeathsig: syscall.SIGKILL, Credential: c.cred}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	var so, se strings.Builder
	cmd.Stdout, cmd.Stderr = &so, &se
	r := Result{Args: args}
	err := cmd.Run()
	r.Stdout, r.Stderr = so.String(), se.String()
	var ee *exec.ExitError
	switch {
	case err == nil:
	case errors.As(err, &ee):
		r.Exit = ee.ExitCode()
	default:
		c.t.Fatalf("harness: start %s: %v: %v", c.Bin, err, serverenv.ErrHarness)
	}
	return r
}

// pathOf is the last PATH entry in the environments, or a minimal default.
func pathOf(envs ...[]string) string {
	p := "/usr/bin:/bin"
	for _, e := range envs {
		for _, kv := range e {
			if v, ok := strings.CutPrefix(kv, "PATH="); ok {
				p = v
			}
		}
	}
	return p
}

// InsecureRegistry allows plain HTTP and unverified TLS for host:port (the
// harness registry on loopback). Podman only.
func (c *Client) InsecureRegistry(hostport string) {
	c.t.Helper()
	conf := filepath.Join(c.Home, ".config", "containers", "registries.conf.d")
	if err := os.MkdirAll(conf, 0o755); err != nil {
		c.t.Fatalf("harness: %v", err)
	}
	body := fmt.Sprintf("[[registry]]\nlocation = %q\ninsecure = true\n", hostport)
	file := filepath.Join(conf, fmt.Sprintf("%d.conf", c.seq.Add(1)))
	if err := os.WriteFile(file, []byte(body), 0o644); err != nil {
		c.t.Fatalf("harness: %v", err)
	}
	c.chown(filepath.Join(c.Home, ".config"))
	c.Env = append(c.Env, "CONTAINERS_REGISTRIES_CONF_DIR="+conf)
}

// Load runs `podman load -i archive` and returns the loaded image reference
// (the "Loaded image: …" line). The archive must be readable by the client user.
func (c *Client) Load(archive string) (string, Result) {
	c.t.Helper()
	r := c.Run("load", "-i", archive)
	for _, l := range strings.Split(r.Stdout, "\n") {
		if ref, ok := strings.CutPrefix(strings.TrimSpace(l), "Loaded image: "); ok {
			return ref, r
		}
		if ref, ok := strings.CutPrefix(strings.TrimSpace(l), "Loaded image(s): "); ok {
			return ref, r
		}
	}
	return "", r
}
