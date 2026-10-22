package fleet

import (
	"context"
	"encoding/json"
	"fmt"
	"go/format"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/megakuul/bob/tests/harness"
	"github.com/megakuul/bob/tests/parity"
	"github.com/megakuul/bob/tests/serverenv"
	"github.com/megakuul/bob/tests/serverenv/stack"
	"github.com/megakuul/bob/tests/store/env"
)

func activeWorkers(t *testing.T, x env.Extra) int {
	c := env.RPC(t, x, "bob.fleet.v1.FleetService/ListWorkers", "", map[string]any{"page_size": 200})
	if c.Status != 200 {
		return -1
	}
	return strings.Count(string(c.Body), "WORKER_STATE_ACTIVE")
}

// workerProc starts one more `bob-server worker` against the stack's proxy with the shipped
// worker credential only: fleet, analytics and artifacts, nothing else (R-FLEET-001).
func workerProc(t *testing.T, s *stack.Stack, x env.Extra, name string, extra ...string) *serverenv.Process {
	t.Helper()
	cmd := exec.Command(harness.Bin(t, "bob-server"), "worker")
	cmd.Env = append([]string{
		"PATH=/usr/bin:/bin", "HOME=" + t.TempDir(), "TZ=UTC", "LC_ALL=C",
		"BOB_STORAGE_ENDPOINT=" + s.Info.S3ProxyURL, "BOB_STORAGE_REGION=" + s.Info.Region,
		"AWS_ACCESS_KEY_ID=" + x.WorkerAccess, "AWS_SECRET_ACCESS_KEY=" + x.WorkerSecret,
		"BOB_SERVER_NAME=" + name, "BOB_STATE_DIR=" + filepath.Join(s.Info.Dir, name),
	}, extra...)
	if err := os.MkdirAll(filepath.Join(s.Info.Dir, name), 0o755); err != nil {
		t.Fatal(err)
	}
	p, err := serverenv.StartProcess(cmd, filepath.Join(t.TempDir(), name+".log"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.Stop(5 * time.Second) })
	return p
}

// TestRFLEET001ZeroSetupRegistration: a worker started with only the worker S3 credential
// becomes active within 5 s (p99, registrar poll 2 s) with no admin step and no LIST.
func TestRFLEET001ZeroSetupRegistration(t *testing.T) {
	parity.Covers(t, "R-FLEET-001")
	s := env.StartStack(t, stack.Options{Orchestrators: 1, Workers: 1})
	if s == nil {
		return
	}
	x := env.MustExtra(t, s, "api", "admin")
	if x.WorkerAccess == "" || x.WorkerSecret == "" {
		t.Fatalf("harness gap: devstack.json has no worker_access_key/worker_secret_key")
	}
	env.Wait(30*time.Second, func() bool { return activeWorkers(t, x) >= 1 })
	var took []time.Duration
	for i := 0; i < 20; i++ {
		before := activeWorkers(t, x)
		start := time.Now()
		p := workerProc(t, s, x, "zs-"+string(rune('a'+i)))
		ok := env.Wait(30*time.Second, func() bool {
			select {
			case <-p.Exited():
				t.Fatalf("worker %d exited: %v (%s)", i, p.Err(), p.Log)
			default:
			}
			return activeWorkers(t, x) > before
		})
		if !ok {
			t.Fatalf("worker %d did not become active within 30 s", i)
		}
		took = append(took, time.Since(start))
	}
	sort.Slice(took, func(i, j int) bool { return took[i] < took[j] })
	if p99 := took[len(took)-1]; p99 > 5*time.Second {
		t.Errorf("registration to active: p99 %v over %d workers (all %v), want <= 5 s", p99, len(took), took)
	}
	for _, b := range []string{"bob-control", "bob-fleet"} {
		if n := env.ProxyCounts(t, s).Lists(b); n != 0 {
			t.Errorf("%d LIST requests on %s", n, b)
		}
	}
}

// TestRFLEET008WorkerRefusesNonRoot: bob-server worker as an unprivileged user refuses to
// start with the SPEC text and hint.
func TestRFLEET008WorkerRefusesNonRoot(t *testing.T) {
	parity.Covers(t, "R-FLEET-008", "R-SEC-000")
	if os.Geteuid() != 0 {
		t.Fatalf("harness error: this test needs root to drop to an unprivileged uid")
	}
	// A directory directly below the temp dir that uid 65534 can traverse (t.TempDir nests
	// 0700 directories).
	dir, err := os.MkdirTemp("", "bob-nonroot-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(dir, "bob-server")
	b, err := os.ReadFile(harness.Bin(t, "bob-server"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bin, b, 0o755); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "worker")
	cmd.Env = []string{"PATH=/usr/bin:/bin", "HOME=/nonexistent", "BOB_STATE_DIR=" + dir}
	cmd.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: 65534, Gid: 65534}}
	out, err := cmd.CombinedOutput()
	r := harness.Result{Args: []string{"worker"}, Stderr: string(out), Exit: cmd.ProcessState.ExitCode()}
	if harness.Red(t, r) {
		return
	}
	if err == nil {
		t.Errorf("worker as uid 65534 started and exited 0")
	}
	for _, want := range []string{
		"✗ the worker must run as root; builds themselves never run as root",
		"hint: run it as a systemd system service or a privileged container",
	} {
		if !strings.Contains(string(out), want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
}

// TestRFLEET008WorkerRefusesWithoutCgroupV2: a root worker on a host without cgroup v2 refuses
// with "✗ worker cannot isolate builds: <reason>". The host is simulated with a private mount
// namespace in which /sys/fs/cgroup is an empty tmpfs. (Landlock ABI < 4 and missing
// auto-allocate-uids cannot be simulated on a host that has them.)
func TestRFLEET008WorkerRefusesWithoutCgroupV2(t *testing.T) {
	parity.Covers(t, "R-FLEET-008", "R-SEC-000")
	unshare, err := exec.LookPath("unshare")
	if err != nil || os.Geteuid() != 0 {
		t.Fatalf("harness error: needs root and unshare(1): %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, unshare, "--mount", "--propagation", "private", "sh", "-c",
		`mount -t tmpfs none /sys/fs/cgroup && exec "$0" worker`, harness.Bin(t, "bob-server"))
	cmd.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + t.TempDir(), "BOB_STATE_DIR=" + t.TempDir()}
	out, _ := cmd.CombinedOutput()
	r := harness.Result{Args: []string{"worker"}, Stderr: string(out), Exit: cmd.ProcessState.ExitCode()}
	if harness.Red(t, r) {
		return
	}
	if cmd.ProcessState.ExitCode() == 0 {
		t.Errorf("worker without cgroup v2 started")
	}
	if !strings.Contains(string(out), "✗ worker cannot isolate builds: ") || !strings.Contains(strings.ToLower(string(out)), "cgroup") {
		t.Errorf("output lacks %q naming cgroup v2:\n%s", "✗ worker cannot isolate builds: <reason>", out)
	}
}

// hostileTests is the Go test file that runs inside a build of the fixture on the worker. It
// fails when the sandbox leaks: root uid, writable inputs, the worker's credentials or
// process environment readable, network reachable.
const hostileTests = `package hostile

import (
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const (
	secretKey  = %q
	accessKey  = %q
	workerPID  = %d
	hostPorts  = %q
)

func TestNotRoot(t *testing.T) {
	if os.Getuid() == 0 || os.Geteuid() == 0 {
		t.Fatalf("build runs as uid %%d", os.Getuid())
	}
}

func TestInputStorePathsAreReadOnly(t *testing.T) {
	entries, err := os.ReadDir("/nix/store")
	if err != nil {
		t.Fatalf("cannot read /nix/store: %%v", err)
	}
	tried := 0
	for _, e := range entries {
		if tried == 50 {
			break
		}
		p := filepath.Join("/nix/store", e.Name())
		tried++
		if err := os.Chmod(p, 0o777); err == nil {
			t.Errorf("chmod of input store path %%s succeeded", p)
		}
		if e.IsDir() {
			if err := os.WriteFile(filepath.Join(p, ".bob-hostile"), []byte("x"), 0o644); err == nil {
				t.Errorf("write into input store path %%s succeeded", p)
			}
		}
	}
	if tried == 0 {
		t.Fatalf("no input store path visible: cannot judge")
	}
}

func TestNoWorkerCredentialsAnywhere(t *testing.T) {
	for _, kv := range os.Environ() {
		if strings.Contains(kv, secretKey) || strings.Contains(kv, accessKey) || strings.HasPrefix(kv, "AWS_") {
			t.Errorf("credential in the build environment: %%.12s...", kv)
		}
	}
	if b, err := os.ReadFile("/proc/" + itoa(workerPID) + "/environ"); err == nil {
		t.Errorf("read the worker's /proc/%%d/environ (%%d bytes)", workerPID, len(b))
	}
	if b, err := os.ReadFile("/proc/1/environ"); err == nil && (strings.Contains(string(b), secretKey) || strings.Contains(string(b), accessKey)) {
		t.Errorf("the credential is in /proc/1/environ")
	}
	procs, _ := filepath.Glob("/proc/[0-9]*/environ")
	for _, p := range procs {
		if b, err := os.ReadFile(p); err == nil && (strings.Contains(string(b), secretKey) || strings.Contains(string(b), accessKey)) {
			t.Errorf("the credential is in %%s", p)
		}
	}
	for _, root := range []string{"/etc", "/run", "/var", "/root", "/home", "/tmp", "/mnt", "/opt", "/srv"} {
		_ = filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return nil
			}
			if info, err := d.Info(); err != nil || info.Size() > 1<<20 || !info.Mode().IsRegular() {
				return nil
			}
			if b, err := os.ReadFile(p); err == nil && (strings.Contains(string(b), secretKey) || strings.Contains(string(b), accessKey)) {
				t.Errorf("the S3 credential is readable in %%s", p)
			}
			return nil
		})
	}
}

func TestNoHostNetwork(t *testing.T) {
	targets := append([]string{"169.254.169.254:80"}, strings.Split(hostPorts, ",")...)
	for _, addr := range targets {
		if addr == "" {
			continue
		}
		c, err := net.DialTimeout("tcp", addr, 2*time.Second)
		if err == nil {
			c.Close()
			t.Errorf("connected to %%s from inside the build", addr)
		}
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	s := ""
	for ; n > 0; n /= 10 {
		s = string(rune('0'+n%%10)) + s
	}
	return s
}
`

// TestRFLEET008BuildsAreIsolated runs hostile test code as a remote build on a root worker and
// checks the host side afterwards: the checks inside (not root, inputs read-only, no worker
// credentials or process environment, no host or metadata network) pass, no process remains
// in a job cgroup, and the worker's persistent store verifies clean.
func TestRFLEET008BuildsAreIsolated(t *testing.T) {
	parity.Covers(t, "R-FLEET-008", "R-SEC-000")
	s := env.StartStack(t, stack.Options{Orchestrators: 1, Workers: 1})
	if s == nil {
		return
	}
	x := env.MustExtra(t, s, "api", "admin")
	if x.WorkerSecret == "" {
		t.Fatalf("harness gap: devstack.json has no worker credential")
	}
	env.Wait(30*time.Second, func() bool { return activeWorkers(t, x) >= 1 })

	dir := harness.Fixture(t, "go")
	ports := strings.Join([]string{hostPort(x.APIURL), hostPort(s.Info.S3ProxyURL), hostPort(s.Info.S3URL)}, ",")
	hdir := filepath.Join(dir, "lib", "hostile")
	if err := os.MkdirAll(hdir, 0o755); err != nil {
		t.Fatal(err)
	}
	src := fmt.Sprintf(hostileTests, x.WorkerSecret, x.WorkerAccess, s.Info.Workers[0].Pid, ports)
	if err := os.WriteFile(filepath.Join(hdir, "hostile_test.go"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "-A"}, {"-c", "user.name=h", "-c", "user.email=h@h.invalid", "commit", "-q", "-m", "hostile"}} {
		c := exec.Command("git", args...)
		c.Dir = dir
		if b, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, b)
		}
	}
	spec := harness.Spec{Dir: dir, Timeout: 5 * time.Minute, Env: []string{"BOB_SERVER=" + x.APIURL, "BOB_TOKEN=" + x.AdminToken, "BOB_HOME=" + t.TempDir()}}
	r := harness.Run(t, "bob", spec, "test", "//lib:tests", "--remote")
	if harness.Red(t, r) {
		return
	}
	if r.Exit != 0 {
		t.Errorf("hostile tests failed inside the build (exit %d):\n%s", r.Exit, r.Combined())
	}
	for _, name := range []string{"TestNotRoot", "TestInputStorePathsAreReadOnly", "TestNoWorkerCredentialsAnywhere", "TestNoHostNetwork"} {
		if !strings.Contains(r.Combined(), name) && !strings.Contains(r.Combined(), "hostile") {
			t.Errorf("the hostile package did not run (%s not mentioned):\n%s", name, r.Combined())
			break
		}
	}

	// After the job: no process remains in any job cgroup.
	jobs, _ := filepath.Glob("/sys/fs/cgroup/**/bob-worker.slice/job-*")
	more, _ := filepath.Glob("/sys/fs/cgroup/bob-worker.slice/job-*")
	for _, cg := range append(jobs, more...) {
		if b, err := os.ReadFile(filepath.Join(cg, "cgroup.procs")); err == nil && strings.TrimSpace(string(b)) != "" {
			t.Errorf("processes remain in %s after the job: %s", cg, strings.Fields(string(b)))
		}
	}
	// The persistent store is unchanged: nix store verify --check-contents reports 0 errors.
	store := filepath.Join(s.Info.Workers[0].Log, "..", "..", "worker-0", "worker", "nix")
	if _, err := os.Stat(store); err != nil {
		t.Fatalf("harness gap: the worker's persistent store is expected at %s (BOB_STATE_DIR/worker/nix): %v", store, err)
	}
	nix, err := serverenv.NixTool(context.Background(), "nix")
	if err != nil {
		t.Fatal(err)
	}
	v := exec.Command(nix, "--extra-experimental-features", "nix-command", "store", "verify", "--check-contents", "--all", "--store", "local?root="+store)
	if b, err := v.CombinedOutput(); err != nil {
		t.Errorf("nix store verify --check-contents on the worker store: %v\n%s", err, b)
	}
}

func hostPort(u string) string {
	u = strings.TrimPrefix(strings.TrimPrefix(u, "http://"), "https://")
	h, _, _ := strings.Cut(u, "/")
	return h
}

// TestRFLEET013ConcurrentJobsShareOneStore: with 2 slots, two builds with overlapping closures
// run at the same time on one worker: both succeed and the shared package is built once.
func TestRFLEET013ConcurrentJobsShareOneStore(t *testing.T) {
	parity.Covers(t, "R-FLEET-013")
	t.Setenv("BOB_WORKER_SLOTS", "2")
	s := env.StartStack(t, stack.Options{Orchestrators: 1, Workers: 1})
	if s == nil {
		return
	}
	x := env.MustExtra(t, s, "api", "admin")
	env.Wait(30*time.Second, func() bool { return activeWorkers(t, x) >= 1 })

	a, b := harness.Fixture(t, "acme"), harness.Fixture(t, "acme")
	if err := os.WriteFile(filepath.Join(b, "NOTES.md"), []byte("second copy\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	results := make([]harness.Result, 2)
	for i, c := range []struct {
		dir  string
		args []string
	}{{a, []string{"//cmd/api:api"}}, {b, []string{"//cmd/api:api", "//libs/foo:foo"}}} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i] = env.RemoteBuild(t, x, c.dir, c.args...)
		}()
	}
	wg.Wait()
	for i, r := range results {
		if harness.Red(t, r) {
			return
		}
		if r.Exit != 0 {
			t.Errorf("build %d failed (exit %d):\n%s", i, r.Exit, r.Combined())
		}
	}
	// The shared package //libs/foo:foo has exactly one run record across both builds.
	var projects struct{ Projects []struct{ ID string } }
	c := env.RPC(t, x, "bob.project.v1.ProjectService/ListProjects", "", map[string]any{"page_size": 50})
	if c.Status != 200 || json.Unmarshal(c.Body, &projects) != nil {
		t.Fatalf("ListProjects: %d %s", c.Status, c.Body)
	}
	runs := int64(0)
	for _, p := range projects.Projects {
		c := env.RPC(t, x, "bob.analytics.v1.AnalyticsService/PackageDurations", "", map[string]any{"project_id": p.ID, "label": "//libs/foo:foo", "days": 1})
		var rows struct{ Rows []struct{ Count string } }
		if c.Status != 200 || json.Unmarshal(c.Body, &rows) != nil {
			t.Fatalf("PackageDurations: %d %s", c.Status, c.Body)
		}
		for _, r := range rows.Rows {
			n, _ := json.Number(r.Count).Int64()
			runs += n
		}
	}
	if runs != 1 {
		t.Errorf("//libs/foo:foo was built %d times by two overlapping jobs on one shared store, want exactly once", runs)
	}
}

// TestHostileSourceIsValidGo guards the generated hostile test file itself: it must be valid
// Go with every format verb filled, so that a failure inside the build means a leak and not a typo.
func TestHostileSourceIsValidGo(t *testing.T) {
	src := fmt.Sprintf(hostileTests, "SECRET", "ACCESS", 4242, "127.0.0.1:1,127.0.0.1:2")
	if strings.Contains(src, "%!") || strings.Contains(src, "%(") {
		t.Fatalf("unfilled format verbs in the hostile source")
	}
	if _, err := format.Source([]byte(src)); err != nil {
		t.Fatalf("hostile source does not parse: %v", err)
	}
	dir := t.TempDir()
	must := func(err error) {
		if err != nil {
			t.Fatal(err)
		}
	}
	must(os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/hostile\n\ngo 1.26\n"), 0o644))
	must(os.WriteFile(filepath.Join(dir, "hostile_test.go"), []byte(src), 0o644))
	cmd := exec.Command("go", "vet", "./...")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0", "GOFLAGS=-mod=mod")
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go vet of the hostile source: %v\n%s", err, b)
	}
}
